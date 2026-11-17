//go:build integration

package obsquery_test

// Workloads.Readiness against a real ClickHouse: the k8s_cluster readiness and
// restart gauges become per-bucket pods / ready / restarts rows.
//
//	go test -tags integration -run Readiness ./pkg/obsquery/

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

type readinessRow struct {
	env, workload string
	bucket        time.Time
	pods, ready   uint64
	restarts      float64
	exit          string
}

func TestReadinessBuckets(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	if err := c.Exec(ctx, `CREATE DATABASE rdb`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `CREATE TABLE rdb.otel_metrics_gauge (
  ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String),
  Attributes Map(LowCardinality(String), String), TimeUnix DateTime, Value Float64) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	put := func(min float64, metric, pod, container string, v float64, extra ...string) {
		t.Helper()
		res := map[string]string{
			"reliant.env.id": "env-1", "k8s.workload.name": "api", "k8s.pod.uid": "uid-" + pod,
			"k8s.pod.name": pod, "k8s.container.name": container,
		}
		for i := 0; i+1 < len(extra); i += 2 {
			res[extra[i]] = extra[i+1]
		}
		at := base.Add(time.Duration(min * float64(time.Minute)))
		if err := c.Exec(ctx, `INSERT INTO rdb.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES (?, 'api', ?, map(), ?, ?)`,
			res, metric, at, v); err != nil {
			t.Fatal(err)
		}
	}
	// api-0: ready the whole time. api-1: ready, then a second container goes unready at minute 3
	// (the pod is NOT ready), and restarts 2 -> 3 at minute 4 with a reason. No samples at all from minute 6.
	for m := 0.0; m < 6; m++ {
		put(m, "k8s.container.ready", "api-0", "app", 1)
		put(m, "k8s.container.restarts", "api-0", "app", 0)
		put(m, "k8s.container.ready", "api-1", "app", 1)
		unready := 1.0
		if m >= 3 {
			unready = 0
		}
		put(m, "k8s.container.ready", "api-1", "sidecar", unready)
		r := 2.0
		reason := ""
		if m >= 4 {
			r, reason = 3, "OOMKilled"
		}
		put(m, "k8s.container.restarts", "api-1", "app", r, "k8s.container.last_terminated_reason", reason)
	}
	// a counter reset (kubelet pruned dead containers) counts as zero restarts
	put(0, "k8s.container.restarts", "api-2", "app", 9)
	put(1, "k8s.container.restarts", "api-2", "app", 0)
	// another environment's workload is outside the scope
	put(1, "k8s.container.ready", "other-0", "app", 1, "reliant.env.id", "env-2")

	sc := obsquery.Scope{
		Database: "rdb", Start: base, End: base.Add(10 * time.Minute),
		EnvRestricted: true, EnvIDs: []string{"env-1"},
	}
	st, err := obsquery.Workloads{Scope: sc}.Readiness(60)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params))), st.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	defer func() { _ = rows.Close() }()
	var got []readinessRow
	for rows.Next() {
		var r readinessRow
		if err := rows.Scan(&r.env, &r.workload, &r.bucket, &r.pods, &r.ready, &r.restarts, &r.exit); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	byMin := map[int]readinessRow{}
	for _, r := range got {
		if r.workload != "api" || r.env != "env-1" {
			t.Fatalf("a row outside the scope or workload: %+v", r)
		}
		byMin[int(r.bucket.Sub(base)/time.Minute)] = r
	}
	// readiness rows: minutes 0-5 only; nothing after (a gap is no row).
	for m := 0; m < 6; m++ {
		r, ok := byMin[m]
		if !ok {
			t.Fatalf("no row for minute %d: %+v", m, got)
		}
		wantReady := uint64(2)
		if m >= 3 {
			wantReady = 1 // api-1's sidecar is unready, so api-1 is not ready
		}
		// api-2 has restart samples only: it reports no readiness, so it is not a pod here
		if r.pods != 2 {
			t.Errorf("minute %d: pods = %d, want 2 (%+v)", m, r.pods, r)
		}
		if r.ready != wantReady {
			t.Errorf("minute %d: ready = %d, want %d", m, r.ready, wantReady)
		}
	}
	for m := 6; m < 10; m++ {
		if r, ok := byMin[m]; ok {
			t.Errorf("minute %d has a row although nothing was delivered: %+v", m, r)
		}
	}
	if r := byMin[4]; r.restarts != 1 || r.exit != "OOMKilled" {
		t.Errorf("minute 4: restarts = %v exit = %q, want 1 OOMKilled", r.restarts, r.exit)
	}
	if r := byMin[1]; r.restarts != 0 {
		t.Errorf("minute 1: restarts = %v: a counter reset must count as zero", r.restarts)
	}
	for _, m := range []int{0, 2, 3, 5} {
		if r := byMin[m]; r.restarts != 0 {
			t.Errorf("minute %d: restarts = %v, want 0", m, r.restarts)
		}
	}
}
