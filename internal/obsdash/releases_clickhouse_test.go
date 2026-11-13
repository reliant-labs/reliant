package obsdash

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

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

// TestReleasesSQLOnClickHouse runs every sql panel of the Releases dashboard
// against a real ClickHouse, bound the way the console binds a sql panel
// (obsquery's compiled parameters), and checks what the numbers mean against
// answers worked by hand from releasesSeed. Same container and conventions as
// TestDefaultAlertSQLOnClickHouse.
func TestReleasesSQLOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	exec1 := func(sql string, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--database", "forge_releases_test", "--multiquery", "--format", "TSV"}, args...)...)
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery")
	seed.Stdin = strings.NewReader(releasesSeed)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	panels := map[string]Panel{}
	for _, p := range BuildReleases("shop").Panels {
		panels[p.ID] = p
	}
	run := func(id string, envs ...string) string {
		t.Helper()
		p, ok := panels[id]
		if !ok || p.Kind != KindSQL {
			t.Fatalf("no sql panel %s", id)
		}
		sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-7 * 24 * time.Hour), End: end}
		if len(envs) > 0 {
			sc.EnvRestricted, sc.EnvIDs = true, envs
			for _, e := range envs {
				sc.Envs = append(sc.Envs, obsquery.Env{ID: e, Name: e})
			}
		}
		st, err := obsquery.SQLPanel{Scope: sc, SQL: p.SQL, StepSeconds: 86400}.Compile()
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for k, v := range st.Params {
			args = append(args, "--param_"+k+"="+v)
		}
		return exec1(st.SQL, args...)
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
