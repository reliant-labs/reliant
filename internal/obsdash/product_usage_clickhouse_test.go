package obsdash

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

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

// TestProductUsageSQLOnClickHouse runs every Product usage panel against a
// real ClickHouse, bound the way the console binds a sql panel (obsquery's
// compiled parameters), and checks what the numbers mean. Same container and
// conventions as TestDefaultAlertSQLOnClickHouse.
func TestProductUsageSQLOnClickHouse(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	exec1 := func(sql string, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", container, "clickhouse-client", "--password", "pw",
			"--database", "forge_product_usage_test", "--multiquery", "--format", "TSV"}, args...)...)
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse: %v\n%s\n--- sql ---\n%s", err, out, sql)
		}
		return strings.TrimSpace(string(out))
	}
	seed := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery")
	seed.Stdin = strings.NewReader(productEventsSeed)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	run := func(id string, env string) string {
		t.Helper()
		var p Panel
		for _, q := range BuildProductUsage("shop").Panels {
			if q.ID == id {
				p = q
			}
		}
		sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end,
			EnvRestricted: true, EnvIDs: []string{env}, Envs: []obsquery.Env{{ID: env, Name: env}}}
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
		// dau = u1, u2; wau adds nobody (u1 again); mau adds u3.
		"active-users": "2\t2\t3\t0.67",
		"entity-actions-by-entity": "order_item\t4\t1\t1\t3\n" +
			"invoice\t1\t0\t0\t1",
		"top-pages": "/orders\t2\t1\t2\n" +
			"/\t1\t0\t1",
		// u1's first event (2026-03-07 in range) is new; 13th and 16th returning.
		"new-vs-returning": "2026-02-24 00:00:00\t1\t0\n" +
			"2026-03-07 00:00:00\t1\t0\n" +
			"2026-03-13 00:00:00\t0\t1\n" +
			"2026-03-16 00:00:00\t1\t1",
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
	ret := strings.Split(run("weekly-retention", "e1"), "\n")
	if len(ret) != 3 {
		t.Fatalf("weekly-retention = %q, want 3 cohorts", ret)
	}
	if !strings.HasPrefix(ret[0], "2026-02-23\t1\t0\t0\t0\t") || !strings.HasPrefix(ret[1], "2026-03-02\t1\t100\t100\t\\N\t") ||
		!strings.HasPrefix(ret[2], "2026-03-16\t1\t\\N\t") {
		t.Errorf("weekly-retention =\n%s", strings.Join(ret, "\n"))
	}

	// Every other panel runs; another environment sees none of e1's events.
	for _, p := range BuildProductUsage("shop").Panels {
		run(p.ID, "e1")
	}
	if got := run("events-by-name", "e2"); got != "order_item_created\t1\t1" {
		t.Errorf("events-by-name in e2 = %q, want only e2's event", got)
	}
	if got := run("most-active-users", "e1"); !strings.HasPrefix(got, "u1\t5\t3\t4\t") {
		t.Errorf("most-active-users =\n%s", got)
	}
}
