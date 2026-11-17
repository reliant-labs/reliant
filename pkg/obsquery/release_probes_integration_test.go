//go:build integration

package obsquery_test

// Release probes against a real ClickHouse, in one window holding both
// populations: the prober's own metrics (as the control plane's release
// prober writes them: delta sums and histograms, reliant.source=probe) beside
// the platform's ingress metrics, and server spans and error logs of real
// requests beside those a workload marked forge.probe=true. ProbeRED counts
// the probes exactly; every real-traffic statement leaves them out exactly.
//
//	go test -tags integration -run 'ReleaseSignalProbe|RealTraffic' ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestReleaseSignalProbeRED(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	if err := c.Exec(ctx, "CREATE DATABASE pr"); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{ingressSumDDL, ingressHistogramDDL} {
		if err := c.Exec(ctx, strings.ReplaceAll(ddl, "%s", "pr")); err != nil {
			t.Fatal(err)
		}
	}
	attrs := func(path, outcome, class, errType string) map[string]string {
		a := map[string]string{release.ProbeAttrPath: path, release.ProbeAttrOutcome: outcome}
		if class != "" {
			a[release.ProbeAttrStatusClass] = class
		}
		if errType != "" {
			a[release.ProbeAttrError] = errType
		}
		return a
	}
	res := func(service, env, source string) map[string]string {
		return map[string]string{"service.name": service, "reliant.env.id": env, "reliant.source": source, "reliant.ingest.class": source}
	}
	count := func(service, env, source, metric string, a map[string]string, at string, n float64) string {
		b, _ := json.Marshal(map[string]any{
			"ResourceAttributes": res(service, env, source), "ServiceName": service, "MetricName": metric, "MetricUnit": "{request}",
			"Attributes": a, "StartTimeUnix": at, "TimeUnix": at, "Value": n, "AggregationTemporality": 1, "IsMonotonic": true,
		})
		return string(b)
	}
	const (
		r                               = release.ProbeMetricRequests
		ok, failed, throttled, degraded = release.ProbeOutcomeOK, release.ProbeOutcomeFailed, release.ProbeOutcomeThrottled, release.ProbeOutcomeDegraded
		probe                           = release.ProbeSource
	)
	rumInsert(t, c, "pr", "otel_metrics_sum", strings.Join([]string{
		// web: / and /about, every outcome, over several flushes.
		count("web", "env-prod", probe, r, attrs("/", ok, "2xx", ""), "2026-10-19 10:00:10", 100),
		count("web", "env-prod", probe, r, attrs("/", ok, "2xx", ""), "2026-10-19 10:00:20", 120),
		count("web", "env-prod", probe, r, attrs("/about", ok, "2xx", ""), "2026-10-19 10:00:20", 10),
		count("web", "env-prod", probe, r, attrs("/", failed, "5xx", ""), "2026-10-19 10:01:00", 3),
		count("web", "env-prod", probe, r, attrs("/about", failed, "", "timeout"), "2026-10-19 10:02:00", 1),
		count("web", "env-prod", probe, r, attrs("/about", failed, "", "timeout"), "2026-10-19 10:05:00", 1),
		count("web", "env-prod", probe, r, attrs("/", throttled, "4xx", ""), "2026-10-19 10:03:00", 2),
		count("web", "env-prod", probe, r, attrs("/", degraded, "5xx", ""), "2026-10-19 10:04:00", 4),
		// api: clean.
		count("api", "env-prod", probe, r, attrs("/ok", ok, "2xx", ""), "2026-10-19 10:00:10", 50),
		// batch: probed but never timed (no histogram at all): counts with
		// no quantiles, never a failed scan.
		count("batch", "env-prod", probe, r, attrs("/health", ok, "2xx", ""), "2026-10-19 10:00:10", 8),
		count("batch", "env-prod", probe, r, attrs("/health", failed, "", "timeout"), "2026-10-19 10:01:00", 1),
		// Another env, out of range, a tenant writing the metric name under
		// its own source: none counts.
		count("web", "env-dev", probe, r, attrs("/", failed, "5xx", ""), "2026-10-19 10:01:00", 1000),
		count("web", "env-prod", probe, r, attrs("/", failed, "5xx", ""), "2026-10-21 10:01:00", 1000),
		count("web", "env-prod", "env", r, attrs("/", ok, "2xx", ""), "2026-10-19 10:01:00", 1000),
		count("web", "env-prod", "env", r, attrs("/", failed, "5xx", ""), "2026-10-19 10:01:00", 1000),
		// The real population of the same site: the ingress's counter.
		count("web", "env-prod", obsquery.IngressSource, obsquery.IngressRequestsMetric, map[string]string{obsquery.IngressStatusClass: "2xx"}, "2026-10-19 10:00:30", 500),
		count("web", "env-prod", obsquery.IngressSource, obsquery.IngressRequestsMetric, map[string]string{obsquery.IngressStatusClass: "5xx"}, "2026-10-19 10:00:30", 7),
	}, "\n"))

	bounds := []float64{0.01, 0.05, 0.1, 0.5}
	hist := func(service, source string, a map[string]string, counts []uint64) string {
		var n uint64
		for _, c := range counts {
			n += c
		}
		b, _ := json.Marshal(map[string]any{
			"ResourceAttributes": res(service, "env-prod", source), "ServiceName": service, "MetricName": release.ProbeMetricDuration, "MetricUnit": "s",
			"Attributes": a, "StartTimeUnix": "2026-10-19 10:01:00", "TimeUnix": "2026-10-19 10:01:00",
			"Count": n, "BucketCounts": counts, "ExplicitBounds": bounds, "AggregationTemporality": 1,
		})
		return string(b)
	}
	rumInsert(t, c, "pr", "otel_metrics_histogram", strings.Join([]string{
		hist("web", probe, attrs("/", ok, "2xx", ""), []uint64{0, 90, 0, 0, 0}),     // 30ms midpoint
		hist("web", probe, attrs("/", failed, "5xx", ""), []uint64{0, 0, 0, 10, 0}), // 300ms midpoint
		// A degraded request is not a sample, and a tenant's own histogram
		// is not the prober's: neither moves the quantiles.
		hist("web", probe, attrs("/", degraded, "5xx", ""), []uint64{0, 0, 0, 0, 1000}),
		hist("web", "env", attrs("/", ok, "2xx", ""), []uint64{1000, 0, 0, 0, 0}),
		hist("api", probe, attrs("/ok", ok, "2xx", ""), []uint64{10, 0, 0, 0, 0}), // 5ms midpoint
	}, "\n"))

	run := rumRunner(c)
	ov := obsquery.Overview{Scope: rumScopeOf("pr", "env-prod")}
	query := func(compile func() (obsquery.Stmt, error)) obsquery.Rows {
		t.Helper()
		st, err := compile()
		if err != nil {
			t.Fatal(err)
		}
		rows, err := run(ctx, st)
		if err != nil {
			t.Fatalf("%v\n%s", err, st.SQL)
		}
		t.Cleanup(func() { _ = rows.Close() })
		return rows
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

	byTarget, err := obsquery.ScanProbeRED(query(ov.ProbeRED))
	if err != nil {
		t.Fatal(err)
	}
	web, api := byTarget["web"], byTarget["api"]
	if batch := byTarget["batch"]; batch != (obsquery.ProbeFigures{Samples: 9, Failures: 1}) {
		t.Errorf("ProbeRED of an untimed target = %+v, want its counts and no quantiles", batch)
	}
	if len(byTarget) != 3 || web.Samples != 235 || web.Failures != 5 || web.Throttled != 2 || web.Degraded != 4 ||
		api.Samples != 50 || api.Failures != 0 || api.Throttled != 0 || api.Degraded != 0 {
		t.Fatalf("ProbeRED = %+v", byTarget)
	}
	// 90 samples at 30ms, 10 at 300ms; the degraded overflow is not timed.
	if !near(web.P50Ms, 30) || !near(web.P95Ms, 300) || !near(api.P50Ms, 5) || !near(api.P95Ms, 5) {
		t.Errorf("ProbeRED quantiles: web %+v api %+v", web, api)
	}

	byPath, err := obsquery.ScanProbeREDByPath(query(ov.ProbeREDByPath))
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := map[obsquery.ProbePath]obsquery.ProbeFigures{
		{Target: "web", Path: "/"}:         {Samples: 223, Failures: 3, Throttled: 2, Degraded: 4, P50Ms: 30, P95Ms: 300},
		{Target: "web", Path: "/about"}:    {Samples: 12, Failures: 2}, // never timed: no quantiles
		{Target: "api", Path: "/ok"}:       {Samples: 50, P50Ms: 5, P95Ms: 5},
		{Target: "batch", Path: "/health"}: {Samples: 9, Failures: 1},
	}
	for k, f := range byPath { // bucket midpoints in float: compare to the microsecond
		f.P50Ms, f.P95Ms = math.Round(f.P50Ms*1000)/1000, math.Round(f.P95Ms*1000)/1000
		byPath[k] = f
	}
	if !reflect.DeepEqual(byPath, wantPaths) {
		t.Errorf("ProbeREDByPath = %+v\nwant %+v", byPath, wantPaths)
	}

	// A server whose profile sets join_use_nulls fills an unmatched LEFT JOIN
	// with NULL where it can: the untimed rows must still scan.
	for name, compile := range map[string]func() (obsquery.Stmt, error){"ProbeRED": ov.ProbeRED, "ProbeREDByPath": ov.ProbeREDByPath} {
		st, err := compile()
		if err != nil {
			t.Fatal(err)
		}
		rows, err := c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params)),
			clickhouse.WithSettings(clickhouse.Settings{"join_use_nulls": 1})), st.SQL)
		if err != nil {
			t.Fatalf("%s with join_use_nulls=1: %v", name, err)
		}
		if name == "ProbeRED" {
			got, err := obsquery.ScanProbeRED(rows)
			if err != nil || got["batch"] != (obsquery.ProbeFigures{Samples: 9, Failures: 1}) {
				t.Errorf("ProbeRED with join_use_nulls=1: batch %+v, err %v", got["batch"], err)
			}
		} else if got, err := obsquery.ScanProbeREDByPath(rows); err != nil || got[obsquery.ProbePath{Target: "batch", Path: "/health"}] != (obsquery.ProbeFigures{Samples: 9, Failures: 1}) {
			t.Errorf("ProbeREDByPath with join_use_nulls=1: %+v, err %v", got, err)
		}
		_ = rows.Close()
	}

	failures, err := obsquery.ScanProbeFailures(query(ov.ProbeFailures))
	if err != nil {
		t.Fatal(err)
	}
	at := func(hm string) time.Time {
		v, _ := time.Parse("2006-01-02 15:04", "2026-10-19 "+hm)
		return v
	}
	wantFailures := map[string][]obsquery.ProbeFailure{"web": {
		{Path: "/", StatusClass: "5xx", First: at("10:01"), Last: at("10:01"), Count: 3},
		{Path: "/about", ErrorType: "timeout", First: at("10:02"), Last: at("10:05"), Count: 2},
	}, "batch": {
		{Path: "/health", ErrorType: "timeout", First: at("10:01"), Last: at("10:01"), Count: 1},
	}}
	if !reflect.DeepEqual(failures, wantFailures) {
		t.Errorf("ProbeFailures = %+v\nwant %+v", failures, wantFailures)
	}

	// The real population of the same site is the ingress's, untouched by
	// the probes beside it.
	rows := query(ov.IngressRED)
	ingress := map[string][2]uint64{}
	for rows.Next() {
		var (
			name       string
			reqs, errs uint64
			q          []float64
			source     string
		)
		if err := rows.Scan(&name, &reqs, &errs, &q, &source); err != nil {
			t.Fatal(err)
		}
		ingress[name] = [2]uint64{reqs, errs}
	}
	if want := map[string][2]uint64{"web": {507, 7}}; !reflect.DeepEqual(ingress, want) {
		t.Errorf("IngressRED beside probes = %v, want %v", ingress, want)
	}
}

func TestRealTrafficExcludesProbes(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt")
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	var spans []string
	n := 0
	span := func(status, probe string) {
		n++
		attrs := map[string]string{"http.route": "/x"}
		if probe != "" {
			attrs[release.ProbeAttr] = probe
		}
		b, _ := json.Marshal(map[string]any{
			"Timestamp": base.Add(time.Duration(n) * time.Second).Format("2006-01-02 15:04:05.000000000"),
			"TraceId":   fmt.Sprintf("%032x", n), "SpanId": fmt.Sprintf("%016x", n),
			"SpanName": "GET /x", "SpanKind": "Server", "ServiceName": "api", "StatusCode": status, "Duration": 5_000_000,
			"SpanAttributes":     attrs,
			"ResourceAttributes": map[string]string{"reliant.env.id": "env-prod", "service.name": "api"},
		})
		spans = append(spans, string(b))
	}
	for i := 0; i < 40; i++ {
		span("Unset", "")
	}
	for i := 0; i < 10; i++ {
		span("Error", "")
	}
	for i := 0; i < 30; i++ {
		span("Unset", "true")
	}
	for i := 0; i < 20; i++ {
		span("Error", "true")
	}
	span("Error", "false") // not a probe: real
	rumInsert(t, c, "rt", "otel_traces", strings.Join(spans, "\n"))

	var logs []string
	logRec := func(severity uint8, text, body, probe string) {
		n++
		attrs := map[string]string{}
		if probe != "" {
			attrs[release.ProbeAttr] = probe
		}
		b, _ := json.Marshal(map[string]any{
			"Timestamp": base.Add(time.Duration(n) * time.Second).Format("2006-01-02 15:04:05.000000000"),
			"TraceId":   fmt.Sprintf("%032x", n), "SpanId": fmt.Sprintf("%016x", n),
			"SeverityNumber": severity, "SeverityText": text, "ServiceName": "api", "Body": body,
			"LogAttributes":      attrs,
			"ResourceAttributes": map[string]string{"reliant.env.id": "env-prod", "service.name": "api"},
		})
		logs = append(logs, string(b))
	}
	for i := 0; i < 5; i++ {
		logRec(17, "ERROR", "database unavailable", "")
	}
	for i := 0; i < 2; i++ {
		logRec(9, "INFO", "served", "")
	}
	for i := 0; i < 3; i++ {
		logRec(17, "ERROR", "database unavailable", "true") // a probe hitting the real issue
	}
	for i := 0; i < 7; i++ {
		logRec(17, "ERROR", "probe target misconfigured", "true") // an issue only probes see
	}
	logRec(9, "INFO", "probed", "true")
	rumInsert(t, c, "rt", "otel_logs", strings.Join(logs, "\n"))

	run := rumRunner(c)
	sc := rumScopeOf("rt", "env-prod")
	ov := obsquery.Overview{Scope: sc}
	// sums wraps a statement and reads its integer columns' totals, so each
	// statement is checked on the figures it reports, whatever else it selects.
	sums := func(name string, compile func() (obsquery.Stmt, error), cols ...string) []uint64 {
		t.Helper()
		st, err := compile()
		if err != nil {
			t.Fatal(name, err)
		}
		sel := make([]string, len(cols))
		for i, c := range cols {
			sel[i] = "toUInt64(sum(" + c + "))"
		}
		st.SQL = "SELECT " + strings.Join(sel, ", ") + " FROM (\n" + st.SQL + "\n)"
		rows, err := run(ctx, st)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, st.SQL)
		}
		defer rows.Close()
		out := make([]uint64, len(cols))
		dest := make([]any, len(cols))
		for i := range out {
			dest[i] = &out[i]
		}
		if !rows.Next() {
			t.Fatalf("%s: no row", name)
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(name, err)
		}
		return out
	}
	for name, compile := range map[string]func() (obsquery.Stmt, error){
		"RED":           ov.RED,
		"REDByEndpoint": ov.REDByEndpoint,
		"Trend":         func() (obsquery.Stmt, error) { return ov.Trend(60) },
	} {
		if got := sums(name, compile, "requests", "errors"); got[0] != 51 || got[1] != 11 {
			t.Errorf("%s: requests, errors = %v, want 51 real requests, 11 errors (50 probes left out)", name, got)
		}
	}
	if got := sums("Logs", ov.Logs, "total", "error_logs"); got[0] != 7 || got[1] != 5 {
		t.Errorf("Logs: total, error_logs = %v, want 7, 5 (11 probe records left out)", got)
	}
	issues := obsquery.ServerIssues{Scope: sc, Limit: 10}
	if got := sums("ServerIssues", issues.Compile, "1", "events"); got[0] != 1 || got[1] != 5 {
		t.Errorf("ServerIssues: issues, events = %v, want the one real issue with its 5 real events", got)
	}
	// The issue's own detail agrees with the list.
	st, err := issues.Compile()
	if err != nil {
		t.Fatal(err)
	}
	st.SQL = "SELECT fingerprint FROM (\n" + st.SQL + "\n)"
	rows, err := run(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	var fp string
	for rows.Next() {
		if err := rows.Scan(&fp); err != nil {
			t.Fatal(err)
		}
	}
	_ = rows.Close()
	events := obsquery.ServerIssueEvents{Scope: sc, Fingerprint: fp, Limit: 50}
	if got := sums("ServerIssueEvents", events.Compile, "1"); got[0] != 5 {
		t.Errorf("ServerIssueEvents(%s) = %v events, want the 5 real ones", fp, got)
	}
	// Search still finds the probes: a probe's trace is how its failure is debugged.
	search := obsquery.LogQuery{Scope: sc, Limit: 100}
	if got := sums("LogQuery", search.Compile, "1"); got[0] != 18 {
		t.Errorf("log search = %v records, want all 18", got)
	}
}
