//go:build integration

package obsquery_test

// The user-activity statement against a real ClickHouse holding the hosted
// product_events table (control-plane's 010_tenant_create.sql shape), over a
// small population whose answer is worked out by hand.
//
//	go test -tags integration -run UserActivity ./pkg/obsquery/

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const productEventsDDL = `CREATE TABLE %s.product_events (
    Timestamp DateTime64(9),
    ServiceName LowCardinality(String),
    ServiceVersion LowCardinality(String),
    EnvId LowCardinality(String),
    SessionId String,
    UserId String,
    EventName LowCardinality(String),
    Props Map(LowCardinality(String), String),
    RetentionDays UInt16 DEFAULT 90,
    Client LowCardinality(String),
    VisitorId String
) ENGINE = MergeTree PARTITION BY toDate(Timestamp) ORDER BY (ServiceName, EventName, Timestamp)`

type productEvent struct {
	at        string // RFC 3339
	env, user string
	event     string
	client    string
	retention uint16
	props     map[string]string
}

func seedProductEvents(t *testing.T, c clickhouse.Conn, db string, events []productEvent) {
	t.Helper()
	ctx := context.Background()
	if err := c.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, fmt.Sprintf(productEventsDDL, db)); err != nil {
		t.Fatal(err)
	}
	batch, err := c.PrepareBatch(ctx, "INSERT INTO "+db+".product_events (Timestamp, ServiceName, EnvId, UserId, EventName, Client, RetentionDays, Props)")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		ts, err := time.Parse(time.RFC3339, e.at)
		if err != nil {
			t.Fatal(err)
		}
		client, retention := e.client, e.retention
		if client == "" {
			client = "backend"
		}
		if retention == 0 {
			retention = 90
		}
		props := e.props
		if props == nil {
			props = map[string]string{}
		}
		if err := batch.Append(ts, "api", e.env, e.user, e.event, client, retention, props); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
}

func TestUserActivityOverAPopulation(t *testing.T) {
	c := startClickHouse(t)
	const db = "t_users"
	seedProductEvents(t, c, db, []productEvent{
		// prod reports sign-ups.
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u1", event: "auth_signed_up"},
		{at: "2026-11-05T09:00:01Z", env: "prod", user: "u1", event: "auth_signed_in"},
		{at: "2026-11-06T10:00:00Z", env: "prod", user: "u1", event: "project_created", client: "web"},
		{at: "2026-09-01T10:00:00Z", env: "prod", user: "u2", event: "auth_signed_up"},
		{at: "2026-10-30T10:00:00Z", env: "prod", user: "u2", event: "auth_signed_in"},
		{at: "2026-11-04T10:00:00Z", env: "prod", user: "u2", event: "auth_signed_in"},
		{at: "2026-11-04T11:00:00Z", env: "prod", user: "u2", event: "auth_signed_in"}, // a second replica's copy
		{at: "2026-10-28T10:00:00Z", env: "prod", user: "u3", event: "auth_signed_in"},
		{at: "2026-11-07T10:00:00Z", env: "prod", user: "u4", event: "auth_signed_out"}, // not activity
		{at: "2026-10-29T10:00:00Z", env: "prod", user: "u5", event: "auth_signed_up"},
		{at: "2026-10-29T10:00:01Z", env: "prod", user: "u5", event: "auth_signed_in"},
		{at: "2026-11-06T10:00:00Z", env: "prod", user: "", event: "page_viewed", client: "web"},        // anonymous
		{at: "2026-11-06T00:00:00Z", env: "prod", user: "", event: "auth_accounts", client: "platform"}, // platform
		// stage reports no sign-ups: new is first seen.
		{at: "2026-11-08T10:00:00Z", env: "stage", user: "s1", event: "task_created", retention: 30},
		{at: "2026-10-01T10:00:00Z", env: "stage", user: "s2", event: "task_created", retention: 30},
		{at: "2026-11-08T10:00:00Z", env: "stage", user: "s2", event: "task_created", retention: 30},
		// out of scope.
		{at: "2026-11-08T10:00:00Z", env: "other", user: "o1", event: "auth_signed_up"},
	})
	end := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	q := obsquery.UserActivityQuery{
		Scope: obsquery.Scope{Database: db, EnvRestricted: true, EnvIDs: []string{"prod", "stage", "empty"},
			Envs: []obsquery.Env{{ID: "prod", Name: "Production"}}, Start: end.Add(-7 * 24 * time.Hour), End: end},
		SeriesDays: 7, WantSeenTotal: true,
	}
	got, err := obsquery.ReadUserActivity(context.Background(), rumRunner(c), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].EnvID != "prod" || got[1].EnvID != "stage" || got[2].EnvID != "empty" {
		t.Fatalf("envs = %+v", got)
	}
	prod, stage, empty := got[0], got[1], got[2]

	counts := func(a, n, r int64) obsquery.UserActivityCounts {
		return obsquery.UserActivityCounts{Active: a, New: n, Returning: r}
	}
	if prod.EnvName != "Production" || prod.NewRule != obsquery.NewUsersSignups {
		t.Errorf("prod name/rule = %q/%q", prod.EnvName, prod.NewRule)
	}
	if prod.Current != counts(2, 1, 1) {
		t.Errorf("prod current = %+v, want active u1,u2; new u1; returning u2", prod.Current)
	}
	if prod.Previous != counts(3, 1, 2) {
		t.Errorf("prod previous = %+v, want active u2,u3,u5; new u5", prod.Previous)
	}
	if !reflect.DeepEqual(prod.Clients, []string{"backend", "web"}) {
		t.Errorf("prod clients = %v", prod.Clients)
	}
	if prod.SeenTotal == nil || *prod.SeenTotal != 4 {
		t.Errorf("prod seen total = %v, want 4 (u1,u2,u3,u5)", prod.SeenTotal)
	}
	if want := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC); !prod.FirstEventAt.Equal(want) || prod.RetentionDays != 90 {
		t.Errorf("prod first event %v retention %d", prod.FirstEventAt, prod.RetentionDays)
	}
	wantDays := map[string]obsquery.UserActivityCounts{
		"2026-11-04": counts(1, 0, 1), "2026-11-05": counts(1, 1, 0), "2026-11-06": counts(1, 0, 1),
	}
	if len(prod.Days) != 7 || prod.Days[0].Day != "2026-11-04" || prod.Days[6].Day != "2026-11-10" {
		t.Fatalf("prod days = %+v", prod.Days)
	}
	for _, d := range prod.Days {
		if d.UserActivityCounts != wantDays[d.Day] {
			t.Errorf("prod %s = %+v, want %+v", d.Day, d.UserActivityCounts, wantDays[d.Day])
		}
	}

	if stage.NewRule != obsquery.NewUsersFirstSeen || stage.Current != counts(2, 1, 1) || stage.Previous != counts(0, 0, 0) {
		t.Errorf("stage = %+v, want first-seen rule, current active s1,s2 new s1", stage)
	}
	if stage.RetentionDays != 30 || stage.SeenTotal == nil || *stage.SeenTotal != 2 {
		t.Errorf("stage retention %d seen %v", stage.RetentionDays, stage.SeenTotal)
	}

	if empty.Current != counts(0, 0, 0) || !empty.FirstEventAt.IsZero() || len(empty.Days) != 7 ||
		empty.SeenTotal == nil || *empty.SeenTotal != 0 || len(empty.Clients) != 0 {
		t.Errorf("an env with no rows must be zero-filled: %+v", empty)
	}

	// stage's data begins 10-01 (s2), so s1 (11-08) is past the grace and new.
	if want := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC); !stage.NewDecidedFrom.Equal(want) {
		t.Errorf("stage decided from %v, want %v", stage.NewDecidedFrom, want)
	}

	// The invariants the console relies on.
	for _, e := range got {
		for _, w := range []obsquery.UserActivityCounts{e.Current, e.Previous} {
			if w.Returning > w.Active || w.Active-w.Returning > w.New {
				t.Errorf("%s breaks returning <= active, active-returning <= new: %+v", e.EnvID, w)
			}
		}
	}
}

// A fact about a user (observe.TrackState) never counts them, and a project's
// declared rules leave out its own not-activity events and every user a
// marker event names, history included.
func TestUserActivityLeavesStateEventsAndDeclaredRulesOut(t *testing.T) {
	c := startClickHouse(t)
	const db = "t_users_rules"
	state := map[string]string{observe.PropKind: observe.KindState}
	seedProductEvents(t, c, db, []productEvent{
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u1", event: "project_created"},                          // active
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u2", event: "billing_subscription_state", props: state}, // a snapshot only
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u3", event: "legacy_snapshot"},                          // declared not activity
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u4", event: "project_created"},                          // staff
		{at: "2026-08-20T09:00:00Z", env: "prod", user: "u4", event: "internal_user"},                            // marked long ago
		{at: "2026-08-21T09:00:00Z", env: "prod", user: "u6", event: "project_created"},                          // the data's start, with or without rules (FirstSeenGraceDays)
		{at: "2026-11-05T09:00:00Z", env: "stage", user: "u5", event: "project_created"},                         // marked in prod only
		{at: "2026-11-05T09:00:00Z", env: "prod", user: "u5", event: "internal_user"},
	})
	end := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	q := obsquery.UserActivityQuery{
		Scope: obsquery.Scope{Database: db, EnvRestricted: true, EnvIDs: []string{"prod", "stage"}, Start: end.Add(-7 * 24 * time.Hour), End: end},
	}
	read := func(a obsquery.Activity) (prod, stage obsquery.UserActivityCounts) {
		t.Helper()
		q.Activity = a
		got, err := obsquery.ReadUserActivity(context.Background(), rumRunner(c), q)
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Current, got[1].Current
	}
	// The convention alone: u2's snapshot is not activity; u3's legacy event
	// and the markers still count (an undeclared marker is an event like any).
	if prod, _ := read(obsquery.Activity{}); prod != (obsquery.UserActivityCounts{Active: 4, New: 3, Returning: 1}) {
		t.Errorf("prod without rules = %+v, want active u1,u3,u4,u5; new u1,u3,u5; returning u4 (first seen in August)", prod)
	}
	prod, stage := read(obsquery.Activity{NotActivity: []string{"legacy_snapshot"}, ExcludeUsersWith: []string{"internal_user"}})
	if prod.Active != 1 || prod.New != 1 {
		t.Errorf("prod with rules = %+v, want active and new u1 only", prod)
	}
	if stage.Active != 1 {
		t.Errorf("stage with rules = %+v, want u5: a marker excludes a user in its own environment", stage)
	}
	// An id excludes its user in every environment, history included.
	prod, stage = read(obsquery.Activity{ExcludeUserIDs: []string{"u5", "u3"}})
	if prod != (obsquery.UserActivityCounts{Active: 2, New: 1, Returning: 1}) || stage.Active != 0 {
		t.Errorf("excluding u3 and u5 by id: prod %+v, want active u1,u4 new u1 returning u4; stage %+v, want no one", prod, stage)
	}
}

// TestUserActivityNewUsersFirstSeenGrace: where New falls back to first-seen,
// the users of the data's first FirstSeenGraceDays are not new, and the read
// says from when New is decided. The range is the 30 days ending 11-13.
func TestUserActivityNewUsersFirstSeenGrace(t *testing.T) {
	c := startClickHouse(t)
	const db = "t_users_grace"
	seedProductEvents(t, c, db, []productEvent{
		// fresh: data begins inside the range.
		{at: "2026-10-24T14:03:00Z", env: "fresh", user: "f1", event: "task_created"},
		{at: "2026-11-10T09:00:00Z", env: "fresh", user: "f1", event: "task_created"},
		{at: "2026-10-27T09:00:00Z", env: "fresh", user: "f2", event: "task_created"},
		{at: "2026-11-05T09:00:00Z", env: "fresh", user: "f3", event: "task_created"},
		{at: "2026-11-12T09:00:00Z", env: "fresh", user: "f4", event: "task_created"},
		// mature: data begins before the range.
		{at: "2026-09-01T09:00:00Z", env: "mature", user: "m1", event: "task_created"},
		{at: "2026-11-10T09:00:00Z", env: "mature", user: "m1", event: "task_created"},
		{at: "2026-10-20T09:00:00Z", env: "mature", user: "m2", event: "task_created"},
		{at: "2026-10-10T09:00:00Z", env: "mature", user: "m3", event: "task_created"},
		{at: "2026-11-01T09:00:00Z", env: "mature", user: "m3", event: "task_created"},
		// signups: sign-ups decide, even one at the very start of the data.
		{at: "2026-10-24T14:03:00Z", env: "signups", user: "g1", event: "auth_signed_up"},
		{at: "2026-10-30T09:00:00Z", env: "signups", user: "g2", event: "task_created"},
		{at: "2026-09-01T09:00:00Z", env: "signups", user: "g3", event: "auth_signed_up"},
		{at: "2026-11-01T09:00:00Z", env: "signups", user: "g3", event: "task_created"},
		// brandnew: data begins too late for any of the range to be decided.
		{at: "2026-11-10T09:00:00Z", env: "brandnew", user: "b1", event: "task_created"},
		{at: "2026-11-11T09:00:00Z", env: "brandnew", user: "b2", event: "task_created"},
	})
	end := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	counts := func(a, n, r int64) obsquery.UserActivityCounts {
		return obsquery.UserActivityCounts{Active: a, New: n, Returning: r}
	}
	cases := []struct {
		name, env string
		rule      obsquery.NewUsersRule
		current   obsquery.UserActivityCounts
		decided   time.Time
		decision  obsquery.NewDecision
	}{
		{"data start inside the range", "fresh", obsquery.NewUsersFirstSeen, counts(4, 2, 2),
			time.Date(2026, 10, 31, 14, 3, 0, 0, time.UTC), obsquery.NewDecidedPart},
		{"data start before the range", "mature", obsquery.NewUsersFirstSeen, counts(3, 1, 2),
			time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC), obsquery.NewDecidedAll},
		{"sign-up event present", "signups", obsquery.NewUsersSignups, counts(3, 1, 2), time.Time{}, obsquery.NewDecidedAll},
		{"no part of the range decided", "brandnew", obsquery.NewUsersFirstSeen, counts(2, 0, 2),
			time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), obsquery.NewDecidedNone},
		{"no events", "empty", obsquery.NewUsersFirstSeen, counts(0, 0, 0), time.Time{}, obsquery.NewDecidedAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := obsquery.UserActivityQuery{Scope: obsquery.Scope{Database: db, EnvRestricted: true, EnvIDs: []string{tc.env}, Start: start, End: end}}
			got, err := obsquery.ReadUserActivity(context.Background(), rumRunner(c), q)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("envs = %+v", got)
			}
			e := got[0]
			if e.NewRule != tc.rule || e.Current != tc.current || !e.NewDecidedFrom.Equal(tc.decided) {
				t.Errorf("rule %s current %+v decided from %v; want %s %+v %v", e.NewRule, e.Current, e.NewDecidedFrom, tc.rule, tc.current, tc.decided)
			}
			if d := obsquery.NewDecided(e.NewDecidedFrom, start, end); d != tc.decision {
				t.Errorf("decision = %s, want %s", d, tc.decision)
			}
		})
	}
}
