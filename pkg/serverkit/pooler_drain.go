package serverkit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// pooler_drain.go — a database pool that rides through a pooler's rollout.
//
// Every forge deployment reaches Postgres through a pooler (PgBouncer, the
// Cloud SQL proxy), and the pooler is rolled like everything else. Two things
// went wrong on every such rollout, in every client:
//
//  1. Statements failed although the other pooler replica was serving. On
//     SIGTERM PgBouncer waits for its endpoint to leave the Service and then
//     shuts down "safely" (SHUTDOWN WAIT_FOR_SERVERS): transactions in flight
//     finish, and any client that needs a server connection is disconnected
//     with "FATAL: server shutting down" (SQLSTATE 08P01). A database/sql pool
//     holds idle connections to that pod, so the next statement on each one
//     failed — "beginning the manifest upsert: FATAL: server shutting down".
//  2. Closing the pool at shutdown reported an error for every connection the
//     pooler had already dropped: "tls: failed to send closeNotify alert (but
//     connection was closed anyway): write: broken pipe", at ERROR, on every
//     deploy, about a close that succeeded.
//
// OpenDB therefore opens the "pgx" driver through poolerDrainConnector: the
// same pgx connections sql.Open("pgx", dsn) makes, which report a draining
// pooler's refusal as driver.ErrBadConn — so database/sql runs the statement
// again on another connection — and whose Close does not report a connection
// the server end had already torn down.

// openPool is sql.Open, except that the pgx driver's connections ride through
// a draining pooler (see above), and every statement is an OpenTelemetry span
// under the caller's span (pkg/observe/dbtrace) — whatever the driver.
func openPool(driverName, dsn string) (*sql.DB, error) {
	if driverName != "pgx" && driverName != "pgx/v5" {
		return dbtrace.Open(driverName, dsn)
	}
	pgxDriver, ok := stdlib.GetDefaultDriver().(driver.DriverContext)
	if !ok {
		return nil, errors.New("the pgx driver cannot open a connector")
	}
	connector, err := pgxDriver.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return dbtrace.OpenDB(poolerDrainConnector{Connector: connector}), nil
}

// poolerDrainConnector hands database/sql poolerDrainConns.
type poolerDrainConnector struct {
	driver.Connector
}

func (c poolerDrainConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, retryIfPoolerDraining(err)
	}
	pgxConn, ok := conn.(*stdlib.Conn)
	if !ok {
		return conn, nil
	}
	return poolerDrainConn{Conn: pgxConn}, nil
}

// poolerDrainConn is a pgx connection that reports a statement a draining
// pooler refused as driver.ErrBadConn, and does not report a close of a
// connection the server end already dropped.
//
// Retrying is safe because PgBouncer sends that refusal only to a client that
// had no server connection, i.e. before forwarding the statement anywhere
// (pause_client and the janitor's wait-queue sweep in pgbouncer's objects.c /
// janitor.c): the statement never ran. database/sql retries an ErrBadConn
// twice on pooled connections and then on a freshly dialed one, which the
// Service routes to a replica that is not draining. The wrapped error keeps
// PgBouncer's message, so a retry that runs out — every replica draining —
// still says why.
//
// The pgx connection is embedded so every optional driver interface it
// implements (session reset, named-value checking, ...) is promoted unchanged;
// only the methods that send a statement, and Close, are overridden. The pool
// wraps it once more for tracing, so code that reaches the *pgx.Conn through
// sql.Conn.Raw uses PgxConn rather than asserting a concrete type.
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

// Close closes the connection. Closing a pgx connection sends Terminate and,
// over TLS, a close_notify; when the pooler already dropped the connection (it
// closes idle clients on its own schedule, and is rolled in the same deploy)
// those writes fail although the connection is closed either way — crypto/tls
// says so in its own message. That is not a close failure.
func (c poolerDrainConn) Close() error {
	return withoutPeerClosedErrors(c.Conn.Close())
}

// PgxConn returns the *pgx.Conn under a driver connection handed to
// sql.Conn.Raw's callback, through every wrapper OpenDB puts around it (the
// tracing driver, the pooler-drain connection).
func PgxConn(driverConn any) (*pgx.Conn, bool) {
	for driverConn != nil {
		if c, ok := driverConn.(interface{ Conn() *pgx.Conn }); ok {
			return c.Conn(), true
		}
		raw, ok := driverConn.(interface{ Raw() driver.Conn })
		if !ok {
			return nil, false
		}
		driverConn = raw.Raw()
	}
	return nil, false
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

// withoutPeerClosedErrors drops, from a close error, the parts that only say
// the server end had already closed the connection. Anything else stays an
// error; a joined error keeps its other members.
func withoutPeerClosedErrors(err error) error {
	kept, _ := dropPeerClosedErrors(err)
	return kept
}

// dropPeerClosedErrors is withoutPeerClosedErrors, also reporting whether it
// dropped anything — so an error it did not touch is returned as it was, not
// rebuilt.
func dropPeerClosedErrors(err error) (kept error, dropped bool) {
	if err == nil {
		return nil, false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var members []error
		for _, member := range joined.Unwrap() {
			memberKept, memberDropped := dropPeerClosedErrors(member)
			dropped = dropped || memberDropped
			if memberKept != nil {
				members = append(members, memberKept)
			}
		}
		if !dropped {
			return err, false
		}
		return errors.Join(members...), true
	}
	if isPeerClosedError(err) {
		return nil, true
	}
	return err, false
}

// isPeerClosedError reports whether err is a write or close on a connection
// the other end had already torn down. crypto/tls wraps the failed
// close_notify write with %w, so the syscall error is reachable through it.
func isPeerClosedError(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	for _, errno := range peerClosedErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
