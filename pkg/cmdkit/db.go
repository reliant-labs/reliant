package cmdkit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// DBOptions configures OpenDB. Only DSN is required; the rest tune the
// connection pool and the open-time ping.
type DBOptions struct {
	// Driver is the database/sql driver name (e.g. "pgx", "sqlite3").
	// The caller blank-imports the driver as usual; cmdkit never imports
	// one. Zero value defaults to "pgx" — the forge default.
	Driver string
	// DSN is the data source name / connection string. Required.
	DSN string
	// MaxOpenConns, when > 0, caps the open-connection pool. A one-shot
	// CLI tool usually wants a small number (or the default).
	MaxOpenConns int
	// MaxIdleConns, when > 0, caps idle connections kept in the pool.
	MaxIdleConns int
	// ConnMaxIdleTime, when > 0, bounds idle-connection lifetime.
	ConnMaxIdleTime time.Duration
	// ConnMaxLifetime, when > 0, bounds total connection reuse.
	ConnMaxLifetime time.Duration
	// PingTimeout bounds the open-time connectivity check. Zero value
	// defaults to 10s. The check fails fast on an unreachable DB rather
	// than letting the first real query hang — the fail-fast-on-setup
	// posture CLI tools want.
	PingTimeout time.Duration
	// SkipPing disables the open-time ping entirely. Use only when the
	// caller deliberately wants lazy connection (rare for CLI tools).
	SkipPing bool
}

// OpenDB opens a *sql.DB, applies pool tuning, and (unless SkipPing)
// verifies connectivity with a bounded ping. It is the single
// replacement for the per-command sql.Open + PingContext blocks that
// every report/backfill command was copying.
//
// The returned *sql.DB is the caller's to close (defer db.Close()).
// On a ping failure the pool is closed before returning, so a failed
// OpenDB never leaks a half-open pool.
func OpenDB(ctx context.Context, opts DBOptions) (*sql.DB, error) {
	if opts.DSN == "" {
		return nil, fmt.Errorf("cmdkit.OpenDB: DSN is required")
	}
	driver := opts.Driver
	if driver == "" {
		driver = "pgx"
	}

	// Traced like serverkit's pool: a statement run under a span is a child
	// span of it (pkg/observe/dbtrace).
	db, err := dbtrace.Open(driver, opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("open db (%s): %w", driver, err)
	}

	if opts.MaxOpenConns > 0 {
		db.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		db.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(opts.ConnMaxIdleTime)
	}
	if opts.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}

	if opts.SkipPing {
		return db, nil
	}

	pingTimeout := opts.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = 10 * time.Second
	}
	pingCtx, cancel := ContextWithTimeout(ctx, pingTimeout, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db (%s): %w", driver, err)
	}
	return db, nil
}
