// Copyright (c) 2025 Reliant Labs

package dashspeccontract

// The SHIPPED analytics and Releases dashboards' SQL, executed against a real
// ClickHouse. deploy/observability/spec/project_gen.json is forge's generated
// output; forge proves its SQL in pkg/obsbuiltin (the built-in analytics
// dashboards) and internal/obsdash (TestReleasesSQLOnClickHouse). These run the
// bytes this repository ships rather than the generator's: a forge bump that
// changes the dashboards is proven here, at the pin, by this repository's own
// lane.
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

	"github.com/reliant-labs/reliant/internal/gatetest"
)

// clickhouseImage is the ClickHouse forge's own dashboard SQL tests run on.
const clickhouseImage = "clickhouse/clickhouse-server:26.8.24-alpine@sha256:b200d774984e092bf35bec8dbe41aebf2cd875bdc6a0cbb093cebc543730012d"

// releasesDashboardID is forge's generated Releases dashboard.
const releasesDashboardID = "releases"

// clickhouse returns a runner over a ClickHouse container seeded with seed:
// run(database, sql, args...) executes sql in database and returns its TSV.
func clickhouse(t *testing.T, seed string) func(db, sql string, args ...string) string {
	t.Helper()
	testkit.Slow(t, "starts a ClickHouse container and runs every shipped dashboard statement in it")
	if _, err := exec.LookPath("docker"); err != nil {
		gatetest.DockerUnavailable(t, "no docker on this host: the shipped dashboard SQL needs a ClickHouse container")
	}
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if container == "" {
		container = fmt.Sprintf("reliant-dashsql-%d", time.Now().UnixNano())
		if out, err := exec.Command("docker", "run", "-d", "--name", container, "-e", "CLICKHOUSE_PASSWORD=pw", clickhouseImage).CombinedOutput(); err != nil {
			gatetest.DockerUnavailable(t, "cannot start a ClickHouse container (docker run: %v): %s", err, out)
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
	var out []dashspec.Panel
	for _, p := range shippedDashboardPanels(t, dashboard) {
		if p.Kind == dashspec.KindSQL {
			out = append(out, p)
		}
	}
	return out
}

// shippedPanel is one shipped panel of any kind, by id.
func shippedPanel(t *testing.T, dashboard, id string) dashspec.Panel {
	t.Helper()
	for _, p := range shippedDashboardPanels(t, dashboard) {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("shipped %s dashboard has no panel %s", dashboard, id)
	return dashspec.Panel{}
}

// shippedDashboardPanels are every panel of one shipped dashboard, as forge's
// validator parses them.
func shippedDashboardPanels(t *testing.T, dashboard string) []dashspec.Panel {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read the shipped spec: %v", err)
	}
	var spec struct {
		Overview   json.RawMessage   `json:"overview"`
		Dashboards []json.RawMessage `json:"dashboards"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode %s: %v", specPath, err)
	}
	for _, d := range append([]json.RawMessage{spec.Overview}, spec.Dashboards...) {
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
		return parsed.Panels
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

// analyticsSeed is the hosted tenant tables the analytics dashboards read
// (product_events with the edge columns, and error_events; the columns the
// panels name, typed as control-plane's tenant schema types them), with a
// visit, a sign-up and a few product events in env e1 and one event in e2.
// The range under test ends Monday 2026-03-16 12:00 UTC.
const analyticsSeed = `
DROP DATABASE IF EXISTS forge_analytics_test;
CREATE DATABASE forge_analytics_test;
CREATE TABLE forge_analytics_test.product_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String),
    SessionId String, UserId String, EventName LowCardinality(String), Props Map(LowCardinality(String), String),
    Client LowCardinality(String), VisitorId String,
    DayVisitor String DEFAULT '', Country LowCardinality(String) DEFAULT '', Region LowCardinality(String) DEFAULT '',
    DeviceType LowCardinality(String) DEFAULT '', Browser LowCardinality(String) DEFAULT '', OS LowCardinality(String) DEFAULT '',
    Privacy LowCardinality(String) DEFAULT '', SaltScope LowCardinality(String) DEFAULT ''
) ENGINE = MergeTree ORDER BY (ServiceName, EventName, Timestamp);
CREATE TABLE forge_analytics_test.error_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String), SessionId String
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_analytics_test.product_events
    (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId, DayVisitor, Country, DeviceType, Browser, OS) VALUES
    ('2026-03-16 10:00:00', 'web', 'e1', 's1', '', 'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 'news.ycombinator.com'}, 'web', '', 'h1', 'US', 'desktop', 'Chrome', 'macOS'),
    ('2026-03-16 10:01:00', 'web', 'e1', 's1', '', 'page_viewed', {'route': '/pricing'}, 'web', '', 'h1', 'US', 'desktop', 'Chrome', 'macOS'),
    ('2026-03-16 10:02:00', 'api', 'e1', 's1', 'u1', 'auth_signed_up', {'method': 'password'}, 'web', '', 'h1', 'US', 'desktop', 'Chrome', 'macOS'),
    ('2026-03-16 10:03:00', 'api', 'e1', 's1', 'u1', 'order_created', {}, 'web', '', 'h1', 'US', 'desktop', 'Chrome', 'macOS'),
    ('2026-03-09 09:00:00', 'api', 'e1', 's2', 'u2', 'auth_signed_in', {'kind': 'resumed'}, 'web', '', '', '', '', '', ''),
    ('2026-03-16 09:00:00', 'api', 'e2', 's9', 'u9', 'order_created', {}, 'web', '', 'h9', 'DE', 'desktop', 'Firefox', 'Linux');
INSERT INTO forge_analytics_test.error_events VALUES
    ('2026-03-16 10:01:30', 'web', 'e1', 's1');
`

// TestShippedAnalyticsSQLOnClickHouse runs every statement of the shipped
// analytics dashboards (forge's built-in Overview panels, Users, Product and
// Acquisition) against analyticsSeed, in one environment and in every one,
// and checks that another environment never sees e1's events. Forge proves
// the figures themselves against hand-worked answers in pkg/obsbuiltin; this
// proves the bytes this repository ships run at the pin.
func TestShippedAnalyticsSQLOnClickHouse(t *testing.T) {
	ch := clickhouse(t, analyticsSeed)

	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	run := func(sql string, envs ...string) string {
		t.Helper()
		sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end}
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
		return ch("forge_analytics_test", st.SQL, args...)
	}

	for _, st := range analyticsStatements(t) {
		run(st.sql, "e1")
		run(st.sql)
	}

	events := shippedSQL(t, "product", "events", false)
	for env, want := range map[string]string{"e1": "5", "e2": "1"} {
		if got := run(events, env); got != want {
			t.Errorf("product events in %s = %q, want %s: another environment's rows reached it", env, got, want)
		}
	}
	if got := run(events); got != "6" {
		t.Errorf("product events over every environment = %q, want 6", got)
	}

	// The shipped Users "Sign-ins by method" panel counts a person's
	// credential sign-ins only: a synthetic monitor's (recorded as a state
	// event, forge.kind = state, under observe.WithStateEvents) and one with
	// no user are left out (forge #2206). Its own database, so the figures
	// above keep their seed.
	ch("default", signInsSeed)
	signIns := shippedSQL(t, "users", "signins-by-method", false)
	sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end,
		EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "e1"}}}
	st, err := obsquery.SQLPanel{Scope: sc, SQL: signIns, StepSeconds: 86400}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	for k, v := range st.Params {
		args = append(args, "--param_"+k+"="+v)
	}
	if got, want := ch("forge_signins_test", st.SQL, args...), "2026-03-15\tpassword\t2"; got != want {
		t.Errorf("shipped users/signins-by-method in e1 =\n%s\nwant\n%s\n(a state event's or a userless sign-in was counted)", got, want)
	}
}

// signInsSeed is a day of sign-ins in env e1, 2026-03-15: two people's
// credential sign-ins (u1, u2), a synthetic monitor's credential sign-in
// recorded as a state event (um), a credential sign-in with no user, a
// resumed session (not a credential sign-in), and one sign-in in e2.
const signInsSeed = `
DROP DATABASE IF EXISTS forge_signins_test;
CREATE DATABASE forge_signins_test;
CREATE TABLE forge_signins_test.product_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String),
    SessionId String, UserId String, EventName LowCardinality(String), Props Map(LowCardinality(String), String),
    Client LowCardinality(String), VisitorId String
) ENGINE = MergeTree ORDER BY (ServiceName, EventName, Timestamp);
INSERT INTO forge_signins_test.product_events VALUES
    ('2026-03-15 10:00:00', 'api', 'e1', 's1', 'u1', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'web', ''),
    ('2026-03-15 10:01:00', 'api', 'e1', 's2', 'u2', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'web', ''),
    ('2026-03-15 10:02:00', 'api', 'e1', 's3', 'um', 'auth_signed_in', {'kind': 'credential', 'method': 'password', 'forge.kind': 'state'}, 'backend', ''),
    ('2026-03-15 10:03:00', 'api', 'e1', 's4', '', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'web', ''),
    ('2026-03-15 10:04:00', 'api', 'e1', 's5', 'u3', 'auth_signed_in', {'kind': 'resumed'}, 'web', ''),
    ('2026-03-15 10:05:00', 'api', 'e2', 's6', 'u9', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'web', '');
`

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
//	  probes          synthetic monitor spans (forge.probe = true), never a
//	                  request: a slow error on v1 and on v3, a slow success
//	                  on v3, and v4, a version only a probe ever reached.
//	                  A v2 and a v3 span carry forge.probe = false: ordinary
//	                  traffic, like every span with no forge.probe at all
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
    ('2026-03-13 10:01:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 50000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {'forge.probe': 'false'}),
    ('2026-03-13 10:02:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 50000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {'error_class': 'canceled'}),
    ('2026-03-15 08:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 60000000, '', {'service.version': 'v2', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 300000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {'forge.probe': 'false'}),
    ('2026-03-15 10:01:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 300000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:02:00', 'api', 'orders.v1.OrdersService/Watch', 'Server', 'Ok', 60000000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {'forge.rpc.stream_type': 'server'}),
    ('2026-03-14 10:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 10000000, '', {'service.version': 'v9', 'reliant.env.id': 'e2'}, {}),
    ('2026-03-11 10:04:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 9000000000, '', {'service.version': 'v1', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:03:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Error', 9000000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:04:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 9000000000, '', {'service.version': 'v3', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-16 09:00:00', 'api', 'orders.v1.OrdersService/Get', 'Server', 'Ok', 9000000000, '', {'service.version': 'v4', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'});
-- The structured (line) panels' compiler also reads StatusMessage.
ALTER TABLE forge_releases_test.otel_traces ADD COLUMN StatusMessage String DEFAULT '';
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

	// The by-version line panels are structured queries, compiled by the
	// same obsquery compiler the hosted console runs. Like the sql panels
	// above, they count the forge.probe = false spans and every span with no
	// forge.probe, and never a probe: no v4 line, and no probe's 9s or error.
	runPanel := func(id string) string {
		t.Helper()
		p := shippedPanel(t, releasesDashboardID, id)
		// This compiler names the database in its FROM, so the scope names the seed's.
		sc := obsquery.Scope{Database: "forge_releases_test", Start: end.Add(-7 * 24 * time.Hour), End: end,
			EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "e1"}}}
		st, _, err := obsquery.PanelQuery{Scope: sc, Panel: compilerPanel(p), StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatalf("compile shipped releases/%s: %v", id, err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		return ch("forge_releases_test", st.SQL, args...)
	}
	for id, want := range map[string]string{
		// Weighted requests per day and version: v1's 12 as above, v2's
		// forge.probe = false span among its 3 on 2026-03-13, v3's on 2026-03-15.
		"request-rate-by-version": "2026-03-11 00:00:00\t['v1']\t12\n" +
			"2026-03-13 00:00:00\t['v2']\t3\n" +
			"2026-03-15 00:00:00\t['v2']\t1\n" +
			"2026-03-15 00:00:00\t['v3']\t3",
		// Errors over requests (the ratio's two series): the probes' errors
		// on v1 and v3 are not among them.
		"error-rate-by-version": "2026-03-11 00:00:00\t['v1']\t1\t12\n" +
			"2026-03-13 00:00:00\t['v2']\t1\t3\n" +
			"2026-03-15 00:00:00\t['v2']\t0\t1\n" +
			"2026-03-15 00:00:00\t['v3']\t1\t3",
		// No probe's 9s: v1's p95 is its 200ms span; v3's is the stream's
		// lifetime, which this panel does not set apart.
		"p95-by-version": "2026-03-11 00:00:00\t['v1']\t200\n" +
			"2026-03-13 00:00:00\t['v2']\t50\n" +
			"2026-03-15 00:00:00\t['v2']\t60\n" +
			"2026-03-15 00:00:00\t['v3']\t60000",
	} {
		if got := runPanel(id); got != want {
			t.Errorf("shipped releases/%s in e1 =\n%s\nwant\n%s\n(a forge.probe span was counted, or ordinary traffic was dropped)", id, got, want)
		}
	}
}

// overviewProbeSeed is a day of reliant's own server spans in env e1, for the
// overview's structured panels, which read only reliant's services: r1's
// ordinary traffic (two failed Sends, one with no forge.probe and one with
// forge.probe = false, and a successful Send), and synthetic monitor spans
// (forge.probe = true): a failed Send and a failed health check on r1, and
// r2, a version only a probe ever reached.
const overviewProbeSeed = `
DROP DATABASE IF EXISTS forge_overview_test;
CREATE DATABASE forge_overview_test;
CREATE TABLE forge_overview_test.otel_traces (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), SpanName LowCardinality(String),
    SpanKind LowCardinality(String), StatusCode LowCardinality(String), StatusMessage String, Duration UInt64, TraceState String,
    ResourceAttributes Map(LowCardinality(String), String), SpanAttributes Map(LowCardinality(String), String)
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
INSERT INTO forge_overview_test.otel_traces VALUES
    ('2026-03-15 10:00:00', 'reliant-api-server', 'reliant.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:01:00', 'reliant-api-server', 'reliant.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'false'}),
    ('2026-03-15 10:02:00', 'reliant-api-server', 'reliant.v1.ChatService/Send', 'Server', 'Ok', '', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {}),
    ('2026-03-15 10:03:00', 'reliant-api-server', 'reliant.v1.ChatService/Send', 'Server', 'Error', 'boom', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:04:00', 'reliant-api-server', 'reliant.v1.HealthService/Check', 'Server', 'Error', 'probe failed', 100000000, '', {'service.version': 'r1', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'}),
    ('2026-03-15 10:05:00', 'reliant-api-server', 'reliant.v1.ChatService/Send', 'Server', 'Ok', '', 100000000, '', {'service.version': 'r2', 'reliant.env.id': 'e1'}, {'forge.probe': 'true'});
`

// TestShippedOverviewExcludesProbes runs the overview's structured panels
// that leave synthetic monitor spans out (forge.probe = true) against
// overviewProbeSeed: ordinary traffic, forge.probe = false or no forge.probe
// at all, is counted; a probe never is.
func TestShippedOverviewExcludesProbes(t *testing.T) {
	ch := clickhouse(t, overviewProbeSeed)
	end := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	for id, want := range map[string]string{
		// The two ordinary failed Sends, and no probe's Send or health check.
		"top-errors": "['reliant-api-server','reliant.v1.ChatService/Send','boom']\t2",
		// r1's three ordinary requests, and no r2.
		"request-rate-by-version": "2026-03-15 00:00:00\t['r1']\t3",
	} {
		p := shippedPanel(t, "overview", id)
		sc := obsquery.Scope{Database: "forge_overview_test", Start: end.Add(-24 * time.Hour), End: end,
			EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "e1"}}}
		st, _, err := obsquery.PanelQuery{Scope: sc, Panel: compilerPanel(p), StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatalf("compile shipped overview/%s: %v", id, err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		if got := ch("forge_overview_test", st.SQL, args...); got != want {
			t.Errorf("shipped overview/%s in e1 =\n%s\nwant\n%s\n(a forge.probe span was counted, or ordinary traffic was dropped)", id, got, want)
		}
	}
}

// compilerPanel is a shipped panel in obsquery's vocabulary.
func compilerPanel(p dashspec.Panel) obsquery.Panel {
	preds := func(in []dashspec.Predicate) []obsquery.PanelPredicate {
		out := make([]obsquery.PanelPredicate, len(in))
		for i, w := range in {
			out[i] = obsquery.PanelPredicate{Field: w.Field, Op: string(w.Op), Value: w.Value, Values: w.Values}
		}
		return out
	}
	out := obsquery.Panel{ID: p.ID, Kind: string(p.Kind), Signal: string(p.Signal), Ratio: p.Ratio, Formula: p.Formula,
		Limit: p.Limit, SQL: p.SQL, Where: preds(p.Where), GroupBy: p.GroupBy, HasError: p.HasError}
	for _, s := range p.Series {
		out.Series = append(out.Series, obsquery.PanelSeries{Label: s.Label, Agg: string(s.Agg), Quantile: s.Quantile,
			Field: s.Field, MetricType: s.MetricType, Where: preds(s.Where)})
	}
	return out
}
