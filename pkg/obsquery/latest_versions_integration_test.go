//go:build integration

package obsquery_test

// Workloads.LatestVersions against a real ClickHouse: one row per (env,
// service), the version reported last, however many versions came before it.
//
//	go test -tags integration -run LatestVersions ./pkg/obsquery/

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestLatestVersionsOnePerWorkload(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	for _, ddl := range []string{
		`CREATE DATABASE lvdb`,
		`CREATE TABLE lvdb.otel_metrics_gauge (
  ResourceAttributes Map(LowCardinality(String), String), ServiceName LowCardinality(String), MetricName LowCardinality(String),
  Attributes Map(LowCardinality(String), String), TimeUnix DateTime, Value Float64) ENGINE = MergeTree ORDER BY (MetricName, TimeUnix)`,
		`CREATE TABLE lvdb.otel_traces (
  Timestamp DateTime64(9), ServiceName LowCardinality(String), ResourceAttributes Map(LowCardinality(String), String))
  ENGINE = MergeTree ORDER BY Timestamp`,
	} {
		if err := c.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	gauge := func(env, svc, version string, at time.Time) {
		t.Helper()
		res := map[string]string{"reliant.env.id": env, "service.version": version}
		if err := c.Exec(ctx, `INSERT INTO lvdb.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES (?, ?, 'm', map(), ?, 1)`,
			res, svc, at); err != nil {
			t.Fatal(err)
		}
	}
	// The span time goes in as epoch nanoseconds: clickhouse-go binds a
	// time.Time `?` argument as toDateTime('<unix seconds>'), which drops the
	// sub-second part the edge case below depends on.
	span := func(env, svc, version string, at time.Time) {
		t.Helper()
		res := map[string]string{"reliant.env.id": env, "service.version": version}
		if err := c.Exec(ctx, `INSERT INTO lvdb.otel_traces (Timestamp, ServiceName, ResourceAttributes) VALUES (fromUnixTimestamp64Nano(?), ?, ?)`,
			at.UnixNano(), svc, res); err != nil {
			t.Fatal(err)
		}
	}
	// Three workloads each report more versions than Versions' row cap, oldest
	// first, so the cap holds only the earliest ones of the first workload. The
	// newest of each comes last.
	names := []string{"api", "web", "worker"}
	n := obsquery.MaxWorkloadChanges + 50
	for wi, w := range names {
		for i := 0; i < n; i++ {
			gauge("env-1", w, fmt.Sprintf("v%04d", i), base.Add(time.Duration(wi*n+i)*time.Second))
		}
	}
	// A rollback: web re-reports an old version after its newest one, in spans.
	span("env-1", "web", "v0001", base.Add(time.Hour))
	// Two versions in one second of spans: the later report wins although its
	// version string is the smaller one.
	span("env-1", "edge", "z-earlier", base.Add(2*time.Hour+100*time.Millisecond))
	span("env-1", "edge", "b-later", base.Add(2*time.Hour+900*time.Millisecond))
	// An exact tie goes to the greater version string.
	gauge("env-1", "worker", "a-tie", base.Add(time.Duration(3*n-1)*time.Second))
	// Another environment of api, and one outside the scope.
	gauge("env-2", "api", "v-env2", base.Add(time.Minute))
	gauge("env-3", "api", "v-outside", base.Add(2*time.Hour))
	// A service not asked for.
	gauge("env-1", "other", "v-other", base.Add(2*time.Hour))

	sc := obsquery.Scope{
		Database: "lvdb", Start: base, End: base.Add(3 * time.Hour),
		EnvRestricted: true, EnvIDs: []string{"env-1", "env-2"},
	}
	st, err := obsquery.Workloads{Scope: sc}.LatestVersions(append(names, "edge"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params))), st.SQL)
	if err != nil {
		t.Fatalf("%v\n%s", err, st.SQL)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var env, svc, version string
		var last time.Time
		if err := rows.Scan(&env, &svc, &version, &last); err != nil {
			t.Fatal(err)
		}
		k := env + "/" + svc
		if _, dup := got[k]; dup {
			t.Fatalf("two rows for %s", k)
		}
		got[k] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"env-1/api":    fmt.Sprintf("v%04d", n-1),
		"env-1/web":    "v0001",
		"env-1/worker": fmt.Sprintf("v%04d", n-1), // "v…" > "a-tie" at the same second
		"env-2/api":    "v-env2",
		"env-1/edge":   "b-later",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: version %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
