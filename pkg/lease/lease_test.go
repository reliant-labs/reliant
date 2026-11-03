package lease_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/lease"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func fast() lease.Options {
	return lease.Options{RetryInterval: 20 * time.Millisecond, HeartbeatInterval: 50 * time.Millisecond, Logger: quiet}
}

// pool opens an independent pool on dsn, so each "replica" has its own
// connections exactly as separate processes would.
func pool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// replica runs Run(name) on its own Locker and reports when it leads.
type replica struct {
	l       *lease.Locker
	leading atomic.Bool
	starts  atomic.Int32
	done    chan error
	cancel  context.CancelFunc
}

func start(t *testing.T, dsn, name string, shared *concurrency) *replica {
	t.Helper()
	r := &replica{l: lease.New(pool(t, dsn), fast()), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(func() { cancel(); _ = r.l.Close() })
	go func() {
		r.done <- r.l.Run(ctx, name, func(ctx context.Context) error {
			r.starts.Add(1)
			r.leading.Store(true)
			defer r.leading.Store(false)
			if shared != nil {
				shared.enter()
				defer shared.exit()
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	return r
}

// concurrency records the highest number of simultaneous leaders.
type concurrency struct{ cur, max atomic.Int32 }

func (c *concurrency) enter() {
	n := c.cur.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			return
		}
	}
}
func (c *concurrency) exit() { c.cur.Add(-1) }

func TestOnlyOneReplicaLeadsAndStandbyTakesOverOnCancel(t *testing.T) {
	dsn := testDSN(t)
	a := start(t, dsn, "sweeper", nil)
	eventually(t, "a to lead", a.leading.Load)

	b := start(t, dsn, "sweeper", nil)
	time.Sleep(300 * time.Millisecond)
	if b.leading.Load() {
		t.Fatal("b led while a held the lease")
	}

	a.cancel()
	if err := <-a.done; err != nil {
		t.Fatalf("a: %v", err)
	}
	eventually(t, "b to take over after a exited", b.leading.Load)
}

func TestDifferentNamesDoNotExclude(t *testing.T) {
	dsn := testDSN(t)
	a := start(t, dsn, "job-a", nil)
	b := start(t, dsn, "job-b", nil)
	eventually(t, "both to lead", func() bool { return a.leading.Load() && b.leading.Load() })
}

func TestBackendDeathFailsOverAndFormerLeaderStopsWithinAHeartbeat(t *testing.T) {
	dsn := testDSN(t)
	// a reaches the database through a link this test can cut.
	k, via := newLink(t, dsn, 0)
	a := start(t, via, "sweeper", nil)
	eventually(t, "a to lead", a.leading.Load)
	b := start(t, dsn, "sweeper", nil)

	// Partition a the way a network split or a node loss does: the server
	// drops its session and its locks, and a cannot open a new one.
	//
	// Cutting a's NEW connections is not optional. Killing only the backend
	// leaves a free to reconnect, and the freed lock goes to whichever
	// replica polls first: when a's heartbeat noticed the loss before b's
	// next poll (one slow CI runner was enough), a re-won on a fresh session
	// and b never led — a correct outcome for the Locker, and a 10s timeout
	// here. Only this test's database is touched: the server is shared, and
	// another package's test holding an advisory lock (migratekit's run lock,
	// testkit's template build) would be killed too.
	k.refuse.Store(true)
	admin := pool(t, dsn)
	var killed int
	if err := admin.QueryRow(`SELECT count(pg_terminate_backend(pid)) FROM pg_locks
		WHERE locktype='advisory' AND granted AND pid <> pg_backend_pid()
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&killed); err != nil || killed != 1 {
		t.Fatalf("terminate leader backend: killed=%d err=%v", killed, err)
	}

	eventually(t, "b to take over", b.leading.Load)
	// The overlap after a partition is bounded by the heartbeat: a's job is
	// cancelled within a couple of intervals of b winning.
	deadline := time.Now().Add(4 * fast().HeartbeatInterval)
	for a.leading.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("a was still leading %v after b took over", 4*fast().HeartbeatInterval)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// a keeps contending as a standby, restartable.
	if a.starts.Load() != 1 {
		t.Fatalf("a started %d times, want 1 until it wins again", a.starts.Load())
	}
}

func TestRestartableAfterLosingTheLease(t *testing.T) {
	dsn := testDSN(t)
	a := start(t, dsn, "solo", nil)
	eventually(t, "a to lead", a.leading.Load)

	admin := pool(t, dsn)
	if _, err := admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype='advisory' AND granted AND pid <> pg_backend_pid()
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a to run again on a fresh session", func() bool { return a.starts.Load() == 2 && a.leading.Load() })
}

func TestManyContendersNeverOverlapAcrossHandoffs(t *testing.T) {
	dsn := testDSN(t)
	shared := &concurrency{}
	var reps []*replica
	for range 5 {
		reps = append(reps, start(t, dsn, "crowd", shared))
	}
	for i := 0; i < 4; i++ {
		var leader *replica
		eventually(t, "a leader", func() bool {
			for _, r := range reps {
				if r.leading.Load() {
					leader = r
					return true
				}
			}
			return false
		})
		leader.cancel()
		<-leader.done
		eventually(t, "handoff", func() bool {
			for _, r := range reps {
				if r != leader && r.leading.Load() {
					return true
				}
			}
			return false
		})
		reps = without(reps, leader)
	}
	if got := shared.max.Load(); got != 1 {
		t.Fatalf("max simultaneous leaders = %d, want 1", got)
	}
}

func without(rs []*replica, x *replica) []*replica {
	var out []*replica
	for _, r := range rs {
		if r != x {
			out = append(out, r)
		}
	}
	return out
}

func TestRunReturnsFnErrorAndReleases(t *testing.T) {
	dsn := testDSN(t)
	boom := errors.New("boom")
	l := lease.New(pool(t, dsn), fast())
	t.Cleanup(func() { _ = l.Close() })
	if err := l.Run(context.Background(), "once", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want boom", err)
	}
	if h := l.Held(); len(h) != 0 {
		t.Fatalf("still holding %v after fn failed", h)
	}
	// A second locker acquires immediately: the first released explicitly.
	other := lease.New(pool(t, dsn), fast())
	t.Cleanup(func() { _ = other.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran := false
	if err := other.Run(ctx, "once", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("other.Run ran=%v err=%v", ran, err)
	}
}

func TestFnReturningNilWhileHeldEndsRunWithoutSpinning(t *testing.T) {
	dsn := testDSN(t)
	l := lease.New(pool(t, dsn), fast())
	t.Cleanup(func() { _ = l.Close() })
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- l.Run(context.Background(), "disabled", func(context.Context) error { calls.Add(1); return nil })
	}()
	select {
	case err := <-done:
		if err != nil || calls.Load() != 1 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after fn finished")
	}
}

func TestSameNameTwiceOnOneLockerIsRefused(t *testing.T) {
	dsn := testDSN(t)
	l := lease.New(pool(t, dsn), fast())
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan struct{})
	go func() {
		_ = l.Run(ctx, "dup", func(ctx context.Context) error { close(up); <-ctx.Done(); return nil })
	}()
	<-up
	err := l.Run(ctx, "dup", func(context.Context) error { t.Error("second Run must not lead"); return nil })
	if !errors.Is(err, lease.ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestHeldListsActiveLeases(t *testing.T) {
	dsn := testDSN(t)
	l := lease.New(pool(t, dsn), fast())
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	up := make(chan struct{}, 2)
	for _, n := range []string{"b", "a"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Run(ctx, n, func(ctx context.Context) error { up <- struct{}{}; <-ctx.Done(); return nil })
		}()
	}
	<-up
	<-up
	if got := l.Held(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Held() = %v, want [a b]", got)
	}
	cancel()
	wg.Wait()
	if got := l.Held(); len(got) != 0 {
		t.Fatalf("Held() after cancel = %v", got)
	}
}

func TestUnreachableDatabaseStandsByAndStopsOnCancel(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://nobody:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := lease.New(db, fast())
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := l.Run(ctx, "never", func(context.Context) error { t.Error("must not lead without a database"); return nil }); err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
}

func TestKeyIsStableDistinctAndNamespaced(t *testing.T) {
	if lease.Key("a") != lease.Key("a") {
		t.Fatal("Key is not deterministic")
	}
	if lease.Key("a") == lease.Key("b") {
		t.Fatal("distinct names collide")
	}
	if lease.Key("a") == 0 {
		t.Fatal("zero key")
	}
}

// A failed or closed session must end the SERVER session. sql.Conn.Close alone
// would hand it back to the pool still holding the advisory lock, so a pool
// that stays open would keep a dead leader's lease forever.
func TestClosingTheLockerReleasesTheLeaseEvenWhenItsPoolStaysOpen(t *testing.T) {
	dsn := testDSN(t)
	db := pool(t, dsn)
	db.SetMaxOpenConns(1)
	l := lease.New(db, fast())
	up := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- l.Run(context.Background(), "pooled", func(ctx context.Context) error {
			close(up)
			<-ctx.Done()
			return nil
		})
	}()
	<-up
	_ = l.Close()
	<-done

	other := pool(t, dsn)
	eventually(t, "the lease to be free for another session", func() bool {
		var got bool
		if err := other.QueryRow(`SELECT pg_try_advisory_lock($1)`, lease.Key("pooled")).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	})
}

// Many leases on ONE Locker share one session. Before the session serialized
// its queries, pgx answered a second concurrent query with "conn busy", the
// session was declared lost and every lease it held was dropped. That is the
// shape control-plane-workers has: fifteen leases per process, a standby
// polling all of them at once, and a heartbeat on top.
func TestManyLeasesOnOneLockerSurviveConcurrentPollingAndHeartbeats(t *testing.T) {
	dsn := testDSN(t)
	const n = 20
	opt := lease.Options{RetryInterval: 5 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond, Logger: quiet}

	run := func(l *lease.Locker, ctx context.Context, wg *sync.WaitGroup, starts *[n]atomic.Int32, leading *atomic.Int32) {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = l.Run(ctx, "job-"+string(rune('a'+i)), func(ctx context.Context) error {
					starts[i].Add(1)
					leading.Add(1)
					defer leading.Add(-1)
					<-ctx.Done()
					return nil
				})
			}()
		}
	}
	oneConn := func() *sql.DB { db := pool(t, dsn); db.SetMaxOpenConns(1); return db }

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	leader, standby := lease.New(oneConn(), opt), lease.New(oneConn(), opt)
	t.Cleanup(func() { cancel(); wg.Wait(); _ = leader.Close(); _ = standby.Close() })

	var lStarts, sStarts [n]atomic.Int32
	var lLeading, sLeading atomic.Int32
	run(leader, ctx, &wg, &lStarts, &lLeading)
	eventually(t, "the leader to hold every lease", func() bool { return lLeading.Load() == n })
	// The standby now polls 20 held leases every 5ms on one session, while
	// both sessions heartbeat every 20ms.
	run(standby, ctx, &wg, &sStarts, &sLeading)
	time.Sleep(1500 * time.Millisecond)

	if got := lLeading.Load(); got != n {
		t.Fatalf("leader holds %d of %d leases after 1.5s of contention", got, n)
	}
	for i := range lStarts {
		if got := lStarts[i].Load(); got != 1 {
			t.Fatalf("job %d restarted %d times: its lease was dropped on a healthy session", i, got)
		}
	}
	if got := sLeading.Load(); got != 0 {
		t.Fatalf("standby led %d jobs while the leader held them", got)
	}
}
