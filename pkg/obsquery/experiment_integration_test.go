//go:build integration

package obsquery_test

// The experiment readout against a real ClickHouse holding the hosted
// product_events table (control-plane's tenant schema, with v29's OrgId), over
// a small population whose answer is worked out by hand: window boundaries,
// conflicted units, the reasons and providers that do not count, and the
// account unit.
//
//	go test -tags integration -run ExperimentReadout ./pkg/obsquery/

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

const experimentEventsDDL = `CREATE TABLE %s.product_events (
    Timestamp DateTime64(9),
    ServiceName LowCardinality(String),
    EnvId LowCardinality(String),
    SessionId String,
    UserId String,
    EventName LowCardinality(String),
    Props Map(LowCardinality(String), String),
    RetentionDays UInt16 DEFAULT 90,
    Client LowCardinality(String),
    VisitorId String,
    OrgId String DEFAULT ''
) ENGINE = MergeTree PARTITION BY toDate(Timestamp) ORDER BY (ServiceName, EventName, Timestamp)`

type expEvent struct {
	at                 string // RFC 3339
	env                string
	user, visitor, org string
	event              string
	props              map[string]string
}

func seedExperimentEvents(t *testing.T, c clickhouse.Conn, db string, events []expEvent) {
	t.Helper()
	ctx := context.Background()
	if err := c.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, fmt.Sprintf(experimentEventsDDL, db)); err != nil {
		t.Fatal(err)
	}
	batch, err := c.PrepareBatch(ctx, "INSERT INTO "+db+".product_events (Timestamp, ServiceName, EnvId, UserId, VisitorId, OrgId, EventName, Client, Props)")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		ts, err := time.Parse(time.RFC3339, e.at)
		if err != nil {
			t.Fatal(err)
		}
		env := e.env
		if env == "" {
			env = "prod"
		}
		props := e.props
		if props == nil {
			props = map[string]string{}
		}
		if err := batch.Append(ts, "api", env, e.user, e.visitor, e.org, e.event, "backend", props); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
}

// assign is a Forge Changes assignment record.
func assign(at, unit, variant, run string, ids expEvent) expEvent {
	ids.at, ids.event = at, "flag_evaluated"
	ids.props = map[string]string{"flag": "new-checkout", "variant": variant, "provider": "forge", "run": run,
		"unit": unit, "reason": "experiment", "revision": "7"}
	return ids
}

func TestExperimentReadoutOverAPopulation(t *testing.T) {
	c := startClickHouse(t)
	const db = "t_experiment"
	const run = "r_8f2cq7xa"
	user := func(id string) expEvent { return expEvent{user: id} }
	order := func(at string, ids expEvent, plan string) expEvent {
		ids.at, ids.event, ids.props = at, "order_placed", map[string]string{"plan": plan}
		return ids
	}
	notCounted := func(e expEvent, key, value string) expEvent {
		e.props[key] = value
		return e
	}
	events := []expEvent{
		// u1: control; converts exactly at the window's end (inclusive).
		assign("2026-11-01T10:00:00Z", "user", "control", run, user("u1")),
		order("2026-11-02T10:00:00Z", user("u1"), "paid"),
		// u2: control; converts one second after its window.
		assign("2026-11-01T10:00:00Z", "user", "control", run, user("u2")),
		order("2026-11-02T10:00:01Z", user("u2"), "paid"),
		// u3: treatment, assigned twice (the first counts); an order before
		// the assignment and one the filter excludes.
		assign("2026-11-02T00:00:00Z", "user", "treatment", run, user("u3")),
		assign("2026-11-03T00:00:00Z", "user", "treatment", run, user("u3")),
		order("2026-11-01T23:00:00Z", user("u3"), "paid"),
		order("2026-11-02T05:00:00Z", user("u3"), "free"),
		// u4: treatment; converts inside its window.
		assign("2026-11-03T00:00:00Z", "user", "treatment", run, user("u4")),
		order("2026-11-03T01:00:00Z", user("u4"), "paid"),
		// u5: conflicted (both arms), converts: in no arm.
		assign("2026-11-02T00:00:00Z", "user", "control", run, user("u5")),
		assign("2026-11-02T01:00:00Z", "user", "treatment", run, user("u5")),
		order("2026-11-02T02:00:00Z", user("u5"), "paid"),
		// u16: two variants at one timestamp: conflicted, not whichever
		// row a tie-break would pick.
		assign("2026-11-03T00:00:00Z", "user", "control", run, user("u16")),
		assign("2026-11-03T00:00:00Z", "user", "treatment", run, user("u16")),
		order("2026-11-03T01:00:00Z", user("u16"), "paid"),
		// u17: assigned treatment, later a control row: conflicted, the
		// later row is not a correction.
		assign("2026-11-03T00:00:00Z", "user", "treatment", run, user("u17")),
		assign("2026-11-05T00:00:00Z", "user", "control", run, user("u17")),
		// u6: treatment, assigned 12h before the horizon: converted so far,
		// window not complete.
		assign("2026-11-07T12:00:00Z", "user", "treatment", run, user("u6")),
		order("2026-11-07T13:00:00Z", user("u6"), "paid"),
		// u12: control; its only order is in another environment.
		assign("2026-11-04T00:00:00Z", "user", "control", run, user("u12")),
		order("2026-11-04T01:00:00Z", expEvent{user: "u12", env: "stage"}, "paid"),
		// Not assignments of this run.
		notCounted(assign("2026-11-02T00:00:00Z", "user", "treatment", run, user("u7")), "reason", "preview"),
		notCounted(assign("2026-11-02T00:00:00Z", "user", "treatment", run, user("u8")), "provider", "launchdarkly"),
		assign("2026-11-02T00:00:00Z", "user", "treatment", "r_other0001", user("u9")),
		assign("2026-11-02T00:00:00Z", "user", "treatment", run, expEvent{user: "u10", env: "stage"}),
		assign("2026-10-31T23:59:59Z", "user", "control", run, user("u11")),
		assign("2026-11-08T00:00:00Z", "user", "control", run, user("u13")), // at the horizon (exclusive)
		assign("2026-11-02T00:00:00Z", "user", "control", run, user("")),
		notCounted(assign("2026-11-02T00:00:00Z", "user", "control", run, user("u14")), "flag", "other-flag"),
		notCounted(assign("2026-11-02T00:00:00Z", "user", "control", run, user("u15")), "unit", "account"),
		order("2026-11-02T00:30:00Z", user("u7"), "paid"),

		// The account run: the unit is the org, else Props['account'].
		assign("2026-11-01T12:00:00Z", "account", "treatment", "r_acct0001", expEvent{user: "a", org: "o1"}),
		order("2026-11-01T13:00:00Z", expEvent{user: "b", org: "o1"}, "paid"), // a teammate converts
		assign("2026-11-01T12:00:00Z", "account", "treatment", "r_acct0001", expEvent{user: "c", org: "o3"}),
		assign("2026-11-01T12:30:00Z", "account", "treatment", "r_acct0001", expEvent{user: "d", org: "o3"}), // same account
		notCounted(assign("2026-11-01T12:00:00Z", "account", "control", "r_acct0001", expEvent{user: "e"}), "account", "acme"),
		notCounted(order("2026-11-01T14:00:00Z", expEvent{user: "f"}, "paid"), "account", "acme"),
		assign("2026-11-01T12:00:00Z", "account", "control", "r_acct0001", expEvent{user: "g"}), // no account: never assigned

		// The visitor run.
		assign("2026-11-01T12:00:00Z", "visitor", "treatment", "r_visit001", expEvent{visitor: "v1"}),
		order("2026-11-01T12:05:00Z", expEvent{visitor: "v1"}, "paid"),
		assign("2026-11-01T12:00:00Z", "visitor", "control", "r_visit001", expEvent{visitor: "v2", user: "u20"}),
		order("2026-11-01T12:05:00Z", expEvent{user: "u20"}, "paid"), // the user, not the visitor
	}
	seedExperimentEvents(t, c, db, events)

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	q := obsquery.ExperimentReadoutQuery{
		Scope:     obsquery.Scope{Database: db, EnvRestricted: true, EnvIDs: []string{"prod"}, Start: start, End: start.Add(7 * 24 * time.Hour)},
		ChangeKey: "new-checkout", RunID: run, Unit: obsquery.ExperimentUnitUser,
		Variants: []string{"control", "treatment"},
		Outcome:  "order_placed", OutcomeFilter: obsquery.ExperimentPropFilter{Key: "plan", Value: "paid"},
		WindowHours: 24,
	}
	ctx := context.Background()
	runner := rumRunner(c)
	// Both join_use_nulls settings give the same answer.
	for _, settings := range []clickhouse.Settings{nil, {"join_use_nulls": 1}} {
		exec := runner
		if settings != nil {
			exec = func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
				return c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params)), clickhouse.WithSettings(settings)), st.SQL)
			}
		}
		got, err := obsquery.ReadExperimentReadout(ctx, exec, q)
		if err != nil {
			t.Fatal(err)
		}
		at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
		want := []obsquery.ExperimentArm{
			{Variant: "control", Assigned: 3, Converted: 1, WindowComplete: 3, WindowCompleteConverted: 1,
				FirstAssignedAt: at("2026-11-01T10:00:00Z"), LastAssignedAt: at("2026-11-04T00:00:00Z")},
			{Variant: "treatment", Assigned: 3, Converted: 2, WindowComplete: 2, WindowCompleteConverted: 1,
				FirstAssignedAt: at("2026-11-02T00:00:00Z"), LastAssignedAt: at("2026-11-07T12:00:00Z")},
		}
		if !reflect.DeepEqual(got.Arms, want) {
			t.Errorf("settings %v: arms\n got %+v\nwant %+v", settings, got.Arms, want)
		}
		if got.Conflicted != 3 || got.Assigned != 6 || got.WindowComplete != 5 || got.Pending() != 1 || len(got.Undeclared) != 0 {
			t.Errorf("settings %v: conflicted %d, assigned %d, complete %d, undeclared %v", settings, got.Conflicted, got.Assigned, got.WindowComplete, got.Undeclared)
		}
		if !got.FirstAssignedAt.Equal(at("2026-11-01T10:00:00Z")) || !got.LastAssignedAt.Equal(at("2026-11-07T12:00:00Z")) {
			t.Errorf("settings %v: assignments [%v, %v]", settings, got.FirstAssignedAt, got.LastAssignedAt)
		}
	}

	acct := q
	acct.RunID, acct.Unit = "r_acct0001", obsquery.ExperimentUnitAccount
	got, err := obsquery.ReadExperimentReadout(ctx, runner, acct)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Arms; a[0].Assigned != 1 || a[0].Converted != 1 || a[1].Assigned != 2 || a[1].Converted != 1 || got.Conflicted != 0 {
		t.Errorf("account run: %+v (conflicted %d); want control 1/1 (acme), treatment 2/1 (o1 converted by a teammate, o3 once)", a, got.Conflicted)
	}

	vis := q
	vis.RunID, vis.Unit = "r_visit001", obsquery.ExperimentUnitVisitor
	got, err = obsquery.ReadExperimentReadout(ctx, runner, vis)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Arms; a[0].Assigned != 1 || a[0].Converted != 0 || a[1].Assigned != 1 || a[1].Converted != 1 {
		t.Errorf("visitor run: %+v; want control 1/0, treatment 1/1", a)
	}

	empty := q
	empty.RunID = "r_nothing01"
	got, err = obsquery.ReadExperimentReadout(ctx, runner, empty)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assigned != 0 || got.Conflicted != 0 || !got.FirstAssignedAt.IsZero() || len(got.Arms) != 2 {
		t.Errorf("a run with no assignments: %+v", got)
	}
}
