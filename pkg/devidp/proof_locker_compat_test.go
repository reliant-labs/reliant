package devidp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// SignInRoutesConfig.ProofLocker is deprecated, not disabled (forge is semver
// v1): a supplied locker is still taken where it always was — around a proof,
// around a verification with the credentials it installs, around a passkey
// enrolment — and one that cannot be taken still refuses. nil takes none.

// semLocker is a shared one-holder ProofLocker that counts its waiters.
type semLocker struct {
	sem     chan struct{}
	waiting atomic.Int32
}

func newSemLocker() *semLocker { return &semLocker{sem: make(chan struct{}, 1)} }

func (l *semLocker) LockAccount(ctx context.Context, _ string) (func(), error) {
	l.waiting.Add(1)
	defer l.waiting.Add(-1)
	select {
	case l.sem <- struct{}{}:
		return func() { <-l.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func withLocker(l ProofLocker) func(*SignInRoutesConfig) {
	return func(c *SignInRoutesConfig) { c.ProofLocker = l }
}

func TestProofLocker_AnUnavailableLockerStillRefuses(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods), withLocker(UnavailableProofLocker(errors.New("store down"))))
	u := h.z.addUser("pending@acme.com", false, "")
	h.z.mu.Lock()
	u.emailCode, u.inviteCode = "ev-1", "inv-1"
	h.z.mu.Unlock()
	before := h.z.snapshot(u.id)

	status, body := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "ev-1"})
	if status != http.StatusBadGateway {
		t.Fatalf("verification without the lock: %d %v, want 502", status, body)
	}
	status, body = h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": MethodPassword, "password": "invitee-password-1"})
	if status != http.StatusBadGateway {
		t.Fatalf("invitation without the lock: %d %v, want 502", status, body)
	}
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "pending@acme.com"})
	before = h.z.snapshot(u.id) // the request mailed a fresh code
	if status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status != http.StatusBadGateway {
		t.Fatalf("magic-link proof without the lock: %d %v, want 502", status, body)
	}
	if after := h.z.snapshot(u.id); after != before {
		t.Fatalf("a refused proof changed the account:\n before %s\n after  %s", before, after)
	}
	if n := h.z.callCount("POST /v2/users/" + u.id + "/email/verify"); n != 0 {
		t.Fatalf("%d proof(s) reached the issuer without the lock", n)
	}

	// Passkey enrolment of a signed-in, verified user is refused too.
	owner := h.z.addUser("owner@acme.com", true, "owner-password-1")
	srvURL, _ := url.Parse(h.srv.URL)
	b.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "app_session", Value: "at-" + owner.id, Path: "/"}})
	if status, body := b.post("/auth/passkey/register/begin", map[string]any{}); status != 200 {
		t.Fatalf("register begin: %d %v", status, body)
	}
	if status, body := b.post("/auth/passkey/register/finish", map[string]any{"credential": credential(testAppBase, WebAuthnCreate, "x"), "name": "laptop"}); status != http.StatusBadGateway {
		t.Fatalf("passkey enrolment without the lock: %d %v, want 502", status, body)
	}
	if len(h.z.user(owner.id).passkeys) != 0 {
		t.Fatal("a passkey was enrolled without the lock")
	}
}

// raceTwoProofs runs two verifications of one account on two replicas: A's
// proof is held at the issuer, then B's starts. It reports whether B's proof
// reached the issuer while A's was still held, then lets A through.
func raceTwoProofs(t *testing.T, l ProofLocker, waiting func() int32) (overlapped bool) {
	t.Helper()
	opt := func(*SignInRoutesConfig) {}
	if l != nil {
		opt = withLocker(l)
	}
	policy := policyWith(SignupOpen, allMethods)
	a := newHarness(t, policy, opt)
	b := newHarnessOn(t, a.z, policy, opt)
	u := a.z.addUser("pending@acme.com", false, "")
	a.z.mu.Lock()
	u.emailCode = "ev-1"
	verifyPath := "/v2/users/" + u.id + "/email/verify"
	paused, resume := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	a.z.beforeRequest = func(r *http.Request) {
		if r.URL.Path == verifyPath && first.CompareAndSwap(false, true) {
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

	aDone, bDone := make(chan int, 1), make(chan int, 1)
	go func() {
		status, _ := a.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "ev-1"})
		aDone <- status
	}()
	testkit.Receive(t, "A's proof at the issuer", paused, testkit.Within(10*time.Second))
	go func() {
		status, _ := b.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"})
		bDone <- status
	}()
	if l != nil {
		testkit.Eventually(t, "B waiting for the shared lock", func() bool { return waiting() == 1 }, testkit.Within(10*time.Second))
		overlapped = a.z.callCount("POST "+verifyPath) > 1
	} else {
		testkit.Eventually(t, "B's proof at the issuer", func() bool { return a.z.callCount("POST "+verifyPath) == 2 }, testkit.Within(10*time.Second))
		overlapped = true
	}
	close(resume)
	if s := testkit.Receive(t, "A's verification", aDone, testkit.Within(10*time.Second)); s != 200 {
		t.Fatalf("A's verification answered %d", s)
	}
	if s := testkit.Receive(t, "B's verification", bDone, testkit.Within(10*time.Second)); s != 401 {
		t.Fatalf("B's wrong code answered %d, want 401", s)
	}
	return overlapped
}

func TestProofLocker_ASharedLockerSerialisesTwoProofs(t *testing.T) {
	l := newSemLocker()
	if raceTwoProofs(t, l, l.waiting.Load) {
		t.Fatal("B's proof reached the issuer while A held the shared lock")
	}
}

func TestProofLocker_NilTakesNoLock(t *testing.T) {
	if !raceTwoProofs(t, nil, nil) {
		t.Fatal("without a locker, B's proof waited for A's")
	}
}
