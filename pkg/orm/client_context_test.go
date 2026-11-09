package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientWithDBContextExpired(t *testing.T) {
	probe, db := newClientContextProbe(t)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceledClientContext(), context.Canceled},
		{"expired", expiredClientContext(t), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClientWithDBContext(tc.ctx, db, "postgres")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if probe.pings.Load() != 0 {
		t.Fatalf("expired request reached driver: %d", probe.pings.Load())
	}
	if _, err := NewClientWithDBContext(nil, db, "postgres"); !errors.Is(err, ErrNilContext) {
		t.Fatalf("nil context: %v", err)
	}
	if _, err := NewClientWithDBContext(context.Background(), db, "sqlite"); !errors.Is(err, ErrInvalidDialect) {
		t.Fatalf("invalid dialect: %v", err)
	}
	close(probe.release)
	assertClientContextRecovery(t, db)
}

func TestNewClientWithDBContextCancelsBlockedProbe(t *testing.T) {
	probe, db := newClientContextProbe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := NewClientWithDBContext(ctx, db, "postgres"); done <- err }()
	select {
	case <-probe.entered:
	case <-time.After(time.Second):
		t.Fatal("driver ping was not reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked ping returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked ping ignored cancellation")
	}
	close(probe.release)
	assertClientContextRecovery(t, db)
}

func TestNewClientWithDBContextCancelsPoolWait(t *testing.T) {
	probe, db := newClientContextProbe(t)
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	// No connection is available and it remains held until the constructor has
	// returned. A background Ping cannot finish; this must use the request ctx.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := NewClientWithDBContext(ctx, db, "postgres"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pool wait returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool wait ignored cancellation")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	assertClientContextRecovery(t, db)
}

func TestNewClientWithDBKeepsBackgroundPing(t *testing.T) {
	probe, db := newClientContextProbe(t)
	done := make(chan error, 1)
	go func() { _, err := NewClientWithDB(db, "postgres"); done <- err }()
	select {
	case ctx := <-probe.entered:
		if ctx.Done() != nil {
			t.Fatal("legacy constructor changed its background context")
		}
	case <-time.After(time.Second):
		t.Fatal("legacy constructor skipped ping")
	}
	close(probe.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy constructor did not finish")
	}
}

func assertClientContextRecovery(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c, err := NewClientWithDBContext(ctx, db, "postgres")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if c.DB() != db || c.Bun() == nil || c.Dialect().Name() != "postgres" {
		t.Fatal("wrapper did not retain caller pool/dialect")
	}
	// The initialization context is not retained by later operations.
	if err := c.DB().PingContext(context.Background()); err != nil {
		t.Fatalf("pool did not recover: %v", err)
	}
}
func canceledClientContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
func expiredClientContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	t.Cleanup(cancel)
	return ctx
}

type clientContextProbe struct {
	entered chan context.Context
	release chan struct{}
	pings   atomic.Int32
}

func newClientContextProbe(t *testing.T) (*clientContextProbe, *sql.DB) {
	t.Helper()
	p := &clientContextProbe{entered: make(chan context.Context, 8), release: make(chan struct{})}
	db := sql.OpenDB(p)
	t.Cleanup(func() {
		// A broken constructor must not strand a probe goroutine during cleanup.
		select {
		case <-p.release:
		default:
			close(p.release)
		}
		_ = db.Close()
	})
	return p, db
}
func (p *clientContextProbe) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &clientContextConn{p}, nil
}
func (*clientContextProbe) Driver() driver.Driver { return clientContextDriver{} }

type clientContextDriver struct{}

func (clientContextDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type clientContextConn struct{ probe *clientContextProbe }

func (*clientContextConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("no query expected")
}
func (*clientContextConn) Begin() (driver.Tx, error) {
	return nil, errors.New("no transaction expected")
}
func (*clientContextConn) Close() error { return nil }
func (c *clientContextConn) Ping(ctx context.Context) error {
	c.probe.pings.Add(1)
	select {
	case c.probe.entered <- ctx:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.probe.release:
		return nil
	}
}
