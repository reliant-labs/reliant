package obsquery

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// catalogTestRows is a Rows over canned values; Scan assigns by reflection so the
// parse functions are tested against the dest types the ClickHouse driver is
// given.
type catalogTestRows struct {
	rows [][]any
	i    int
	err  error
}

func (r *catalogTestRows) Next() bool { r.i++; return r.i <= len(r.rows) }
func (r *catalogTestRows) Err() error { return r.err }
func (r *catalogTestRows) Close() error {
	return nil
}
func (r *catalogTestRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	if len(row) != len(dest) {
		return fmt.Errorf("scan: %d columns into %d dests", len(row), len(dest))
	}
	for i, d := range dest {
		dv := reflect.ValueOf(d)
		if dv.Kind() != reflect.Pointer {
			return fmt.Errorf("scan: dest %d is not a pointer", i)
		}
		sv := reflect.ValueOf(row[i])
		if !sv.Type().AssignableTo(dv.Elem().Type()) {
			return fmt.Errorf("scan: column %d is %s, dest is %s", i, sv.Type(), dv.Elem().Type())
		}
		dv.Elem().Set(sv)
	}
	return nil
}

func TestCatalogKindNumbersMatchTheWire(t *testing.T) {
	// cp's MetricType enum and obsquery's MetricType share 1..3.
	for k, want := range map[CatalogKind]int{
		CatalogGauge: 1, CatalogSum: 2, CatalogHistogram: 3, CatalogExpHistogram: 4, CatalogSummary: 5,
	} {
		if int(k) != want {
			t.Errorf("%s = %d, want %d", k, int(k), want)
		}
	}
	if int(CatalogGauge) != int(MetricGauge) || int(CatalogSum) != int(MetricSum) || int(CatalogHistogram) != int(MetricHistogram) {
		t.Error("CatalogKind and MetricType disagree on gauge/sum/histogram")
	}
}

func TestMetricCatalogCompile(t *testing.T) {
	const hostile = "x') OR 1=1 --"
	st, err := MetricCatalog{Scope: scope(), NameContains: hostile, Limit: 25}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, hostile)
	for _, tbl := range []string{
		"t_org_a.otel_metrics_gauge", "t_org_a.otel_metrics_sum", "t_org_a.otel_metrics_histogram",
		"t_org_a.otel_metrics_exponential_histogram", "t_org_a.otel_metrics_summary",
	} {
		if !strings.Contains(st.SQL, tbl) {
			t.Errorf("catalog does not read %s:\n%s", tbl, st.SQL)
		}
	}
	if n := strings.Count(st.SQL, "UNION ALL"); n != 4 {
		t.Errorf("%d UNION ALLs, want 4 (five tables)", n)
	}
	if !strings.HasSuffix(st.SQL, "LIMIT 26") {
		t.Errorf("limit+1 missing:\n%s", st.SQL)
	}
	for _, col := range []string{"uniqCombined(cityHash64(ResourceAttributes, Attributes))", "groupUniqArray(10)(ServiceName)", "max(TimeUnix)"} {
		if !strings.Contains(st.SQL, col) {
			t.Errorf("catalog is missing %s", col)
		}
	}
}

func TestMetricCatalogClampsTheRangeToTheLast24Hours(t *testing.T) {
	long := scopeWith(func(s *Scope) { s.End = t0.Add(30 * 24 * time.Hour); s.Start = t0 })
	st, err := MetricCatalog{Scope: long, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	wantStart := strconv.FormatInt(long.End.Add(-24*time.Hour).Unix(), 10)
	wantEnd := strconv.FormatInt(long.End.Unix(), 10)
	var gotStart, gotEnd bool
	for _, v := range st.Params {
		gotStart = gotStart || v == wantStart
		gotEnd = gotEnd || v == wantEnd
		if v == strconv.FormatInt(t0.Unix(), 10) {
			t.Errorf("the unclamped start %s was bound", v)
		}
	}
	if !gotStart || !gotEnd {
		t.Errorf("params %v lack the clamped window [%s, %s)", st.Params, wantStart, wantEnd)
	}
}

func TestCatalogRange(t *testing.T) {
	short := scopeWith(nil)
	if got := CatalogRange(short); !got.Start.Equal(short.Start) || !got.End.Equal(short.End) {
		t.Errorf("a 1h range changed: %v - %v", got.Start, got.End)
	}
	exact := scopeWith(func(s *Scope) { s.End = s.Start.Add(24 * time.Hour) })
	if got := CatalogRange(exact); !got.Start.Equal(exact.Start) {
		t.Error("a 24h range changed")
	}
	long := scopeWith(func(s *Scope) { s.End = s.Start.Add(72 * time.Hour) })
	got := CatalogRange(long)
	if !got.End.Equal(long.End) || !got.Start.Equal(long.End.Add(-24*time.Hour)) {
		t.Errorf("72h range clamped to %v - %v", got.Start, got.End)
	}
	if again := CatalogRange(got); !again.Start.Equal(got.Start) {
		t.Error("CatalogRange is not idempotent")
	}
}

func TestMetricCatalogRefusals(t *testing.T) {
	for name, q := range map[string]MetricCatalog{
		"no limit":       {Scope: scope()},
		"limit too big":  {Scope: scope(), Limit: ListMetricsMax + 1},
		"long filter":    {Scope: scope(), Limit: 5, NameContains: strings.Repeat("a", 129)},
		"bad database":   {Scope: scopeWith(func(s *Scope) { s.Database = "x; DROP" }), Limit: 5},
		"no range":       {Scope: Scope{Database: "t_org_a"}, Limit: 5},
		"range over 30d": {Scope: scopeWith(func(s *Scope) { s.End = s.Start.Add(31 * 24 * time.Hour) }), Limit: 5},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, err := (MetricCatalog{Scope: scope(), Limit: ListMetricsMax}).Compile(); err != nil {
		t.Errorf("limit at max rejected: %v", err)
	}
}

func TestMetricCatalogAppliesResourceFiltersOnly(t *testing.T) {
	sc := scopeWith(func(s *Scope) {
		s.EnvRestricted, s.EnvIDs = true, []string{"env-1"}
		s.Services = []string{"api"}
		s.Filters = []AttrFilter{
			{Key: "k8s.pod.name", Op: "eq", Value: "pod-resource-filter"},
			{Key: "http.route", Op: "eq", Value: "/span-level-filter"},
		}
	})
	st, err := MetricCatalog{Scope: sc, Limit: 5}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	var sawRes bool
	for _, v := range st.Params {
		sawRes = sawRes || v == "pod-resource-filter"
		if v == "/span-level-filter" {
			t.Error("a data-point filter narrowed the catalog")
		}
	}
	if !sawRes {
		t.Errorf("the resource filter was dropped: %v", st.Params)
	}
	empty := scopeWith(func(s *Scope) { s.EnvRestricted = true })
	st, err = MetricCatalog{Scope: empty, Limit: 5}.Compile()
	if err != nil || !strings.Contains(st.SQL, "1 = 0") {
		t.Errorf("an empty restricted env list must match nothing: %v\n%s", err, st.SQL)
	}
}

func catalogRow(name string, kind CatalogKind, unit, desc string, mono uint8, temp int32, svcs []string, nsvc uint64, last time.Time, series uint64) []any {
	return []any{name, uint8(kind), unit, desc, mono, temp, svcs, nsvc, last, series}
}

func TestParseCatalog(t *testing.T) {
	last := t0.Add(time.Minute)
	rows := &catalogTestRows{rows: [][]any{
		catalogRow("a.counter", CatalogSum, "{call}", "calls", 1, 2, []string{"web", "api"}, 12, last, 40),
		catalogRow("go.memory.used", CatalogSum, "By", "", 0, 2, []string{"api"}, 1, last, 3),
		catalogRow("lat", CatalogExpHistogram, "s", "", 0, 1, nil, 0, last, 7),
		catalogRow("lat", CatalogHistogram, "s", "", 0, 1, nil, 0, last, 7),
		catalogRow("q", CatalogSummary, "", "", 0, 0, nil, 0, last, 1),
		catalogRow("g", CatalogGauge, "1", "", 0, 0, nil, 0, last, 1),
	}}
	got, trunc, err := ParseCatalog(rows, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !trunc || len(got) != 5 {
		t.Fatalf("len %d truncated %v, want 5 and true (limit+1 rows)", len(got), trunc)
	}
	c := got[0]
	if c.Name != "a.counter" || c.Kind != CatalogSum || !c.Monotonic || c.Temporality != TemporalityCumulative ||
		c.ServiceCount != 12 || c.SeriesEstimate != 40 || !c.LastSeen.Equal(last) || !c.Seen || c.Declared {
		t.Errorf("entry mapped wrong: %+v", c)
	}
	if !reflect.DeepEqual(c.Services, []string{"api", "web"}) {
		t.Errorf("services are not sorted: %v", c.Services)
	}
	if c.Builtin || !got[1].Builtin {
		t.Errorf("builtin: a.counter=%v go.memory.used=%v", c.Builtin, got[1].Builtin)
	}
	for i, wantQ := range []bool{true, true, true, true, true} {
		if got[i].Queryable != wantQ {
			t.Errorf("%s (%s) queryable = %v, want %v", got[i].Name, got[i].Kind, got[i].Queryable, wantQ)
		}
	}
	if got[2].Temporality != TemporalityDelta {
		t.Errorf("temporality = %v, want delta", got[2].Temporality)
	}

	all, trunc, err := ParseCatalog(&catalogTestRows{rows: rows.rows[:5]}, 5)
	if err != nil || trunc || len(all) != 5 {
		t.Errorf("exactly limit rows: len %d truncated %v err %v", len(all), trunc, err)
	}
	empty, trunc, err := ParseCatalog(&catalogTestRows{}, 5)
	if err != nil || trunc || empty == nil || len(empty) != 0 {
		t.Errorf("no rows must be an empty non-nil slice: %#v %v %v", empty, trunc, err)
	}
}

func TestParseCatalogSurfacesErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, _, err := ParseCatalog(&catalogTestRows{rows: [][]any{{"x"}}}, 5); err == nil {
		t.Error("a scan error was swallowed")
	}
	if _, _, err := ParseCatalog(&catalogTestRows{err: boom}, 5); !errors.Is(err, boom) {
		t.Errorf("rows.Err = %v, want boom", err)
	}
}

func TestMergeDeclared(t *testing.T) {
	last := t0
	obs := func(name string, k CatalogKind, desc string) CatalogEntry {
		return CatalogEntry{Name: name, Kind: k, Description: desc, Unit: "By", Seen: true, Queryable: k.Queryable(), LastSeen: last, SeriesEstimate: 9}
	}
	observed := []CatalogEntry{
		obs("both", CatalogSum, "sdk text"),
		obs("seen.only", CatalogGauge, "sdk text"),
		obs("dep", CatalogHistogram, "sdk text"),
		obs("rpc.server.calls", CatalogSum, ""),
	}
	declared := []metrics.Schema{{
		Service: "api",
		Metrics: []metrics.Spec{
			{Name: "both", Type: metrics.Counter, Unit: "{x}", Description: "authored text", Stability: metrics.Stable,
				Attributes: []metrics.Attribute{{Key: "code", Values: []string{"ok", "err"}}, {Key: "route"}}},
			{Name: "declared.only", Type: metrics.UpDownCounter, Unit: "{y}", Description: "never emitted", Stability: metrics.Experimental},
			{Name: "dep", Type: metrics.Histogram, Description: "old", Stability: metrics.Deprecated},
			{Name: "dep.unseen", Type: metrics.Gauge, Description: "gone", Stability: metrics.Deprecated},
			{Name: "rpc.server.calls", Type: metrics.Counter, Stability: metrics.Stable},
		},
	}}
	snapshot := append([]CatalogEntry(nil), observed...)
	got := MergeDeclared(observed, declared)
	if !reflect.DeepEqual(observed, snapshot) {
		t.Error("MergeDeclared modified its input")
	}
	by := map[string]CatalogEntry{}
	for _, e := range got {
		by[e.Name] = e
	}
	if len(got) != 6 {
		t.Fatalf("%d entries, want 6: %+v", len(got), got)
	}

	b := by["both"]
	if !b.Declared || !b.Seen || b.Description != "authored text" || b.Stability != "stable" || b.Unit != "By" || b.SeriesEstimate != 9 {
		t.Errorf("seen+declared: %+v", b)
	}
	wantAttrs := []metrics.Attribute{{Key: "code", Values: []string{"ok", "err"}}, {Key: "route"}}
	if !reflect.DeepEqual(b.DeclaredAttributes, wantAttrs) {
		t.Errorf("declared attributes = %+v", b.DeclaredAttributes)
	}

	s := by["seen.only"]
	if s.Declared || !s.Seen || s.Description != "sdk text" || s.Stability != "" || len(s.DeclaredAttributes) != 0 {
		t.Errorf("seen only: %+v", s)
	}

	d := by["declared.only"]
	if !d.Declared || d.Seen || d.Kind != CatalogSum || d.Monotonic || d.Unit != "{y}" || d.Description != "never emitted" ||
		d.Stability != "experimental" || !d.LastSeen.IsZero() || d.SeriesEstimate != 0 || d.Builtin {
		t.Errorf("declared only: %+v", d)
	}
	if !by["rpc.server.calls"].Builtin || !by["rpc.server.calls"].Declared {
		t.Errorf("builtin: %+v", by["rpc.server.calls"])
	}

	if e := by["dep"]; !e.Declared || !e.Seen || e.Stability != "deprecated" {
		t.Errorf("deprecated seen is not kept and flagged: %+v", e)
	}
	if e := by["dep.unseen"]; !e.Declared || e.Seen || e.Stability != "deprecated" || e.Kind != CatalogGauge {
		t.Errorf("deprecated unseen is not kept and flagged: %+v", e)
	}
	if c := by["declared.only"]; c.Kind != CatalogSum || !c.Queryable {
		t.Errorf("declared-only sum should be queryable: %+v", c)
	}

	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		if a.Name > b.Name || (a.Name == b.Name && a.Kind > b.Kind) {
			t.Errorf("not ordered by name, kind at %d: %s/%s then %s/%s", i, a.Name, a.Kind, b.Name, b.Kind)
		}
	}
}

func TestMergeDeclaredMapsEveryInstrumentType(t *testing.T) {
	got := MergeDeclared(nil, []metrics.Schema{{Metrics: []metrics.Spec{
		{Name: "c", Type: metrics.Counter}, {Name: "u", Type: metrics.UpDownCounter},
		{Name: "h", Type: metrics.Histogram}, {Name: "g", Type: metrics.Gauge},
	}}})
	want := map[string]struct {
		k    CatalogKind
		mono bool
	}{"c": {CatalogSum, true}, "u": {CatalogSum, false}, "h": {CatalogHistogram, false}, "g": {CatalogGauge, false}}
	for _, e := range got {
		w := want[e.Name]
		if e.Kind != w.k || e.Monotonic != w.mono {
			t.Errorf("%s: kind %s monotonic %v, want %s %v", e.Name, e.Kind, e.Monotonic, w.k, w.mono)
		}
	}
	if len(got) != 4 {
		t.Errorf("%d entries", len(got))
	}
}

func TestMergeDeclaredFirstDeclarationWins(t *testing.T) {
	got := MergeDeclared(nil, []metrics.Schema{
		{Service: "a", Metrics: []metrics.Spec{{Name: "m", Type: metrics.Counter, Description: "from a", Stability: metrics.Stable}}},
		{Service: "b", Metrics: []metrics.Spec{{Name: "m", Type: metrics.Counter, Description: "from b", Stability: metrics.Deprecated}}},
	})
	if len(got) != 1 || got[0].Description != "from a" {
		t.Errorf("a metric declared by two schemas must appear once, first wins: %+v", got)
	}
}

func TestIsBuiltinMetric(t *testing.T) {
	for name, want := range map[string]bool{
		"rpc.server.calls": true, "go.memory.used": true, "forge.metrics.rejected_attributes": true,
		"orders_created": false, "rpcx.thing": false, "k8s.container.ready": false, "": false, "go": false,
	} {
		if got := IsBuiltinMetric(name); got != want {
			t.Errorf("IsBuiltinMetric(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMetricAttributesCompile(t *testing.T) {
	const hostile = "n') OR 1=1 --"
	for _, k := range []CatalogKind{CatalogGauge, CatalogSum, CatalogHistogram, CatalogExpHistogram, CatalogSummary} {
		st, err := MetricAttributes{Scope: scope(), Name: hostile, Kind: k}.Compile()
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		assertParameterized(t, st, hostile)
		if !strings.Contains(st.SQL, "t_org_a."+k.table()) {
			t.Errorf("%s: wrong table:\n%s", k, st.SQL)
		}
		for _, frag := range []string{"uniqCombined", "mapKeys(Attributes)", "mapKeys(ResourceAttributes)", "LIMIT 200"} {
			if !strings.Contains(st.SQL, frag) {
				t.Errorf("%s: missing %q:\n%s", k, frag, st.SQL)
			}
		}
	}
	long := scopeWith(func(s *Scope) { s.End = s.Start.Add(10 * 24 * time.Hour) })
	st, err := MetricAttributes{Scope: long, Name: "m", Kind: CatalogSum}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.FormatInt(long.End.Add(-24*time.Hour).Unix(), 10); !hasValue(st.Params, want) {
		t.Errorf("attribute scan is not clamped to 24h: %v", st.Params)
	}
	for name, q := range map[string]MetricAttributes{
		"no name":      {Scope: scope(), Kind: CatalogSum},
		"long name":    {Scope: scope(), Name: strings.Repeat("a", 257), Kind: CatalogSum},
		"unknown kind": {Scope: scope(), Name: "m"},
		"bad database": {Scope: scopeWith(func(s *Scope) { s.Database = "a b" }), Name: "m", Kind: CatalogSum},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
}

func attrRow(key string, resource uint8, d uint64) []any { return []any{key, resource, d} }

func TestParseAttributes(t *testing.T) {
	spec := &metrics.Spec{Name: "m", Attributes: []metrics.Attribute{
		{Key: "code", Values: []string{"ok", "err", "x"}}, {Key: "route"},
	}}
	rows := &catalogTestRows{rows: [][]any{
		attrRow("code", 0, 3),
		attrRow("route", 0, 30),
		attrRow("user", 0, 500),
		attrRow("pod", 1, 12),
		attrRow("service.name", 1, 2),
		attrRow("one", 0, 1),
	}}
	got, err := ParseAttributes(rows, spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AttributeStat{}
	for _, s := range got {
		by[s.Key] = s
	}
	if !reflect.DeepEqual(by["code"].Enumerated, []string{"ok", "err", "x"}) || by["code"].HighCardinality {
		t.Errorf("code: %+v", by["code"])
	}
	if by["route"].HighCardinality || len(by["route"].Enumerated) != 0 {
		t.Errorf("route (30, open): %+v", by["route"])
	}
	if !by["user"].HighCardinality {
		t.Errorf("user (500, open) must be high cardinality: %+v", by["user"])
	}
	if !by["pod"].Resource || by["pod"].HighCardinality || by["code"].Resource {
		t.Errorf("resource flag: pod=%+v code=%+v", by["pod"], by["code"])
	}
	// best breakdown first: enumerated, then 2..50, then the rest
	order := make([]string, len(got))
	for i, s := range got {
		order[i] = s.Key
	}
	want := []string{"code", "route", "pod", "service.name", "one", "user"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if sb := SuggestedBreakdowns(got); !reflect.DeepEqual(sb, []string{"code", "route", "pod", "service.name"}) {
		t.Errorf("suggested = %v", sb)
	}
}

func TestParseAttributesHighCardinalityIsRelativeToTheRestOfTheGroupBy(t *testing.T) {
	rows := func() *catalogTestRows { return &catalogTestRows{rows: [][]any{attrRow("a", 0, 10)}} }
	for _, tc := range []struct {
		other int64
		want  bool
	}{{0, false}, {1, false}, {5, false}, {6, true}, {1000, true}} {
		got, err := ParseAttributes(rows(), nil, tc.other)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].HighCardinality != tc.want {
			t.Errorf("distinct 10 x other %d: high = %v, want %v", tc.other, got[0].HighCardinality, tc.want)
		}
	}
	// enumerated keys are exempt from the "more than 50 and open" rule only
	spec := &metrics.Spec{Attributes: []metrics.Attribute{{Key: "a", Values: []string{"x"}}}}
	got, _ := ParseAttributes(&catalogTestRows{rows: [][]any{attrRow("a", 0, 60)}}, spec, 1)
	if !got[0].HighCardinality {
		t.Error("60 distinct x 1 exceeds the series cap even when enumerated")
	}
}

func TestParseAttributesCapsKeys(t *testing.T) {
	rows := &catalogTestRows{}
	for i := 0; i < MaxDeclaredKeys+30; i++ {
		rows.rows = append(rows.rows, attrRow(fmt.Sprintf("k%03d", i), 0, 2))
	}
	got, err := ParseAttributes(rows, nil, 1)
	if err != nil || len(got) != MaxDeclaredKeys {
		t.Errorf("len %d err %v, want %d", len(got), err, MaxDeclaredKeys)
	}
	if _, err := ParseAttributes(&catalogTestRows{rows: [][]any{{"k"}}}, nil, 1); err == nil {
		t.Error("a scan error was swallowed")
	}
}

func TestParseExemplarsAndMarkStored(t *testing.T) {
	rows := &catalogTestRows{rows: [][]any{
		{t0.Add(3 * time.Second), 0.9, "AAAA", "s1", map[string]string{"k": "v"}},
		{t0.Add(2 * time.Second), 0.8, "bbbb", "s2", map[string]string(nil)},
		{t0.Add(1 * time.Second), 0.7, "cccc", "s3", map[string]string{}},
	}}
	got, err := ParseExemplars(rows)
	if err != nil || len(got) != 3 {
		t.Fatalf("len %d err %v", len(got), err)
	}
	if got[0].TraceID != "aaaa" || got[0].SpanID != "s1" || got[0].Value != 0.9 || got[0].Attributes["k"] != "v" || !got[0].Time.Equal(t0.Add(3*time.Second)) {
		t.Errorf("row 0: %+v", got[0])
	}
	if got[1].Attributes == nil {
		t.Error("attributes must be a non-nil map")
	}
	marked := MarkStored(got, map[string]bool{"cccc": true, "bbbb": true})
	order := []string{marked[0].TraceID, marked[1].TraceID, marked[2].TraceID}
	if !reflect.DeepEqual(order, []string{"bbbb", "cccc", "aaaa"}) || !marked[0].Stored || !marked[1].Stored || marked[2].Stored {
		t.Errorf("stored first, then newest: %+v", marked)
	}
	if got[0].Stored || got[1].Stored {
		t.Error("MarkStored modified its input")
	}
	if _, err := ParseExemplars(&catalogTestRows{rows: [][]any{{"x"}}}); err == nil {
		t.Error("a scan error was swallowed")
	}
	ids, err := ParseStoredTraceIDs(&catalogTestRows{rows: [][]any{{"AA"}, {"bb"}}})
	if err != nil || !ids["aa"] || !ids["bb"] || len(ids) != 2 {
		t.Errorf("stored ids = %v err %v", ids, err)
	}
	if _, err := ParseStoredTraceIDs(&catalogTestRows{err: errors.New("boom")}); err == nil {
		t.Error("rows.Err was swallowed")
	}
}
