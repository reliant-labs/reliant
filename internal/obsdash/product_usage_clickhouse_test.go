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
	statement := func(id string, trend bool) string {
		t.Helper()
		for _, q := range BuildProductUsage("shop").Panels {
			if q.ID == id && trend {
				return q.TrendSQL
			}
			if q.ID == id {
				return q.SQL
			}
		}
		t.Fatalf("no panel %s", id)
		return ""
	}
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
		return exec1(st.SQL, args...)
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
		return exec1(st.SQL, args...)
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
	for _, p := range BuildProductUsage("shop").Panels {
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
