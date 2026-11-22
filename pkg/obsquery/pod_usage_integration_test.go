//go:build integration

package obsquery_test

// Workloads.PodUsage against a real ClickHouse: the kubeletstats and
// k8s_cluster gauges become one row per pod, container and metric, and those
// rows agree with the control plane's reader of its per-minute rollup
// (control-plane's container_metrics_1m and rollupWorkloads.PodUsage), which
// answers the same question when a read can use the rollups.
//
//	go test -tags integration -run PodUsage ./pkg/obsquery/

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

type podUsageRow struct {
	env, workload, podName, podUID, container, metric string
	latest, p95                                       float64
	img, tag                                          string
}

func (r podUsageRow) key() string {
	return strings.Join([]string{r.env, r.workload, r.podName, r.podUID, r.container, r.metric}, "|")
}

// podUsageRollupDDL mirrors the columns of control-plane's container_metrics_1m
// and its materialized view that rollupWorkloads.PodUsage reads.
const podUsageRollupDDL = `CREATE TABLE pu.container_metrics_1m (
  MetricName LowCardinality(String), EnvId LowCardinality(String), Workload LowCardinality(String), Container LowCardinality(String),
  Pod String, PodName String, PodUid String, ImageName LowCardinality(String), ImageTag LowCardinality(String), Minute DateTime,
  LastAt SimpleAggregateFunction(max, DateTime), Last AggregateFunction(argMax, Float64, DateTime), P95 AggregateFunction(quantile(0.95), Float64))
ENGINE = AggregatingMergeTree ORDER BY (MetricName, EnvId, Workload, Container, Pod, Minute, PodName, PodUid, ImageName, ImageTag)`

const podUsageRollupMV = `CREATE MATERIALIZED VIEW pu.mv_container_metrics_1m TO pu.container_metrics_1m AS
SELECT MetricName, ResourceAttributes['reliant.env.id'] AS EnvId, ResourceAttributes['k8s.workload.name'] AS Workload,
  ResourceAttributes['k8s.container.name'] AS Container,
  if(ResourceAttributes['k8s.pod.uid'] != '', ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']) AS Pod,
  ResourceAttributes['k8s.pod.name'] AS PodName, ResourceAttributes['k8s.pod.uid'] AS PodUid,
  ResourceAttributes['container.image.name'] AS ImageName, ResourceAttributes['container.image.tag'] AS ImageTag,
  toStartOfMinute(TimeUnix) AS Minute, max(TimeUnix) AS LastAt, argMaxState(Value, TimeUnix) AS Last, quantileState(0.95)(Value) AS P95
FROM pu.otel_metrics_gauge
GROUP BY MetricName, EnvId, Workload, Container, Pod, PodName, PodUid, ImageName, ImageTag, Minute`

// podUsageRollupRead is control-plane's rollupWorkloads.PodUsage over the
// mirror, with the scope's conditions written out for env-1's api workload.
const podUsageRollupRead = `SELECT EnvId AS env, Workload AS workload, PodName AS pod_name, PodUid AS pod_uid, Container AS container, MetricName AS metric,
       argMaxMerge(Last) AS latest, quantileMerge(0.95)(P95) AS p95,
       argMaxIf(ImageName, LastAt, ImageName != '') AS img, argMaxIf(ImageTag, LastAt, ImageTag != '') AS tag
FROM pu.container_metrics_1m
WHERE Minute >= {start:DateTime} AND Minute < {end:DateTime} AND EnvId = 'env-1' AND MetricName IN ({metrics:Array(String)})
  AND Workload != '' AND Container != '' AND Workload = 'api'
GROUP BY env, workload, pod_name, pod_uid, container, metric ORDER BY env, workload, pod_name, container, metric`

func scanPodUsage(t *testing.T, rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) map[string]podUsageRow {
	t.Helper()
	defer func() { _ = rows.Close() }()
	out := map[string]podUsageRow{}
	for rows.Next() {
		var r podUsageRow
		if err := rows.Scan(&r.env, &r.workload, &r.podName, &r.podUID, &r.container, &r.metric, &r.latest, &r.p95, &r.img, &r.tag); err != nil {
			t.Fatal(err)
		}
		if _, dup := out[r.key()]; dup {
			t.Fatalf("two rows for %s", r.key())
		}
		out[r.key()] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPodUsagePerPodAndContainer(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	for _, s := range []string{
		`CREATE DATABASE pu`,
		`CREATE TABLE pu.otel_metrics_gauge (
  ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String),
  Attributes Map(LowCardinality(String), String), TimeUnix DateTime, Value Float64) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`,
		podUsageRollupDDL, podUsageRollupMV,
	} {
		if err := c.Exec(ctx, s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	put := func(sec int, metric, pod, uid, container string, v float64, extra ...string) {
		t.Helper()
		res := map[string]string{
			"reliant.env.id": "env-1", "k8s.workload.name": "api", "k8s.pod.name": pod, "k8s.pod.uid": uid,
			"k8s.container.name": container, "container.image.name": "ghcr.io/acme/" + container, "container.image.tag": "v1",
		}
		for i := 0; i+1 < len(extra); i += 2 {
			res[extra[i]] = extra[i+1]
		}
		if err := c.Exec(ctx, `INSERT INTO pu.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES (?, 'api', ?, map(), ?, ?)`,
			res, metric, base.Add(time.Duration(sec)*time.Second), v); err != nil {
			t.Fatal(err)
		}
	}
	// api-0's app: cpu 0.1 .. 1.0 over ten samples, 20s apart (so across
	// minutes), the latest 0.5 though not the largest; a limit and request of
	// both resources. Its image tag moves to v2 at the last sample.
	for i := 0; i < 10; i++ {
		v := float64(i+1) / 10
		if i == 9 {
			v = 0.5
		}
		tag := "v1"
		if i == 9 {
			tag = "v2"
		}
		put(i*20, obsquery.MetricCPUUsage, "api-0", "u0", "app", v, "container.image.tag", tag)
		put(i*20, obsquery.MetricMemWorkingSet, "api-0", "u0", "app", 100+float64(i))
	}
	put(0, obsquery.MetricCPULimit, "api-0", "u0", "app", 1)
	put(0, obsquery.MetricMemLimit, "api-0", "u0", "app", 256)
	put(0, obsquery.MetricMemRequest, "api-0", "u0", "app", 128)
	// api-0's sidecar: usage and a memory limit, and nothing else.
	put(10, obsquery.MetricCPUUsage, "api-0", "u0", "sidecar", 0.25)
	put(10, obsquery.MetricMemLimit, "api-0", "u0", "sidecar", 64)
	// api-0 again, recreated under a new uid: a different pod.
	put(60, obsquery.MetricCPUUsage, "api-0", "u0b", "app", 2)
	// A pod with no uid is keyed by its name.
	put(60, obsquery.MetricCPUUsage, "api-1", "", "app", 3)
	// Not usage, another environment, another workload, out of range: no row.
	put(30, obsquery.MetricRestarts, "api-0", "u0", "app", 4)
	put(30, obsquery.MetricCPUUsage, "api-0", "u9", "app", 64, "reliant.env.id", "env-2")
	put(30, obsquery.MetricCPUUsage, "web-0", "w0", "app", 64, "k8s.workload.name", "web")
	put(-120, obsquery.MetricCPUUsage, "api-0", "u0", "app", 64)
	put(15*60, obsquery.MetricCPUUsage, "api-0", "u0", "app", 64)

	sc := obsquery.Scope{
		Database: "pu", Start: base, End: base.Add(10 * time.Minute),
		EnvRestricted: true, EnvIDs: []string{"env-1"},
	}
	st, err := obsquery.Workloads{Scope: sc, Workload: "api"}.PodUsage()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params))), st.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	got := scanPodUsage(t, rows)

	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	want := []podUsageRow{
		// latest is the newest sample, not the largest; p95 interpolates over
		// every sample (0.95 of the way through 0.1..0.4, 0.5, 0.5, 0.6..0.9).
		{"env-1", "api", "api-0", "u0", "app", obsquery.MetricCPUUsage, 0.5, 0.855, "ghcr.io/acme/app", "v2"},
		{"env-1", "api", "api-0", "u0", "app", obsquery.MetricMemWorkingSet, 109, 108.55, "ghcr.io/acme/app", "v1"},
		{"env-1", "api", "api-0", "u0", "app", obsquery.MetricCPULimit, 1, 1, "ghcr.io/acme/app", "v1"},
		{"env-1", "api", "api-0", "u0", "app", obsquery.MetricMemLimit, 256, 256, "ghcr.io/acme/app", "v1"},
		{"env-1", "api", "api-0", "u0", "app", obsquery.MetricMemRequest, 128, 128, "ghcr.io/acme/app", "v1"},
		{"env-1", "api", "api-0", "u0", "sidecar", obsquery.MetricCPUUsage, 0.25, 0.25, "ghcr.io/acme/sidecar", "v1"},
		{"env-1", "api", "api-0", "u0", "sidecar", obsquery.MetricMemLimit, 64, 64, "ghcr.io/acme/sidecar", "v1"},
		{"env-1", "api", "api-0", "u0b", "app", obsquery.MetricCPUUsage, 2, 2, "ghcr.io/acme/app", "v1"},
		{"env-1", "api", "api-1", "", "app", obsquery.MetricCPUUsage, 3, 3, "ghcr.io/acme/app", "v1"},
	}
	for _, w := range want {
		g, ok := got[w.key()]
		if !ok {
			t.Errorf("no row for %s", w.key())
			continue
		}
		if !near(g.latest, w.latest) || !near(g.p95, w.p95) || g.img != w.img || g.tag != w.tag {
			t.Errorf("%s = latest %v p95 %v img %q tag %q, want %v %v %q %q", w.key(), g.latest, g.p95, g.img, g.tag, w.latest, w.p95, w.img, w.tag)
		}
	}
	// A metric the container never reported (the sidecar's cpu limit, app's
	// cpu request) has no row, and nothing outside the scope does either.
	if len(got) != len(want) {
		for k := range got {
			t.Logf("row %s", k)
		}
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}

	// The control plane's rollup reader agrees, row for row.
	rctx := clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters{
		"start": base.Format(time.DateTime), "end": base.Add(10 * time.Minute).Format(time.DateTime),
		"metrics": "['" + strings.Join(obsquery.UsageMetrics, "','") + "']",
	}))
	rrows, err := c.Query(rctx, podUsageRollupRead)
	if err != nil {
		t.Fatalf("rollup read: %v", err)
	}
	rolled := scanPodUsage(t, rrows)
	if len(rolled) != len(got) {
		t.Fatalf("the rollup read has %d rows, the raw read %d", len(rolled), len(got))
	}
	for k, g := range got {
		r, ok := rolled[k]
		if !ok {
			t.Errorf("the rollup read has no row for %s", k)
			continue
		}
		if !near(r.latest, g.latest) || !near(r.p95, g.p95) || r.img != g.img || r.tag != g.tag {
			t.Errorf("%s: rollup %+v, raw %+v", k, r, g)
		}
	}
}
