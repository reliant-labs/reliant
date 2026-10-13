// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// openPostgres opens a database/sql pool over pgx, exactly as
// sql.Open("pgx", dsn) does, except that its connections tell database/sql to
// retry a statement a draining PgBouncer refused (see poolerDrainConn).
func openPostgres(dsn string) (*sql.DB, error) {
	pgxDriver, ok := stdlib.GetDefaultDriver().(driver.DriverContext)
	if !ok {
		return nil, errors.New("the pgx driver cannot open a connector")
	}
	connector, err := pgxDriver.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(poolerDrainConnector{Connector: connector}), nil
}

// poolerDrainConnector hands database/sql poolerDrainConns.
type poolerDrainConnector struct {
	driver.Connector
}

func (c poolerDrainConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pgxConn, ok := conn.(*stdlib.Conn)
	if !ok {
		return conn, nil
	}
	return poolerDrainConn{Conn: pgxConn}, nil
}

// poolerDrainConn is a pgx connection that reports a statement PgBouncer
// refused while draining as driver.ErrBadConn, so database/sql runs it again
// on another connection instead of failing the caller.
//
// Every pooled client sees this on every PgBouncer rollout. On SIGTERM the
// pooler pod waits for its endpoint to leave the Service and then shuts down
// "safely" (SHUTDOWN WAIT_FOR_SERVERS): transactions in flight finish, and any
// client that needs a server connection is disconnected with
// "FATAL: server shutting down" (SQLSTATE 08P01). A database/sql pool holds
// idle connections to that pod, so the next statement on each one failed —
// the temporal-worker's "Failed to build available drivers from settings:
// ... vault: loading key: FATAL: server shutting down" — although the other
// replica was serving the whole time.
//
// Retrying is safe because PgBouncer sends that refusal only to a client that
// had no server connection, i.e. before forwarding the statement anywhere
// (pause_client and the janitor's wait-queue sweep in pgbouncer's
// objects.c / janitor.c): the statement never ran. database/sql retries an
// ErrBadConn twice on pooled connections and then on a freshly dialed one,
// which the Service routes to a replica that is not draining.
//
// The wrapped error keeps PgBouncer's message, so a retry that runs out —
// both replicas down — still says why.
//
// The pgx connection is embedded so every optional driver interface it
// implements (session reset, named-value checking, ...) is promoted unchanged;
// only the methods that send a statement are overridden.
type poolerDrainConn struct {
	*stdlib.Conn
}

func (c poolerDrainConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.Conn.PrepareContext(ctx, query)
	return stmt, retryIfPoolerDraining(err)
}

func (c poolerDrainConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.BeginTx(ctx, opts)
	return tx, retryIfPoolerDraining(err)
}

func (c poolerDrainConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.ExecContext(ctx, query, args)
	return result, retryIfPoolerDraining(err)
}

func (c poolerDrainConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.QueryContext(ctx, query, args)
	return rows, retryIfPoolerDraining(err)
}

func (c poolerDrainConn) Ping(ctx context.Context) error {
	return retryIfPoolerDraining(c.Conn.Ping(ctx))
}

// retryIfPoolerDraining marks a draining pooler's refusal as
// driver.ErrBadConn and passes every other error through unchanged.
func retryIfPoolerDraining(err error) error {
	if isPoolerDraining(err) {
		return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
	}
	return err
}

// isPoolerDraining reports whether err is PgBouncer refusing a client because
// it is shutting down. Matched exactly: the same SQLSTATE carries every other
// PgBouncer refusal too, and most of those are not safe to retry.
func isPoolerDraining(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Severity == "FATAL" &&
		pgErr.Code == "08P01" &&
		pgErr.Message == "server shutting down"
}
