//go:build integration

// Copyright (c) 2025 Reliant Labs

package alerting_test

// Real Postgres (a scratch database from pkg/pgtest: embedded, or the server
// FORGE_TEST_POSTGRES_URL names) + a real ClickHouse 26.8 container. Gated by
// the `integration` tag and by -short.
//
//	go test -tags integration ./pkg/alerting/...

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/lease"
	"github.com/reliant-labs/forge/pkg/pgtest"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/alerting/chquery"
	"github.com/reliant-labs/forge/pkg/alerting/pgstore"
)

const chImage = "clickhouse/clickhouse-server:26.8.24-alpine"

func startClickHouse(t *testing.T) (addr string) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: starts a ClickHouse container")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", chImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", chImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", chImage, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	name := fmt.Sprintf("alerting-it-ch-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		"-e", "CLICKHOUSE_USER=alerts", "-e", "CLICKHOUSE_PASSWORD=pw", "-e", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1", chImage).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	addr = fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(90 * time.Second)
	for {
		c, err := openCH(addr, "default")
		if err == nil {
			err = c.Ping(context.Background())
			_ = c.Close()
		}
		if err == nil {
			return addr
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("clickhouse never became ready: %v\n%s", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func openCH(addr, database string) (clickhouse.Conn, error) {
	return clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: database, Username: "alerts", Password: "pw"}, DialTimeout: 5 * time.Second})
}

// perOwner maps the opaque owner key to a connection on that owner's own
// ClickHouse database: the shape of every multi-owner Querier.
type perOwner map[string]chquery.Conn

func (p perOwner) Query(ctx context.Context, owner, q string, o alerting.QueryOptions) ([]alerting.Row, error) {
	c, ok := p[owner]
	if !ok {
		return nil, fmt.Errorf("unknown owner %q", owner)
	}
	return chquery.Run(ctx, c, q, o)
}

type amSink struct {
	mu     sync.Mutex
	alerts []map[string]any
}

func (s *amSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var in []map[string]any
	_ = json.Unmarshal(b, &in)
	s.mu.Lock()
	s.alerts = append(s.alerts, in...)
	s.mu.Unlock()
}

func (s *amSink) snapshot() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.alerts...)
}

type staticRules map[string][]alerting.Rule

func (s staticRules) Owners(context.Context) ([]string, error) {
	var out []string
	for t := range s {
		out = append(out, t)
	}
	return out, nil
}
func (s staticRules) Rules(_ context.Context, t string) ([]alerting.Rule, error) { return s[t], nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClickHousePostgresEndToEnd(t *testing.T) {
	addr := startClickHouse(t)
	// A scratch database of this test's own on pgtest's shared server, dropped
	// when the test ends. pgtest decides which server: the one
	// FORGE_TEST_POSTGRES_URL names, or embedded postgres.
	dsn, dropDB, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision a scratch Postgres database: %v", err)
	}
	t.Cleanup(dropDB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── ClickHouse: one database per owner ──
	admin, err := openCH(addr, "default")
	if err != nil {
		t.Fatal(err)
	}
	owners := perOwner{}
	for _, org := range []string{"org-a", "org-b"} {
		db := "t_" + map[string]string{"org-a": "a", "org-b": "b"}[org]
		for _, q := range []string{
			"CREATE DATABASE " + db,
			"CREATE TABLE " + db + ".spans (service String, dur Float64, ts DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY ts",
		} {
			if err := admin.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		c, err := openCH(addr, db)
		if err != nil {
			t.Fatal(err)
		}
		owners[org] = c
	}
	exec := func(q string) {
		t.Helper()
		if err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// org-a is slow; org-b is fine.
	exec("INSERT INTO t_a.spans (service, dur) VALUES ('api', 950), ('api', 900), ('web', 10)")
	exec("INSERT INTO t_b.spans (service, dur) VALUES ('api', 20), ('web', 30)")

	// ── Postgres: the shipped schema, in a schema of our own ──
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("alerting_it_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") }()
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatalf("the schema must be idempotent: %v", err)
	}
	store, err := pgstore.New(db, schema)
	if err != nil {
		t.Fatal(err)
	}

	// ── a fake Alertmanager ──
	sink := &amSink{}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	// ── rules: the same expr for both orgs, as a RuleSource would hand them out ──
	const expr = "SELECT service, quantile(0.95)(dur) AS value FROM spans GROUP BY service HAVING value > 800"
	mk := func(org string) alerting.Rule {
		return alerting.Rule{
			RuleKey: alerting.RuleKey{Owner: org, Env: "prod", Name: "slow-service"}, Expr: expr, Interval: 100 * time.Millisecond,
			Labels:      map[string]string{"severity": "critical", "reliant_org_id": "org-b"}, // an attempted spoof
			Annotations: map[string]string{"summary": "{{ $labels.service }} p95 {{ $value }}ms"},
			Identity:    map[string]string{"reliant_org_id": org, "reliant_env_id": "prod"},
		}
	}
	rules := staticRules{"org-a": {mk("org-a")}, "org-b": {mk("org-b")}}

	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier: owners, Notifier: alerting.NewAlertmanagerNotifier(nil, srv.URL), Store: store,
		Limits: alerting.Limits{MinInterval: 100 * time.Millisecond, MaxExecutionTime: 10 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two schedulers on two lockers = two replicas contending for the same shards.
	runReplica := func() {
		ldb, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ldb.Close() })
		lk := lease.New(ldb, lease.Options{RetryInterval: 100 * time.Millisecond, HeartbeatInterval: time.Second})
		t.Cleanup(func() { _ = lk.Close() })
		s, err := alerting.NewScheduler(alerting.SchedulerDeps{Rules: rules, Store: store, Eval: ev, Locker: lk, Shards: 4,
			LeasePrefix: schema, Tick: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = s.Run(ctx) }()
	}
	runReplica()
	runReplica()

	firingFor := func(org string) []map[string]any {
		var out []map[string]any
		for _, a := range sink.snapshot() {
			l := a["labels"].(map[string]any)
			if l["reliant_org_id"] == org {
				out = append(out, a)
			}
		}
		return out
	}

	// ── firing: only org-a, with identity and annotations intact ──
	waitFor(t, "org-a to fire", func() bool { return len(firingFor("org-a")) > 0 })
	a := firingFor("org-a")[0]
	l := a["labels"].(map[string]any)
	if l["service"] != "api" || l["alertname"] != "slow-service" || l["reliant_env_id"] != "prod" || l["severity"] != "critical" {
		t.Fatalf("labels = %v", l)
	}
	if _, bad := l["value"]; bad {
		t.Fatal("the value column leaked into labels")
	}
	if got := a["annotations"].(map[string]any)["summary"]; got == nil || got == "" {
		t.Fatalf("annotations = %v", a["annotations"])
	}
	time.Sleep(600 * time.Millisecond)
	if n := len(firingFor("org-b")); n != 0 {
		t.Fatalf("org-b has no slow service but %d alerts carry its identity (a spoofed label?)", n)
	}
	for _, al := range sink.snapshot() {
		if al["labels"].(map[string]any)["reliant_org_id"] != "org-a" {
			t.Fatalf("an alert escaped with the wrong identity: %v", al["labels"])
		}
	}
	// exactly one replica evaluates org-a: re-sends are one per interval, not two.
	stored, _ := store.LoadOwner(ctx, "org-a")
	if len(stored) != 1 || stored[0].State != alerting.StateFiring {
		t.Fatalf("stored = %+v", stored)
	}

	// ── resolving: clear org-a's data ──
	exec("TRUNCATE TABLE t_a.spans")
	waitFor(t, "org-a to resolve", func() bool {
		for _, al := range firingFor("org-a") {
			end, _ := time.Parse(time.RFC3339Nano, al["endsAt"].(string))
			if end.Before(time.Now().Add(time.Second)) {
				return true
			}
		}
		return false
	})
	waitFor(t, "the resolved row to be cleaned up", func() bool {
		s, _ := store.LoadOwner(ctx, "org-a")
		return len(s) == 0
	})
	ev2, err := store.Events(ctx, alerting.EventQuery{Owner: "org-a", Env: "prod", Since: time.Now().Add(-time.Hour), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[alerting.Transition]int{}
	for _, e := range ev2 {
		seen[e.Transition]++
	}
	if seen[alerting.TransitionFiring] != 1 || seen[alerting.TransitionResolved] != 1 {
		t.Fatalf("history = %v (two replicas must not double-record a transition)", seen)
	}
	if evB, _ := store.Events(ctx, alerting.EventQuery{Owner: "org-b", Since: time.Now().Add(-time.Hour)}); len(evB) != 0 {
		t.Fatalf("org-b history = %v", evB)
	}
}

// An owner whose envs together exceed what the old per-owner cap allowed must
// lose nothing, and the one env that is itself over the per-env cap must be
// LOUD about it: its critical rule survives, AlertRulesOverCap reaches
// Alertmanager with that env's identity, and the envs under the cap are untouched.
func TestPerEnvRuleCapEndToEndIsLoud(t *testing.T) {
	addr := startClickHouse(t)
	dsn, dropDB, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision a scratch Postgres database: %v", err)
	}
	t.Cleanup(dropDB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	admin, err := openCH(addr, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE DATABASE t_cap",
		"CREATE TABLE t_cap.spans (service String, dur Float64, ts DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY ts",
		"INSERT INTO t_cap.spans (service, dur) VALUES ('api', 950)",
	} {
		if err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	c, err := openCH(addr, "t_cap")
	if err != nil {
		t.Fatal(err)
	}
	owners := perOwner{"org-cap": c}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("alerting_cap_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") }()
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	store, err := pgstore.New(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	sink := &amSink{}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	// Always-true queries; severity decides who survives the cap.
	const hit = "SELECT service, quantile(0.95)(dur) AS value FROM spans GROUP BY service HAVING value > 800"
	mk := func(env, name, severity string) alerting.Rule {
		return alerting.Rule{
			RuleKey: alerting.RuleKey{Owner: "org-cap", Env: env, Name: name}, Expr: hit, Interval: 100 * time.Millisecond,
			Labels:   map[string]string{"severity": severity},
			Identity: map[string]string{"reliant_org_id": "org-cap", "reliant_env_id": env},
		}
	}
	var all []alerting.Rule
	// "crowded" declares 5 rules against a cap of 4: the critical one must stay.
	all = append(all, mk("crowded", "ZZCritical", "critical"))
	for _, n := range []string{"w1", "w2", "w3", "w4"} {
		all = append(all, mk("crowded", n, "warning"))
	}
	// "quiet" and "other" are under the cap, and with "crowded" push the OWNER's
	// total (11) past 4: a per-owner cap of 4 would drop most of them.
	all = append(all, mk("quiet", "q1", "warning"), mk("quiet", "q2", "warning"),
		mk("other", "o1", "warning"), mk("other", "o2", "warning"), mk("other", "o3", "warning"), mk("other", "o4", "warning"))
	rules := staticRules{"org-cap": all}

	n := alerting.NewAlertmanagerNotifier(nil, srv.URL)
	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier: owners, Notifier: n, Store: store,
		Limits: alerting.Limits{MinInterval: 100 * time.Millisecond, MaxExecutionTime: 10 * time.Second, MaxRulesPerEnv: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := alerting.NewScheduler(alerting.SchedulerDeps{Rules: rules, Store: store, Eval: ev, Shards: 1, Tick: 100 * time.Millisecond, Notifier: n})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Run(ctx) }()

	byName := func(name string) []map[string]any {
		var out []map[string]any
		for _, a := range sink.snapshot() {
			if a["labels"].(map[string]any)["alertname"] == name {
				out = append(out, a)
			}
		}
		return out
	}
	waitFor(t, "AlertRulesOverCap", func() bool { return len(byName(alerting.OverCapAlertName)) > 0 })
	oc := byName(alerting.OverCapAlertName)[0]
	l := oc["labels"].(map[string]any)
	if l["reliant_env_id"] != "crowded" || l["reliant_org_id"] != "org-cap" || l["severity"] != "critical" {
		t.Fatalf("AlertRulesOverCap labels = %v", l)
	}
	sum, _ := oc["annotations"].(map[string]any)["summary"].(string)
	if !strings.Contains(sum, "5 alert rules") || !strings.Contains(sum, "w4") {
		t.Fatalf("summary must say how many rules there are and name the dropped one: %q", sum)
	}

	waitFor(t, "the critical rule of the capped env to fire", func() bool { return len(byName("ZZCritical")) > 0 })
	waitFor(t, "every rule of the envs under the cap to fire", func() bool {
		for _, nm := range []string{"q1", "q2", "o1", "o2", "o3", "o4", "w1", "w2", "w3"} {
			if len(byName(nm)) == 0 {
				return false
			}
		}
		return true
	})
	time.Sleep(500 * time.Millisecond)
	if got := byName("w4"); len(got) != 0 {
		t.Fatalf("w4 is past the cap and must not be evaluated, but alerted: %v", got)
	}
	// one firing instance per kept rule: 4 in crowded + 2 in quiet + 4 in other.
	if st, _ := store.LoadOwner(ctx, "org-cap"); len(st) != 10 {
		t.Fatalf("instances = %d, want 10", len(st))
	}
}

func TestGuardAndSettingsAreEnforcedByClickHouse(t *testing.T) {
	addr := startClickHouse(t)
	conn, err := openCH(addr, "default")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := conn.Exec(ctx, "CREATE TABLE victim (x UInt8) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}

	// the row cap is server-side, and overflow throws rather than truncating.
	_, err = chquery.Run(ctx, conn, "SELECT number AS value FROM numbers(100)", alerting.QueryOptions{MaxResultRows: 5, MaxExecutionTime: 5 * time.Second})
	if err == nil {
		t.Fatal("max_result_rows was not enforced")
	}
	rows, err := chquery.Run(ctx, conn, "SELECT number AS value FROM numbers(5)", alerting.QueryOptions{MaxResultRows: 5})
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}

	// the guard refuses a write before it is sent...
	if _, err := chquery.Run(ctx, conn, "INSERT INTO victim VALUES (1)", alerting.QueryOptions{}); err == nil {
		t.Fatal("the guard let an INSERT through")
	}
	// ...and the settings alone stop one that bypasses it.
	bypass := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings(alerting.QueryOptions{MaxExecutionTime: time.Second}.Settings())))
	if err := conn.Exec(bypass, "INSERT INTO victim VALUES (1)"); err == nil {
		t.Fatal("readonly=2 did not stop a write")
	}
	if err := conn.Exec(bypass, "DROP TABLE victim"); err == nil {
		t.Fatal("readonly=2 did not stop DDL")
	}
	// readonly=2 must NOT let the statement lift its own limits.
	if err := conn.Exec(bypass, "SET readonly = 0"); err == nil {
		t.Fatal("a statement lifted readonly")
	}
}
