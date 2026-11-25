package activity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaSQL is the canonical activity_events table [Store] reads and writes,
// with the indexes its List filters use. A project owns the table as one of
// its migrations; `forge project new` and `forge scaffold activity` write
// this DDL into db/migrations. Keep a hand-written table's columns to this
// shape.
const SchemaSQL = `CREATE TABLE activity_events (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    scope        text        NOT NULL DEFAULT '',
    actor_id     text        NOT NULL,
    actor_name   text        NOT NULL DEFAULT '',
    actor_kind   text        NOT NULL CHECK (actor_kind IN ('user', 'machine', 'system')),
    verb         text        NOT NULL,
    target_kind  text        NOT NULL,
    target_id    text        NOT NULL,
    target_label text        NOT NULL DEFAULT '',
    env          text        NOT NULL DEFAULT '',
    via          text        NOT NULL DEFAULT '',
    procedure    text        NOT NULL DEFAULT '',
    detail       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    trace_id     text        NOT NULL DEFAULT ''
);

CREATE INDEX activity_events_scope_time_idx ON activity_events (scope, occurred_at DESC, id DESC);
CREATE INDEX activity_events_scope_target_idx ON activity_events (scope, target_kind, target_id, occurred_at DESC);
CREATE INDEX activity_events_scope_actor_idx ON activity_events (scope, actor_id, occurred_at DESC);
`

// DB is the database handle a [Store] runs statements through. *orm.Client
// satisfies it, and a statement issued with a ctx carrying an orm RunTx joins
// that transaction. Wrap a bare *sql.DB with [SQLDB].
type DB interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SQLDB adapts a *sql.DB to [DB]. Its statements never join an orm
// transaction; prefer the project's *orm.Client.
func SQLDB(db *sql.DB) DB { return sqlDB{db} }

type sqlDB struct{ db *sql.DB }

func (s sqlDB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, q, args...)
}

func (s sqlDB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, q, args...)
}

// Store is the Postgres [Writer] and reader for the activity_events table.
type Store struct {
	db    DB
	table string
}

// eventsTable is the table the store names.
const eventsTable = "activity_events"

// StoreOption configures a [Store].
type StoreOption func(*Store)

// WithSchema names the schema activity_events lives in, so every statement
// says "<schema>".activity_events rather than resolving the bare name through
// the connection's search_path, which a transaction-pooling proxy can leave
// pointing anywhere (see audit.WithSchema). Empty leaves it unqualified.
func WithSchema(schema string) StoreOption {
	return func(s *Store) {
		s.table = eventsTable
		if schema != "" {
			s.table = `"` + strings.ReplaceAll(schema, `"`, `""`) + `".` + eventsTable
		}
	}
}

// NewStore returns a Store over db. Pass the same Store to the interceptor
// (Deps.Writer), to Record, and to the ListActivity handler.
func NewStore(db DB, opts ...StoreOption) *Store {
	s := &Store{db: db, table: eventsTable}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Write inserts ev. The store assigns the id; a zero OccurredAt is now.
func (s *Store) Write(ctx context.Context, ev Event) error {
	ev.normalize()
	detail := []byte("{}")
	if len(ev.Detail) > 0 {
		b, err := json.Marshal(ev.Detail)
		if err != nil {
			return fmt.Errorf("activity store: encode detail: %w", err)
		}
		detail = b
	}
	_, err := s.db.Exec(ctx, `INSERT INTO `+s.table+` (occurred_at, scope, actor_id, actor_name, actor_kind, verb,
		target_kind, target_id, target_label, env, via, procedure, detail, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		ev.OccurredAt, ev.Scope, ev.Actor.ID, ev.Actor.Name, string(ev.Actor.Kind), ev.Verb,
		ev.Target.Kind, ev.Target.ID, ev.Target.Label, ev.Env, ev.Via, ev.Procedure, string(detail), ev.TraceID)
	if err != nil {
		return fmt.Errorf("activity store: insert: %w", err)
	}
	return nil
}

// ScopeSeparator separates the segments of a hierarchical scope: an event
// scoped "org_1/billing" belongs to project billing of org org_1, and one
// scoped "org_1" to the org itself. See Filter.IncludeAncestors and
// Filter.IncludeDescendants.
const ScopeSeparator = "/"

// Filter selects events for [Store.List]. Set fields are AND-combined; an
// empty field does not filter. Scope is matched exactly, including "": a
// read handler serving several orgs or projects must always set it.
type Filter struct {
	Scope string
	// IncludeAncestors also matches events scoped to each ancestor of Scope:
	// a project's feed ("org_1/billing") then includes the events of its org
	// ("org_1"), such as an alert receiver created org-wide.
	IncludeAncestors bool
	// IncludeDescendants also matches events scoped below Scope: an org's
	// feed ("org_1") then includes every project's ("org_1/billing", ...).
	IncludeDescendants bool
	ActorID            string
	ActorKind          ActorKind
	Verb               string
	TargetKind         string
	TargetID           string
	Env                string
	// Envs matches any of the listed environments (AND-combined with Env).
	// Empty does not filter.
	Envs []string
	Via  string
	// Since and Until bound OccurredAt (inclusive, exclusive). Zero is
	// unbounded.
	Since, Until time.Time
	// PageSize is the page length. Zero is DefaultPageSize; it is capped at
	// MaxPageSize.
	PageSize int
	// PageToken continues from a previous page's NextPageToken.
	PageToken string
}

// Page sizes for [Store.List].
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// ErrBadPageToken is returned for a PageToken List did not issue.
var ErrBadPageToken = errors.New("activity store: malformed page token")

// Page is one page of events, newest first.
type Page struct {
	Events []Event
	// NextPageToken continues the listing; empty on the last page.
	NextPageToken string
}

// List returns the events matching f, newest first. Paging is keyset on
// (occurred_at, id), so a page never repeats or skips a row as new events
// arrive.
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	var args []any
	var where []string
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	add("scope = ANY($%d)", scopeSet(f))
	if f.IncludeDescendants {
		// The prefix is escaped so a scope containing LIKE's own wildcards
		// matches literally.
		add(`scope LIKE $%d ESCAPE '\'`, likeEscape(f.Scope+ScopeSeparator)+"%")
		// The two scope conditions are alternatives, not a conjunction.
		n := len(where)
		where = append(where[:n-2], "("+where[n-2]+" OR "+where[n-1]+")")
	}
	if len(f.Envs) > 0 {
		add("env = ANY($%d)", f.Envs)
	}
	for _, c := range []struct{ col, v string }{
		{"actor_id", f.ActorID}, {"actor_kind", string(f.ActorKind)}, {"verb", f.Verb},
		{"target_kind", f.TargetKind}, {"target_id", f.TargetID}, {"env", f.Env}, {"via", f.Via},
	} {
		if c.v != "" {
			add(c.col+" = $%d", c.v)
		}
	}
	if !f.Since.IsZero() {
		add("occurred_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("occurred_at < $%d", f.Until)
	}
	if f.PageToken != "" {
		at, id, err := decodePageToken(f.PageToken)
		if err != nil {
			return Page{}, err
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(occurred_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	size := f.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	args = append(args, size+1)
	q := `SELECT id, occurred_at, scope, actor_id, actor_name, actor_kind, verb, target_kind, target_id,
		target_label, env, via, procedure, detail, trace_id FROM ` + s.table +
		` WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(` ORDER BY occurred_at DESC, id DESC LIMIT $%d`, len(args))

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("activity store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var page Page
	for rows.Next() {
		var ev Event
		var kind string
		var detail []byte
		if err := rows.Scan(&ev.ID, &ev.OccurredAt, &ev.Scope, &ev.Actor.ID, &ev.Actor.Name, &kind, &ev.Verb,
			&ev.Target.Kind, &ev.Target.ID, &ev.Target.Label, &ev.Env, &ev.Via, &ev.Procedure, &detail, &ev.TraceID); err != nil {
			return Page{}, fmt.Errorf("activity store: scan: %w", err)
		}
		ev.Actor.Kind = ActorKind(kind)
		if len(detail) > 0 && string(detail) != "{}" {
			_ = json.Unmarshal(detail, &ev.Detail)
		}
		page.Events = append(page.Events, ev)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("activity store: rows: %w", err)
	}
	if len(page.Events) > size {
		page.Events = page.Events[:size]
		last := page.Events[size-1]
		page.NextPageToken = encodePageToken(last.OccurredAt, last.ID)
	}
	return page, nil
}

// scopeSet is Scope, plus each of its ancestors under IncludeAncestors.
func scopeSet(f Filter) []string {
	set := []string{f.Scope}
	if !f.IncludeAncestors {
		return set
	}
	for s := f.Scope; ; {
		i := strings.LastIndex(s, ScopeSeparator)
		if i < 0 {
			return set
		}
		s = s[:i]
		set = append(set, s)
	}
}

// likeEscape escapes LIKE's wildcards (and its escape character) in s.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func encodePageToken(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodePageToken(tok string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return time.Time{}, "", ErrBadPageToken
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrBadPageToken
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", ErrBadPageToken
	}
	return at, id, nil
}
