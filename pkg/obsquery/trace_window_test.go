package obsquery

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// A windowed page draws its candidates from the window's spans alone and
// computes their columns over the whole range: phase 1 (and every candidate
// prefilter) is bounded below by the window, phase 2 by the range start, and
// phase 1 keeps windowSlack more ids than the page.
func TestCompileWindowBoundsPhaseOneOnly(t *testing.T) {
	sc := scope()
	sc.End = t0.Add(24 * time.Hour)
	from := sc.End.Add(-20 * time.Minute)
	for name, q := range map[string]TraceQuery{
		"newest":   {Scope: sc, Limit: 50},
		"filtered": {Scope: sc, Limit: 50, ErrorsOnly: true, RootSpanName: "Get", UserID: "u1"},
	} {
		st, err := q.CompileWindow(from)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st)
		starts, froms := paramsBound(st, sc.Start.UnixNano()), paramsBound(st, from.UnixNano())
		inner, outer := cutSubquery(t, st.SQL, "TraceId IN (SELECT trace_id FROM (")
		if uses(outer, starts) != 1 || uses(outer, froms) != 0 {
			t.Errorf("%s: phase 2 is not over the whole range:\n%s", name, outer)
		}
		if uses(inner, starts) != 0 || uses(inner, froms) == 0 {
			t.Errorf("%s: phase 1 or a prefilter reads before the window:\n%s", name, inner)
		}
		if want := "LIMIT " + strconv.Itoa(q.Limit+1+windowSlack(q.Limit)) + "\n"; !strings.Contains(inner, want) {
			t.Errorf("%s: phase 1 does not keep %q:\n%s", name, want, inner)
		}
		if !strings.HasSuffix(st.SQL, "ORDER BY start DESC, trace_id DESC\nLIMIT 51") {
			t.Errorf("%s: the page is not the newest Limit+1 rows:\n%s", name, st.SQL)
		}
		if q.ErrorsOnly && uses(inner, froms) != 4 {
			t.Errorf("%s: a candidate prefilter is not bounded by the window:\n%s", name, inner)
		}
	}
}

// A window at or before the range start is the whole range: Compile's
// statement, byte for byte.
func TestCompileWindowAtTheRangeStartIsCompile(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 10, ErrorsOnly: true}
	want, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []time.Time{q.Scope.Start, q.Scope.Start.Add(-time.Hour)} {
		got, err := q.CompileWindow(from)
		if err != nil {
			t.Fatal(err)
		}
		if got.SQL != want.SQL || len(got.Params) != len(want.Params) {
			t.Errorf("from %v: statement differs from Compile's:\n%s", from, got.SQL)
		}
	}
}

func TestCompileWindowRefusals(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 10}
	if _, err := q.CompileWindow(q.Scope.End); err == nil {
		t.Error("a window starting at the range end compiled")
	}
	q.Order = OrderDurationDesc
	if _, err := q.CompileWindow(q.Scope.End.Add(-time.Minute)); err == nil {
		t.Error("a duration-ordered window compiled")
	}
}

func TestPageWindows(t *testing.T) {
	day := scope()
	day.End = t0.Add(24 * time.Hour)
	week := scope()
	week.End = t0.Add(7 * 24 * time.Hour)
	cursorAt := func(q TraceQuery, at time.Time) TraceQuery {
		c, err := DecodeTraceCursor(q.NextCursor(at, "abc"))
		if err != nil {
			t.Fatal(err)
		}
		q.Cursor = c
		return q
	}
	ago := func(end time.Time, ds ...time.Duration) []time.Time {
		var out []time.Time
		for _, d := range ds {
			out = append(out, end.Add(-d))
		}
		return out
	}
	m := time.Minute
	cases := map[string]struct {
		q    TraceQuery
		want []time.Time
	}{
		"24h":                 {TraceQuery{Scope: day, Limit: 50}, ago(day.End, 22*m+30*time.Second, 3*time.Hour)},
		"7d":                  {TraceQuery{Scope: week, Limit: 50}, ago(week.End, 157*m+30*time.Second, 21*time.Hour)},
		"1h, five minutes up": {TraceQuery{Scope: scope(), Limit: 50}, ago(t0.Add(time.Hour), 5*m, 40*m)},
		"under an hour":       {TraceQuery{Scope: Scope{Database: "d", Start: t0, End: t0.Add(59 * m)}, Limit: 50}, nil},
		"slowest":             {TraceQuery{Scope: day, Limit: 50, Order: OrderDurationDesc}, nil},
		"cursor":              {cursorAt(TraceQuery{Scope: day, Limit: 50}, t0.Add(12*time.Hour)), ago(t0.Add(12*time.Hour), 11*m+15*time.Second, 90*m)},
		"cursor at start":     {cursorAt(TraceQuery{Scope: day, Limit: 50}, t0), nil},
	}
	for name, c := range cases {
		got := c.q.PageWindows()
		if len(got) != len(c.want) {
			t.Errorf("%s: windows %v, want %v", name, got, c.want)
			continue
		}
		for i := range got {
			if !got[i].Equal(c.want[i]) {
				t.Errorf("%s: windows %v, want %v", name, got, c.want)
				break
			}
		}
		for _, from := range got {
			if _, err := c.q.CompileWindow(from); err != nil {
				t.Errorf("%s: window %v does not compile: %v", name, from, err)
			}
		}
	}
}

func TestWindowPageExact(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 2}
	from := t0.Add(30 * time.Minute)
	at := func(ms ...int) []time.Time {
		var out []time.Time
		for _, m := range ms {
			out = append(out, t0.Add(time.Duration(m)*time.Minute))
		}
		return out
	}
	for name, c := range map[string]struct {
		from   time.Time
		starts []time.Time
		want   bool
	}{
		"a full page in the window":              {from, at(50, 40, 31), true},
		"the lookahead row starts exactly at it": {from, at(50, 40, 30), false},
		"the lookahead row began before it":      {from, at(50, 40, 29), false},
		"too few rows to know the page":          {from, at(50, 40), false},
		"no rows":                                {from, nil, false},
		"the window is the range":                {t0, at(50), true},
		"the window covers more than the range":  {t0.Add(-time.Hour), nil, true},
	} {
		if got := q.WindowPageExact(c.from, c.starts); got != c.want {
			t.Errorf("%s: WindowPageExact = %v, want %v", name, got, c.want)
		}
	}
}

// paramsBound returns the names of st's Int64 parameters bound to v.
func paramsBound(st Stmt, v int64) []string {
	var out []string
	for n, val := range st.Params {
		if val == strconv.FormatInt(v, 10) {
			out = append(out, n)
		}
	}
	return out
}

// uses counts the references in sql to the Int64 parameters names.
func uses(sql string, names []string) int {
	n := 0
	for _, name := range names {
		n += strings.Count(sql, "{"+name+":Int64}")
	}
	return n
}
