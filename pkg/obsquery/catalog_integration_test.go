//go:build integration

package obsquery_test

// The metric catalog, attribute stats, exemplars and the stored-trace probe
// against a real ClickHouse holding the exporter's five metric tables.
//
//	go test -tags integration -run 'Catalog|Exemplar|MetricAttributes' ./pkg/obsquery/

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const metricCommonCols = `
  ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String),
  MetricDescription String, MetricUnit String, Attributes Map(LowCardinality(String), String),
  StartTimeUnix DateTime, TimeUnix DateTime,`

const exemplarCols = `
  ` + "`Exemplars.FilteredAttributes`" + ` Array(Map(LowCardinality(String), String)), ` + "`Exemplars.TimeUnix`" + ` Array(DateTime),
  ` + "`Exemplars.Value`" + ` Array(Float64), ` + "`Exemplars.SpanId`" + ` Array(String), ` + "`Exemplars.TraceId`" + ` Array(String)`

// catalogTables are the exporter's five metric tables, trimmed to what the
// catalog reads, in the exporter's column types.
var catalogTables = map[string]string{
	"otel_metrics_gauge": metricCommonCols + ` Value Float64,` + exemplarCols,
	"otel_metrics_sum": metricCommonCols + ` Value Float64,` + exemplarCols + `,
  AggregationTemporality Int32, IsMonotonic Bool`,
	"otel_metrics_histogram": metricCommonCols + ` Count UInt64, Sum Float64, BucketCounts Array(UInt64), ExplicitBounds Array(Float64),` + exemplarCols + `,
  AggregationTemporality Int32`,
	"otel_metrics_exponential_histogram": metricCommonCols + ` Count UInt64, Sum Float64, Scale Int32, ZeroCount UInt64,
  PositiveOffset Int32, PositiveBucketCounts Array(UInt64), NegativeOffset Int32, NegativeBucketCounts Array(UInt64),` + exemplarCols + `,
  AggregationTemporality Int32`,
	"otel_metrics_summary": metricCommonCols + ` Count UInt64, Sum Float64,
  ` + "`ValueAtQuantiles.Quantile`" + ` Array(Float64), ` + "`ValueAtQuantiles.Value`" + ` Array(Float64)`,
}

func catalogDB(t *testing.T, c clickhouse.Conn, db string, lookupTable bool) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string) {
		t.Helper()
		if err := c.Exec(ctx, sql); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`CREATE DATABASE ` + db)
	for name, cols := range catalogTables {
		exec(`CREATE TABLE ` + db + `.` + name + ` (` + cols + `) ENGINE = MergeTree ORDER BY (ServiceName, MetricName, TimeUnix)`)
	}
	exec(`CREATE TABLE ` + db + `.otel_traces (Timestamp DateTime64(9), TraceId String, SpanId String, ServiceName LowCardinality(String))
ENGINE = MergeTree ORDER BY (ServiceName, Timestamp)`)
	if lookupTable {
		exec(`CREATE TABLE ` + db + `.otel_traces_trace_id_ts (TraceId String, Start DateTime, End DateTime) ENGINE = MergeTree ORDER BY (TraceId, Start)`)
	}
}

func queryScope(db string, start, end time.Time) obsquery.Scope {
	return obsquery.Scope{Database: db, Start: start, End: end}
}

func runStmt(t *testing.T, c clickhouse.Conn, st obsquery.Stmt) clickhouseRows {
	t.Helper()
	rows, err := c.Query(clickhouse.Context(context.Background(), clickhouse.WithParameters(clickhouse.Parameters(st.Params))), st.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	t.Cleanup(func() { _ = rows.Close() })
	return rows
}

// clickhouseRows is what the driver returns; it satisfies obsquery.Rows as is.
type clickhouseRows interface {
	obsquery.Rows
}

func TestCatalogAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	catalogDB(t, c, "cat", false)
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	ts := func(m int) string {
		return fmt.Sprintf("toDateTime('%s')", base.Add(time.Duration(m)*time.Minute).Format("2006-01-02 15:04:05"))
	}
	exec := func(sql string) {
		t.Helper()
		if err := c.Exec(ctx, sql); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}

	// a counter with 300 series (pod x code) from two services, two data points each
	exec(`INSERT INTO cat.otel_metrics_sum (ResourceAttributes, ServiceName, MetricName, MetricDescription, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Value, AggregationTemporality, IsMonotonic)
SELECT map('k8s.pod.name', concat('pod-', toString(n % 100)), 'reliant.env.id', 'env-1'), if(n % 3 = 0, 'web', 'api'), 'orders_created', 'Orders created.', '{order}',
  map('code', concat('c', toString(intDiv(n, 100)))), ` + ts(0) + `, ` + ts(1) + ` + INTERVAL (n % 2) MINUTE, toFloat64(n), 2, true
FROM (SELECT number n FROM numbers(300))`)
	// an up-down counter and a delta counter
	exec(`INSERT INTO cat.otel_metrics_sum (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Value, AggregationTemporality, IsMonotonic)
VALUES (map('reliant.env.id','env-1'), 'api', 'queue_depth', '{item}', map(), ` + ts(0) + `, ` + ts(2) + `, 5, 2, false),
       (map('reliant.env.id','env-1'), 'api', 'jobs_done', '{job}', map(), ` + ts(0) + `, ` + ts(3) + `, 5, 1, true)`)
	exec(`INSERT INTO cat.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Value)
VALUES (map('reliant.env.id','env-1'), 'api', 'heap', 'By', map(), ` + ts(0) + `, ` + ts(4) + `, 10),
       (map('reliant.env.id','env-2'), 'api', 'other_env_gauge', 'By', map(), ` + ts(0) + `, ` + ts(4) + `, 10)`)
	exec(`INSERT INTO cat.otel_metrics_histogram (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Count, Sum, BucketCounts, ExplicitBounds, AggregationTemporality)
VALUES (map('reliant.env.id','env-1'), 'api', 'rpc.server.duration', 's', map('procedure','List'), ` + ts(0) + `, ` + ts(5) + `, 3, 1.5, [1,1,1,0], [0.1,0.5,1.0], 1)`)
	exec(`INSERT INTO cat.otel_metrics_exponential_histogram (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Count, Sum, Scale, ZeroCount, PositiveOffset, PositiveBucketCounts, NegativeOffset, NegativeBucketCounts, AggregationTemporality)
VALUES (map('reliant.env.id','env-1'), 'gateway', 'http.server.duration', 'ms', map(), ` + ts(0) + `, ` + ts(6) + `, 3, 9, 2, 0, 0, [1,1,1], 0, [], 2)`)
	exec(`INSERT INTO cat.otel_metrics_summary (ResourceAttributes, ServiceName, MetricName, MetricUnit, Attributes, StartTimeUnix, TimeUnix, Count, Sum, ` + "`ValueAtQuantiles.Quantile`, `ValueAtQuantiles.Value`" + `)
VALUES (map('reliant.env.id','env-1'), 'legacy', 'legacy.latency', 's', map(), ` + ts(0) + `, ` + ts(7) + `, 4, 2, [0.5, 0.99], [0.4, 0.9])`)

	sc := queryScope("cat", base, base.Add(time.Hour))
	run := func(q obsquery.MetricCatalog) ([]obsquery.CatalogEntry, bool) {
		t.Helper()
		st, err := q.Compile()
		if err != nil {
			t.Fatal(err)
		}
		got, trunc, err := obsquery.ParseCatalog(runStmt(t, c, st), q.Limit)
		if err != nil {
			t.Fatalf("%v\n%s", err, st.SQL)
		}
		return got, trunc
	}

	got, trunc := run(obsquery.MetricCatalog{Scope: sc, Limit: 50})
	if trunc {
		t.Error("truncated with room to spare")
	}
	by := map[string]obsquery.CatalogEntry{}
	for _, e := range got {
		by[e.Name] = e
	}
	type want struct {
		kind  obsquery.CatalogKind
		mono  bool
		temp  obsquery.Temporality
		unit  string
		query bool
	}
	for name, w := range map[string]want{
		"orders_created":       {obsquery.CatalogSum, true, obsquery.TemporalityCumulative, "{order}", true},
		"queue_depth":          {obsquery.CatalogSum, false, obsquery.TemporalityCumulative, "{item}", true},
		"jobs_done":            {obsquery.CatalogSum, true, obsquery.TemporalityDelta, "{job}", true},
		"heap":                 {obsquery.CatalogGauge, false, obsquery.TemporalityUnknown, "By", true},
		"rpc.server.duration":  {obsquery.CatalogHistogram, false, obsquery.TemporalityDelta, "s", true},
		"http.server.duration": {obsquery.CatalogExpHistogram, false, obsquery.TemporalityCumulative, "ms", true},
		"legacy.latency":       {obsquery.CatalogSummary, false, obsquery.TemporalityUnknown, "s", true},
		"other_env_gauge":      {obsquery.CatalogGauge, false, obsquery.TemporalityUnknown, "By", true},
	} {
		e, ok := by[name]
		if !ok {
			t.Errorf("%s is not in the catalog (%d entries)", name, len(got))
			continue
		}
		if e.Kind != w.kind || e.Monotonic != w.mono || e.Temporality != w.temp || e.Unit != w.unit || e.Queryable != w.query || !e.Seen {
			t.Errorf("%s = %+v, want %+v", name, e, w)
		}
	}
	if len(got) != 8 {
		t.Errorf("%d entries, want 8", len(got))
	}
	o := by["orders_created"]
	if o.Description != "Orders created." || o.ServiceCount != 2 || strings.Join(o.Services, ",") != "api,web" {
		t.Errorf("orders_created services/description: %+v", o)
	}
	if want := 300; math.Abs(float64(o.SeriesEstimate-int64(want))) > 0.05*float64(want) {
		t.Errorf("series estimate = %d, want %d +-5%%", o.SeriesEstimate, want)
	}
	if !o.LastSeen.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("last seen = %v, want %v", o.LastSeen.UTC(), base.Add(2*time.Minute))
	}
	if !by["rpc.server.duration"].Builtin || by["orders_created"].Builtin {
		t.Error("builtin flag is wrong")
	}

	// limit+1 truncation
	page, trunc := run(obsquery.MetricCatalog{Scope: sc, Limit: 3})
	if !trunc || len(page) != 3 {
		t.Errorf("limit 3: %d entries, truncated %v", len(page), trunc)
	}
	// name filter, case-insensitive
	got, _ = run(obsquery.MetricCatalog{Scope: sc, NameContains: "QUEUE", Limit: 10})
	if len(got) != 1 || got[0].Name != "queue_depth" {
		t.Errorf("name filter: %+v", got)
	}
	// a resource filter narrows; a scope restricted to env-2 sees one metric
	two := sc
	two.EnvRestricted, two.EnvIDs = true, []string{"env-2"}
	got, _ = run(obsquery.MetricCatalog{Scope: two, Limit: 10})
	if len(got) != 1 || got[0].Name != "other_env_gauge" {
		t.Errorf("env scope: %+v", got)
	}
	// the catalog reads the LAST 24h of a longer range: data 3 days before the
	// end is invisible to the catalog, while a short range sees it.
	long := queryScope("cat", base.Add(-100*time.Hour), base.Add(3*24*time.Hour))
	if got, _ = run(obsquery.MetricCatalog{Scope: long, Limit: 50}); len(got) != 0 {
		t.Errorf("a metric last seen 3 days before the end must not be listed: %d entries", len(got))
	}

	// the declared join over real rows
	merged := obsquery.MergeDeclared(page, []metrics.Schema{{Metrics: []metrics.Spec{{Name: "never_called", Type: metrics.Counter, Description: "typo?"}}}})
	var sawUnseen bool
	for _, e := range merged {
		sawUnseen = sawUnseen || (e.Name == "never_called" && e.Declared && !e.Seen)
	}
	if !sawUnseen {
		t.Error("declared-not-seen entry is missing after the merge")
	}
}

func TestMetricAttributesAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	catalogDB(t, c, "attr", false)
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := c.Exec(ctx, `INSERT INTO attr.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, StartTimeUnix, TimeUnix, Value)
SELECT map('service.name', 'api', 'k8s.pod.name', concat('pod-', toString(n % 6)), 'k8s.cluster.name', 'one'), 'api', 'depth',
  map('few', concat('v', toString(n % 3)), 'many', concat('u', toString(n)), 'const', 'x'),
  toDateTime('`+base.Format("2006-01-02 15:04:05")+`'), toDateTime('`+base.Add(time.Minute).Format("2006-01-02 15:04:05")+`'), 1
FROM (SELECT number n FROM numbers(500))`); err != nil {
		t.Fatal(err)
	}
	// another metric's keys must not leak in
	if err := c.Exec(ctx, `INSERT INTO attr.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, StartTimeUnix, TimeUnix, Value)
VALUES (map(), 'api', 'other', map('leak', '1'), toDateTime('`+base.Format("2006-01-02 15:04:05")+`'), toDateTime('`+base.Add(time.Minute).Format("2006-01-02 15:04:05")+`'), 1)`); err != nil {
		t.Fatal(err)
	}

	sc := queryScope("attr", base, base.Add(time.Hour))
	st, err := obsquery.MetricAttributes{Scope: sc, Name: "depth", Kind: obsquery.CatalogGauge}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	spec := &metrics.Spec{Name: "depth", Attributes: []metrics.Attribute{{Key: "few", Values: []string{"v0", "v1", "v2"}}}}
	got, err := obsquery.ParseAttributes(runStmt(t, c, st), spec, 1)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	by := map[string]obsquery.AttributeStat{}
	for _, s := range got {
		by[s.Key] = s
	}
	if _, leaked := by["leak"]; leaked {
		t.Error("another metric's attribute key leaked in")
	}
	if s := by["few"]; s.DistinctEstimate != 3 || s.Resource || s.HighCardinality || len(s.Enumerated) != 3 {
		t.Errorf("few: %+v", s)
	}
	if s := by["many"]; math.Abs(float64(s.DistinctEstimate)-500) > 25 || !s.HighCardinality || s.Resource {
		t.Errorf("many: %+v", s)
	}
	if s := by["const"]; s.DistinctEstimate != 1 {
		t.Errorf("const: %+v", s)
	}
	if s := by["k8s.pod.name"]; !s.Resource || s.DistinctEstimate != 6 {
		t.Errorf("pod (varies, resource): %+v", s)
	}
	if _, ok := by["k8s.cluster.name"]; ok {
		t.Error("a resource key with one value is not a breakdown and must be left out")
	}
	if len(got) == 0 || got[0].Key != "few" {
		t.Errorf("the enumerated key must rank first: %v", got)
	}
	for _, k := range obsquery.SuggestedBreakdowns(got) {
		if k == "many" || k == "const" {
			t.Errorf("%s suggested as a breakdown", k)
		}
	}
}

func TestMetricExemplarsAgainstClickHouse(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	catalogDB(t, c, "exh", true)
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	f := func(m int) string { return base.Add(time.Duration(m) * time.Minute).Format("2006-01-02 15:04:05") }
	const (
		slow    = "0af7651916cd43dd8448eb211c80319c" // stored
		mid     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // sampled out
		fast    = "cccccccccccccccccccccccccccccccc" // stored, other series
		outside = "dddddddddddddddddddddddddddddddd" // outside the window
	)
	// two series of one histogram; every row carries exemplars
	ins := func(proc string, minute int, ex string) {
		t.Helper()
		if err := c.Exec(ctx, `INSERT INTO exh.otel_metrics_histogram (ResourceAttributes, ServiceName, MetricName, Attributes, StartTimeUnix, TimeUnix, Count, Sum, BucketCounts, ExplicitBounds, AggregationTemporality,
  `+"`Exemplars.FilteredAttributes`, `Exemplars.TimeUnix`, `Exemplars.Value`, `Exemplars.SpanId`, `Exemplars.TraceId`"+`)
SELECT map('reliant.env.id','env-1'), 'api', 'lat', map('procedure','`+proc+`'), toDateTime('`+f(0)+`'), toDateTime('`+f(minute)+`'), 1, 1, [1,0], [1.0], 1, `+ex); err != nil {
			t.Fatal(err)
		}
	}
	ex := func(m int, v float64, trace, span string) string {
		return fmt.Sprintf("[map('http.route','/r')], [toDateTime('%s')], [%g], ['%s'], ['%s']", f(m), v, span, trace)
	}
	ins("List", 10, ex(9, 2.5, slow, "s-slow"))
	ins("List", 11, ex(10, 0.8, mid, "s-mid"))
	ins("Get", 12, ex(11, 0.2, fast, "s-fast"))
	ins("List", 50, ex(49, 3.0, outside, "s-out"))
	// a row with no exemplars and one with an empty trace id add nothing
	ins("List", 13, `[], [], [], [], []`)
	ins("List", 14, ex(13, 9, "", "s-none"))

	// traces: slow and fast are stored; the hosted form has the lookup table
	for _, tr := range []string{slow, fast} {
		if err := c.Exec(ctx, `INSERT INTO exh.otel_traces (Timestamp, TraceId, SpanId, ServiceName) VALUES (toDateTime64('`+f(9)+`', 9), '`+tr+`', 'root', 'api')`); err != nil {
			t.Fatal(err)
		}
		if err := c.Exec(ctx, `INSERT INTO exh.otel_traces_trace_id_ts VALUES ('`+tr+`', toDateTime('`+f(9)+`'), toDateTime('`+f(9)+`'))`); err != nil {
			t.Fatal(err)
		}
	}

	sc := queryScope("exh", base, base.Add(time.Hour))
	fetch := func(q obsquery.MetricExemplars) []obsquery.Exemplar {
		t.Helper()
		st, err := q.Compile()
		if err != nil {
			t.Fatal(err)
		}
		got, err := obsquery.ParseExemplars(runStmt(t, c, st))
		if err != nil {
			t.Fatalf("%v\n%s", err, st.SQL)
		}
		return got
	}
	ids := func(es []obsquery.Exemplar) string {
		var s []string
		for _, e := range es {
			s = append(s, e.TraceID)
		}
		return strings.Join(s, ",")
	}

	all := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram, Limit: 20})
	if got, want := ids(all), strings.Join([]string{outside, fast, mid, slow}, ","); got != want {
		t.Errorf("exemplars (newest first) = %s, want %s", got, want)
	}
	if e := all[3]; e.SpanID != "s-slow" || e.Value != 2.5 || e.Attributes["http.route"] != "/r" || !e.Time.Equal(base.Add(9*time.Minute)) {
		t.Errorf("slow exemplar: %+v", e)
	}

	min := 0.5
	tail := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram, MinValue: &min, Limit: 20})
	if got, want := ids(tail), strings.Join([]string{outside, mid, slow}, ","); got != want {
		t.Errorf("min value 0.5: %s, want %s", got, want)
	}
	win := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram, From: base.Add(8 * time.Minute), To: base.Add(12 * time.Minute), Limit: 20})
	if got, want := ids(win), strings.Join([]string{fast, mid, slow}, ","); got != want {
		t.Errorf("window: %s, want %s", got, want)
	}
	byGroup := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram, Group: map[string]string{"metric.attr.procedure": "Get"}, Limit: 20})
	if ids(byGroup) != fast {
		t.Errorf("group Get: %s, want %s", ids(byGroup), fast)
	}
	byWhere := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram,
		Where: []obsquery.PanelPredicate{{Field: "metric.attr.procedure", Op: "eq", Value: "List"}}, From: base.Add(8 * time.Minute), To: base.Add(12 * time.Minute), Limit: 20})
	if got, want := ids(byWhere), strings.Join([]string{mid, slow}, ","); got != want {
		t.Errorf("where List: %s, want %s", got, want)
	}
	if got := fetch(obsquery.MetricExemplars{Scope: sc, Name: "lat", Kind: obsquery.CatalogHistogram, Limit: 2}); len(got) != 2 {
		t.Errorf("limit 2 returned %d", len(got))
	}
	if got := fetch(obsquery.MetricExemplars{Scope: sc, Name: "nope", Kind: obsquery.CatalogHistogram, Limit: 5}); len(got) != 0 {
		t.Errorf("unknown metric returned %d exemplars", len(got))
	}

	// the stored probe, hosted form (lookup table) and local form (otel_traces)
	candidates := []string{slow, mid, fast, "ee" + strings.Repeat("0", 30)}
	for _, lookup := range []bool{true, false} {
		st, err := obsquery.ExemplarsStored{Scope: sc, TraceIDs: candidates, UseLookupTable: lookup}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		stored, err := obsquery.ParseStoredTraceIDs(runStmt(t, c, st))
		if err != nil {
			t.Fatalf("lookup=%v: %v\n%s", lookup, err, st.SQL)
		}
		if len(stored) != 2 || !stored[slow] || !stored[fast] {
			t.Errorf("lookup=%v: stored = %v, want only %s and %s", lookup, stored, slow, fast)
		}
	}
	// a probe window more than an hour past the trace does not find it
	late := sc
	late.End = base.Add(5 * time.Hour)
	st, err := obsquery.ExemplarsStored{Scope: late, TraceIDs: []string{slow}, From: base.Add(3 * time.Hour), To: base.Add(4 * time.Hour), UseLookupTable: true}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := obsquery.ParseStoredTraceIDs(runStmt(t, c, st)); len(got) != 0 {
		t.Errorf("a trace outside the probe window was reported stored: %v", got)
	}

	marked := obsquery.MarkStored(all, map[string]bool{slow: true, fast: true})
	if got, want := ids(marked), strings.Join([]string{fast, slow, outside, mid}, ","); got != want {
		t.Errorf("stored first: %s, want %s", got, want)
	}
}
