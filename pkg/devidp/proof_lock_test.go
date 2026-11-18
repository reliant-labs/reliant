package devidp

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// forge#2121: proofs of one account, and the credential installs after one,
// serialised across broker replicas by a lock on the app's own Postgres.

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

// The forge#2121 acceptance case. Replica A's wrong-code proof sends a
// revocation — the removal of the account's TOTP, a call that names no
// credential id and so would take the OWNER's TOTP as readily as a planted
// one — and that request reaches the issuer only after a delay past today's
// whole fence (the 15s window plus 5s slack, scaled down here), though
// within the shared lock's window. B's proof starts meanwhile. With the
// #2098 fence alone, A gives up at its window, B verifies, the owner sets a
// password and enrols a TOTP, and A's removal then lands on it. Under the
// shared lock, B cannot start until A's request has been served, so what
// the owner sets afterwards survives.
//
// (A late password replacement is already refused by the issuer: each one
// redeems a reset code, and a newer code supersedes it. Passkeys and links
// are removed by id, so a late removal never names the owner's new one.)
func TestTakeover_UnderASharedLockAStaleRevocationServedPastTheFenceSparesTheOwnersPassword(t *testing.T) {
	dsn := lockDSN(t)
	admin := lockPool(t, dsn)
	shrinkProofWindow(t, 200*time.Millisecond, 50*time.Millisecond)
	w := lockedPreProofWindow
	lockedPreProofWindow = 30 * time.Second
	t.Cleanup(func() { lockedPreProofWindow = w })

	policy := policyWith(SignupOpen, allMethods)
	shared := func(c *SignInRoutesConfig) {
		c.ProofLocker = NewPostgresProofLocker(lockPool(t, dsn), PostgresProofLockerOptions{Logger: quietLog})
	}
	a := newHarness(t, policy, shared)
	b := newHarnessOn(t, a.z, policy, shared)
	u := preRegister(t, a)
	code := a.z.lastMail(t).code
	c := a.z.client()
	ctx := context.Background()
	before, err := c.FindUserByID(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}

	// The attacker planted a TOTP too. Hold A's first removal of it between
	// send and arrival.
	a.z.mu.Lock()
	u.totp = true
	a.z.mu.Unlock()
	paused, resume := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	a.z.mu.Lock()
	a.z.beforeRequest = func(r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/users/"+u.id+"/totp") && first.CompareAndSwap(false, true) {
			close(paused)
			<-resume
		}
	}
	a.z.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})

	aDone := make(chan error, 1)
	go func() {
		_, err := a.routes.proveAddress(ctx, before, func() error { return ErrInvalidCode })
		aDone <- err
	}()
	testkit.Receive(t, "A's revocation reaching the issuer's door", paused, testkit.Within(15*time.Second))
	sentAt := time.Now()

	bDone := make(chan error, 1)
	go func() {
		_, err := b.routes.proveAddress(ctx, before, func() error { return c.VerifyEmail(ctx, u.id, code) })
		bDone <- err
	}()
	awaitLockWaiter(t, admin)
	// forge:lint-disable-next-line forge-test-fixed-sleep: this IS the injected delay under test — A's revocation stays in flight past the whole #2098 fence before the issuer serves it
	time.Sleep(time.Until(sentAt.Add(preProofWindow + proofFenceSlack + 300*time.Millisecond)))
	select {
	case err := <-bDone:
		t.Fatalf("PROOF RACE: B's proof ran while A's revocation was still in flight (err %v)", err)
	default:
	}
	close(resume) // A's revocation lands now, past the fence

	if err := testkit.Receive(t, "A's proof", aDone, testkit.Within(15*time.Second)); err == nil {
		t.Fatal("A's wrong-code proof succeeded")
	}
	if err := testkit.Receive(t, "B's proof", bDone, testkit.Within(15*time.Second)); err != nil {
		t.Fatalf("the owner's proof: %v", err)
	}
	// The owner, now proven, sets a password and enrols a TOTP.
	const ownerPassword = "owner-password-1"
	if err := c.SetPassword(ctx, u.id, ownerPassword); err != nil {
		t.Fatal(err)
	}
	a.z.mu.Lock()
	u.totp = true
	a.z.mu.Unlock()
	if !signsIn(t, a, "victim@acme.com", ownerPassword) {
		t.Fatal("PROOF RACE: a wrong-code proof's late revocation replaced the password the owner set after proving the address")
	}
	got := a.z.user(u.id)
	a.z.mu.Lock()
	defer a.z.mu.Unlock()
	if !got.totp {
		t.Fatal("PROOF RACE: a wrong-code proof's late revocation removed the TOTP the owner enrolled after proving the address")
	}
	if !got.verified || got.locked {
		t.Fatalf("after both proofs: verified=%v locked=%v, want verified and unlocked", got.verified, got.locked)
	}
}

// downDriver is a lock store that is down: every connection is refused.
type downDriver struct{}

func (downDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("dial tcp: connection refused (the lock store is down)")
}

var registerDownDriver sync.Once

func downLockStore() *PostgresProofLocker {
	registerDownDriver.Do(func() { sql.Register("devidp-lock-store-down", downDriver{}) })
	db, _ := sql.Open("devidp-lock-store-down", "")
	return NewPostgresProofLocker(db, PostgresProofLockerOptions{Logger: quietLog})
}

// While the shared lock cannot be taken, everything it guards refuses —
// proofs, verification, passkey enrolment — and leaves the account as it
// was; signing in with a credential the account already has does not take
// the lock, and keeps working.
func TestProofLock_ALockStoreOutageFailsProofsClosedButNotSignIn(t *testing.T) {
	assertProofsFailClosedButNotSignIn(t, downLockStore())
}

// A shared lock store that cannot even be set up — a database_url pgx cannot
// parse, as the scaffolded broker's proofLockerFor checks — fails closed like
// an outage, never back to the in-process lock that would let every replica
// accept proofs unserialised (forge#2166).
func TestProofLock_AMalformedLockStoreDSNFailsProofsClosedButNotSignIn(t *testing.T) {
	_, err := pgx.ParseConfig("postgres://broker@:not-a-port/app")
	if err == nil {
		t.Fatal("pgx accepted a malformed DSN; the test needs one it refuses")
	}
	assertProofsFailClosedButNotSignIn(t, UnavailableProofLocker(err))
}

func assertProofsFailClosedButNotSignIn(t *testing.T, locker ProofLocker) {
	t.Helper()
	h := newHarness(t, policyWith(SignupOpen, allMethods), func(c *SignInRoutesConfig) { c.ProofLocker = locker })
	u := preRegister(t, h)

	status, body := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if status != http.StatusBadGateway {
		t.Fatalf("verification without the lock: %d %v, want 502", status, body)
	}
	victim := h.browser()
	victim.post("/auth/magic/request", map[string]any{"email": "victim@acme.com"})
	if status, body := victim.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status == http.StatusOK {
		t.Fatalf("a magic-link proof ran without the lock: %v", body)
	}
	got := h.z.user(u.id)
	h.z.mu.Lock()
	verified, locked, password := got.verified, got.locked, got.password
	h.z.mu.Unlock()
	if verified || locked || password != attackerPassword {
		t.Fatalf("a refused proof changed the account: verified=%v locked=%v password changed=%v", verified, locked, password != attackerPassword)
	}

	owner := h.z.addUser("owner@acme.com", true, "owner-password-1")
	if !signsIn(t, h, "owner@acme.com", "owner-password-1") {
		t.Fatal("password sign-in failed while the lock store was down")
	}
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "owner@acme.com"})
	if status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status != http.StatusOK {
		t.Fatalf("a verified account's magic-link sign-in needs no proof, and failed: %d %v", status, body)
	}

	srvURL, _ := url.Parse(h.srv.URL)
	b.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "app_session", Value: "at-" + owner.id, Path: "/"}})
	if status, body := b.post("/auth/passkey/register/begin", map[string]any{}); status != http.StatusOK {
		t.Fatalf("register begin: %d %v", status, body)
	}
	if status, body := b.post("/auth/passkey/register/finish", map[string]any{"credential": credential(testAppBase, WebAuthnCreate, "x"), "name": "laptop"}); status != http.StatusBadGateway {
		t.Fatalf("passkey enrolment without the lock: %d %v, want 502", status, body)
	}
	h.z.mu.Lock()
	defer h.z.mu.Unlock()
	if len(owner.passkeys) != 0 {
		t.Fatal("a passkey was enrolled without the account's proof lock")
	}
}

// A window longer than the default is announced so a replica that reads
// every value as a 15s window's start still waits for its real end.
func TestAnnounceWindow_AnOlderReplicaReadsTheRealEnd(t *testing.T) {
	end := time.UnixMilli(time.Now().Add(2 * time.Minute).UnixMilli())
	v := announceWindow(end)
	if got, ok := windowEnd(v); !ok || !got.Equal(end) {
		t.Fatalf("windowEnd(%q) = %v %v, want %v", v, got, ok, end)
	}
	// What a #2098 replica computes: the value as a start, plus 15s.
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if older := time.UnixMilli(ms).Add(15 * time.Second); !older.Equal(end) {
		t.Fatalf("an older replica reads the window as ending %v, want %v", older, end)
	}
}
