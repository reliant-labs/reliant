package serverkit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gateConnector dials fake connections, counting dials and closes. Dials past
// the first `allow` wait until release (or their context ends), so a test can
// hold the spare filler mid-dial — a slow database — and watch what a request
// does meanwhile.
type gateConnector struct {
	dials, closes atomic.Int64
	bad           atomic.Bool // every connection fails its session reset
	// ignoreCtx makes a held dial wait for release alone, as Driver.Open
	// does: it has no context to cancel.
	ignoreCtx bool

	mu      sync.Mutex
	allow   int64         // dials that pass at once; <0 = all
	gate    chan struct{} // closed by release
	blocked chan struct{} // one send per dial that waits at the gate
}

func newGateConnector() *gateConnector {
	return &gateConnector{allow: -1, gate: make(chan struct{}), blocked: make(chan struct{}, 64)}
}

// hold makes every dial after the next n wait at the gate.
func (g *gateConnector) hold(n int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allow = g.dials.Load() + n
}

func (g *gateConnector) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allow = -1
	close(g.gate)
}

func (g *gateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	n := g.dials.Add(1)
	g.mu.Lock()
	wait := g.allow >= 0 && n > g.allow
	gate := g.gate
	g.mu.Unlock()
	if wait {
		g.blocked <- struct{}{}
		done := ctx.Done()
		if g.ignoreCtx {
			done = nil
		}
		select {
		case <-gate:
		case <-done:
			return nil, ctx.Err()
		}
	}
	return &gateConn{g: g}, nil
}

func (g *gateConnector) Driver() driver.Driver { return gateDriver{g} }

type gateDriver struct{ g *gateConnector }

func (d gateDriver) Open(string) (driver.Conn, error) { return d.g.Connect(context.Background()) }

type gateConn struct{ g *gateConnector }

func (c *gateConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *gateConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *gateConn) Close() error                        { c.g.closes.Add(1); return nil }

func (c *gateConn) ResetSession(context.Context) error {
	if c.g.bad.Load() {
		return driver.ErrBadConn
	}
	return nil
}

// openSpares opens a pool over g that keeps want spares.
func openSpares(t *testing.T, g *gateConnector, want int, maxOpen int) (*sql.DB, *spareConnector) {
	t.Helper()
	spares := newSpareConnector(g, want, time.Hour)
	db := sql.OpenDB(spares)
	db.SetMaxOpenConns(maxOpen)
	spares.db.Store(db)
	t.Cleanup(func() {
		g.mu.Lock()
		open := g.allow >= 0
		g.mu.Unlock()
		if open {
			g.release()
		}
		_ = db.Close()
	})
	return db, spares
}

func (c *spareConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.spares)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The filler never takes a connection from the pool: while it is stuck
// dialling a slow database, a request still gets the pool's idle connection
// at once, even with the pool one connection short of MaxOpenConns.
func TestSpareConns_RequestNeverWaitsOnTheFiller(t *testing.T) {
	g := newGateConnector()
	g.hold(1) // the Ping's dial passes; the filler's first spare dial sticks
	db, _ := openSpares(t, g, 3, 2)
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	<-g.blocked // the filler is mid-dial

	if st := db.Stats(); st.InUse != 0 || st.Idle != 1 {
		t.Fatalf("while the filler dials: in use %d, idle %d; want 0 and 1", st.InUse, st.Idle)
	}
	start := time.Now()
	conn, err := db.Conn(shortCtx(t))
	if err != nil {
		t.Fatalf("a request waited on the filler's dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the idle connection took %s to hand out", d)
	}
}

// When the pool needs a new connection it gets a spare, not a dial: with
// every dial stuck, requests past the idle connection are still served.
func TestSpareConns_NewConnectionIsASpare(t *testing.T) {
	g := newGateConnector()
	db, spares := openSpares(t, g, 2, 0)
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two spares", func() bool { return spares.count() == 2 })
	g.hold(0)
	dials := g.dials.Load()

	ctx := shortCtx(t)
	for i := range 3 { // the idle connection, then both spares
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
	}
	// The refill the takes kicked off is stuck at the gate; nothing else dialled.
	waitFor(t, "the refill dial", func() bool { return g.dials.Load() > dials })
	if got := g.dials.Load() - dials; got != 1 {
		t.Fatalf("serving three connections dialled %d times, want only the filler's 1", got)
	}
}

// With MaxOpenConns set, spares fill only the room the pool's open
// connections leave under it.
func TestSpareConns_StayUnderMaxOpenConns(t *testing.T) {
	g := newGateConnector()
	db, spares := openSpares(t, g, 2, 2)
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "one spare", func() bool { return spares.count() == 1 })
	if spares.short() {
		t.Fatal("one open connection and one spare fill MaxOpenConns 2, but the filler wants another")
	}
}

// A spare the server dropped while it waited fails the same session reset an
// idle pooled connection would, and is closed instead of handed out.
func TestSpareConns_DropsDeadSpares(t *testing.T) {
	g := newGateConnector()
	db, spares := openSpares(t, g, 2, 0)
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two spares", func() bool { return spares.count() == 2 })
	idle, err := db.Conn(shortCtx(t)) // hold the pool's idle connection
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()

	g.bad.Store(true)
	closes := g.closes.Load()
	conn, err := spares.Connect(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := g.closes.Load() - closes; got < 2 {
		t.Fatalf("closed %d dead spares, want both", got)
	}
	_ = conn.Close()
}

// A spare older than its age is closed rather than handed out.
func TestSpareConns_ExpireByAge(t *testing.T) {
	g := newGateConnector()
	c := newSpareConnector(g, 1, time.Millisecond)
	conn, _ := g.Connect(context.Background())
	c.put(conn)
	time.Sleep(5 * time.Millisecond)
	if c.take() != nil {
		t.Fatal("an expired spare was handed out")
	}
	if g.closes.Load() != 1 {
		t.Fatal("the expired spare was not closed")
	}
}

// Closing the pool stops the filler — even one stuck mid-dial — and closes
// every spare: no connection outlives the pool.
func TestSpareConns_CloseStopsTheFiller(t *testing.T) {
	g := newGateConnector()
	spares := newSpareConnector(g, 3, time.Hour)
	db := sql.OpenDB(spares)
	spares.db.Store(db)
	g.hold(3) // the Ping and two spares; the third spare's dial sticks
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	<-g.blocked

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the pool waited on the filler's dial")
	}
	<-spares.done
	if dials, closes := g.dials.Load(), g.closes.Load(); closes != dials-1 {
		t.Fatalf("%d dials (one cancelled), %d closes: a connection outlived the pool", dials, closes)
	}
}

// A connector that ignores its context cannot have its dial cancelled, so
// closing the pool does not wait for it; the connection that dial eventually
// returns is closed, not leaked.
func TestSpareConns_CloseDoesNotWaitOnAnUncancellableDial(t *testing.T) {
	g := newGateConnector()
	g.ignoreCtx = true
	spares := newSpareConnector(g, 1, time.Hour)
	db := sql.OpenDB(spares)
	spares.db.Store(db)
	g.hold(1) // the Ping passes; the spare's dial hangs
	if err := db.PingContext(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	<-g.blocked

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the pool waited on a dial it cannot cancel")
	}
	g.release() // the hung dial finally returns a connection
	waitFor(t, "the late spare to be closed", func() bool { return g.closes.Load() == g.dials.Load() })
}

func TestSpareConnsFor_CapsAtMaxOpenConns(t *testing.T) {
	for _, tc := range []struct {
		tuning DBPoolTuning
		want   int
	}{
		{DBPoolTuning{}, 0},
		{DBPoolTuning{SpareConns: 4}, 4},
		{DBPoolTuning{SpareConns: 4, MaxOpenConns: 3}, 3},
		{DBPoolTuning{SpareConns: -1}, 0},
	} {
		if got := spareConnsFor(tc.tuning); got != tc.want {
			t.Errorf("spareConnsFor(%+v) = %d, want %d", tc.tuning, got, tc.want)
		}
	}
}

func TestSpareMaxAgeFor(t *testing.T) {
	for _, tc := range []struct {
		tuning DBPoolTuning
		want   time.Duration
	}{
		{DBPoolTuning{}, spareMaxAgeDefault},
		{DBPoolTuning{ConnMaxIdleTime: time.Minute}, time.Minute},
		{DBPoolTuning{ConnMaxLifetime: 4 * time.Minute}, 2 * time.Minute},
		{DBPoolTuning{ConnMaxIdleTime: time.Hour, ConnMaxLifetime: time.Hour}, spareMaxAgeDefault},
	} {
		if got := spareMaxAgeFor(tc.tuning); got != tc.want {
			t.Errorf("spareMaxAgeFor(%+v) = %s, want %s", tc.tuning, got, tc.want)
		}
	}
}

var openDBSpares = newGateConnector()

func init() { sql.Register("serverkit-spares-test", gateDriver{openDBSpares}) }

// OpenDB keeps SpareConns dialled once the database answers.
func TestOpenDB_KeepsSpareConns(t *testing.T) {
	dials, closes := openDBSpares.dials.Load(), openDBSpares.closes.Load()
	db, err := OpenDB(context.Background(), "serverkit-spares-test", "",
		DBPoolTuning{MaxOpenConns: 8, SpareConns: 3}, ConnectPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ping and three spares", func() bool { return openDBSpares.dials.Load()-dials == 4 })
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Close does not wait for the filler, which may still be between a dial
	// and handing its connection over; that connection is closed when it
	// does. Waiting for all four also keeps a repeated run's counts clean.
	waitFor(t, "every connection closed", func() bool { return openDBSpares.closes.Load()-closes == 4 })
	if got := openDBSpares.dials.Load() - dials; got != 4 {
		t.Fatalf("dials = %d, want 4", got)
	}
}
