package report

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/ofunc/dt"
	"github.com/ofunc/dt/io/csv"
	"github.com/ofunc/dt/io/xlsx"
)

// Report is the report entity.
type Report struct {
	path     string
	feed     func(name string) (time.Time, *dt.Frame, error)
	group    func(base, data *dt.Frame) *dt.Frame
	sdate    time.Time
	edate    time.Time
	base     *dt.Frame // ID[, NAME], LEVEL, SUPER, TARGET
	data     *dt.Frame // ID, 20060102, 20060103, ...
	schedule *dt.Frame // DATE, VALUE
	adjust   *dt.Frame // DATE, ID[, NAME], VALUE
}

// Load loads the report.
func Load(path string, feed func(string) (time.Time, *dt.Frame, error), group func(*dt.Frame, *dt.Frame) *dt.Frame) (*Report, error) {
	if feed == nil {
		return nil, errors.New("report.Load: feed can't be nil")
	}
	if group == nil {
		group = Group
	}

	cr, xr := csv.NewReader(), xlsx.NewReader()

	base, err := xr.ReadFile(filepath.Join(path, "base.xlsx"))
	if err != nil {
		return nil, err
	}
	if err := base.Check("ID", "LEVEL", "SUPER", "TARGET"); err != nil {
		return nil, err
	}
	base.Get("ID").String()
	base.Get("LEVEL").Number()
	base.Get("SUPER").String()
	base.Get("TARGET").Number()

	data, err := cr.ReadFile(filepath.Join(path, "data.csv"))
	if err != nil {
		return nil, err
	}
	if err := data.Check("ID"); err != nil {
		return nil, err
	}
	data.Get("ID").String()
	data = base.Pick("ID").Join(data, "ID").Do("").FillNA(dt.Number(0))

	schedule, err := xr.ReadFile(filepath.Join(path, "schedule.xlsx"))
	if err != nil {
		return nil, err
	}
	if err := schedule.Check("DATE", "VALUE"); err != nil {
		return nil, err
	}
	schedule.Get("DATE").String()
	schedule.Get("VALUE").Number()
	schedule.Sort(func(x, y dt.Record) bool {
		return x.String("DATE") < y.String("DATE")
	}).FillNA(dt.Number(1), "VALUE")

	dates := schedule.Get("DATE")
	ndates := len(dates)
	if ndates < 2 {
		return nil, errors.New("report: invalid schedule frame")
	}
	sdate, err := ParseDate(dates[0].String())
	if err != nil {
		return nil, err
	}
	edate, err := ParseDate(dates[ndates-1].String())
	if err != nil {
		return nil, err
	}

	adjust, err := xr.ReadFile(filepath.Join(path, "adjust.xlsx"))
	if err != nil {
		return nil, err
	}
	if err := adjust.Check("DATE", "ID", "VALUE"); err != nil {
		return nil, err
	}
	adjust.Get("DATE").String()
	adjust.Get("ID").String()
	adjust.Get("VALUE").Number()

	return &Report{
		path:     path,
		feed:     feed,
		group:    group,
		sdate:    sdate,
		edate:    edate,
		base:     base,
		data:     data,
		schedule: schedule,
		adjust:   adjust,
	}, nil
}

// Base returns a copy of base frame.
func (a *Report) Base() *dt.Frame {
	return a.base.Copy(false)
}

// Feed feeds a file to report.
func (a *Report) Feed(name string) (time.Time, error) {
	date, data, err := a.feed(name)
	if err != nil {
		return date, err
	}
	if err := data.Check("ID", "VALUE"); err != nil {
		return date, err
	}
	data.Get("ID").String()
	data.Get("VALUE").Number()
	data = a.group(a.base, data.Pick("ID", "VALUE"))
	a.data.Set(FormatDate(date), data.Get("VALUE"))

	path := filepath.Join(a.path, "data.csv")
	bak, err := bakup(path)
	if err != nil {
		return date, err
	}
	err = csv.NewWriter().WriteFile(a.data, path)
	if err != nil {
		os.Remove(path)
		os.Rename(bak, path)
		return date, err
	}
	os.Remove(bak)
	return date, nil
}

// Stat statistics data from s to e.
func (a *Report) Stat(s, e time.Time) dt.List {
	adjust := a.adjust.Filter(func(r dt.Record) bool {
		date, err := ParseDate(r.String("DATE"))
		if err != nil {
			return false
		}
		return !date.Before(s) && date.Before(e)
	}).GroupBy("ID").Apply("VALUE", "VALUE", dt.Sum).Do()

	return a.base.Join(adjust, "ID").Do("A_").
		Set("S_VALUE", a.value(s.AddDate(0, 0, -1))).
		Set("E_VALUE", a.value(e.AddDate(0, 0, -1))).
		FillNA(dt.Number(0), "A_VALUE").
		MapTo("VALUE", func(r dt.Record) dt.Value {
			return dt.Number(r.Number("A_VALUE") + r.Number("E_VALUE") - r.Number("S_VALUE"))
		}).Get("VALUE")
}

// Target gets the target from s to e.
func (a *Report) Target(s, e time.Time) dt.List {
	svalues := a.Stat(a.sdate, s)
	starget := a.TargetBy(s)
	etarget := a.TargetBy(e)
	return a.base.Pick("TARGET").
		Set("VALUE", svalues).Set("S_TARGET", starget).Set("E_TARGET", etarget).
		Map(func(r dt.Record) dt.Value {
			et, st, t, v := r.Number("E_TARGET"), r.Number("S_TARGET"), r.Number("TARGET"), r.Number("VALUE")
			rt := (et - st) * (t - v) / (t - st)
			if rt > 0 {
				return dt.Number(rt)
			}
			return dt.Number(0)
		})
}

// StatBy statistics data from start time to e.
func (a *Report) StatBy(e time.Time) dt.List {
	return a.Stat(a.sdate, e)
}

// TargetBy gets the target from start time to e.
func (a *Report) TargetBy(e time.Time) dt.List {
	dates := a.schedule.Get("DATE")
	values := a.schedule.Get("VALUE")

	k := math.NaN()
	d1, err := ParseDate(dates[0].String())
	if err != nil {
		panic(err)
	}
	for i := 1; i < len(dates); i++ {
		d2, err := ParseDate(dates[i].String())
		if err != nil {
			panic(err)
		}
		if d2.After(e) {
			v1, v2 := values[i-1].Number(), values[i].Number()
			k = v1 + (v2-v1)*(e.Sub(d1).Hours())/(d2.Sub(d1).Hours())
			break
		}
		d1 = d2
	}

	return a.base.Pick("TARGET").
		Map(func(r dt.Record) dt.Value {
			return dt.Number(k * r.Number("TARGET"))
		})
}

func (a *Report) value(d time.Time) dt.List {
	if key := FormatDate(d); a.data.Has(key) {
		return a.data.Get(key)
	}

	var d1, d2 time.Time
	var v1, v2 dt.List
	for i := 1; i < 16; i++ {
		d1 = d.AddDate(0, 0, -i)
		if key := FormatDate(d1); a.data.Has(key) {
			v1 = a.data.Get(key)
			break
		}
	}
	if v1 == nil {
		panic("report: invalid date: " + FormatDate(d))
	}

	for i := 1; i < 16; i++ {
		d2 = d.AddDate(0, 0, i)
		if key := FormatDate(d2); a.data.Has(key) {
			v2 = a.data.Get(key)
			break
		}
	}
	if v2 == nil {
		panic("report: invalid date: " + FormatDate(d))
	}

	k := d.Sub(d1).Hours() / d2.Sub(d1).Hours()
	v := make(dt.List, len(v1))
	for i := range v {
		v[i] = dt.Number(v1[i].Number() + k*(v2[i].Number()-v1[i].Number()))
	}
	return v
}
