// Copyright (c) 2025 Reliant Labs

package dashspeccontract

// The SHIPPED product-usage and Releases dashboards' SQL, executed against a
// real ClickHouse. deploy/observability/spec/project_gen.json is forge's
// generated output; forge proves its generator's SQL in internal/obsdash
// (TestProductUsageSQLOnClickHouse, TestReleasesSQLOnClickHouse). These are the
// same fixtures and the same hand-worked answers, run over the bytes this
// repository ships rather than the generator's: a forge bump that changes the
// dashboards is proven here, at the pin, by this repository's own lane.
//
// Every statement is compiled with forge's sql-panel compiler (obsquery), the
// one the hosted console runs, so the parameters are bound exactly as in
// production. The seeds carry a second environment whose rows must never reach
// the first's figures.
//
// It starts its own ClickHouse container (the image forge's gate pins), or uses
// FORGE_ALERT_CLICKHOUSE=<running container> (password "pw") when set.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// clickhouseImage is the ClickHouse forge's own dashboard SQL tests run on.
const clickhouseImage = "clickhouse/clickhouse-server:26.8.24-alpine@sha256:b200d774984e092bf35bec8dbe41aebf2cd875bdc6a0cbb093cebc543730012d"

// releasesDashboardID is forge's generated Releases dashboard.
const releasesDashboardID = "releases"

// retentionWeeks is the number of week columns forge's weekly-retention panel
// draws (forge internal/obsdash retentionWeeks).
const retentionWeeks = 12

// clickhouse returns a runner over a ClickHouse container seeded with seed:
// run(database, sql, args...) executes sql in database and returns its TSV.
func clickhouse(t *testing.T, seed string) func(db, sql string, args ...string) string {
	t.Helper()
	testkit.Slow(t, "starts a ClickHouse container and runs every shipped dashboard statement in it")
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker on this host: the shipped dashboard SQL needs a ClickHouse container")
	}
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if container == "" {
		container = fmt.Sprintf("reliant-dashsql-%d", time.Now().UnixNano())
		if out, err := exec.Command("docker", "run", "-d", "--name", container, "-e", "CLICKHOUSE_PASSWORD=pw", clickhouseImage).CombinedOutput(); err != nil {
			t.Skipf("cannot start a ClickHouse container (docker run: %v): %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", container).Run() })
		deadline := time.Now().Add(2 * time.Minute)
		for exec.Command("docker", "exec", container, "clickhouse-client", "--password", "pw", "-q", "SELECT 1").Run() != nil {
			if time.Now().After(deadline) {
				t.Fatalf("ClickHouse container %s did not become ready in 2m", container)
			}
			time.Sleep(time.Second)
		}
	}
	cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery")
	cmd.Stdin = strings.NewReader(seed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	return func(db, sql string, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--database", db, "--joined_subquery_requires_alias=0", "--multiquery", "--format", "TSV"}, args...)...)
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
}

// shippedPanels are the sql panels of one shipped dashboard, as forge's
// validator parses them.
func shippedPanels(t *testing.T, dashboard string) []dashspec.Panel {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read the shipped spec: %v", err)
	}
	var spec struct {
		Dashboards []json.RawMessage `json:"dashboards"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode %s: %v", specPath, err)
	}
	for _, d := range spec.Dashboards {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(d, &probe); err != nil || probe.ID != dashboard {
			continue
		}
		parsed, err := dashspec.ParseDashboard(dashboard+".json", d)
		if err != nil {
			t.Fatalf("forge's validator refuses the shipped %q dashboard: %v", dashboard, err)
		}
		var out []dashspec.Panel
		for _, p := range parsed.Panels {
			if p.Kind == dashspec.KindSQL {
				out = append(out, p)
			}
		}
		return out
	}
	t.Fatalf("%s has no %q dashboard", specPath, dashboard)
	return nil
}

// shippedSQL is one shipped sql panel's statement, or its trend.
func shippedSQL(t *testing.T, dashboard, id string, trend bool) string {
	t.Helper()
	for _, p := range shippedPanels(t, dashboard) {
		if p.ID != id {
			continue
		}
		if trend {
			if p.TrendSQL == "" {
				t.Fatalf("shipped %s panel %s has no trendSql", dashboard, id)
			}
			return p.TrendSQL
		}
		return p.SQL
	}
	t.Fatalf("shipped %s dashboard has no sql panel %s", dashboard, id)
	return ""
}

// productEventsSeed is the hosted product_events table (the columns the
// panels read; control-plane's tenant schema has these and more) with a week
// and a half of events in env e1 and one stray event in e2. The range under
// test ends Monday 2026-03-16 12:00 UTC.
//
//	u1  active today, 3 days ago and 9 days ago (cohort 2026-03-02)
//	u2  active today, and 105 days ago: past the 90-day first-seen window, so
//	    still new today (cohort 2026-03-16)
//	u3  active 20 days ago only (cohort 2026-02-23)
//	anonymous page views count as views, never as users
const productEventsSeed = `
DROP DATABASE IF EXISTS forge_product_usage_test;
CREATE DATABASE forge_product_usage_test;
CREATE TABLE forge_product_usage_test.product_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String),
    SessionId String, UserId String, EventName LowCardinality(String), Props Map(LowCardinality(String), String)
) ENGINE = MergeTree ORDER BY (ServiceName, EventName, Timestamp);
INSERT INTO forge_product_usage_test.product_events VALUES
    ('2026-03-16 10:00:00', 'api', 'e1', 's1', 'u1', 'order_item_created', {}),
    ('2026-03-16 10:05:00', 'api', 'e1', 's1', 'u1', 'order_item_updated', {}),
    ('2026-03-16 10:06:00', 'api', 'e1', 's1', 'u1', 'order_item_deleted', {}),
    ('2026-03-16 11:00:00', 'api', 'e1', 's2', 'u2', 'order_item_created', {}),
    ('2026-03-16 11:00:00', 'api', 'e1', 's2', 'u2', 'invoice_created', {}),
    ('2026-03-16 11:01:00', 'shop-frontend', 'e1', 's2', 'u2', 'page_viewed', {'route': '/orders'}),
    ('2026-03-16 11:02:00', 'shop-frontend', 'e1', 's9', '', 'page_viewed', {'route': '/orders'}),
    ('2026-03-16 11:03:00', 'shop-frontend', 'e1', 's9', '', 'page_viewed', {'route': '/'}),
    ('2026-03-13 09:00:00', 'api', 'e1', 's3', 'u1', 'subscription_started', {'plan': 'pro'}),
    ('2026-03-07 09:00:00', 'api', 'e1', 's4', 'u1', 'order_item_created', {}),
    ('2026-02-24 09:00:00', 'api', 'e1', 's5', 'u3', 'order_item_created', {}),
    ('2025-12-01 09:00:00', 'api', 'e1', 's7', 'u2', 'order_item_created', {}),
    ('2026-03-16 09:00:00', 'api', 'e2', 's6', 'u9', 'order_item_created', {});
`

// TestShippedUsageSQLOnClickHouse runs every shipped Product usage panel and
// trend against productEventsSeed and checks what the numbers mean.
func TestShippedUsageSQLOnClickHouse(t *testing.T) {
	ch := clickhouse(t, productEventsSeed)

	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	statement := func(id string, trend bool) string { t.Helper(); return shippedSQL(t, usageDashboardID, id, trend) }
	runSQL := func(sql string, env string) string {
		t.Helper()
		sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end,
			EnvRestricted: true, EnvIDs: []string{env}, Envs: []obsquery.Env{{ID: env, Name: env}}}
		st, err := obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		return ch("forge_product_usage_test", st.SQL, args...)
	}
	run := func(id, env string) string { t.Helper(); return runSQL(statement(id, false), env) }
	trend := func(id, env string) string { t.Helper(); return runSQL(statement(id, true), env) }

	for id, want := range map[string]string{
		// dau = u1, u2; wau adds nobody (u1 again); mau adds u3.
		"dau": "2", "wau": "2", "mau": "3", "stickiness": "0.6667",
		// u1, u2 and u3 are all first seen in the range (u2's December event is
		// outside the first-seen window).
		"new-users": "3",
		// e1's events in the range, over its 3 signed-in users.
		"events": "11", "events-per-user": "3.7",
		"records-written": "7",
		"actions-per-entity": "order_item\t4\t1\t1\n" +
			"invoice\t1\t0\t0",
		"top-pages": "/orders\t1\t1\n" +
			"/\t0\t1",
		// u1's first event (2026-03-07 in range) is new; 13th and 16th returning.
		"new-vs-returning": "2026-02-24 00:00:00\t1\t0\n" +
			"2026-03-07 00:00:00\t1\t0\n" +
			"2026-03-13 00:00:00\t0\t1\n" +
			"2026-03-16 00:00:00\t1\t1",
		// Week 1 and 2 after each cohort; weeks 4 and 8 are still to come.
		"retention-curve": "2026-02-23\t0\t0\t\\N\t\\N\n" +
			"2026-03-02\t1\t1\t\\N\t\\N\n" +
			"2026-03-16\t\\N\t\\N\t\\N\t\\N",
	} {
		if got := run(id, "e1"); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", id, got, want)
		}
	}

	// Retention: three cohorts. u3's (2026-02-23) never came back; u1's
	// (2026-03-02) is back in weeks 1 and 2, and week 3 is still to come (NULL);
	// u2's is this week (its event 105 days back is outside the first-seen
	// window, so it neither moves u2's cohort nor drops it), so every later
	// week is.
	// Each row is named by its cohort and size, and its cells are fractions.
	ret := strings.Split(run("weekly-retention", "e1"), "\n")
	if len(ret) != 3 {
		t.Fatalf("weekly-retention = %q, want 3 cohorts", ret)
	}
	if !strings.HasPrefix(ret[0], "2026-02-23 · 1 user\t0\t0\t0\t") || !strings.HasPrefix(ret[1], "2026-03-02 · 1 user\t1\t1\t\\N\t") ||
		!strings.HasPrefix(ret[2], "2026-03-16 · 1 user\t\\N\t") || strings.Count(ret[0], "\t") != retentionWeeks-1 {
		t.Errorf("weekly-retention =\n%s", strings.Join(ret, "\n"))
	}

	// Active users: each day's users, and the rolling seven days ending that
	// day (u3, seen 2026-02-24, counts through 2026-03-02), never past the end
	// of the range.
	active := run("active-users-over-time", "e1")
	for _, line := range []string{"2026-02-24\t1\t1", "2026-02-25\t1\t0", "2026-03-02\t1\t0", "2026-03-13\t1\t1", "2026-03-16\t2\t2"} {
		if !strings.Contains(active, line+"\n") && !strings.HasSuffix(active, line) {
			t.Errorf("active-users-over-time lacks %q:\n%s", line, active)
		}
	}
	if strings.Contains(active, "2026-03-03\t") || strings.Contains(active, "2026-03-17") || strings.Contains(active, "2026-02-23") {
		t.Errorf("active-users-over-time has a day outside the range or the rolling window:\n%s", active)
	}
	if got := run("records-by-entity", "e1"); !strings.Contains(got, "2026-03-16 00:00:00\tinvoice\t1") || !strings.Contains(got, "2026-03-16 00:00:00\torder_item\t4") {
		t.Errorf("records-by-entity =\n%s", got)
	}

	// The figures' trends: one row per UTC day with data, the figure's own
	// measure over that day (or the window ending on it).
	for id, want := range map[string]string{
		"dau":             "2026-02-24\t1\n2026-03-07\t1\n2026-03-13\t1\n2026-03-16\t2",
		"events":          "2026-02-24\t1\n2026-03-07\t1\n2026-03-13\t1\n2026-03-16\t8",
		"records-written": "2026-02-24\t1\n2026-03-07\t1\n2026-03-16\t5",
		// u3 on 2026-02-24, u1 on 2026-03-07, u2 today.
		"new-users": "2026-02-24\t1\n2026-03-07\t1\n2026-03-16\t1",
	} {
		if got := trend(id, "e1"); got != want {
			t.Errorf("%s trend =\n%s\nwant\n%s", id, got, want)
		}
	}
	// The rolling windows: every day of the range a window still holds
	// someone. On the last day WAU holds u1 and u2; MAU adds u3 (seen
	// 2026-02-24, inside 30 days); stickiness is today's 2 of those 3.
	for id, last := range map[string]string{"wau": "2026-03-16\t2", "mau": "2026-03-16\t3", "stickiness": "2026-03-16\t0.6667"} {
		got := trend(id, "e1")
		if !strings.HasSuffix(got, last) {
			t.Errorf("%s trend ends\n%s\nwant it to end %q", id, got, last)
		}
		if strings.Contains(got, "2026-02-13") || strings.Contains(got, "2026-03-17") {
			t.Errorf("%s trend has a day outside the range:\n%s", id, got)
		}
	}
	// The rolling trends' calendar days: over a range from noon on 2026-03-02
	// to midnight on 2026-03-17, the first day's 7-day window starts at
	// midnight six days earlier, so u3's 09:00 event on 2026-02-24 counts; and
	// no point is drawn for 2026-03-17, the day the range ends on.
	ranged := func(id string, trend bool, start, stop time.Time) string {
		t.Helper()
		sc := obsquery.Scope{Database: "t_org_a", Start: start, End: stop,
			EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "e1"}}}
		st, err := obsquery.SQLPanel{Scope: sc, SQL: statement(id, trend), StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		return ch("forge_product_usage_test", st.SQL, args...)
	}
	noon, midnight := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	wau := ranged("wau", true, noon, midnight)
	if !strings.HasPrefix(wau, "2026-03-02\t1\n") || !strings.HasSuffix(wau, "2026-03-16\t2") || strings.Contains(wau, "2026-03-17") {
		t.Errorf("wau trend over a noon start and a midnight end =\n%s\nwant 2026-03-02 holding u3's early event first, 2026-03-16 last", wau)
	}
	// The Active users chart itself, over the same noon start and midnight
	// end: its first day's weekly window holds u3's 09:00 event of 2026-02-24
	// (day, weekly, daily), its last point is 2026-03-16, and 2026-03-17 (the
	// day the exclusive end starts) is not drawn.
	activeRanged := ranged("active-users-over-time", false, noon, midnight)
	if !strings.HasPrefix(activeRanged, "2026-03-02\t1\t0\n") || !strings.HasSuffix(activeRanged, "2026-03-16\t2\t2") ||
		strings.Contains(activeRanged, "2026-03-17") || strings.Contains(activeRanged, "2026-03-01") {
		t.Errorf("active-users-over-time over a noon start and a midnight end =\n%s\nwant 2026-03-02 holding u3's early lookback event first, 2026-03-16 last", activeRanged)
	}
	if got := trend("events-per-user", "e1"); !strings.HasSuffix(got, "2026-03-16\t4") {
		t.Errorf("events-per-user trend ends\n%s\nwant 8 events over 2 users today", got)
	}

	// Every other panel and trend runs; another environment sees none of e1's events.
	for _, p := range shippedPanels(t, usageDashboardID) {
		run(p.ID, "e1")
		if p.TrendSQL != "" {
			trend(p.ID, "e1")
		}
	}
	if got := run("events-by-name", "e2"); got != "order_item_created\t1\t0" {
		t.Errorf("events-by-name in e2 = %q, want only e2's event", got)
	}
	if got := run("most-active-users", "e1"); !strings.HasPrefix(got, "u1\t5\t3\t4\t") {
		t.Errorf("most-active-users =\n%s", got)
	}
}

// releasesSeed is a week of one project's telemetry in env e1 (and one stray
// row of each kind in e2), over the tables the Releases dashboard's sql panels
// read: the columns they use, typed as the control plane's tenant schema types
// them. The range under test is the week ending Monday 2026-03-16 12:00 UTC.
//
// Three releases of api: v1 ran before 2026-03-12, v2 from then, v3 from
// 2026-03-15 (v2 still serving that morning: the rollout overlap).
//
//	server spans (api, e1)
//	  v1  2026-03-11  three spans, one an error, one tail-sampled at 10%
//	                  (weight 10): 12 requests, 1 error; p50 100ms, p95 200ms
//	      2026-03-11  a client span (not a request) and one before the range
//	  v2  2026-03-13  three spans, the error one a cancellation (not a failure)
//	      2026-03-15  one more at 08:00
//	  v3  2026-03-15  three spans: one error, one a stream (a request, but no
//	                  latency: its 60s is the stream's lifetime)
//	  v9  (e2)        one span, never in e1's figures
//
//	browser sessions (shop-web, e1)
//	  v1  s1 clean, s2 two errors; s10 began before the range
//	  v2  s3 (two day rows, one error on the second), s4 clean, s11 on 2026-03-15
//	  v3  s5, s6, s7 clean, s8 one error
//
//	issues (first seen in the 30 days before the end of the range)
//	  server  A v3 x3 (new), B v3 x2 (under the 3-event bar), C first seen
//	          2026-03-01 on v1 (not new), D v2 x3 (new), H v3 x4 (new),
//	          E in e2
//	  browser F: v2's stack 111 once, then v3's stack 222 three times — one
//	          issue across two builds, new in v2; G: v3's stack 333 x3, new in
//	          v3 (its older error_stacks row had no fingerprint yet)
const releasesSeed = `
DROP DATABASE IF EXISTS forge_releases_test;
CREATE DATABASE forge_releases_test;
CREATE TABLE forge_releases_test.otel_traces (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), SpanName LowCardinality(String),
    SpanKind LowCardinality(String), StatusCode LowCardinality(String), Duration UInt64, TraceState String,
    ResourceAttributes Map(LowCardinality(String), String), SpanAttributes Map(LowCardinality(String), String)
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_releases_test.otel_traces VALUES
    ('2026-03-11 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 100000000, '', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-11 10:01:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 100000000, 'ot=th:e6666666666666', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-11 10:02:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 200000000, '', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-11 10:03:00', 'api', 'payments.Charge', 'Client', 'Ok', 5000000, '', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-08 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 10000000, '', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-13 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 50000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-13 10:01:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 50000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-13 10:02:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 50000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {'error_class': 'canceled'}),
    ('2026-03-15 08:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 60000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 300000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:01:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 300000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:02:00', 'api', 'orders.v1.OrdersService/Watch', 'Server', 'Ok', 60000000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {'forge.rpc.stream_type': 'server'}),
    ('2026-03-14 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 10000000, '', {'service.version': 'v9', 'reliant.env.id': 'e2'}, {});
CREATE TABLE forge_releases_test.rum_sessions (
    ServiceName LowCardinality(String), SessionId String, Day Date,
    Start SimpleAggregateFunction(min, DateTime64(3)), End SimpleAggregateFunction(max, DateTime64(3)),
    ServiceVersion SimpleAggregateFunction(max, String), EnvId SimpleAggregateFunction(max, String),
    Errors SimpleAggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree PARTITION BY Day ORDER BY (ServiceName, SessionId);
INSERT INTO forge_releases_test.rum_sessions VALUES
    ('shop-web', 's1', '2026-03-11', '2026-03-11 09:00:00', '2026-03-11 09:05:00', 'v1', 'e1', 0),
    ('shop-web', 's2', '2026-03-11', '2026-03-11 09:30:00', '2026-03-11 09:40:00', 'v1', 'e1', 2),
    ('shop-web', 's10', '2026-03-09', '2026-03-09 08:00:00', '2026-03-09 08:10:00', 'v1', 'e1', 0),
    ('shop-web', 's3', '2026-03-13', '2026-03-13 09:00:00', '2026-03-13 23:59:00', 'v2', 'e1', 0),
    ('shop-web', 's3', '2026-03-14', '2026-03-14 00:00:00', '2026-03-14 00:20:00', 'v2', 'e1', 1),
    ('shop-web', 's4', '2026-03-13', '2026-03-13 12:00:00', '2026-03-13 12:30:00', 'v2', 'e1', 0),
    ('shop-web', 's11', '2026-03-15', '2026-03-15 08:00:00', '2026-03-15 08:10:00', 'v2', 'e1', 0),
    ('shop-web', 's5', '2026-03-15', '2026-03-15 09:00:00', '2026-03-15 09:05:00', 'v3', 'e1', 0),
    ('shop-web', 's6', '2026-03-15', '2026-03-15 09:10:00', '2026-03-15 09:15:00', 'v3', 'e1', 0),
    ('shop-web', 's7', '2026-03-15', '2026-03-15 09:20:00', '2026-03-15 09:25:00', 'v3', 'e1', 0),
    ('shop-web', 's8', '2026-03-15', '2026-03-15 09:30:00', '2026-03-15 09:35:00', 'v3', 'e1', 1),
    ('shop-web', 's9', '2026-03-14', '2026-03-14 09:00:00', '2026-03-14 09:05:00', 'v9', 'e2', 0);
CREATE TABLE forge_releases_test.server_error_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), ServiceVersion LowCardinality(String),
    EnvId LowCardinality(String), Fingerprint String
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_releases_test.server_error_events VALUES
    ('2026-03-15 10:01:00', 'api', 'v3', 'e1', 'A'), ('2026-03-15 10:02:00', 'api', 'v3', 'e1', 'A'), ('2026-03-15 10:03:00', 'api', 'v3', 'e1', 'A'),
    ('2026-03-15 10:05:00', 'api', 'v3', 'e1', 'B'), ('2026-03-15 10:06:00', 'api', 'v3', 'e1', 'B'),
    ('2026-03-01 10:00:00', 'api', 'v1', 'e1', 'C'),
    ('2026-03-15 11:00:00', 'api', 'v3', 'e1', 'C'), ('2026-03-15 11:01:00', 'api', 'v3', 'e1', 'C'), ('2026-03-15 11:02:00', 'api', 'v3', 'e1', 'C'),
    ('2026-03-13 10:00:00', 'api', 'v2', 'e1', 'D'), ('2026-03-13 10:01:00', 'api', 'v2', 'e1', 'D'), ('2026-03-13 10:02:00', 'api', 'v2', 'e1', 'D'),
    ('2026-03-16 09:00:00', 'api', 'v3', 'e1', 'H'), ('2026-03-16 09:01:00', 'api', 'v3', 'e1', 'H'),
    ('2026-03-16 09:02:00', 'api', 'v3', 'e1', 'H'), ('2026-03-16 09:03:00', 'api', 'v3', 'e1', 'H'),
    ('2026-03-14 10:00:00', 'api', 'v9', 'e2', 'E'), ('2026-03-14 10:01:00', 'api', 'v9', 'e2', 'E'), ('2026-03-14 10:02:00', 'api', 'v9', 'e2', 'E');
CREATE TABLE forge_releases_test.error_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), ServiceVersion LowCardinality(String),
    EnvId LowCardinality(String), RawStackHash UInt64
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_releases_test.error_events VALUES
    ('2026-03-13 11:00:00', 'shop-web', 'v2', 'e1', 111),
    ('2026-03-15 09:31:00', 'shop-web', 'v3', 'e1', 222), ('2026-03-15 09:32:00', 'shop-web', 'v3', 'e1', 222), ('2026-03-15 09:33:00', 'shop-web', 'v3', 'e1', 222),
    ('2026-03-15 09:40:00', 'shop-web', 'v3', 'e1', 333), ('2026-03-15 09:41:00', 'shop-web', 'v3', 'e1', 333), ('2026-03-15 09:42:00', 'shop-web', 'v3', 'e1', 333);
CREATE TABLE forge_releases_test.error_stacks (
    ServiceName LowCardinality(String), ServiceVersion LowCardinality(String), RawStackHash UInt64,
    Fingerprint String, UpdatedAt DateTime64(3)
) ENGINE = ReplacingMergeTree(UpdatedAt) ORDER BY (ServiceName, ServiceVersion, RawStackHash);
INSERT INTO forge_releases_test.error_stacks VALUES
    ('shop-web', 'v2', 111, 'F', '2026-03-13 11:00:01'),
    ('shop-web', 'v3', 222, 'F', '2026-03-15 09:31:01'),
    ('shop-web', 'v3', 333, '', '2026-03-15 09:40:01'),
    ('shop-web', 'v3', 333, 'G', '2026-03-15 09:40:30');
`

// TestShippedReleasesSQLOnClickHouse runs every shipped Releases sql panel
// against releasesSeed and checks what the numbers mean.
func TestShippedReleasesSQLOnClickHouse(t *testing.T) {
	ch := clickhouse(t, releasesSeed)

	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	run := func(id string, envs ...string) string {
		t.Helper()
		sql := shippedSQL(t, releasesDashboardID, id, false)
		sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-7 * 24 * time.Hour), End: end}
		if len(envs) > 0 {
			sc.EnvRestricted, sc.EnvIDs = true, envs
			for _, e := range envs {
				sc.Envs = append(sc.Envs, obsquery.Env{ID: e, Name: e})
			}
		}
		st, err := obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		return ch("forge_releases_test", st.SQL, args...)
	}

	for id, want := range map[string]string{
		// Newest first. v3: three requests (the stream counts), one error;
		// latency over its two unary spans. v2: four requests and no error
		// (the cancellation is the client's). v1: 1 + 10 + 1 weighted
		// requests, one error (1/12); p50 is the weight-10 100ms span, p95
		// and p99 reach the 200ms one (11 of 12 is under 95%).
		"version-comparison": "v3\t2026-03-15 10:00\t2026-03-15 10:02\t3\t33.33\t300\t300\t300\n" +
			"v2\t2026-03-13 10:00\t2026-03-15 08:00\t4\t0\t50\t60\t60\n" +
			"v1\t2026-03-11 10:00\t2026-03-11 10:02\t12\t8.33\t100\t200\t200",
		// Each day's weighted server requests by version, as shares: on the
		// rollout day v2 served 1 of 4.
		"backend-version-share": "2026-03-11 00:00:00\tv1\t1\n" +
			"2026-03-13 00:00:00\tv2\t1\n" +
			"2026-03-15 00:00:00\tv2\t0.25\n" +
			"2026-03-15 00:00:00\tv3\t0.75",
		// Sessions by the day they began: s3 counts once (its two day rows
		// merge), s10 began before the range.
		"browser-version-share": "2026-03-11 00:00:00\tv1\t1\n" +
			"2026-03-13 00:00:00\tv2\t1\n" +
			"2026-03-15 00:00:00\tv2\t0.2\n" +
			"2026-03-15 00:00:00\tv3\t0.8",
		// v3: 4 sessions, s8 hit an error. v2: s3's error is on its second
		// day row, so 1 of 3. v1: s2 of 2.
		"crash-free-sessions-by-version": "v3\t2026-03-15 09:00\t4\t1\t75\n" +
			"v2\t2026-03-13 09:00\t3\t1\t66.67\n" +
			"v1\t2026-03-11 09:00\t2\t1\t50",
		// v2 raised D and F (F's v3 stack is the same issue); v3 raised A, G
		// and H. B is under the bar; C was first seen before the range.
		"new-issues-by-release": "v2\t2\nv3\t3",
	} {
		if got := run(id, "e1"); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", id, got, want)
		}
	}

	// Another environment sees only its own rows, and every environment sees
	// both.
	if got := run("version-comparison", "e2"); !strings.HasPrefix(got, "v9\t2026-03-14 10:00\t2026-03-14 10:00\t1\t0\t") || strings.Contains(got, "\n") {
		t.Errorf("version-comparison in e2 = %q, want v9 alone", got)
	}
	if got := run("new-issues-by-release", "e2"); got != "v9\t1" {
		t.Errorf("new-issues-by-release in e2 = %q, want E alone", got)
	}
	if got := run("crash-free-sessions-by-version"); !strings.Contains(got, "v9\t2026-03-14 09:00\t1\t0\t100") || !strings.HasPrefix(got, "v3\t") {
		t.Errorf("crash-free-sessions-by-version over every environment =\n%s", got)
	}
}
