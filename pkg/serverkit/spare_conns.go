package serverkit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// spare_conns.go — connections dialled ahead of need (DBPoolTuning.SpareConns).
//
// database/sql dials a connection when a caller finds none idle, and that
// caller waits out the dial: TCP, TLS and auth, through a pooler — measured
// at 85-336ms per connection on a request that fanned out ten queries at
// once. Connections leave the idle set without anyone asking for fewer:
// ConnMaxLifetime retires them, a burst past MaxIdleConns closes the excess,
// and a fresh process starts with none.
//
// database/sql has no minimum-idle setting, and it cannot be given
// connections from outside: the only way to put one in its idle set is to
// check connections out and return them. A warmer that does that holds the
// pool's idle connections while it dials more — a request arriving meanwhile
// finds none, and with MaxOpenConns reached waits on the warmer. So the
// spares live one layer down instead, in the connector: spareConnector dials
// them in the background, and when database/sql next needs a new connection
// it gets a spare at once rather than waiting for a dial. The pool's own
// connections are never touched, so a request never waits on the warmer;
// at worst — no spare left — it dials, exactly as without one.

// spareCheckInterval is how often the filler drops spares past their age and
// retries after a failed dial.
var spareCheckInterval = 10 * time.Second

// spareDialTimeout bounds one background dial.
const spareDialTimeout = 10 * time.Second

// spareMaxAgeDefault bounds how long a spare waits unused, below the idle
// timeouts of the load balancers and poolers between a pod and its database.
const spareMaxAgeDefault = 5 * time.Minute

// spareConnsFor is t.SpareConns capped at MaxOpenConns: database/sql never
// asks for a connection past that, so a spare beyond it could not be used.
func spareConnsFor(t DBPoolTuning) int {
	n := t.SpareConns
	if t.MaxOpenConns > 0 {
		n = min(n, t.MaxOpenConns)
	}
	return max(n, 0)
}

// spareMaxAgeFor is how long a spare may wait unused before it is redialled.
// database/sql starts a connection's ConnMaxLifetime clock when it receives
// it, so the time a spare spent waiting is not counted there; capping the
// wait at half the lifetime keeps a connection's real age under 1.5x it.
func spareMaxAgeFor(t DBPoolTuning) time.Duration {
	age := spareMaxAgeDefault
	if t.ConnMaxIdleTime > 0 {
		age = min(age, t.ConnMaxIdleTime)
	}
	if t.ConnMaxLifetime > 0 {
		age = min(age, t.ConnMaxLifetime/2)
	}
	return age
}

// spareConnector is a driver.Connector that keeps up to want connections
// dialled ahead of need and hands one out, instead of dialling, when
// database/sql asks for a new connection.
//
// The filler starts at the first connection the database accepts, so a
// database that is not up yet is dialled only by the caller's own retry
// loop. While the pool has a MaxOpenConns it dials a spare only while the
// pool's open connections plus the spares stay under that cap, so the
// database sees at most MaxOpenConns connections from this pool, give or
// take a dial racing a request's. database/sql closes the connector when the
// pool closes (it calls io.Closer on its connector), which stops the filler
// and closes the spares, without waiting on a dial in flight.
type spareConnector struct {
	driver.Connector

	want   int
	maxAge time.Duration
	db     atomic.Pointer[sql.DB] // for the open count; set once the pool exists

	ctx    context.Context // cancelled by Close: aborts an in-flight dial
	cancel context.CancelFunc
	kick   chan struct{} // a spare was taken: refill now
	start  sync.Once
	done   chan struct{} // closed when the filler exits

	mu     sync.Mutex
	spares []spareConn // oldest first
	closed bool
}

type spareConn struct {
	conn   driver.Conn
	dialed time.Time
}

func newSpareConnector(c driver.Connector, want int, maxAge time.Duration) *spareConnector {
	ctx, cancel := context.WithCancel(context.Background())
	return &spareConnector{
		Connector: c,
		want:      want,
		maxAge:    maxAge,
		ctx:       ctx,
		cancel:    cancel,
		kick:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
}

// Connect hands out the oldest usable spare, or dials when there is none.
func (c *spareConnector) Connect(ctx context.Context) (driver.Conn, error) {
	for {
		conn := c.take()
		if conn == nil {
			break
		}
		if spareUsable(ctx, conn) {
			c.refill()
			return conn, nil
		}
		_ = conn.Close()
	}
	conn, err := c.Connector.Connect(ctx)
	if err == nil {
		c.start.Do(func() { go c.fill() })
	}
	c.refill()
	return conn, err
}

// spareUsable reports whether a spare that waited unused can still serve: the
// same check database/sql runs on an idle connection it reuses (pgx pings one
// that has been idle for more than a second), so a spare the server or a
// pooler dropped while it waited is discarded instead of failing a statement.
func spareUsable(ctx context.Context, conn driver.Conn) bool {
	if r, ok := conn.(driver.SessionResetter); ok && r.ResetSession(ctx) != nil {
		return false
	}
	if v, ok := conn.(driver.Validator); ok && !v.IsValid() {
		return false
	}
	return true
}

// take removes and returns the oldest spare that is not past its age, or nil.
func (c *spareConnector) take() driver.Conn {
	c.expire()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.spares) == 0 {
		return nil
	}
	s := c.spares[0]
	c.spares = c.spares[1:]
	return s.conn
}

func (c *spareConnector) refill() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// fill keeps the spares topped up until Close.
func (c *spareConnector) fill() {
	defer close(c.done)
	t := time.NewTicker(spareCheckInterval)
	defer t.Stop()
	for {
		c.expire()
		failed := false
		for c.short() {
			ctx, cancel := context.WithTimeout(c.ctx, spareDialTimeout)
			conn, err := c.Connector.Connect(ctx)
			cancel()
			if err != nil {
				failed = true
				break
			}
			if !c.put(conn) {
				_ = conn.Close()
				return
			}
		}
		// After a failed dial wait for the ticker, not the next take: while
		// the database is unreachable every request already dials it.
		kick := c.kick
		if failed {
			kick = nil
		}
		select {
		case <-c.ctx.Done():
			return
		case <-kick:
		case <-t.C:
		}
	}
}

// short reports whether another spare should be dialled.
func (c *spareConnector) short() bool {
	var st sql.DBStats
	if db := c.db.Load(); db != nil {
		st = db.Stats()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.spares)
	if c.closed || n >= c.want {
		return false
	}
	return st.MaxOpenConnections <= 0 || st.OpenConnections+n < st.MaxOpenConnections
}

// put stores a freshly dialled spare; false means the connector closed.
func (c *spareConnector) put(conn driver.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.spares = append(c.spares, spareConn{conn: conn, dialed: time.Now()})
	return true
}

// expire closes the spares that waited past their age.
func (c *spareConnector) expire() {
	c.mu.Lock()
	var stale []driver.Conn
	for len(c.spares) > 0 && time.Since(c.spares[0].dialed) >= c.maxAge {
		stale = append(stale, c.spares[0].conn)
		c.spares = c.spares[1:]
	}
	c.mu.Unlock()
	for _, conn := range stale {
		_ = conn.Close()
	}
}

// Close stops the filler, closes the spares, and closes the wrapped
// connector if it is closable. database/sql calls it from DB.Close.
func (c *spareConnector) Close() error {
	c.mu.Lock()
	c.closed = true
	spares := c.spares
	c.spares = nil
	c.mu.Unlock()
	// Close does not wait for the filler. Cancelling ends a dial in flight
	// when the connector honours its context; one that does not (a driver
	// without DriverContext dials with Driver.Open) could otherwise hold
	// DB.Close for as long as its dial hangs. Whatever such a dial returns,
	// put refuses and the filler closes before it exits.
	c.cancel()
	// The filler may never have started; starting it now as a no-op makes
	// done close either way.
	c.start.Do(func() { close(c.done) })
	for _, s := range spares {
		_ = s.conn.Close()
	}
	if closer, ok := c.Connector.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
