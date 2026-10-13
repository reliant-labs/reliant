// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// The prod line, as crypto/tls builds it when PgBouncer has already dropped
// the connection whose close_notify it is writing.
func closeNotifyBrokenPipe() error {
	writeErr := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	return fmt.Errorf("tls: failed to send closeNotify alert (but connection was closed anyway): %w", writeErr)
}

func TestPeerClosedErrorsAreNotCloseFailures(t *testing.T) {
	for name, err := range map[string]error{
		"close_notify broken pipe": closeNotifyBrokenPipe(),
		"connection reset":         &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.ECONNRESET)},
		"already closed":           fmt.Errorf("close: %w", net.ErrClosed),
		"joined, all peer-closed":  errors.Join(closeNotifyBrokenPipe(), net.ErrClosed),
	} {
		if got := withoutPeerClosedErrors(err); got != nil {
			t.Errorf("%s: %v must not be reported as a close failure, got %v", name, err, got)
		}
	}
}

func TestRealCloseErrorsStayErrors(t *testing.T) {
	real := errors.New("pq: unexpected message while closing")
	if got := withoutPeerClosedErrors(real); got != real { //nolint:errorlint // identity is the assertion
		t.Fatalf("a real close error must be returned unchanged, got %v", got)
	}

	joined := errors.Join(closeNotifyBrokenPipe(), real)
	got := withoutPeerClosedErrors(joined)
	if !errors.Is(got, real) {
		t.Fatalf("the real member of a joined error must survive, got %v", got)
	}
	if errors.Is(got, syscall.EPIPE) {
		t.Fatalf("the peer-closed member must be dropped, got %v", got)
	}

	if withoutPeerClosedErrors(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

// closeFailsConnector hands out connections whose Close fails with err, the
// way a pgx connection's does when its TLS close_notify cannot be written.
type closeFailsConnector struct{ err error }

func (c closeFailsConnector) Connect(context.Context) (driver.Conn, error) {
	return closeFailsConn(c), nil
}
func (c closeFailsConnector) Driver() driver.Driver { return nil }

type closeFailsConn struct{ err error }

func (c closeFailsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c closeFailsConn) Close() error                        { return c.err }
func (c closeFailsConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c closeFailsConn) Ping(context.Context) error          { return nil }

func repoWithOneConnection(t *testing.T, closeErr error) *Repo {
	t.Helper()
	pool := sql.OpenDB(closeFailsConnector{err: closeErr})
	if err := pool.Ping(); err != nil { // opens the connection Close will close
		t.Fatal(err)
	}
	return &Repo{DB: &WrappedDBTX{db: pool}}
}

// The prod line on api-server and daemon-gateway shutdowns: "Error closing
// database: tls: failed to send closeNotify alert (but connection was closed
// anyway): ... write: broken pipe". PgBouncer had already dropped the
// connection; the close succeeded.
func TestRepoCloseIgnoresConnectionsThePoolerAlreadyDropped(t *testing.T) {
	if err := repoWithOneConnection(t, closeNotifyBrokenPipe()).Close(); err != nil {
		t.Fatalf("Repo.Close reported a connection the server end had already closed: %v", err)
	}
}

func TestRepoCloseReportsRealCloseErrors(t *testing.T) {
	real := errors.New("pq: unexpected message while closing")
	if err := repoWithOneConnection(t, real).Close(); !errors.Is(err, real) {
		t.Fatalf("Repo.Close must report a real close failure, got %v", err)
	}
}
