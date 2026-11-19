//go:build integration

package pgstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/alerting/pgstore"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func scratchSchema(t *testing.T) (*sql.DB, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: needs a real Postgres")
	}
	dsn, drop, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision a scratch Postgres database: %v", err)
	}
	t.Cleanup(drop)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("why_it_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func TestWhyRoundTripsThroughInstancesAndEvents(t *testing.T) {
	ctx := context.Background()
	db, schema := scratchSchema(t)
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	st, err := pgstore.New(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	key := alerting.RuleKey{Owner: "o", Env: "e", Name: "r"}
	why := json.RawMessage(`{"detector":"release_regressed","signal":"error_rate","observed":0.041,"context":{}}`)
	in := alerting.Instance{RuleKey: key, Fingerprint: "fp", Labels: map[string]string{"a": "b"}, State: alerting.StateFiring,
		ActiveAt: now, FiringAt: now, LastEvalAt: now, LastValue: 2, Why: why}
	noWhy := alerting.Instance{RuleKey: alerting.RuleKey{Owner: "o", Env: "e", Name: "plain"}, Fingerprint: "fp2",
		Labels: map[string]string{}, State: alerting.StateFiring, ActiveAt: now, LastEvalAt: now}
	if err := st.Apply(ctx, alerting.Changes{
		Upsert: []alerting.Instance{in, noWhy},
		Events: []alerting.Event{{RuleKey: key, Fingerprint: "fp", Labels: in.Labels, Transition: alerting.TransitionFiring, Value: 2, At: now, Why: why}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadOwner(ctx, "o")
	if err != nil || len(got) != 2 {
		t.Fatalf("LoadOwner: %v %v", got, err)
	}
	by := map[string]alerting.Instance{got[0].Name: got[0], got[1].Name: got[1]}
	var a, b map[string]any
	_ = json.Unmarshal(by["r"].Why, &a)
	if a["detector"] != "release_regressed" || a["observed"] != 0.041 {
		t.Fatalf("instance why = %s", by["r"].Why)
	}
	if by["plain"].Why != nil {
		t.Fatalf("a rule with no payload came back with %s", by["plain"].Why)
	}

	// A later evaluation refreshes it in place.
	in.Why = json.RawMessage(`{"detector":"release_regressed","observed":0.5,"context":{}}`)
	if err := st.Apply(ctx, alerting.Changes{Upsert: []alerting.Instance{in}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.LoadOwner(ctx, "o")
	for _, x := range got {
		if x.Name == "r" {
			_ = json.Unmarshal(x.Why, &b)
		}
	}
	if b["observed"] != 0.5 {
		t.Fatalf("refreshed why = %v", b)
	}

	evs, err := st.Events(ctx, alerting.EventQuery{Owner: "o", Since: now.Add(-time.Hour)})
	if err != nil || len(evs) != 1 || string(evs[0].Why) == "" {
		t.Fatalf("events: %+v %v", evs, err)
	}
}

// A database created by the first release of the schema has no why column:
// Migrate adds it and keeps every row.
func TestMigrateAddsTheWhyColumnToAnOlderSchema(t *testing.T) {
	ctx := context.Background()
	db, schema := scratchSchema(t)
	for _, q := range []string{
		`CREATE TABLE %s.alert_instances (owner TEXT NOT NULL, env TEXT NOT NULL, rule TEXT NOT NULL, fingerprint TEXT NOT NULL,
			labels JSONB NOT NULL, annotations JSONB NOT NULL DEFAULT '{}'::jsonb, state TEXT NOT NULL, active_at TIMESTAMPTZ NOT NULL,
			firing_at TIMESTAMPTZ, cleared_at TIMESTAMPTZ, resolved_at TIMESTAMPTZ, resolve_sent BOOLEAN NOT NULL DEFAULT FALSE,
			last_eval_at TIMESTAMPTZ NOT NULL, last_value DOUBLE PRECISION NOT NULL, PRIMARY KEY (owner, env, rule, fingerprint))`,
		`CREATE TABLE %s.alert_events (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, owner TEXT NOT NULL, env TEXT NOT NULL,
			rule TEXT NOT NULL, fingerprint TEXT NOT NULL, labels JSONB NOT NULL, transition TEXT NOT NULL, value DOUBLE PRECISION NOT NULL,
			at TIMESTAMPTZ NOT NULL)`,
		`INSERT INTO %s.alert_instances (owner, env, rule, fingerprint, labels, state, active_at, last_eval_at, last_value)
			VALUES ('o','e','old','f','{}','firing', now(), now(), 1)`,
	} {
		if _, err := db.Exec(fmt.Sprintf(q, schema)); err != nil {
			t.Fatal(err)
		}
	}
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	st, _ := pgstore.New(db, schema)
	got, err := st.LoadOwner(ctx, "o")
	if err != nil || len(got) != 1 || got[0].Name != "old" || got[0].Why != nil {
		t.Fatalf("after the upgrade: %+v %v", got, err)
	}
	if err := pgstore.Migrate(ctx, db, schema); err != nil {
		t.Fatalf("Migrate is not idempotent: %v", err)
	}
}
