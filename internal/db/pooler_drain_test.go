// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/stdlib"
)

// fakePooler speaks just enough of the Postgres wire protocol to stand in for
// a PgBouncer pod being rolled: connections opened before drain() serve
// normally until then, and afterwards refuse their next statement exactly as
// PgBouncer does in SHUTDOWN WAIT_FOR_SERVERS — "FATAL: server shutting down"
// (08P01), then the connection closes. Connections opened after drain() reach
// the other replica and serve normally.
type fakePooler struct {
	addr     string
	accepted atomic.Int32

	mu       sync.Mutex
	draining map[int32]bool // connection number -> drains on its next statement
}

func startFakePooler(t *testing.T) *fakePooler {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	p := &fakePooler{addr: ln.Addr().String(), draining: map[int32]bool{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(conn, p.accepted.Add(1))
		}
	}()
	return p
}

// drain makes every connection accepted so far a connection to the
// replica that is shutting down.
func (p *fakePooler) drain() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for n := int32(1); n <= p.accepted.Load(); n++ {
		p.draining[n] = true
	}
}

func (p *fakePooler) isDraining(n int32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.draining[n]
}

func (p *fakePooler) dsn() string {
	host, port, _ := net.SplitHostPort(p.addr)
	return fmt.Sprintf("postgres://app:app@%s:%s/app?sslmode=disable&default_query_exec_mode=simple_protocol", host, port)
}

func (p *fakePooler) serve(conn net.Conn, n int32) {
	defer conn.Close()
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
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
		if strings.HasPrefix(query.String, "-- ping") {
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
// so they are run again on a connection to the replica that is serving.
//
// The prod defect: "registry-meter: a repository could not be read ...
// beginning the manifest upsert: FATAL: server shutting down (SQLSTATE
// 08P01)", and the same refusal under every other pooled client, on every
// rollout that rolled PgBouncer.
func TestStatementRefusedByDrainingPoolerRunsOnAnotherConnection(t *testing.T) {
	pooler := startFakePooler(t)
	pool, err := openPostgres(pooler.dsn())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var n int
	for _, statement := range []struct {
		name string
		run  func() error
	}{
		{"query", func() error { return pool.QueryRowContext(ctx, "select 1").Scan(&n) }},
		{"exec", func() error { _, err := pool.ExecContext(ctx, "select 1"); return err }},
	} {
		// A pooled connection in steady use. Used twice because database/sql
		// resets a connection before reusing it, and pgx pings during that
		// reset when the connection has not been reset in the last second —
		// which includes its first reuse. A ping would meet the refusal
		// first and pgx already reports that as a bad connection; a
		// connection in steady use is not pinged, which is how hot worker
		// loops met the refusal in prod.
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

// When every connection keeps being refused — both replicas draining — the
// caller still learns why: the error is a bad connection AND PgBouncer's own
// message.
func TestDrainingPoolerErrorKeepsItsMessageWhenRetriesRunOut(t *testing.T) {
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
func TestOtherErrorsAreNotRetried(t *testing.T) {
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
// implements: database/sql chooses its code paths by asserting them, so a
// lost one would silently change how every statement runs.
func TestPoolerDrainConnKeepsThePgxConnInterfaces(t *testing.T) {
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
