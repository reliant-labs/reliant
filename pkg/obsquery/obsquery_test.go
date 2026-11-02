package obsquery

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func scope() Scope {
	return Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour)}
}

// paramRe finds every {pN:Type} placeholder.
var paramRe = regexp.MustCompile(`\{([a-z][a-z0-9]*):[A-Za-z0-9()]+\}`)

// assertParameterized fails unless every placeholder has a value, every value
// has a placeholder, and none of the hostile marker strings appears in the SQL
// text itself.
func assertParameterized(t *testing.T, st Stmt, hostile ...string) {
	t.Helper()
	used := map[string]bool{}
	for _, m := range paramRe.FindAllStringSubmatch(st.SQL, -1) {
		used[m[1]] = true
		if _, ok := st.Params[m[1]]; !ok {
			t.Errorf("placeholder %s has no value\n%s", m[1], st.SQL)
		}
	}
	for k := range st.Params {
		if !used[k] {
			t.Errorf("parameter %s is bound but never used\n%s", k, st.SQL)
		}
	}
	for _, h := range hostile {
		if strings.Contains(st.SQL, h) {
			t.Errorf("caller text %q reached the SQL text:\n%s", h, st.SQL)
		}
	}
}

func isInvalid(err error) bool { return errors.Is(err, ErrInvalid) }

func TestParseLogQueryAccepts(t *testing.T) {
	for _, q := range []string{
		``, `   `, `timeout`, `"connection refused"`, `severity:error`, `severity:>=warn`, `service:api`, `service:api*`,
		`trace_id:ABCDEF0123`, `attr.http.status:500`, `attr.user.id:*`, `res.k8s.pod.name:web-1`,
		`-severity:debug`, `NOT timeout`, `a OR b`, `(a OR b) c`, `a AND b`, `error -"health check"`,
		`body:"x y"`, `a OR b OR c d`, `((a))`, `attr.k:"quoted value"`, `"esc \" quote"`,
	} {
		if _, err := ParseLogQuery(q); err != nil {
			t.Errorf("ParseLogQuery(%q) = %v, want ok", q, err)
		}
	}
}

func TestParseLogQueryRejectsWithPosition(t *testing.T) {
	for _, tc := range []struct{ q, want string }{
		{`(a`, "unclosed '('"},
		{`a)`, "unexpected ')'"},
		{`a OR`, "unexpected end"},
		{`OR a`, "operator needs a term"},
		{`"unterminated`, "unterminated quote"},
		{`""`, "empty quoted string"},
		{`a - b`, "'-' must be directly followed"},
		{`:value`, "missing field name"},
		{`field:`, "missing value"},
		{`bogus:1`, "unknown field"},
		{`severity:loud`, "unknown severity"},
		{`a"b`, "unexpected '\"' inside a word"},
		{strings.Repeat("(", 20) + "a" + strings.Repeat(")", 20), "nested more than"},
		{strings.Repeat("a ", 60), "more than 40 terms"},
		{strings.Repeat("a", MaxQueryLen+1), "longer than"},
	} {
		_, err := ParseLogQuery(tc.q)
		if err == nil {
			// unknown field / severity are caught at compile time, not parse time
			_, err = LogQuery{Scope: scope(), Query: tc.q, Limit: 10}.Compile()
		}
		if err == nil || !isInvalid(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want InvalidArgument containing %q", tc.q, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "position") == false && tc.want != "longer than" {
			t.Errorf("%q: error %q does not name a position", tc.q, err)
		}
	}
}

func TestLogCompileParameterizesEverything(t *testing.T) {
	hostile := []string{
		"x') OR 1=1 --", "'; DROP TABLE t_org_b.otel_logs; --", "\\'", "t_org_b",
		"bad\\u0000", "Robert'); DROP", "1 OR 1=1",
	}
	for _, h := range hostile {
		quoted := `"` + strings.ReplaceAll(strings.ReplaceAll(h, `\`, `\\`), `"`, `\"`) + `"`
		q := LogQuery{
			Scope:      Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{h}, Services: []string{h, "b"}, Start: t0, End: t0.Add(time.Hour)},
			Query:      quoted + ` service:` + quoted + ` attr.k:` + quoted + ` res.k:` + quoted + ` trace_id:` + quoted,
			Severities: []string{"error"},
			Limit:      50,
		}
		st, err := q.Compile()
		if err != nil {
			t.Fatalf("hostile %q: %v", h, err)
		}
		assertParameterized(t, st, h)
	}
}

func TestLogCompileShape(t *testing.T) {
	st, err := LogQuery{Scope: scope(), Query: `timeout severity:>=warn -service:db* (attr.a:1 OR attr.b:*)`, Limit: 10}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM t_org_a.otel_logs", "positionCaseInsensitive(Body,", "upper(SeverityText) IN (", "NOT (startsWith(ServiceName,",
		"LogAttributes[", "mapContains(LogAttributes,", "ORDER BY Timestamp DESC", "LIMIT 11",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// warn and above: WARN, WARNING, ERROR, ERR, FATAL, CRITICAL, PANIC
	n := 0
	for _, v := range st.Params {
		switch v {
		case "WARN", "WARNING", "ERROR", "ERR", "FATAL", "CRITICAL", "PANIC":
			n++
		}
	}
	if n != 7 {
		t.Errorf(">=warn bound %d severity spellings, want 7: %v", n, st.Params)
	}
	old, _ := LogQuery{Scope: scope(), Oldest: true, Limit: 1}.Compile()
	if !strings.Contains(old.SQL, "ORDER BY Timestamp ASC") {
		t.Errorf("oldest-first not honoured:\n%s", old.SQL)
	}
}

func TestLogCursorRoundTripAndBinding(t *testing.T) {
	q := LogQuery{Scope: scope(), Query: "x", Limit: 10}
	tok := q.NextCursor(t0.Add(5*time.Minute), 1<<63+7)
	c, err := DecodeLogCursor(tok)
	if err != nil || c.TsNano != t0.Add(5*time.Minute).UnixNano() || c.Hash != 1<<63+7 {
		t.Fatalf("round trip = %+v, %v", c, err)
	}
	q.Cursor = c
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "(Timestamp, cityHash64(") || !strings.Contains(st.SQL, ") < (") {
		t.Errorf("cursor predicate missing:\n%s", st.SQL)
	}
	assertParameterized(t, st)

	// a token minted for another query, or another org, is refused
	other := LogQuery{Scope: scope(), Query: "y", Limit: 10, Cursor: c}
	if _, err := other.Compile(); !isInvalid(err) {
		t.Errorf("cursor reused across queries: err = %v", err)
	}
	otherOrg := LogQuery{Scope: Scope{Database: "t_org_b", Start: t0, End: t0.Add(time.Hour)}, Query: "x", Limit: 10, Cursor: c}
	if _, err := otherOrg.Compile(); !isInvalid(err) {
		t.Errorf("cursor reused across orgs: err = %v", err)
	}
	for _, bad := range []string{"!!!", "e30", "eyJmIjoiIn0"} {
		if _, err := DecodeLogCursor(bad); !isInvalid(err) {
			t.Errorf("DecodeLogCursor(%q) = %v, want InvalidArgument", bad, err)
		}
	}
	if c, err := DecodeLogCursor(""); c != nil || err != nil {
		t.Errorf("empty token = %v, %v", c, err)
	}
}

func TestScopeGuardrails(t *testing.T) {
	good := scope()
	for name, mut := range map[string]func(*Scope){
		"database injection": func(s *Scope) { s.Database = "t_org_a.otel_logs; DROP TABLE x" },
		"database dash":      func(s *Scope) { s.Database = "t-org" },
		"database empty":     func(s *Scope) { s.Database = "" },
		"zero range":         func(s *Scope) { s.End = s.Start },
		"inverted range":     func(s *Scope) { s.End = s.Start.Add(-time.Hour) },
		"unset":              func(s *Scope) { s.Start = time.Time{} },
		"too long":           func(s *Scope) { s.End = s.Start.Add(MaxRangeLogs + time.Second) },
		"too many services":  func(s *Scope) { s.Services = make([]string, 51) },
	} {
		s := good
		mut(&s)
		if _, err := (LogQuery{Scope: s, Limit: 10}).Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, err := (LogQuery{Scope: good, Limit: 0}).Compile(); !isInvalid(err) {
		t.Errorf("limit 0 accepted")
	}
	if _, err := (LogQuery{Scope: good, Limit: MaxLogLimit + 1}).Compile(); !isInvalid(err) {
		t.Errorf("limit over max accepted")
	}
	if _, err := (LogQuery{Scope: good, Limit: MaxLogLimit}).Compile(); err != nil {
		t.Errorf("limit at max rejected: %v", err)
	}
	// per-RPC maxima differ
	long := good
	long.End = long.Start.Add(20 * 24 * time.Hour)
	if _, err := (TraceQuery{Scope: long, Limit: 10}).Compile(); !isInvalid(err) {
		t.Errorf("20d traces accepted")
	}
	if _, err := (MetricCatalog{Scope: long, Limit: 10}).Compile(); err != nil {
		t.Errorf("20d metrics rejected: %v", err)
	}
	for _, id := range []string{"", "xyz", strings.Repeat("a", 65), "abc'--"} {
		if _, err := NormalizeTraceID(id); !isInvalid(err) {
			t.Errorf("NormalizeTraceID(%q) = %v", id, err)
		}
	}
}

func TestTraceStatementsParameterize(t *testing.T) {
	h := "x') OR 1=1 --"
	q := TraceQuery{
		Scope: Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{h, "e2"}, Services: []string{h}, Start: t0, End: t0.Add(time.Hour),
			Filters: []AttrFilter{
				{Key: "http.route", Op: "eq", Value: h}, {Key: "rpc.method", Op: "exists"}, {Key: "service.name", Op: "neq", Value: h},
				{Key: "k8s.workload.name", Op: "in", Values: []string{h, "b"}}, {Key: "peer.service", Op: "contains", Value: h},
				{Key: "status", Op: "eq", Value: "ERROR"}, {Key: "deployment.environment.name", Op: "eq", Value: h},
			}},
		ErrorsOnly:   true,
		RootSpanName: h, MinDurationMs: 12.5,
		GroupBy: []string{"reliant.env.id", "rpc.method"},
		Limit:   20,
	}
	for name, compile := range map[string]func() (Stmt, error){"page": q.Compile, "red": q.CompileRED, "redgroups": q.CompileREDGroups} {
		st, err := compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st, h)
		if !strings.Contains(st.SQL, "FROM t_org_a.otel_traces") {
			t.Errorf("%s: wrong table:\n%s", name, st.SQL)
		}
	}
	withFilter := func(f AttrFilter) TraceQuery {
		sc := scope()
		sc.Filters = []AttrFilter{f}
		return TraceQuery{Scope: sc, Limit: 1}
	}
	if _, err := withFilter(AttrFilter{Key: "http.route", Op: "regex"}).Compile(); !isInvalid(err) {
		t.Errorf("unknown operator accepted: %v", err)
	}
	if _, err := withFilter(AttrFilter{Key: ""}).Compile(); !isInvalid(err) {
		t.Errorf("empty key accepted")
	}
	if _, err := withFilter(AttrFilter{Key: "http.route", Op: "in"}).Compile(); !isInvalid(err) {
		t.Errorf("an in filter with no values accepted")
	}
	if _, err := withFilter(AttrFilter{Key: "peer.service", Op: "contains"}).Compile(); !isInvalid(err) {
		t.Errorf("a contains filter with no value accepted")
	}
	if _, err := (TraceQuery{Scope: scope(), Limit: 1, GroupBy: []string{"a", "b", "c", "d"}}).CompileREDGroups(); !isInvalid(err) {
		t.Errorf("4 group keys accepted")
	}
	if _, err := (TraceQuery{Scope: scope(), Limit: 1}).CompileREDGroups(); !isInvalid(err) {
		t.Errorf("red groups with no key accepted")
	}

	spans, err := TraceSpans{Scope: scope(), TraceID: "ABCDEF01"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, spans)
	if spans.Params["p3"] != "abcdef01" && !hasValue(spans.Params, "abcdef01") {
		t.Errorf("trace id not lowercased/bound: %v", spans.Params)
	}
	if _, err := (TraceSpans{Scope: scope(), TraceID: "x' OR 1=1"}).Compile(); !isInvalid(err) {
		t.Errorf("hostile trace id accepted")
	}
}

func hasValue(m map[string]string, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

func TestTraceCursorBinding(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 5}
	c, err := DecodeTraceCursor(q.NextCursor(t0, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	q.Cursor = c
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	q.ErrorsOnly = true
	if _, err := q.Compile(); !isInvalid(err) {
		t.Errorf("cursor survived a filter change: %v", err)
	}
}

func TestOverviewAndDashboardStatements(t *testing.T) {
	h := "x') OR 1=1 --"
	sc := Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{h}, Services: []string{h}, Start: t0, End: t0.Add(time.Hour),
		Filters: []AttrFilter{{Key: "k8s.pod.name", Op: "eq", Value: h}}}
	o := Overview{Scope: sc, GroupBy: []string{"reliant.env.id", "k8s.workload.name"}}
	for name, f := range map[string]func() (Stmt, error){
		"red": o.RED, "logs": o.Logs, "seen": o.SeenServices, "trend": func() (Stmt, error) { return o.Trend(60) },
	} {
		st, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertParameterized(t, st, h)
	}
	if got := StepFor(time.Hour, 48, 60); got != 120 {
		t.Errorf("StepFor(1h,48,60) = %d, want 120", got)
	}
	if got := StepFor(7*24*time.Hour, 120, 1); got != 7200 {
		t.Errorf("StepFor(7d,120) = %d, want 7200", got)
	}
}

func TestMetricResolveAgg(t *testing.T) {
	gauge := MetricInfo{Type: MetricGauge}
	counter := MetricInfo{Type: MetricSum, Monotonic: true, Temporality: TemporalityCumulative}
	updown := MetricInfo{Type: MetricSum}
	hist := MetricInfo{Type: MetricHistogram}
	for _, tc := range []struct {
		name    string
		info    MetricInfo
		in, out Agg
		bad     bool
	}{
		{"gauge default", gauge, AggDefault, AggAvg, false},
		{"counter default", counter, AggDefault, AggRate, false},
		{"up-down default is the sum of last values", updown, AggDefault, AggSum, false},
		{"histogram default is p95, not avg", hist, AggDefault, AggP95, false},
		{"sum on a monotonic sum is its increase", counter, AggSum, AggIncrease, false},
		{"sum on an up-down stays a level", updown, AggSum, AggSum, false},
		{"up-down rate refused", updown, AggRate, 0, true},
		{"up-down increase refused", updown, AggIncrease, 0, true},
		{"sum count refused", counter, AggCount, 0, true},
		{"gauge rate refused", gauge, AggRate, 0, true},
		{"gauge increase refused", gauge, AggIncrease, 0, true},
		{"gauge count is data points", gauge, AggCount, AggCount, false},
		{"gauge percentile refused", gauge, AggP95, 0, true},
		{"sum percentile refused", counter, AggQuantile, 0, true},
		{"gauge last", gauge, AggLast, AggLast, false},
		{"counter last allowed", counter, AggLast, AggLast, false},
		{"histogram any quantile", hist, AggQuantile, AggQuantile, false},
		{"histogram p99 alias", hist, AggP99, AggP99, false},
		{"histogram rate", hist, AggRate, AggRate, false},
		{"histogram count", hist, AggCount, AggCount, false},
		{"histogram sum", hist, AggSum, AggSum, false},
		{"histogram min", hist, AggMin, AggMin, false},
		{"histogram last refused", hist, AggLast, 0, true},
		{"unknown kind", MetricInfo{}, AggDefault, 0, true},
	} {
		got, err := ResolveAgg(tc.info, tc.in)
		if tc.bad != (err != nil) || (!tc.bad && got != tc.out) {
			t.Errorf("%s: ResolveAgg = %v, %v", tc.name, got, err)
		}
		if tc.bad && !isInvalid(err) {
			t.Errorf("%s: error is not InvalidArgument: %v", tc.name, err)
		}
	}
}

func TestStepForMetric(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rng       time.Duration
		requested int32
		interval  int64
		step      int64
		raised    bool
	}{
		{"1h at a 60s export is never 30s by default", time.Hour, 0, 60, 60, false},
		{"15m at a 60s export is not the 10s the spike read 6x high at", 15 * time.Minute, 0, 60, 60, false},
		{"an explicit step below the interval is raised and said so", time.Hour, 10, 60, 60, true},
		{"a step above the interval is honoured", time.Hour, 300, 60, 300, false},
		{"no interval known: the 1500-point floor only", time.Hour, 0, 0, 30, false},
		{"a 30s infra metric allows 30s", time.Hour, 0, 30, 30, false},
		{"a 30d range at 1s is raised by the point cap", 30 * 24 * time.Hour, 1, 60, 1800, true},
		{"the raised step is a nice one", time.Hour, 20, 45, 60, true},
	} {
		step, raised := StepForMetric(tc.rng, tc.requested, tc.interval)
		if step != tc.step || raised != tc.raised {
			t.Errorf("%s: StepForMetric(%v, %d, %d) = %d, %v; want %d, %v", tc.name, tc.rng, tc.requested, tc.interval, step, raised, tc.step, tc.raised)
		}
	}
}

func TestMetricQueryCompile(t *testing.T) {
	h := "x') OR 1=1 --"
	infos := map[MetricType]MetricInfo{
		MetricGauge:     {Type: MetricGauge, IntervalSeconds: 60},
		MetricSum:       {Type: MetricSum, Monotonic: true, Temporality: TemporalityCumulative, IntervalSeconds: 60},
		MetricHistogram: {Type: MetricHistogram, Temporality: TemporalityCumulative, IntervalSeconds: 60},
	}
	for _, ty := range []MetricType{MetricGauge, MetricSum, MetricHistogram} {
		for _, agg := range []Agg{AggDefault, AggAvg, AggSum, AggMin, AggMax, AggLast, AggCount, AggIncrease, AggRate, AggP50, AggP95, AggQuantile} {
			for _, reduce := range []bool{false, true} {
				for _, series := range []int64{3, 500} {
					info := infos[ty]
					info.Series = series
					if _, err := ResolveAgg(info, agg); err != nil {
						continue
					}
					q := MetricQuery{
						Scope: Scope{Database: "t_org_a", EnvRestricted: true, EnvIDs: []string{h}, Services: []string{h}, Start: t0, End: t0.Add(time.Hour),
							Declared: map[string]bool{"pod": true},
							Filters:  []AttrFilter{{Key: "pod", Op: "eq", Value: h}, {Key: "k8s.container.name", Op: "in", Values: []string{h}}}},
						Name: h, Info: info, Agg: agg, Quantile: 0.9, GroupBy: []string{"pod", "service.name"},
						Where:       []PanelPredicate{{Field: "metric.attr.k", Op: "eq", Value: h}},
						StepSeconds: 60, Reduce: reduce,
					}
					st, err := q.Compile()
					if err != nil {
						t.Fatalf("type %v agg %v reduce %v: %v", ty, agg, reduce, err)
					}
					assertParameterized(t, st, h)
					if !strings.Contains(st.SQL, "FROM t_org_a."+ty.table()) {
						t.Errorf("type %v: wrong table:\n%s", ty, st.SQL)
					}
					if capped := series > MaxMetricSeries; capped != strings.Contains(st.SQL, "LIMIT 50)") {
						t.Errorf("type %v agg %v: series %d: cap in SQL = %v:\n%s", ty, agg, series, !capped, st.SQL)
					}
				}
			}
		}
	}
	base := MetricQuery{Scope: scope(), Name: "m", Info: MetricInfo{Type: MetricGauge}, Agg: AggAvg, StepSeconds: 60}
	for name, mut := range map[string]func(*MetricQuery){
		"no name":                func(q *MetricQuery) { q.Name = "" },
		"4 group keys":           func(q *MetricQuery) { q.GroupBy = []string{"a", "b", "c", "d"} },
		"no step":                func(q *MetricQuery) { q.StepSeconds = 0 },
		"too many pts":           func(q *MetricQuery) { q.StepSeconds = 1; q.Scope.End = q.Scope.Start.Add(3 * time.Hour) },
		"no type":                func(q *MetricQuery) { q.Info.Type = MetricUnknown },
		"empty group":            func(q *MetricQuery) { q.GroupBy = []string{""} },
		"hist pct on gauge":      func(q *MetricQuery) { q.Agg = AggP95 },
		"quantile out of range":  func(q *MetricQuery) { q.Info.Type, q.Agg, q.Quantile = MetricHistogram, AggQuantile, 1.5 },
		"quantile level missing": func(q *MetricQuery) { q.Info.Type, q.Agg = MetricHistogram, AggQuantile },
		"negative cap":           func(q *MetricQuery) { q.MaxSeries = -1 },
		"up-down rate":           func(q *MetricQuery) { q.Info.Type, q.Agg = MetricSum, AggRate },
	} {
		q := base
		mut(&q)
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, err := (MetricCatalog{Scope: scope(), Limit: ListMetricsMax + 1}).Compile(); !isInvalid(err) {
		t.Errorf("metric list over max accepted")
	}
	st, err := MetricLookup{Scope: scope(), Name: h}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, h)
}

func TestHistogramQuantile(t *testing.T) {
	bounds := []float64{10, 20, 50}
	// 100 observations: 50 in (0,10], 30 in (10,20], 15 in (20,50], 5 above
	counts := []float64{50, 30, 15, 5}
	for _, tc := range []struct{ q, want float64 }{
		{0.25, 5},  // half way through the first bucket
		{0.5, 10},  // the edge of the first bucket
		{0.65, 15}, // half way through the second
		{0.99, 50}, // the open top bucket reports the last bound
	} {
		got, ok := HistogramQuantile(bounds, counts, tc.q)
		if !ok || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("q%.2f = %v, %v want %v", tc.q, got, ok, tc.want)
		}
	}
	if _, ok := HistogramQuantile(bounds, []float64{0, 0, 0, 0}, 0.5); ok {
		t.Error("an empty histogram has no quantile")
	}
	if _, ok := HistogramQuantile(bounds, []float64{1, 2}, 0.5); ok {
		t.Error("mismatched bucket count accepted")
	}
	if got, ok := HistogramQuantile(nil, []float64{4}, 0.5); !ok || got != 0 {
		t.Errorf("boundless histogram = %v, %v", got, ok)
	}
}

func TestFlamegraphUsesForgesSQLOnTheOrgDatabase(t *testing.T) {
	h := "x') OR 1=1 --"
	for _, trace := range []string{"", "ABC123"} {
		fg := Flamegraph{Scope: scope(), Service: h, SampleType: h, TraceID: trace}
		st, err := fg.Compile()
		if err != nil {
			t.Fatal(err)
		}
		assertParameterized(t, st, h)
		if strings.Contains(st.SQL, "default.") || strings.Contains(st.SQL, "FORMAT") {
			t.Errorf("profilequery SQL not rewritten:\n%s", st.SQL)
		}
		want := "t_org_a.profile_samples_1m"
		if trace != "" {
			want = "t_org_a.otel_profiles"
		}
		if !strings.Contains(st.SQL, want) {
			t.Errorf("trace=%q: SQL lacks %s:\n%s", trace, want, st.SQL)
		}
		if !strings.Contains(st.SQL, "LIMIT 5001") {
			t.Errorf("no stack cap:\n%s", st.SQL)
		}
	}
	for _, bad := range []Flamegraph{
		{Scope: scope(), SampleType: "cpu"},
		{Scope: scope(), Service: "s"},
		{Scope: scope(), Service: "s", SampleType: "cpu", TraceID: "not hex!"},
		{Scope: Scope{Database: "default", Start: t0, End: t0.Add(time.Hour)}, Service: "s", SampleType: "cpu"},
	} {
		if bad.Scope.Database == "default" {
			// "default" is a plain identifier: the rewrite must still refuse to run a statement that names it
			if _, err := bad.Compile(); err == nil {
				t.Errorf("database 'default' accepted")
			}
			continue
		}
		if _, err := bad.Compile(); !isInvalid(err) {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
	pt, err := Flamegraph{Scope: scope(), Service: h}.ProfileTypes()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, pt, h)
}

func TestOperatorSource(t *testing.T) {
	for s, want := range map[string]bool{"daemon": true, "platform": true, "gateway": true, "env": false, "": false, "other": false} {
		if OperatorSource(s) != want {
			t.Errorf("OperatorSource(%q) != %v", s, want)
		}
	}
}

func TestTraceOrderDurationDesc(t *testing.T) {
	newest, err := TraceQuery{Scope: scope(), Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(newest.SQL, "ORDER BY start DESC, trace_id DESC") {
		t.Errorf("default order is not newest first:\n%s", newest.SQL)
	}

	q := TraceQuery{Scope: scope(), Limit: 5, Order: OrderDurationDesc}
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "ORDER BY "+traceDurNanoExpr+" DESC, start DESC, trace_id DESC") {
		t.Errorf("not slowest first:\n%s", st.SQL)
	}

	// The cursor continues the slowest-first order by (duration, start, id), and
	// is refused by a query of the other order.
	tok := q.NextDurationCursor(t0, "abc", 1234.5)
	c, err := DecodeTraceCursor(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.DurNano != 1234500000 {
		t.Errorf("DurNano = %d, want 1234500000", c.DurNano)
	}
	q.Cursor = c
	paged, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, paged)
	if !strings.Contains(paged.SQL, "("+traceDurNanoExpr+", start, trace_id) < (") {
		t.Errorf("duration keyset missing:\n%s", paged.SQL)
	}
	other := TraceQuery{Scope: scope(), Limit: 5, Cursor: c}
	if _, err := other.Compile(); !isInvalid(err) {
		t.Errorf("a duration cursor continued a newest-first query: %v", err)
	}

	if _, err := (TraceQuery{Scope: scope(), Limit: 5, Order: "sideways"}).Compile(); !isInvalid(err) {
		t.Errorf("unknown order accepted: %v", err)
	}
}

// A newest-first token must not change when the order knob is added: tokens
// minted by a release without Order keep working through a rollout.
func TestTraceNewestCursorFingerprintIsUnchangedByOrder(t *testing.T) {
	q := TraceQuery{Scope: scope(), Limit: 5}
	want := fingerprint(append(q.Scope.Fingerprint(), "false", "0", "")...)
	if got := q.Fingerprint(); got != want {
		t.Errorf("newest-first fingerprint changed: %q != %q", got, want)
	}
}

// An errors-only or root-name filter narrows the traces before the per-trace
// aggregation, so a wide window does not group every span in it; with neither
// filter there is nothing to narrow by.
func TestTraceQueryPrefiltersCandidateTraces(t *testing.T) {
	q := TraceQuery{Scope: scope(), ErrorsOnly: true, RootSpanName: "ListProcesses", Limit: 10}
	for name, compile := range map[string]func() (Stmt, error){"page": q.Compile, "red": q.CompileRED} {
		st, err := compile()
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(st.SQL, "TraceId IN (SELECT TraceId FROM t_org_a.otel_traces"); n != 2 {
			t.Errorf("%s: %d candidate prefilters, want 2:\n%s", name, n, st.SQL)
		}
		if !strings.Contains(st.SQL, "HAVING error_count > 0") || !strings.Contains(st.SQL, "positionCaseInsensitive(root_name, ") {
			t.Errorf("%s: the exact per-trace predicates must stay:\n%s", name, st.SQL)
		}
	}
	st, err := TraceQuery{Scope: scope(), Limit: 10}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, "TraceId IN (") {
		t.Errorf("an unfiltered page has nothing to prefilter by:\n%s", st.SQL)
	}
}

// A streaming RPC's server span lasts as long as the stream, so the RED
// latency quantiles are over unary spans only, while requests count both.
func TestREDLatencyExcludesStreams(t *testing.T) {
	st, err := Overview{Scope: scope()}.RED()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "quantilesExactWeightedIf(0.5, 0.95, 0.99)(Duration / 1000000, ") || !strings.Contains(st.SQL, "NOT "+spanStreamExpr) {
		t.Errorf("latency is not limited to unary spans:\n%s", st.SQL)
	}
	if !strings.Contains(spanStreamExpr, "'"+StreamTypeAttr+"'") {
		t.Errorf("stream predicate does not read %s", StreamTypeAttr)
	}
}
