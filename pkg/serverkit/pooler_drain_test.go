package serverkit

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/stdlib"
)

// pooler_drain_test.go — OpenDB's pool rides through a pooler's rollout.

// fakePooler speaks just enough of the Postgres wire protocol to stand in for
// a PgBouncer pod being rolled: connections opened before drain() serve
// normally until then, and afterwards refuse their next statement exactly as
// PgBouncer does in SHUTDOWN WAIT_FOR_SERVERS — "FATAL: server shutting down"
// (08P01), then the connection closes. Connections opened after drain() reach
// the other replica and serve normally. With TLS it accepts SSLRequest, as
// PgBouncer with client_tls_sslmode does.
type fakePooler struct {
	addr     string
	tls      *tls.Config
	accepted atomic.Int32

	mu       sync.Mutex
	draining map[int32]bool     // connection number -> drains on its next statement
	conns    map[int32]net.Conn // the raw TCP connections, for dropAll
}

func startFakePooler(t *testing.T, tlsConfig *tls.Config) *fakePooler {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	p := &fakePooler{addr: ln.Addr().String(), tls: tlsConfig, draining: map[int32]bool{}, conns: map[int32]net.Conn{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n := p.accepted.Add(1)
			p.mu.Lock()
			p.conns[n] = conn
			p.mu.Unlock()
			go p.serve(conn, n)
		}
	}()
	return p
}

// drain makes every connection accepted so far a connection to the replica
// that is shutting down.
func (p *fakePooler) drain() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for n := int32(1); n <= p.accepted.Load(); n++ {
		p.draining[n] = true
	}
}

// dropAll resets every client connection, as a pooler closing idle clients
// (or a pooler pod going away) does.
func (p *fakePooler) dropAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0) // RST, not FIN: the client's next write fails
		}
		_ = c.Close()
	}
}

func (p *fakePooler) isDraining(n int32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.draining[n]
}

func (p *fakePooler) dsn() string {
	host, port, _ := net.SplitHostPort(p.addr)
	mode := "disable"
	if p.tls != nil {
		mode = "require"
	}
	return fmt.Sprintf("postgres://app:app@%s:%s/app?sslmode=%s&default_query_exec_mode=simple_protocol", host, port, mode)
}

func (p *fakePooler) serve(conn net.Conn, n int32) {
	defer func() { _ = conn.Close() }()
	var rw net.Conn = conn
	backend := pgproto3.NewBackend(rw, rw)
	startup, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := startup.(*pgproto3.SSLRequest); ok {
		if p.tls == nil {
			_, _ = conn.Write([]byte{'N'})
			return
		}
		if _, err := conn.Write([]byte{'S'}); err != nil {
			return
		}
		tlsConn := tls.Server(conn, p.tls)
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		rw = tlsConn
		backend = pgproto3.NewBackend(rw, rw)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			return
		}
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	for name, value := range map[string]string{
		"server_version": "16.0", "client_encoding": "UTF8", "standard_conforming_strings": "on",
		"DateStyle": "ISO, MDY", "integer_datetimes": "on",
	} {
		backend.Send(&pgproto3.ParameterStatus{Name: name, Value: value})
	}
	backend.Send(&pgproto3.BackendKeyData{ProcessID: uint32(n), SecretKey: []byte{0, 0, 0, 1}})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		query, ok := msg.(*pgproto3.Query)
		if !ok {
			return // Terminate, or anything this fake does not speak
		}
		if p.isDraining(n) {
			backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "08P01", Message: "server shutting down"})
			_ = backend.Flush()
			return
		}
		if strings.HasPrefix(query.String, "-- ping") || strings.TrimSpace(query.String) == ";" {
			backend.Send(&pgproto3.EmptyQueryResponse{})
		} else {
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{
				Name: []byte("n"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1, Format: 0,
			}}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		}
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if backend.Flush() != nil {
			return
		}
	}
}

// A PgBouncer rollout must not fail the statements that happen to land on
// pooled connections to the replica being drained: PgBouncer never ran them,
// so they run again on a connection to the replica that is serving.
//
// The prod defect: "a repository could not be read ... beginning the manifest
// upsert: FATAL: server shutting down (SQLSTATE 08P01)", and the same refusal
// under every other pooled client, on every rollout that rolled PgBouncer.
func TestOpenDB_StatementRefusedByADrainingPoolerRunsOnAnotherConnection(t *testing.T) {
	t.Parallel()
	pooler := startFakePooler(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := OpenDB(ctx, "pgx", pooler.dsn(), DBPoolTuning{MaxOpenConns: 4, MaxIdleConns: 4},
		ConnectPolicy{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	var n int
	for _, statement := range []struct {
		name string
		run  func() error
	}{
		{"query", func() error { return pool.QueryRowContext(ctx, "select 1").Scan(&n) }},
		{"exec", func() error { _, err := pool.ExecContext(ctx, "select 1"); return err }},
		{"begin", func() error {
			tx, err := pool.BeginTx(ctx, nil)
			if err == nil {
				err = tx.Rollback()
			}
			return err
		}},
	} {
		// A pooled connection in steady use. Used twice because database/sql
		// resets a connection before reusing it, and pgx pings during that
		// reset when the connection has not been reset in the last second —
		// which includes its first reuse. A ping would meet the refusal first
		// and pgx already reports that as a bad connection; a connection in
		// steady use is not pinged, which is how hot loops met it in prod.
		for range 2 {
			if err := pool.QueryRowContext(ctx, "select 1").Scan(&n); err != nil {
				t.Fatalf("warm-up query: %v", err)
			}
		}
		dialed := pooler.accepted.Load()

		pooler.drain()

		if err := statement.run(); err != nil {
			t.Fatalf("%s on a draining pooler failed instead of running on another connection: %v", statement.name, err)
		}
		if pooler.accepted.Load() == dialed {
			t.Fatalf("%s: the statement must have run on a newly dialed connection", statement.name)
		}
	}
}

// selfSignedTLS is a server certificate for the fake pooler; the client runs
// sslmode=require, which encrypts without verifying.
func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

// The prod line on every shutdown behind a pooler: "Error closing database:
// tls: failed to send closeNotify alert (but connection was closed anyway):
// ... write: broken pipe". The pooler had already dropped the connection; the
// close succeeded, and must not be reported — OnShutdown logs any error it
// returns at ERROR.
func TestOpenDB_ClosingConnectionsThePoolerDroppedIsNotAnError(t *testing.T) {
	t.Parallel()
	pooler := startFakePooler(t, selfSignedTLS(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := OpenDB(ctx, "pgx", pooler.dsn(), DBPoolTuning{MaxOpenConns: 2, MaxIdleConns: 2},
		ConnectPolicy{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, "select 1").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}

	pooler.dropAll()
	time.Sleep(200 * time.Millisecond) // let the resets land

	if err := pool.Close(); err != nil {
		t.Fatalf("closing a pool whose connections the pooler already dropped = %v, want nil", err)
	}
}

// When every connection keeps being refused — both replicas draining — the
// caller still learns why: the error is a bad connection AND PgBouncer's own
// message.
func TestPoolerDrain_ErrorKeepsItsMessageWhenRetriesRunOut(t *testing.T) {
	t.Parallel()
	refusal := &pgconn.PgError{Severity: "FATAL", Code: "08P01", Message: "server shutting down"}
	err := retryIfPoolerDraining(refusal)
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatal("a draining pooler's refusal must tell database/sql to retry on another connection")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Message != "server shutting down" {
		t.Fatalf("the refusal must stay readable when retries run out: %v", err)
	}
}

// Only the drain refusal is retried. PgBouncer uses the same SQLSTATE for
// refusals that are not safe or not useful to repeat, and a Postgres error
// means the statement ran.
func TestPoolerDrain_OtherErrorsAreNotRetried(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		&pgconn.PgError{Severity: "FATAL", Code: "08P01", Message: "query_wait_timeout"},
		&pgconn.PgError{Severity: "FATAL", Code: "08P01", Message: "server conn crashed?"},
		&pgconn.PgError{Severity: "ERROR", Code: "08P01", Message: "server shutting down"},
		&pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key value violates unique constraint"},
		errors.New("server shutting down"),
		nil,
	} {
		if got := retryIfPoolerDraining(err); got != err { //nolint:errorlint // identity is the assertion
			t.Errorf("%v was rewritten to %v", err, got)
		}
	}
}

// The wrapper must not hide any optional interface the pgx connection
// implements: database/sql chooses its code paths by asserting them, so a lost
// one would silently change how every statement runs.
func TestPoolerDrainConn_KeepsThePgxConnInterfaces(t *testing.T) {
	t.Parallel()
	var pgx driver.Conn = &stdlib.Conn{}
	wrapped := driver.Conn(poolerDrainConn{Conn: &stdlib.Conn{}})
	checks := map[string]func(driver.Conn) bool{
		"ConnBeginTx":        func(c driver.Conn) bool { _, ok := c.(driver.ConnBeginTx); return ok },
		"ConnPrepareContext": func(c driver.Conn) bool { _, ok := c.(driver.ConnPrepareContext); return ok },
		"ExecerContext":      func(c driver.Conn) bool { _, ok := c.(driver.ExecerContext); return ok },
		"QueryerContext":     func(c driver.Conn) bool { _, ok := c.(driver.QueryerContext); return ok },
		"Pinger":             func(c driver.Conn) bool { _, ok := c.(driver.Pinger); return ok },
		"SessionResetter":    func(c driver.Conn) bool { _, ok := c.(driver.SessionResetter); return ok },
		"Validator":          func(c driver.Conn) bool { _, ok := c.(driver.Validator); return ok },
		"NamedValueChecker":  func(c driver.Conn) bool { _, ok := c.(driver.NamedValueChecker); return ok },
	}
	for name, implements := range checks {
		if implements(pgx) != implements(wrapped) {
			t.Errorf("%s: pgx conn %v, wrapper %v", name, implements(pgx), implements(wrapped))
		}
	}
}

// closeNotifyBrokenPipe is the prod error, as crypto/tls builds it when the
// pooler has already dropped the connection whose close_notify it writes.
func closeNotifyBrokenPipe() error {
	writeErr := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	return fmt.Errorf("tls: failed to send closeNotify alert (but connection was closed anyway): %w", writeErr)
}

func TestPeerClosedErrors_AreNotCloseFailures(t *testing.T) {
	t.Parallel()
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

func TestPeerClosedErrors_RealCloseErrorsStayErrors(t *testing.T) {
	t.Parallel()
	real := errors.New("unexpected message while closing")
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
