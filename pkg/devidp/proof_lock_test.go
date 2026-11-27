package devidp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The deprecated proof lockers keep their guarantees (forge is semver v1),
// though SignInRoutes no longer uses them (forge#2121).

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// lockDSN is a fresh database on the shared embedded Postgres.
func lockDSN(t *testing.T) string {
	t.Helper()
	testkit.Slow(t, "boots the shared embedded Postgres the proof lock runs on")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// lockPool opens an independent pool on dsn, so each "replica" has its own
// connections exactly as separate processes would.
func lockPool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// lockWaiters counts sessions of this database waiting for an advisory lock.
func lockWaiters(t *testing.T, admin *sql.DB) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(`SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND NOT granted
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func awaitLockWaiter(t *testing.T, admin *sql.DB) {
	t.Helper()
	testkit.Eventually(t, "a second holder waiting on the account's proof lock", func() bool { return lockWaiters(t, admin) > 0 }, testkit.Within(15*time.Second))
}

type lockResult struct {
	release func()
	err     error
}

func lockAsync(l ProofLocker, userID string) <-chan lockResult {
	ch := make(chan lockResult, 1)
	go func() {
		r, err := l.LockAccount(context.Background(), userID)
		ch <- lockResult{r, err}
	}()
	return ch
}

func TestPostgresProofLocker_OneHolderPerAccountAcrossReplicas(t *testing.T) {
	dsn := lockDSN(t)
	admin := lockPool(t, dsn)
	replicas := []*PostgresProofLocker{
		NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{Logger: quietLog}),
		NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{Logger: quietLog}),
	}
	ctx := context.Background()

	// Eight proofs of one account, four on each replica, released together:
	// exactly one is inside at any moment.
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	for i := range 8 {
		l := replicas[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, err := l.LockAccount(ctx, "u1")
			if err != nil {
				errs <- err
				return
			}
			n := inside.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			// A round trip while holding it, so an overlapping holder has
			// the time to be seen.
			if _, err := admin.ExecContext(ctx, "SELECT 1"); err != nil {
				errs <- err
			}
			inside.Add(-1)
			release()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := most.Load(); got != 1 {
		t.Fatalf("%d proofs of one account held its lock at once across two replicas, want 1", got)
	}

	// While one replica holds it, the other waits — and only for that
	// account.
	release, err := replicas[0].LockAccount(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := replicas[1].LockAccount(ctx, "u2")
	if err != nil {
		t.Fatalf("another account waited on u1: %v", err)
	}
	other()
	waiting := lockAsync(replicas[1], "u1")
	awaitLockWaiter(t, admin)
	select {
	case r := <-waiting:
		t.Fatalf("the second replica took u1 while the first held it (err %v)", r.err)
	default:
	}
	release()
	r := testkit.Receive(t, "the second replica's lock after the first released it", waiting, testkit.Within(15*time.Second))
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.release()
	r.release() // a release may be called twice

	// The wait is bounded: past MaxWait it gives up, and says why.
	held, err := replicas[0].LockAccount(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	impatient := NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{MaxWait: 200 * time.Millisecond, Logger: quietLog})
	if _, err := impatient.LockAccount(ctx, "u1"); !errors.Is(err, ErrProofLockBusy) {
		t.Fatalf("a wait past MaxWait: %v, want ErrProofLockBusy", err)
	}
}

// A replica that crashes holding the lock — its database session ends, with
// no release — frees it at once: the lock is the session's transaction, so
// there is nothing to expire. Its own release, if it ever runs, reports the
// lock lost.
func TestPostgresProofLocker_AHolderWhoseSessionDiesReleasesTheLock(t *testing.T) {
	dsn := lockDSN(t)
	admin := lockPool(t, dsn)
	var logs bytes.Buffer
	crashed := NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	survivor := NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{Logger: quietLog})

	releaseCrashed, err := crashed.LockAccount(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	waiting := lockAsync(survivor, "u1")
	awaitLockWaiter(t, admin)

	var pid int
	if err := admin.QueryRow(`SELECT pid FROM pg_locks
		WHERE locktype = 'advisory' AND granted
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	r := testkit.Receive(t, "the lock after its holder's session died", waiting, testkit.Within(15*time.Second))
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.release()
	releaseCrashed()
	if !strings.Contains(logs.String(), "lost before its release") {
		t.Fatalf("the dead holder's release did not report the lock lost; logs:\n%s", logs.String())
	}
}

// Deprecated, still working: an UnavailableProofLocker refuses every
// acquisition, as it always did (forge is semver v1).
func TestUnavailableProofLocker_StillRefuses(t *testing.T) {
	cause := errors.New("store down")
	if _, err := UnavailableProofLocker(cause).LockAccount(context.Background(), "u1"); !errors.Is(err, cause) {
		t.Fatalf("UnavailableProofLocker granted a lock: %v", err)
	}
}
