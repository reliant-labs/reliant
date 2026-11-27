// Copyright (c) 2025 Reliant Labs

package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests run the store against a real postgres (pkg/pgtest's embedded
// server), since what it promises — one transaction per Apply, the CHECK
// constraints, ordering, JSONB round trips — is postgres's behaviour. They
// are not integration-tagged, so they count toward the package's coverage.

// newStore returns a store whose tables live in a fresh schema of a fresh
// database, the schema already migrated.
func newStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots embedded postgres; runs in task test")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	schema := "alerts"
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db, schema); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	st, err := New(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	return st, db, schema
}

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func instance(owner, env, rule, fp string, state alerting.State) alerting.Instance {
	return alerting.Instance{
		RuleKey:     alerting.RuleKey{Owner: owner, Env: env, Name: rule},
		Fingerprint: fp,
		Labels:      map[string]string{"service": "api", "fp": fp},
		Annotations: map[string]string{"summary": "errors high"},
		State:       state,
		ActiveAt:    t0,
		LastEvalAt:  t0.Add(time.Minute),
		LastValue:   0.5,
	}
}

func event(owner, env, rule, fp string, tr alerting.Transition, at time.Time) alerting.Event {
	return alerting.Event{
		RuleKey: alerting.RuleKey{Owner: owner, Env: env, Name: rule}, Fingerprint: fp,
		Labels: map[string]string{"fp": fp}, Transition: tr, Value: 1, At: at,
	}
}

// Every field of an instance survives the round trip, nullable times and the
// resolve_sent flag included; an instance that never fired reads back with zero
// FiringAt / ClearedAt / ResolvedAt.
func TestStore_InstanceRoundTrip(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()

	full := instance("o", "prod", "high-errors", "fp1", alerting.StateFiring)
	full.FiringAt = t0.Add(2 * time.Minute)
	full.ClearedAt = t0.Add(3 * time.Minute)
	full.ResolvedAt = t0.Add(4 * time.Minute)
	full.ResolveSent = true
	full.LastValue = 12.75
	full.Why = json.RawMessage(`{"detector":"threshold","observed":12.75}`)
	pending := instance("o", "prod", "high-errors", "fp0", alerting.StatePending)

	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{full, pending}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := st.LoadOwner(ctx, "o")
	if err != nil {
		t.Fatalf("LoadOwner: %v", err)
	}
	if len(got) != 2 || got[0].Fingerprint != "fp0" || got[1].Fingerprint != "fp1" {
		t.Fatalf("instances = %+v, want fp0 then fp1 (ordered by env, rule, fingerprint)", got)
	}

	p, f := got[0], got[1]
	if p.State != alerting.StatePending || !p.FiringAt.IsZero() || !p.ClearedAt.IsZero() || !p.ResolvedAt.IsZero() || p.ResolveSent || p.Why != nil {
		t.Errorf("an instance that never fired: %+v", p)
	}
	if f.State != alerting.StateFiring || !f.ResolveSent || f.LastValue != 12.75 {
		t.Errorf("firing instance scalars: %+v", f)
	}
	for name, pair := range map[string][2]time.Time{
		"ActiveAt":   {f.ActiveAt, full.ActiveAt},
		"FiringAt":   {f.FiringAt, full.FiringAt},
		"ClearedAt":  {f.ClearedAt, full.ClearedAt},
		"ResolvedAt": {f.ResolvedAt, full.ResolvedAt},
		"LastEvalAt": {f.LastEvalAt, full.LastEvalAt},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	if f.Labels["service"] != "api" || f.Annotations["summary"] != "errors high" || f.Name != "high-errors" || f.Env != "prod" {
		t.Errorf("identity and maps: %+v", f)
	}
	var why map[string]any
	if err := json.Unmarshal(f.Why, &why); err != nil || why["detector"] != "threshold" {
		t.Errorf("why = %s (%v)", f.Why, err)
	}
}

// A nil label or annotation map is stored as {} and read back as an empty map,
// not as JSON null.
func TestStore_NilMapsStoreAsEmptyObjects(t *testing.T) {
	st, db, schema := newStore(t)
	ctx := context.Background()
	in := instance("o", "e", "r", "f", alerting.StatePending)
	in.Labels, in.Annotations = nil, nil
	if err := st.Apply(ctx, alerting.Changes{
		Upsert: []alerting.Instance{in},
		Events: []alerting.Event{{RuleKey: in.RuleKey, Fingerprint: "f", Transition: alerting.TransitionPending, At: t0}},
	}); err != nil {
		t.Fatal(err)
	}
	var labels, annotations, evLabels string
	if err := db.QueryRow(fmt.Sprintf(`SELECT labels::text, annotations::text FROM %s.alert_instances`, schema)).Scan(&labels, &annotations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(fmt.Sprintf(`SELECT labels::text FROM %s.alert_events`, schema)).Scan(&evLabels); err != nil {
		t.Fatal(err)
	}
	if labels != "{}" || annotations != "{}" || evLabels != "{}" {
		t.Fatalf("stored labels=%s annotations=%s event labels=%s, want {} for each", labels, annotations, evLabels)
	}
	got, err := st.LoadOwner(ctx, "o")
	if err != nil || len(got) != 1 || len(got[0].Labels) != 0 {
		t.Fatalf("LoadOwner = %+v, %v", got, err)
	}
}

// An owner sees only its own instances, and Owners lists each once, sorted.
func TestStore_OwnersAreIsolatedAndListedOnce(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	if owners, err := st.Owners(ctx); err != nil || len(owners) != 0 {
		t.Fatalf("an empty store: %v, %v", owners, err)
	}
	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{
		instance("zed", "e", "r1", "a", alerting.StateFiring),
		instance("zed", "e", "r2", "b", alerting.StateFiring),
		instance("amy", "e", "r1", "c", alerting.StatePending),
	}}); err != nil {
		t.Fatal(err)
	}
	owners, err := st.Owners(ctx)
	if err != nil || strings.Join(owners, ",") != "amy,zed" {
		t.Fatalf("Owners = %v, %v; want [amy zed]", owners, err)
	}
	amy, _ := st.LoadOwner(ctx, "amy")
	zed, _ := st.LoadOwner(ctx, "zed")
	none, err := st.LoadOwner(ctx, "nobody")
	if len(amy) != 1 || len(zed) != 2 || len(none) != 0 || err != nil {
		t.Fatalf("amy=%d zed=%d nobody=%d err=%v", len(amy), len(zed), len(none), err)
	}
}

// Upserting an existing identity refreshes it in place; Delete removes exactly
// the addressed instance.
func TestStore_UpsertRefreshesAndDeleteRemoves(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	a := instance("o", "e", "r", "a", alerting.StatePending)
	b := instance("o", "e", "r", "b", alerting.StatePending)
	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{a, b}}); err != nil {
		t.Fatal(err)
	}

	a.State, a.FiringAt, a.LastValue = alerting.StateFiring, t0.Add(time.Hour), 99
	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{a}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.LoadOwner(ctx, "o")
	if len(got) != 2 || got[0].State != alerting.StateFiring || got[0].LastValue != 99 || !got[0].FiringAt.Equal(a.FiringAt) {
		t.Fatalf("after the refresh: %+v", got)
	}
	if got[1].State != alerting.StatePending {
		t.Fatalf("the refresh touched the other instance: %+v", got[1])
	}

	if err := st.Apply(ctx, alerting.Changes{Delete: []alerting.InstanceRef{{RuleKey: a.RuleKey, Fingerprint: "a"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.LoadOwner(ctx, "o")
	if len(got) != 1 || got[0].Fingerprint != "b" {
		t.Fatalf("after Delete: %+v, want only b", got)
	}
	// Deleting what is not there is not an error.
	if err := st.Apply(ctx, alerting.Changes{Delete: []alerting.InstanceRef{{RuleKey: a.RuleKey, Fingerprint: "gone"}}}); err != nil {
		t.Fatalf("deleting a missing instance: %v", err)
	}
	// An empty change set is a no-op transaction.
	if err := st.Apply(ctx, alerting.Changes{}); err != nil {
		t.Fatalf("empty Apply: %v", err)
	}
}

// Apply is ONE transaction: a statement that fails part-way (here an event the
// CHECK constraint refuses) takes the earlier upserts and deletes with it.
func TestStore_ApplyIsAtomic(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	keep := instance("o", "e", "r", "keep", alerting.StateFiring)
	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{keep}}); err != nil {
		t.Fatal(err)
	}

	newOne := instance("o", "e", "r", "new", alerting.StateFiring)
	err := st.Apply(ctx, alerting.Changes{
		Upsert: []alerting.Instance{newOne},
		Delete: []alerting.InstanceRef{{RuleKey: keep.RuleKey, Fingerprint: "keep"}},
		Events: []alerting.Event{event("o", "e", "r", "new", alerting.Transition("exploded"), t0)},
	})
	if err == nil || !strings.Contains(err.Error(), "pgstore: recording event") {
		t.Fatalf("Apply = %v, want the recording-event failure", err)
	}
	got, _ := st.LoadOwner(ctx, "o")
	if len(got) != 1 || got[0].Fingerprint != "keep" {
		t.Fatalf("after the failed Apply: %+v — the upsert or the delete leaked out of the rolled-back transaction", got)
	}
}

// Each statement's failure is reported with what it was doing.
func TestStore_ApplyNamesTheFailingStatement(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	bad := instance("o", "e", "r", "f", alerting.State("zombie")) // violates ck_alert_instances_state
	err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{bad}})
	if err == nil || !strings.Contains(err.Error(), "pgstore: upserting instance") {
		t.Fatalf("an unknown state = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := st.Apply(cancelled, alerting.Changes{}); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "pgstore: begin") {
		t.Fatalf("a cancelled context = %v, want pgstore: begin wrapping context.Canceled", err)
	}
}

// Events come back newest first, filter by env and since, and honour the
// limit: default 200, an out-of-range limit falling back to it.
func TestStore_EventsFilterOrderAndLimit(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	var evs []alerting.Event
	for i := range 5 {
		evs = append(evs, event("o", "prod", "r", "f", alerting.TransitionFiring, t0.Add(time.Duration(i)*time.Minute)))
	}
	evs = append(evs,
		event("o", "dev", "r", "f", alerting.TransitionPending, t0.Add(10*time.Minute)),
		event("other", "prod", "r", "f", alerting.TransitionFiring, t0.Add(20*time.Minute)),
	)
	// Two events at the same instant: the later insert sorts first.
	evs = append(evs, event("o", "prod", "tie", "first", alerting.TransitionPending, t0.Add(30*time.Minute)),
		event("o", "prod", "tie", "second", alerting.TransitionPending, t0.Add(30*time.Minute)))
	if err := st.Apply(ctx, alerting.Changes{Events: evs}); err != nil {
		t.Fatal(err)
	}

	all, err := st.Events(ctx, alerting.EventQuery{Owner: "o", Since: t0.Add(-time.Hour)})
	if err != nil || len(all) != 8 {
		t.Fatalf("all of o's events = %d, %v; want 8 (the other owner's is excluded)", len(all), err)
	}
	if all[0].Fingerprint != "second" || all[1].Fingerprint != "first" {
		t.Errorf("a tie at one instant: %q then %q, want the later insert first", all[0].Fingerprint, all[1].Fingerprint)
	}
	for i := 1; i < len(all); i++ {
		if all[i].At.After(all[i-1].At) {
			t.Fatalf("events are not newest first: %v after %v", all[i].At, all[i-1].At)
		}
	}
	if e := all[len(all)-1]; e.Transition != alerting.TransitionFiring || e.Labels["fp"] != "f" || e.Value != 1 || e.Why != nil {
		t.Errorf("an event's fields: %+v", e)
	}

	prod, _ := st.Events(ctx, alerting.EventQuery{Owner: "o", Env: "prod", Since: t0.Add(-time.Hour)})
	if len(prod) != 7 {
		t.Errorf("env filter: %d events, want 7", len(prod))
	}
	recent, _ := st.Events(ctx, alerting.EventQuery{Owner: "o", Since: t0.Add(5 * time.Minute)})
	if len(recent) != 3 { // dev@+10, tie@+30 ×2
		t.Errorf("since filter: %d events, want 3", len(recent))
	}
	two, _ := st.Events(ctx, alerting.EventQuery{Owner: "o", Since: t0.Add(-time.Hour), Limit: 2})
	if len(two) != 2 {
		t.Errorf("limit 2: %d events", len(two))
	}
	for _, limit := range []int{0, -1, 1001} {
		got, err := st.Events(ctx, alerting.EventQuery{Owner: "o", Since: t0.Add(-time.Hour), Limit: limit})
		if err != nil || len(got) != 8 {
			t.Errorf("limit %d: %d events, %v; want the default window of 8", limit, len(got), err)
		}
	}
}

// PruneEvents deletes history strictly older than the cutoff, for every owner,
// and reports how many rows went.
func TestStore_PruneEvents(t *testing.T) {
	st, _, _ := newStore(t)
	ctx := context.Background()
	if err := st.Apply(ctx, alerting.Changes{Events: []alerting.Event{
		event("a", "e", "r", "f", alerting.TransitionFiring, t0),
		event("b", "e", "r", "f", alerting.TransitionFiring, t0.Add(time.Hour)),
		event("a", "e", "r", "f", alerting.TransitionResolved, t0.Add(2*time.Hour)),
	}}); err != nil {
		t.Fatal(err)
	}
	n, err := st.PruneEvents(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneEvents = %d, %v; want 1 (the cutoff itself is kept)", n, err)
	}
	if n, err := st.PruneEvents(ctx, t0.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("a second prune = %d, %v; want 0", n, err)
	}
	left, _ := st.Events(ctx, alerting.EventQuery{Owner: "a", Since: t0.Add(-time.Hour)})
	if len(left) != 1 || left[0].Transition != alerting.TransitionResolved {
		t.Fatalf("a's remaining events: %+v", left)
	}
}

// Migrate is idempotent, works unqualified through search_path, and refuses an
// unsafe schema name before touching the database.
func TestMigrate_IdempotentAndValidated(t *testing.T) {
	st, db, schema := newStore(t)
	ctx := context.Background()
	if err := Migrate(ctx, db, schema); err != nil {
		t.Fatalf("a second Migrate: %v", err)
	}
	if err := Migrate(ctx, db, "no spaces allowed"); err == nil {
		t.Fatal("Migrate accepted an invalid schema name")
	}
	if err := Migrate(ctx, db, "does_not_exist"); err == nil || !strings.Contains(err.Error(), "pgstore: applying schema") {
		t.Fatalf("Migrate into a missing schema = %v", err)
	}
	// An unqualified store runs against the connection's search_path.
	bare, err := New(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, ""); err != nil {
		t.Fatalf("Migrate with no schema: %v", err)
	}
	if err := bare.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{instance("o", "e", "r", "f", alerting.StatePending)}}); err != nil {
		t.Fatalf("Apply on the search_path tables: %v", err)
	}
	if got, _ := bare.LoadOwner(ctx, "o"); len(got) != 1 {
		t.Fatalf("search_path store sees %d instances", len(got))
	}
	// The schema-qualified store does not see the search_path store's rows.
	if got, _ := st.LoadOwner(ctx, "o"); len(got) != 0 {
		t.Fatalf("the two schemas leaked into each other: %d instances", len(got))
	}
}

// A store over a dead database reports each failure with what it was doing.
func TestStore_ReportsDatabaseFailures(t *testing.T) {
	st, db, schema := newStore(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadOwner(ctx, "o"); err == nil || !strings.Contains(err.Error(), "pgstore: loading instances") {
		t.Errorf("LoadOwner = %v", err)
	}
	if _, err := st.Owners(ctx); err == nil || !strings.Contains(err.Error(), "pgstore: listing owners") {
		t.Errorf("Owners = %v", err)
	}
	if _, err := st.Events(ctx, alerting.EventQuery{Owner: "o"}); err == nil || !strings.Contains(err.Error(), "pgstore: listing events") {
		t.Errorf("Events = %v", err)
	}
	if _, err := st.PruneEvents(ctx, t0); err == nil || !strings.Contains(err.Error(), "pgstore: pruning events") {
		t.Errorf("PruneEvents = %v", err)
	}
	if err := st.Apply(ctx, alerting.Changes{}); err == nil || !strings.Contains(err.Error(), "pgstore: begin") {
		t.Errorf("Apply = %v", err)
	}
	if err := Migrate(ctx, db, schema); err == nil || !strings.Contains(err.Error(), "pgstore: applying schema") {
		t.Errorf("Migrate = %v", err)
	}
}

// Rows that cannot be decoded are reported rather than skipped.
func TestStore_CorruptRowsAreReported(t *testing.T) {
	st, db, schema := newStore(t)
	ctx := context.Background()
	// jsonb accepts any JSON, so a scalar where an object belongs decodes only
	// as far as the store's own json.Unmarshal into a map.
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s.alert_instances
		(owner, env, rule, fingerprint, labels, annotations, state, active_at, last_eval_at, last_value)
		VALUES ('o','e','r','f','"not an object"','{}','firing', now(), now(), 1)`, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadOwner(ctx, "o"); err == nil || !strings.Contains(err.Error(), "pgstore: decoding labels") {
		t.Errorf("a corrupt labels column = %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`UPDATE %s.alert_instances SET labels = '{}', annotations = '[1]'`, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadOwner(ctx, "o"); err == nil || !strings.Contains(err.Error(), "pgstore: decoding annotations") {
		t.Errorf("a corrupt annotations column = %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s.alert_events (owner, env, rule, fingerprint, labels, transition, value, at)
		VALUES ('o','e','r','f','[1]','firing', 1, now())`, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Events(ctx, alerting.EventQuery{Owner: "o"}); err == nil {
		t.Error("an event with corrupt labels was returned")
	}
}

func TestHelpers_NilMapNullTimeAndWhy(t *testing.T) {
	if got := nonNil(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNil(nil) = %#v", got)
	}
	m := map[string]string{"a": "b"}
	if got := nonNil(m); got["a"] != "b" {
		t.Errorf("nonNil(m) = %#v", got)
	}
	if nullTime(time.Time{}) != nil {
		t.Error("a zero time must be SQL NULL")
	}
	if got := nullTime(t0); got != t0 {
		t.Errorf("nullTime(t0) = %v", got)
	}
	if whyIn(nil) != nil || whyIn(json.RawMessage{}) != nil {
		t.Error("an absent why must be SQL NULL")
	}
	if got := whyIn(json.RawMessage(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("whyIn = %#v", got)
	}
	if whyOut(nil) != nil || whyOut([]byte{}) != nil {
		t.Error("a NULL why must read back as nil")
	}
	if got := whyOut([]byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Errorf("whyOut = %s", got)
	}
}

// New refuses an unsafe schema even with no database, and builds statements
// naming only its own schema.
func TestNew_StatementsCarryTheSchemaPrefix(t *testing.T) {
	st, err := New(nil, "monitoring")
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"load": st.q.load, "owners": st.q.owners, "upsert": st.q.upsert, "del": st.q.del,
		"insertEvent": st.q.insertEvent, "events": st.q.events, "prune": st.q.prune,
	} {
		if !strings.Contains(q, "monitoring.alert_") || strings.Contains(q, "{{") {
			t.Errorf("%s statement is not schema-qualified: %s", name, q)
		}
	}
	bare, err := New(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare.q.load, ".alert_") || strings.Contains(bare.q.load, "{{") {
		t.Errorf("unqualified load: %s", bare.q.load)
	}
}
