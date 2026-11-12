package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/jackc/pgx/v5/pgconn"
)

// golang-migrate runs on the session the run holds (runlock.go, ONE SESSION),
// through every entry point. Its pgx v5 driver cannot: it opens its own pool
// from a URL, or takes a *sql.DB and checks a connection out of it, and either
// way it is a second session. Its postgres driver binds to a *sql.Conn the
// caller owns (postgres.WithConnection), and is driver-agnostic over
// database/sql, so it runs on the pgx stdlib connection migratekit already
// has.

// bindMigrate builds golang-migrate over src and conn. The caller holds the
// run lock on conn: building the driver creates schema_migrations. On error,
// conn and src are the caller's to close; nothing here closes either.
//
// Every migration runs under the run's lock_timeout and statement_timeout and
// is retried when its lock times out (locktimeout.go). The guard sits inside
// pgxErrors, so it sees postgres's own errors.
func bindMigrate(ctx context.Context, conn *sql.Conn, src source.Driver, embedded sourceSet, cfg *postgres.Config, run RunOptions) (*migrate.Migrate, error) {
	drv, err := postgres.WithConnection(ctx, conn, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating migration db driver: %w", err)
	}
	guarded := newLockGuard(conn, drv, embedded, run, !cfg.MultiStatementEnabled)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", pgxErrors{guarded})
	if err != nil {
		return nil, fmt.Errorf("creating migrator: %w", err)
	}
	return m, nil
}

// driverConfig reads golang-migrate's own `x-` options out of a DSN — the
// options its pgx v5 driver read when Open built it from the URL, so a DSN
// that set one keeps working: x-migrations-table, x-migrations-table-quoted,
// x-statement-timeout (milliseconds), x-multi-statement and
// x-multi-statement-max-size. Anything else is the server's (sqlDSN strips the
// x- options before connecting).
func driverConfig(dsn string) (*postgres.Config, error) {
	u, err := url.Parse(NormalizeDSN(dsn))
	if err != nil {
		// sql.Open reports an unusable DSN, with the driver's own words.
		return &postgres.Config{MultiStatementMaxSize: postgres.DefaultMultiStatementMaxSize}, nil
	}
	q := u.Query()
	cfg := &postgres.Config{
		MigrationsTable:       q.Get("x-migrations-table"),
		MultiStatementMaxSize: postgres.DefaultMultiStatementMaxSize,
	}
	if s := q.Get("x-migrations-table-quoted"); s != "" {
		if cfg.MigrationsTableQuoted, err = strconv.ParseBool(s); err != nil {
			return nil, fmt.Errorf("unable to parse option x-migrations-table-quoted: %w", err)
		}
	}
	if t := cfg.MigrationsTable; cfg.MigrationsTableQuoted && t != "" && (t[0] != '"' || t[len(t)-1] != '"') {
		return nil, fmt.Errorf("x-migrations-table must be quoted (for instance '\"migrate\".\"schema_migrations\"') "+
			"when x-migrations-table-quoted is enabled, current value is: %s", t)
	}
	if s := q.Get("x-statement-timeout"); s != "" {
		ms, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("unable to parse option x-statement-timeout: %w", err)
		}
		cfg.StatementTimeout = time.Duration(ms) * time.Millisecond
	}
	if s := q.Get("x-multi-statement"); s != "" {
		if cfg.MultiStatementEnabled, err = strconv.ParseBool(s); err != nil {
			return nil, fmt.Errorf("unable to parse option x-multi-statement: %w", err)
		}
	}
	if s := q.Get("x-multi-statement-max-size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("unable to parse option x-multi-statement-max-size: %w", err)
		}
		if n > 0 {
			cfg.MultiStatementMaxSize = n
		}
	}
	return cfg, nil
}

// pgxErrors restores what a failed migration's error says. The postgres driver
// formats a *pq.Error with the message first and the line and column it failed
// at; over pgx it gets a *pgconn.PgError instead and reports "migration failed
// in line 0", with the message only in the details. This rebuilds the pgx v5
// driver's wording from the PgError.
type pgxErrors struct{ database.Driver }

func (d pgxErrors) Run(migration io.Reader) error {
	err := d.Driver.Run(migration)
	var dbErr database.Error
	var pgErr *pgconn.PgError
	if !errors.As(err, &dbErr) || !errors.As(dbErr.OrigErr, &pgErr) {
		return err
	}
	msg := "migration failed: " + pgErr.Message
	line, col, ok := lineAndColumn(string(dbErr.Query), int(pgErr.Position))
	if ok {
		msg = fmt.Sprintf("%s (column %d)", msg, col)
	}
	if pgErr.Detail != "" {
		msg += ", " + pgErr.Detail
	}
	return database.Error{OrigErr: dbErr.OrigErr, Err: msg, Query: dbErr.Query, Line: line}
}

// lineAndColumn maps postgres's error position — 1-based, in characters — to
// a line and column of query.
func lineAndColumn(query string, pos int) (line, col uint, ok bool) {
	runes := []rune(strings.ReplaceAll(query, "\r\n", "\n"))
	if pos < 1 || pos > len(runes) {
		return 0, 0, false
	}
	before := runes[:pos]
	lastNL := -1
	for i, r := range before {
		if r == '\n' {
			line++
			lastNL = i
		}
	}
	return line + 1, uint(pos - 1 - lastNL), true
}
