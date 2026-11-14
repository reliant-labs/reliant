package dbanalyze

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Default per-statement limits of a session.
const (
	DefaultStatementTimeout = 60 * time.Second
	DefaultLockTimeout      = 5 * time.Second
)

// SQLSTATE codes a session classifies.
const (
	sqlStateQueryCanceled    = "57014" // statement_timeout
	sqlStateLockNotAvailable = "55P03" // lock_timeout
)

// SessionOptions bound every statement a session runs.
type SessionOptions struct {
	// StatementTimeout is the session's statement_timeout. Zero means
	// DefaultStatementTimeout; negative means none.
	StatementTimeout time.Duration
	// LockTimeout is the session's lock_timeout. Zero means
	// DefaultLockTimeout; negative means none.
	LockTimeout time.Duration
}

// Session is ONE connection to a DISPOSABLE database — a clone of the target
// (CloneDatabase) or a scratch database (PrepareScratch) — that an analysis
// applies migrations to the way forge's migrator does: each file in a
// transaction of its own that commits, a statement that cannot run in a
// transaction on its own. The migrations' effects persist in that database
// for whatever reads it next (a query pass's EXPLAIN); the database itself is
// dropped by whoever made it. Nothing a session runs ever reaches the target
// it was cloned from.
//
// A Session is not safe for concurrent use.
type Session struct {
	db   *sql.DB
	conn *sql.Conn
	// tx is the open file's transaction, nil between files.
	tx *sql.Tx
	sp int
}

// Open connects to dsn and sets the session's timeouts. dsn must name a
// disposable database: what a session runs is committed.
func Open(ctx context.Context, dsn string, opts SessionOptions) (*Session, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open target: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to target: %w", parseCause(err))
	}
	s := &Session{db: db, conn: conn}
	settings := []string{
		"SET statement_timeout = " + timeoutSetting(opts.StatementTimeout, DefaultStatementTimeout),
		"SET lock_timeout = " + timeoutSetting(opts.LockTimeout, DefaultLockTimeout),
		"SET application_name = 'forge db analyze'",
	}
	for _, q := range settings {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%s: %w", q, err)
		}
	}
	return s, nil
}

// timeoutSetting renders a duration as a postgres timeout literal: "0"
// (none) for a negative one, def for zero.
func timeoutSetting(d, def time.Duration) string {
	switch {
	case d < 0:
		return "0"
	case d == 0:
		d = def
	}
	return fmt.Sprintf("'%dms'", d.Milliseconds())
}

// querier is what a session reads and writes through: the open file's
// transaction, or the bare connection between files.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Session) q() querier {
	if s.tx != nil {
		return s.tx
	}
	return s.conn
}

// Conn is the session's connection, for a caller's own reads between files
// (EXPLAIN against the schema the migrations left, the catalog).
func (s *Session) Conn() *sql.Conn { return s.conn }

// begin opens a file's transaction.
func (s *Session) begin(ctx context.Context) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	s.tx = tx
	return nil
}

// commit commits the open file's transaction.
func (s *Session) commit() error {
	tx := s.tx
	s.tx = nil
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rollback abandons the open file's transaction, if there is one.
func (s *Session) rollback() {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
}

// Exec runs one statement. Inside a file's transaction it runs under a
// savepoint, so a failure rolls back to it and the transaction stays usable
// for what follows; between files it runs on its own, as postgres runs a
// statement that cannot be in a transaction. The error is the statement's own.
func (s *Session) Exec(ctx context.Context, query string) error {
	if s.tx == nil {
		_, err := s.conn.ExecContext(ctx, query)
		return err
	}
	s.sp++
	name := fmt.Sprintf("forge_analyze_%d", s.sp)
	if _, err := s.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	if _, err := s.tx.ExecContext(ctx, query); err != nil {
		// A rollback that fails leaves an aborted transaction: every later
		// call reports it, which is the honest outcome.
		_, _ = s.tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name)
		return err
	}
	if _, err := s.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// Close abandons an open file's transaction and disconnects. It is safe to
// call more than once.
func (s *Session) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.rollback()
	var errs []error
	if err := s.conn.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, err)
	}
	s.db = nil
	return errors.Join(errs...)
}

// Info reads the server's version and whether the database's statistics can
// size tables. kind and identity are the caller's and are copied in.
//
// Statistics count only on a dsn target: a scratch target's tables are
// empty whatever pg_class says about them. Read them from the target itself
// (Source.Info), not from a clone of it: a clone has the target's row
// estimates but none of its analyze history.
func (s *Session) Info(ctx context.Context, kind TargetKind, identity string) (TargetInfo, error) {
	return readInfo(ctx, sqlQuerier{s.q()}, kind, identity)
}

// rowScanner and rowQuerier are the one-row reads readInfo and
// readMigrationState make, over database/sql (a session) or pgx (a Source).
type rowScanner interface{ Scan(dest ...any) error }

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) rowScanner
}

type sqlQuerier struct{ q querier }

func (s sqlQuerier) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return s.q.QueryRowContext(ctx, query, args...)
}

func readInfo(ctx context.Context, q rowQuerier, kind TargetKind, identity string) (TargetInfo, error) {
	info := TargetInfo{Kind: kind, Identity: identity}
	if err := q.QueryRowContext(ctx,
		"SELECT current_setting('server_version'), current_setting('server_version_num')::int").
		Scan(&info.ServerVersion, &info.ServerVersionNum); err != nil {
		return info, fmt.Errorf("read server version: %w", err)
	}
	// "18.1 (Debian 18.1-1.pgdg120+2)" → "18.1".
	info.ServerVersion, _, _ = strings.Cut(info.ServerVersion, " ")
	if kind != TargetDSN {
		return info, nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_stat_user_tables
		WHERE last_analyze IS NOT NULL OR last_autoanalyze IS NOT NULL)`).
		Scan(&info.HasStatistics); err != nil {
		return info, fmt.Errorf("read statistics: %w", err)
	}
	return info, nil
}

// MigrationState is what a target's schema_migrations (golang-migrate's
// version table, which forge's migrator keeps) says.
type MigrationState struct {
	// Applied is false when the table is absent or empty: nothing applied.
	Applied bool
	Version uint64
	Dirty   bool
}

// MigrationState reads the database's schema_migrations without creating it
// (migratekit.Open would: golang-migrate creates its version table on open).
func (s *Session) MigrationState(ctx context.Context) (MigrationState, error) {
	return readMigrationState(ctx, sqlQuerier{s.q()})
}

func readMigrationState(ctx context.Context, q rowQuerier) (MigrationState, error) {
	var st MigrationState
	var exists bool
	if err := q.QueryRowContext(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return st, fmt.Errorf("look for schema_migrations: %w", err)
	}
	if !exists {
		return st, nil
	}
	var version int64
	err := q.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &st.Dirty)
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, pgx.ErrNoRows):
		return st, nil
	case err != nil:
		return st, fmt.Errorf("read schema_migrations: %w", err)
	case version < 0:
		// golang-migrate's "no version".
		return MigrationState{}, nil
	}
	st.Applied, st.Version = true, uint64(version)
	return st, nil
}

// sqlState is err's SQLSTATE, or "".
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
