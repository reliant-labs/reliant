package cronkit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"
)

// Table is the claims table [Postgres] creates and writes. It is bookkeeping,
// like the migrator's own tables: a row per tick that ran, pruned after
// [DefaultRetention].
const Table = "forge_cron_ticks"

// DefaultRetention is how long a claimed tick's row is kept. It only has to
// outlast the spread between replicas waking for the same tick.
const DefaultRetention = 24 * time.Hour

// schemaSQL is the claims table. The primary key IS the claim: the first
// INSERT of (job, tick) lands and every later one conflicts.
const schemaSQL = `CREATE TABLE IF NOT EXISTS ` + Table + ` (
    job        text        NOT NULL,
    tick       timestamptz NOT NULL,
    claimed_by text        NOT NULL DEFAULT '',
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job, tick)
)`

// DB is what [Postgres] needs from a pool. A forge app's ORM handle
// (orm.Context, *orm.Client) satisfies it; wrap a *sql.DB with [SQLDB].
type DB interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SQLDB adapts a *sql.DB to [DB].
func SQLDB(db *sql.DB) DB {
	if db == nil {
		return nil
	}
	return sqlDB{db}
}

type sqlDB struct{ db *sql.DB }

func (s sqlDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, query, args...)
}

// PostgresOptions tunes [Postgres]. The zero value is production-ready.
type PostgresOptions struct {
	// Replica names the claimant in claimed_by (default: the hostname,
	// which is the pod name in Kubernetes).
	Replica string
	// Retention is how long claimed rows are kept (default
	// [DefaultRetention]).
	Retention time.Duration
}

// Postgres returns [Claims] over the app's database, or nil when db is nil
// (or a nil pointer in an interface, as an ORM handle is when the app has no
// DATABASE_URL), so [Run] falls back to firing on every replica with its boot
// warning.
//
// The claims table is created on first use (CREATE TABLE IF NOT EXISTS), so
// the database role the app runs as needs CREATE on its schema, as it does to
// run its own migrations at boot. Any pool works, including one behind a
// transaction-pooling PgBouncer: each claim is one autocommit statement.
func Postgres(db DB, opts ...PostgresOptions) Claims {
	if isNil(db) {
		return nil
	}
	var o PostgresOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Replica == "" {
		o.Replica, _ = os.Hostname()
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	return &pgClaims{db: db, opts: o}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

type pgClaims struct {
	db   DB
	opts PostgresOptions

	mu    sync.Mutex
	ready bool
}

// Claim inserts (job, tick); the insert that lands wins.
func (p *pgClaims) Claim(ctx context.Context, job string, tick time.Time) (bool, error) {
	if err := p.ensure(ctx); err != nil {
		return false, err
	}
	tick = tick.UTC()
	res, err := p.db.Exec(ctx,
		`INSERT INTO `+Table+` (job, tick, claimed_by) VALUES ($1, $2, $3) ON CONFLICT (job, tick) DO NOTHING`,
		job, tick, p.opts.Replica)
	if err != nil {
		return false, fmt.Errorf("claim %s tick %s: %w", job, tick.Format(time.RFC3339), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim %s tick %s: rows affected: %w", job, tick.Format(time.RFC3339), err)
	}
	if n != 1 {
		return false, nil
	}
	// The winner prunes its job's old rows. A failure costs nothing but
	// space, so it does not undo the win.
	_, _ = p.db.Exec(ctx, `DELETE FROM `+Table+` WHERE job = $1 AND tick < $2`, job, tick.Add(-p.opts.Retention))
	return true, nil
}

// ensure creates the table once per process. Replicas racing to create it
// can see a duplicate-object error from the one that lost; the table exists
// either way.
func (p *pgClaims) ensure(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ready {
		return nil
	}
	if _, err := p.db.Exec(ctx, schemaSQL); err != nil && !duplicateObject(err) {
		return fmt.Errorf("create %s: %w", Table, err)
	}
	p.ready = true
	return nil
}

// duplicateObject reports a concurrent CREATE TABLE IF NOT EXISTS losing its
// race: unique_violation on the catalog, or duplicate_table.
func duplicateObject(err error) bool {
	var st interface{ SQLState() string }
	if !errors.As(err, &st) {
		return false
	}
	switch st.SQLState() {
	case "23505", "42P07":
		return true
	}
	return false
}
