//go:build integration

package obsquery_test

// Ingress RED and browser RED against a real ClickHouse: the ingress
// histogram rows as the control plane's workspace-proxy writes them
// (internal/proxy/ingress_red.go: delta histograms per status class, seconds),
// and the browser spans captured from a real Chrome (testdata/rum/traces.jsonl)
// plus fetch spans of each outcome.
//
//	go test -tags integration -run 'ReleaseSignal' ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

const ingressSumDDL = `CREATE TABLE %s.otel_metrics_sum (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime64(9), TimeUnix DateTime64(9), Value Float64, AggregationTemporality Int32, IsMonotonic Bool) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`

const ingressHistogramDDL = `CREATE TABLE %s.otel_metrics_histogram (ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String), MetricUnit String, Attributes Map(LowCardinality(String), String), StartTimeUnix DateTime64(9), TimeUnix DateTime64(9), Count UInt64, Sum Float64, BucketCounts Array(UInt64), ExplicitBounds Array(Float64), Min Float64, Max Float64, AggregationTemporality Int32) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`

func TestReleaseSignalIngressRED(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	if err := c.Exec(ctx, "CREATE DATABASE ig"); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, strings.ReplaceAll(ingressHistogramDDL, "%s", "ig")); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, strings.ReplaceAll(ingressSumDDL, "%s", "ig")); err != nil {
		t.Fatal(err)
	}
	bounds := []float64{0.01, 0.05, 0.1, 0.5}
	row := func(service, env, source, metric, class, at string, counts []uint64) string {
		var n uint64
		for _, c := range counts {
			n += c
		}
		b, _ := json.Marshal(map[string]any{
			"ResourceAttributes": map[string]string{"service.name": service, "reliant.env.id": env, "reliant.source": source, "reliant.ingest.class": source},
			"ServiceName":        service, "MetricName": metric, "MetricUnit": "s",
			"Attributes":    map[string]string{obsquery.IngressStatusClass: class},
			"StartTimeUnix": at, "TimeUnix": at, "Count": n, "BucketCounts": counts, "ExplicitBounds": bounds,
			"AggregationTemporality": 1,
		})
		return string(b)
	}
	const d = obsquery.IngressDurationMetric
	lines := []string{
		// site: two flushes of 2xx, one of 5xx, in range.
		row("site", "env-prod", "ingress", d, "2xx", "2026-10-19 10:00:30", []uint64{90, 0, 0, 0, 0}),
		row("site", "env-prod", "ingress", d, "2xx", "2026-10-19 10:01:00", []uint64{0, 0, 0, 0, 0}),
		row("site", "env-prod", "ingress", d, "5xx", "2026-10-19 10:01:00", []uint64{0, 0, 0, 10, 0}),
		// Out of the env, out of range, the wrong source, the wrong metric: none counts.
		row("site", "env-dev", "ingress", d, "2xx", "2026-10-19 10:00:30", []uint64{1000, 0, 0, 0, 0}),
		row("site", "env-prod", "ingress", d, "2xx", "2026-10-21 10:00:30", []uint64{1000, 0, 0, 0, 0}),
		row("site", "env-prod", "env", d, "2xx", "2026-10-19 10:00:30", []uint64{1000, 0, 0, 0, 0}),
		row("site", "env-prod", "ingress", "other.metric", "2xx", "2026-10-19 10:00:30", []uint64{1000, 0, 0, 0, 0}),
		// api: everything in the overflow bucket.
		row("api", "env-prod", "ingress", d, "2xx", "2026-10-19 10:00:30", []uint64{0, 0, 0, 0, 60}),
	}
	rumInsert(t, c, "ig", "otel_metrics_histogram", strings.Join(lines, "\n"))

	// The request counter, which is what requests and errors are read from:
	// it also counts what the histogram never times (protocol upgrades, 1xx
	// answers, abandoned requests).
	count := func(service, env, source, metric, class, at string, n float64) string {
		b, _ := json.Marshal(map[string]any{
			"ResourceAttributes": map[string]string{"service.name": service, "reliant.env.id": env, "reliant.source": source, "reliant.ingest.class": source},
			"ServiceName":        service, "MetricName": metric, "MetricUnit": "{request}",
			"Attributes":    map[string]string{obsquery.IngressStatusClass: class},
			"StartTimeUnix": at, "TimeUnix": at, "Value": n, "AggregationTemporality": 1, "IsMonotonic": true,
		})
		return string(b)
	}
	const r = obsquery.IngressRequestsMetric
	counts := []string{
		// site: the 100 the histogram timed, plus 20 websocket upgrades and 5
		// abandoned 5xx it never timed.
		count("site", "env-prod", "ingress", r, "2xx", "2026-10-19 10:00:30", 90),
		count("site", "env-prod", "ingress", r, "1xx", "2026-10-19 10:00:30", 20),
		count("site", "env-prod", "ingress", r, "5xx", "2026-10-19 10:01:00", 15),
		// api: exactly what the histogram timed.
		count("api", "env-prod", "ingress", r, "2xx", "2026-10-19 10:00:30", 60),
		// ws: only upgrades and failures, nothing timed, so no quantiles; it
		// is still judged on its requests and errors.
		count("ws", "env-prod", "ingress", r, "1xx", "2026-10-19 10:00:30", 40),
		count("ws", "env-prod", "ingress", r, "5xx", "2026-10-19 10:00:30", 8),
		// Out of the env, out of range, the wrong source, the wrong metric: none counts.
		count("site", "env-dev", "ingress", r, "2xx", "2026-10-19 10:00:30", 1000),
		count("site", "env-prod", "ingress", r, "2xx", "2026-10-21 10:00:30", 1000),
		count("site", "env-prod", "env", r, "2xx", "2026-10-19 10:00:30", 1000),
		count("site", "env-prod", "ingress", "other.metric", "2xx", "2026-10-19 10:00:30", 1000),
	}
	rumInsert(t, c, "ig", "otel_metrics_sum", strings.Join(counts, "\n"))

	st, err := obsquery.Overview{Scope: rumScopeOf("ig", "env-prod")}.IngressRED()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := rumRunner(c)(ctx, st)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	defer rows.Close()
	type red struct {
		reqs, errs uint64
		q          []float64
		source     string
	}
	got := map[string]red{}
	for rows.Next() {
		var name string
		var r red
		if err := rows.Scan(&name, &r.reqs, &r.errs, &r.q, &r.source); err != nil {
			t.Fatal(err)
		}
		got[name] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	site, api, ws := got["site"], got["api"], got["ws"]
	if len(got) != 3 || site.reqs != 125 || site.errs != 15 || site.source != "ingress" || api.reqs != 60 || api.errs != 0 {
		t.Fatalf("ingress RED = %+v", got)
	}
	if ws.reqs != 48 || ws.errs != 8 || len(ws.q) != 0 {
		t.Fatalf("a service the histogram never timed: %+v, want 48 requests, 8 errors, no quantiles", ws)
	}
	// 90 requests in (0, 10ms] at its 5ms midpoint, 10 in (100ms, 500ms] at 300ms:
	// p50 5ms, p99 300ms. The overflow bucket sits at the last bound.
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	if len(site.q) != 3 || !near(site.q[0], 5) || !near(site.q[2], 300) || len(api.q) != 3 || !near(api.q[1], 500) {
		t.Errorf("quantiles: site %v api %v", site.q, api.q)
	}
}

func TestReleaseSignalBrowserRED(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "br")
	loadCapture(t, c, "br")
	fetch := func(id, status, code string) string {
		b, _ := json.Marshal(map[string]any{
			"Timestamp": "2026-10-19 10:00:00.000000000", "TraceId": "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e" + id[:1], "SpanId": id,
			"SpanName": "HTTP GET", "SpanKind": "Client", "ServiceName": "reliant-web", "ScopeName": "@opentelemetry/instrumentation-fetch",
			"StatusCode": status, "Duration": 42_000_000,
			"SpanAttributes":     map[string]string{"component": "fetch", "http.status_code": code},
			"ResourceAttributes": map[string]string{"reliant.env.id": "env-prod", "service.name": "reliant-web"},
		})
		return string(b)
	}
	rumInsert(t, c, "br", "otel_traces", strings.Join([]string{
		fetch("a000000000000001", "Unset", "200"),
		fetch("a000000000000002", "Error", "503"), // failed: 5xx
		fetch("a000000000000003", "Error", ""),    // failed: no response
		fetch("a000000000000004", "Unset", "404"), // the page's own business
	}, "\n"))

	st, err := obsquery.Overview{Scope: rumScopeOf("br", "env-prod")}.BrowserRED()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := rumRunner(c)(ctx, st)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	defer rows.Close()
	got, err := obsquery.ScanBrowserRED(rows)
	if err != nil {
		t.Fatal(err)
	}
	web, ok := got["reliant-web"]
	// The capture: 2 documentLoad + 2 routeChange; 5 error-status spans of the
	// errors and record-exception scopes (a console.error of a plain string has
	// none and is no error).
	if !ok || web.PageViews != 4 || web.PageLoads != 2 || web.JSErrors != 5 || web.Requests != 4 || web.FailedRequests != 2 || web.PageLoadP95Ms <= 0 {
		t.Fatalf("browser RED = %+v", got)
	}
}
