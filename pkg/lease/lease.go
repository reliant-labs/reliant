// Package lease is a named, process-level singleton lock over Postgres
// advisory locks: at most one replica at a time runs a given job, every other
// replica waits as a hot standby and takes over when the holder exits, crashes
// or loses its database connection.
//
// # What it is for
//
// A background job that is NOT safe to run on two replicas at once (a sweeper
// that reads a value and writes a derived one, a reconciler that talks to a
// billing provider) cannot be scaled by raising the replica count. Without a
// lease the choices are one replica and an outage on every rollout, or two
// replicas and a race. Jobs that already tolerate concurrency (queue
// consumers on SELECT ... FOR UPDATE SKIP LOCKED, durable JetStream consumers,
// Temporal workers, idempotent set-based sweeps) need no lease and should not
// take one.
//
// # Why advisory locks
//
// Every forge service already has a database, so the lease needs no new
// infrastructure, no schema and no Kubernetes RBAC, and behaves identically
// under `forge env up`, docker compose and a cluster. A SESSION-level
// advisory lock is released by Postgres the instant the holder's connection
// ends, so a crashed replica needs no TTL to expire.
//
// # The connection must be session-capable
//
// The lock lives on one dedicated connection, so the [*sql.DB] given to [New]
// must hand back the same server session for the life of a connection. That
// excludes a transaction-pooling PgBouncer (the lock would be taken on a
// backend the client stops talking to, and silently never released). Point it
// at the direct database or a session-mode pooler entry, and give it its own
// pool: one open connection per [Locker] is all it uses.
//
// # Overlap is bounded, not impossible
//
// Postgres will not grant a held lock to a second session, so two replicas
// are never leaders while the first holder's connection is alive. If the
// holder's connection is lost the server drops the lock, a standby can take
// it at once, and the old holder only learns of it at its next heartbeat. The
// jobs run under a lease therefore see at most [Options.HeartbeatInterval]
// plus [Options.QueryTimeout] of overlap after a partition (the next check,
// and the time it may take to give up on a silent network), the same kind of
// guarantee as a Kubernetes Lease, and must stay correct (if inefficient)
// under that window.
//
// # A slow reply is not a lost session
//
// Every query on the session runs under [Options.QueryTimeout], not the
// heartbeat interval. A query that outlives its deadline cannot be abandoned
// politely: the driver closes the connection, the server drops the session and
// every lock on it, so a timeout does not merely report a lost lease, it
// causes one. A deadline as short as the heartbeat interval turned local
// scheduling delay (a CPU-starved process, a stalled pod, a slow round trip)
// into dropped leases on a perfectly healthy session. How OFTEN to check and
// how LONG to wait for an answer are separate questions with separate knobs.
//
// # Key space
//
// A lease name is hashed to the 64-bit advisory-lock key (see [Key]). Other
// code sharing the database may use advisory locks of its own; the hash is
// namespaced so a collision needs a 64-bit coincidence.
package lease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

const (
	keyNamespace = "forge/lease/"

	// DefaultRetryInterval is how often a standby polls for a lease it does
	// not hold. It bounds failover after a graceful release.
	DefaultRetryInterval = 5 * time.Second

	// DefaultHeartbeatInterval is how often the holder re-checks that its
	// session still owns its locks.
	DefaultHeartbeatInterval = 5 * time.Second

	// DefaultQueryTimeout is how long one query on the lease session may take
	// before the session is declared lost. It is deliberately generous: a
	// query that times out closes the connection and drops every lease on it,
	// so it must only fire on a network or server that has stopped answering,
	// never on a slow reply.
	DefaultQueryTimeout = 10 * time.Second
)

// ErrAlreadyRunning is returned by [Locker.Run] when this Locker is already
// running the same lease name. Postgres advisory locks are re-entrant within
// a session, so without this check two in-process callers would both believe
// they lead.
var ErrAlreadyRunning = errors.New("lease: this lease name is already running on this Locker")

// Key returns the 64-bit advisory-lock key a lease name maps to, for
// debugging against pg_locks (classid is the high 32 bits, objid the low 32).
func Key(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(keyNamespace + name))
	return int64(h.Sum64())
}

// Options tunes a Locker. The zero value is production-ready.
type Options struct {
	// RetryInterval is the standby poll interval (default
	// [DefaultRetryInterval]). Each wait is jittered by up to 20% so replicas
	// that start together do not poll in lockstep.
	RetryInterval time.Duration
	// HeartbeatInterval is the holder's session check interval (default
	// [DefaultHeartbeatInterval]). A check that fails loses every lease held
	// on the session.
	HeartbeatInterval time.Duration
	// QueryTimeout bounds every query on the session, the heartbeat's
	// included (default [DefaultQueryTimeout]). It is independent of
	// HeartbeatInterval: a short interval checks often, it does not demand a
	// faster answer. A query that times out loses every lease on the session
	// (see the package doc), so keep it well above the worst round trip a
	// healthy database and a busy process can produce.
	QueryTimeout time.Duration
	// Logger receives acquire / lose / session events (default slog.Default()).
	Logger *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.RetryInterval <= 0 {
		o.RetryInterval = DefaultRetryInterval
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if o.QueryTimeout <= 0 {
		o.QueryTimeout = DefaultQueryTimeout
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Locker contends for leases on one dedicated database session. Create one
// per process and share it between every job that needs a lease: all of them
// ride a single connection, so the cost is one session however many jobs.
type Locker struct {
	db  *sql.DB
	opt Options

	mu      sync.Mutex
	sess    *session
	running map[string]struct{}
	closed  bool
}

// New returns a Locker over db, which must be session-capable (see the
// package doc). The Locker does not own db; Close releases only its leases.
func New(db *sql.DB, opt Options) *Locker {
	return &Locker{db: db, opt: opt.withDefaults(), running: map[string]struct{}{}}
}

// Held returns the lease names this Locker currently holds, sorted.
func (l *Locker) Held() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sess == nil {
		return nil
	}
	return l.sess.heldNames()
}

// Close drops the session, which releases every lease it holds, and makes
// every Run (running or later) return nil. It is for tests and orderly teardown; a normal
// process just cancels the contexts it passed to Run.
func (l *Locker) Close() error {
	l.mu.Lock()
	l.closed = true
	s := l.sess
	l.sess = nil
	l.mu.Unlock()
	if s != nil {
		s.fail(errors.New("lease: locker closed"))
	}
	return nil
}

// Run contends for the lease name and, while it holds it, runs fn.
//
// fn receives a context that is cancelled when the lease is lost or ctx ends;
// it must return promptly then. Run's behaviour on fn's return:
//
//   - ctx is done: Run returns nil.
//   - the lease was lost (heartbeat failure) and fn returned: Run goes back to
//     contending and calls fn again when it next wins. fn must therefore be
//     restartable.
//   - fn returned an error: Run releases the lease and returns the error.
//   - fn returned nil with the lease still held: the job is finished (for
//     example a worker that disables itself). Run releases the lease and
//     returns nil rather than spin re-acquiring it.
//
// The lease is always released (pg_advisory_unlock) after fn has returned,
// never before, so a graceful handover never overlaps the outgoing leader.
func (l *Locker) Run(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	if err := l.begin(name); err != nil {
		return err
	}
	defer l.end(name)

	key := Key(name)
	for {
		sess, ok := l.contend(ctx, name, key)
		if !ok {
			return nil // ctx done
		}
		l.opt.Logger.InfoContext(ctx, "lease acquired", "lease", name)

		leaderCtx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-sess.lost:
				cancel()
			case <-leaderCtx.Done():
			}
		}()
		err := fn(leaderCtx)
		lost := sess.isLost()
		cancel()
		l.release(sess, name, key)

		switch {
		case ctx.Err() != nil:
			return nil
		case lost:
			l.opt.Logger.WarnContext(ctx, "lease lost; contending again", "lease", name, "cause", sess.cause())
			continue
		case err != nil && !errors.Is(err, context.Canceled):
			return err
		default:
			return nil
		}
	}
}

func (l *Locker) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *Locker) begin(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("lease: locker closed")
	}
	if _, dup := l.running[name]; dup {
		return fmt.Errorf("%w: %q", ErrAlreadyRunning, name)
	}
	l.running[name] = struct{}{}
	return nil
}

func (l *Locker) end(name string) {
	l.mu.Lock()
	delete(l.running, name)
	l.mu.Unlock()
}

// contend polls until it holds the lease or ctx ends.
func (l *Locker) contend(ctx context.Context, name string, key int64) (*session, bool) {
	var lastErr string
	for {
		if ctx.Err() != nil || l.isClosed() {
			return nil, false
		}
		sess, held, err := l.tryLock(ctx, name, key)
		switch {
		case err == nil && held:
			return sess, true
		case err != nil && ctx.Err() == nil && err.Error() != lastErr:
			lastErr = err.Error() // log each distinct failure once, not every poll
			l.opt.Logger.WarnContext(ctx, "lease: cannot reach the database; standing by", "lease", name, "error", err)
		}
		wait := l.opt.RetryInterval
		wait += time.Duration(rand.Int64N(int64(wait)/5 + 1))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, false
		case <-t.C:
		}
	}
}

func (l *Locker) tryLock(ctx context.Context, name string, key int64) (*session, bool, error) {
	sess, err := l.session(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := sess.scan(ctx, []any{&got}, `SELECT pg_try_advisory_lock($1)`, key); err != nil {
		if ctx.Err() == nil {
			sess.fail(err)
		}
		return nil, false, err
	}
	if got {
		sess.add(name)
	}
	return sess, got, nil
}

// release unlocks name on sess. Best effort: if the session is already dead the
// server released the lock with it.
func (l *Locker) release(sess *session, name string, key int64) {
	sess.remove(name)
	if sess.isLost() {
		return
	}
	var ok bool
	if err := sess.scan(context.Background(), []any{&ok}, `SELECT pg_advisory_unlock($1)`, key); err != nil || !ok {
		// A lock we cannot unlock must not outlive this call: drop the whole
		// session so the server releases it.
		sess.fail(fmt.Errorf("unlock %q: ok=%v err=%v", name, ok, err))
	}
}

func (l *Locker) session(ctx context.Context) (*session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("lease: locker closed")
	}
	if l.sess != nil && !l.sess.isLost() {
		return l.sess, nil
	}
	// The query timeout bounds the dial too: this runs under l.mu, and a
	// database that stopped answering must not park Held and Close behind it.
	qctx, qcancel := context.WithTimeout(ctx, l.opt.QueryTimeout)
	defer qcancel()
	conn, err := l.db.Conn(qctx)
	if err != nil {
		return nil, err
	}
	var pid int
	if err := conn.QueryRowContext(qctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		_ = conn.Close()
		return nil, err
	}
	s := &session{conn: conn, pid: pid, timeout: l.opt.QueryTimeout, lost: make(chan struct{}), held: map[string]struct{}{}}
	l.sess = s
	go s.heartbeat(l.opt.HeartbeatInterval)
	return s, nil
}

// session is one dedicated connection and the leases riding on it.
type session struct {
	conn    *sql.Conn
	pid     int
	timeout time.Duration // per query: Options.QueryTimeout
	lost    chan struct{}

	// qmu serializes every query on conn. A *sql.Conn is one driver
	// connection, and pgx refuses a second query while the first's result is
	// still open ("conn busy"); database/sql does not prevent that. A process
	// with fifteen leases has fifteen goroutines contending on this one
	// session, so without it the session is declared lost within milliseconds
	// of start and every lease it held is dropped.
	qmu sync.Mutex

	mu       sync.Mutex
	held     map[string]struct{}
	failure  error
	lostOnce sync.Once
}

// scan runs one single-row query on the session and scans it into dest, under
// qmu and the query timeout, so a dead network cannot park a goroutine (and
// qmu) for longer than that. The deadline starts once qmu is held: waiting
// behind the session's other queries is not the database being slow.
func (s *session) scan(ctx context.Context, dest []any, query string, args ...any) error {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.conn.QueryRowContext(ctx, query, args...).Scan(dest...)
}

func (s *session) add(name string) {
	s.mu.Lock()
	s.held[name] = struct{}{}
	s.mu.Unlock()
}

func (s *session) remove(name string) {
	s.mu.Lock()
	delete(s.held, name)
	s.mu.Unlock()
}

func (s *session) heldNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.held))
	for n := range s.held {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *session) isLost() bool {
	select {
	case <-s.lost:
		return true
	default:
		return false
	}
}

func (s *session) cause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// fail marks the session lost, signals every leader riding on it and closes
// the server session, which makes Postgres release the locks.
func (s *session) fail(err error) {
	s.lostOnce.Do(func() {
		s.mu.Lock()
		s.failure = err
		s.mu.Unlock()
		close(s.lost)
		// sql.Conn.Close only returns the connection to the pool, where it
		// would keep holding the advisory locks. Reporting it bad makes
		// database/sql close the underlying server session instead.
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = s.conn.Close()
	})
}

// heartbeat verifies, every interval, that the session is the same backend and
// still owns as many advisory locks as it believes it holds. A pooler that
// swapped the server connection under us, a terminated backend and a network
// partition all fail this check.
func (s *session) heartbeat(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.lost:
			return
		case <-t.C:
		}
		var pid, n int
		err := s.scan(context.Background(), []any{&pid, &n},
			`SELECT pg_backend_pid(),
			        (SELECT count(*) FROM pg_locks
			          WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted)`)
		if err != nil {
			s.fail(fmt.Errorf("heartbeat: %w", err))
			return
		}
		s.mu.Lock()
		want := len(s.held)
		s.mu.Unlock()
		if pid != s.pid || n < want {
			s.fail(fmt.Errorf("heartbeat: session no longer owns its locks (backend %d->%d, advisory locks %d, want %d)", s.pid, pid, n, want))
			return
		}
	}
}
