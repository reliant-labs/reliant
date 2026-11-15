// Copyright (c) 2025 Reliant Labs

// Package pgstore is the Postgres alerting.StateStore.
//
// The schema is not applied by the store. A consumer applies it the way it
// applies every other migration: SchemaSQL renders the DDL for a schema name,
// and a consumer with its own migration tool copies that into a migration file
// (and pins it with a test comparing the file to SchemaSQL). Migrate applies it
// directly for a deployment that has no migration tool of its own.
package pgstore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/alerting"
)

//go:embed schema.sql
var schemaTemplate string

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// SchemaSQL is the DDL with its tables in schema ("" for the connection's
// search_path).
func SchemaSQL(schema string) (string, error) {
	prefix := ""
	if schema != "" {
		if !identRe.MatchString(schema) {
			return "", fmt.Errorf("pgstore: schema %q is not a plain lower-case identifier", schema)
		}
		prefix = schema + "."
	}
	return strings.ReplaceAll(schemaTemplate, "{{schema}}", prefix), nil
}

// Migrate applies SchemaSQL. It is idempotent.
func Migrate(ctx context.Context, db *sql.DB, schema string) error {
	ddl, err := SchemaSQL(schema)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("pgstore: applying schema: %w", err)
	}
	return nil
}

// Store implements alerting.StateStore over database/sql with a Postgres driver.
type Store struct {
	db *sql.DB
	q  queries
}

// queries are the statements with the schema prefix already applied, built once
// from constants so no SQL is assembled at call time.
type queries struct {
	load, owners, upsert, del, insertEvent, events, prune string
}

var _ alerting.StateStore = (*Store)(nil)

// New builds a Store whose tables live in schema ("" for search_path).
func New(db *sql.DB, schema string) (*Store, error) {
	if _, err := SchemaSQL(schema); err != nil {
		return nil, err
	}
	prefix := ""
	if schema != "" {
		prefix = schema + "."
	}
	r := strings.NewReplacer("{{t}}", prefix, "{{cols}}", instanceCols)
	return &Store{db: db, q: queries{
		load:        r.Replace(`SELECT {{cols}} FROM {{t}}alert_instances WHERE owner = $1 ORDER BY env, rule, fingerprint`),
		owners:      r.Replace(`SELECT DISTINCT owner FROM {{t}}alert_instances ORDER BY owner`),
		upsert:      r.Replace(upsertSQL),
		del:         r.Replace(`DELETE FROM {{t}}alert_instances WHERE owner=$1 AND env=$2 AND rule=$3 AND fingerprint=$4`),
		insertEvent: r.Replace(`INSERT INTO {{t}}alert_events (owner, env, rule, fingerprint, labels, transition, value, at, why) VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb)`),
		events: r.Replace(`SELECT owner, env, rule, fingerprint, labels, transition, value, at, why FROM {{t}}alert_events
WHERE owner = $1 AND ($2 = '' OR env = $2) AND at >= $3 ORDER BY at DESC, id DESC LIMIT $4`),
		prune: r.Replace(`DELETE FROM {{t}}alert_events WHERE at < $1`),
	}}, nil
}

const instanceCols = `owner, env, rule, fingerprint, labels, annotations, state, active_at, firing_at, cleared_at, resolved_at, resolve_sent, last_eval_at, last_value, why`

const upsertSQL = `
INSERT INTO {{t}}alert_instances ({{cols}})
VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb)
ON CONFLICT (owner, env, rule, fingerprint) DO UPDATE SET
    labels = EXCLUDED.labels, annotations = EXCLUDED.annotations, state = EXCLUDED.state,
    active_at = EXCLUDED.active_at, firing_at = EXCLUDED.firing_at, cleared_at = EXCLUDED.cleared_at,
    resolved_at = EXCLUDED.resolved_at, resolve_sent = EXCLUDED.resolve_sent,
    last_eval_at = EXCLUDED.last_eval_at, last_value = EXCLUDED.last_value, why = EXCLUDED.why`

// LoadOwner implements alerting.StateStore.
func (s *Store) LoadOwner(ctx context.Context, owner string) ([]alerting.Instance, error) {
	rows, err := s.db.QueryContext(ctx, s.q.load, owner)
	if err != nil {
		return nil, fmt.Errorf("pgstore: loading instances: %w", err)
	}
	defer rows.Close()
	var out []alerting.Instance
	for rows.Next() {
		var (
			in                        alerting.Instance
			labels, annotations, why  []byte
			state                     string
			firing, cleared, resolved sql.NullTime
		)
		if err := rows.Scan(&in.Owner, &in.Env, &in.Name, &in.Fingerprint, &labels, &annotations, &state, &in.ActiveAt,
			&firing, &cleared, &resolved, &in.ResolveSent, &in.LastEvalAt, &in.LastValue, &why); err != nil {
			return nil, fmt.Errorf("pgstore: scanning instance: %w", err)
		}
		if err := json.Unmarshal(labels, &in.Labels); err != nil {
			return nil, fmt.Errorf("pgstore: decoding labels: %w", err)
		}
		if err := json.Unmarshal(annotations, &in.Annotations); err != nil {
			return nil, fmt.Errorf("pgstore: decoding annotations: %w", err)
		}
		in.Why = whyOut(why)
		in.State = alerting.State(state)
		in.FiringAt, in.ClearedAt, in.ResolvedAt = firing.Time, cleared.Time, resolved.Time
		out = append(out, in)
	}
	return out, rows.Err()
}

// Owners implements alerting.StateStore.
func (s *Store) Owners(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q.owners)
	if err != nil {
		return nil, fmt.Errorf("pgstore: listing owners: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Apply implements alerting.StateStore. One transaction per call.
func (s *Store) Apply(ctx context.Context, ch alerting.Changes) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, in := range ch.Upsert {
		labels, err := json.Marshal(nonNil(in.Labels))
		if err != nil {
			return err
		}
		annotations, err := json.Marshal(nonNil(in.Annotations))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q.upsert,
			in.Owner, in.Env, in.Name, in.Fingerprint, string(labels), string(annotations), string(in.State), in.ActiveAt,
			nullTime(in.FiringAt), nullTime(in.ClearedAt), nullTime(in.ResolvedAt), in.ResolveSent, in.LastEvalAt, in.LastValue, whyIn(in.Why)); err != nil {
			return fmt.Errorf("pgstore: upserting instance: %w", err)
		}
	}
	for _, r := range ch.Delete {
		if _, err := tx.ExecContext(ctx, s.q.del,
			r.Owner, r.Env, r.Name, r.Fingerprint); err != nil {
			return fmt.Errorf("pgstore: deleting instance: %w", err)
		}
	}
	for _, e := range ch.Events {
		labels, err := json.Marshal(nonNil(e.Labels))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q.insertEvent,
			e.Owner, e.Env, e.Name, e.Fingerprint, string(labels), string(e.Transition), e.Value, e.At, whyIn(e.Why)); err != nil {
			return fmt.Errorf("pgstore: recording event: %w", err)
		}
	}
	return tx.Commit()
}

// Events implements alerting.StateStore.
func (s *Store) Events(ctx context.Context, q alerting.EventQuery) ([]alerting.Event, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.q.events, q.Owner, q.Env, q.Since, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: listing events: %w", err)
	}
	defer rows.Close()
	var out []alerting.Event
	for rows.Next() {
		var (
			e          alerting.Event
			labels     []byte
			why        []byte
			transition string
		)
		if err := rows.Scan(&e.Owner, &e.Env, &e.Name, &e.Fingerprint, &labels, &transition, &e.Value, &e.At, &why); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(labels, &e.Labels); err != nil {
			return nil, err
		}
		e.Why = whyOut(why)
		e.Transition = alerting.Transition(transition)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents deletes history older than before and returns how many rows went.
func (s *Store) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.q.prune, before)
	if err != nil {
		return 0, fmt.Errorf("pgstore: pruning events: %w", err)
	}
	return res.RowsAffected()
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// whyIn is the why column's value: NULL for no payload.
func whyIn(w json.RawMessage) any {
	if len(w) == 0 {
		return nil
	}
	return string(w)
}

// whyOut is a stored why column as an instance carries it: nil for NULL.
func whyOut(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}
