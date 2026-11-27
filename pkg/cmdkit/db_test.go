package cmdkit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDriver is a database/sql driver whose connections are controlled by
// the DSN: "ok" connects, "refuse" fails to connect, "hang" blocks until the
// connect context ends.
type fakeDriver struct {
	opens atomic.Int32
}

func (d *fakeDriver) Open(dsn string) (driver.Conn, error) {
	return d.open(context.Background(), dsn)
}

// OpenConnector makes the driver context-aware, so the ping's deadline
// reaches the connect the way it does with pgx.
func (d *fakeDriver) OpenConnector(dsn string) (driver.Connector, error) {
	return fakeConnector{d: d, dsn: dsn}, nil
}

type fakeConnector struct {
	d   *fakeDriver
	dsn string
}

func (c fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.d.open(ctx, c.dsn)
}
func (c fakeConnector) Driver() driver.Driver { return c.d }

func (d *fakeDriver) open(ctx context.Context, dsn string) (driver.Conn, error) {
	d.opens.Add(1)
	switch dsn {
	case "refuse":
		return nil, errors.New("connection refused")
	case "hang":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

var testDriver = &fakeDriver{}

func init() { sql.Register("cmdkitfake", testDriver) }

func TestOpenDB(t *testing.T) {
	t.Run("opens, tunes the pool and pings", func(t *testing.T) {
		before := testDriver.opens.Load()
		db, err := OpenDB(context.Background(), DBOptions{
			Driver:          "cmdkitfake",
			DSN:             "ok",
			MaxOpenConns:    7,
			MaxIdleConns:    3,
			ConnMaxIdleTime: time.Minute,
			ConnMaxLifetime: time.Hour,
		})
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		defer db.Close()
		if got := db.Stats().MaxOpenConnections; got != 7 {
			t.Errorf("MaxOpenConnections = %d, want 7", got)
		}
		if got := testDriver.opens.Load() - before; got < 1 {
			t.Errorf("the open-time ping never connected (opens +%d)", got)
		}
	})

	t.Run("SkipPing returns without connecting", func(t *testing.T) {
		before := testDriver.opens.Load()
		// "refuse" would fail the ping; SkipPing must not even try.
		db, err := OpenDB(context.Background(), DBOptions{Driver: "cmdkitfake", DSN: "refuse", SkipPing: true})
		if err != nil {
			t.Fatalf("OpenDB with SkipPing: %v", err)
		}
		defer db.Close()
		if got := testDriver.opens.Load() - before; got != 0 {
			t.Errorf("SkipPing connected %d times, want 0", got)
		}
	})

	t.Run("unknown driver names the driver", func(t *testing.T) {
		_, err := OpenDB(context.Background(), DBOptions{Driver: "no-such-driver", DSN: "x"})
		if err == nil || !strings.Contains(err.Error(), "open db (no-such-driver)") {
			t.Errorf("err = %v, want it to wrap an 'open db (no-such-driver)' failure", err)
		}
	})

	t.Run("ping failure is wrapped and the pool closed", func(t *testing.T) {
		_, err := OpenDB(context.Background(), DBOptions{Driver: "cmdkitfake", DSN: "refuse"})
		if err == nil {
			t.Fatal("OpenDB succeeded against a refusing database")
		}
		if !strings.Contains(err.Error(), "ping db (cmdkitfake)") || !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("err = %v, want 'ping db (cmdkitfake): ...connection refused'", err)
		}
	})

	t.Run("PingTimeout bounds an unreachable database", func(t *testing.T) {
		began := time.Now()
		_, err := OpenDB(context.Background(), DBOptions{
			Driver:      "cmdkitfake",
			DSN:         "hang",
			PingTimeout: 20 * time.Millisecond,
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(began); elapsed > 10*time.Second {
			t.Errorf("OpenDB took %v; the ping was not bounded by PingTimeout", elapsed)
		}
	})

	t.Run("a cancelled parent context fails the ping", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := OpenDB(ctx, DBOptions{Driver: "cmdkitfake", DSN: "ok"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}
