package devidp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The second security re-review of forge#1821 (at 72d47a1c): the race in the
// takeover fix, the sealed-password size oracle, and the low findings.

// ── A. No sign-in in the window between "verified" and "revoked" ───────

// raceProof runs proof while an attacker signs in with the planted password
// as fast as it can, with the issuer holding each proof's answer for a while
// after marking the address verified. It returns how many sign-ins got tokens.
func raceProof(t *testing.T, h *harness, proof func()) int64 {
	t.Helper()
	h.z.mu.Lock()
	h.z.proofDelay = 150 * time.Millisecond
	h.z.mu.Unlock()
	stop := plantedSignIns(h)
	proof()
	// forge:lint-disable-next-line forge-test-fixed-sleep: a negative wait — keeps the attacker signing in just past the proof, the window the race lives in; a correct fix passes however long this takes
	time.Sleep(20 * time.Millisecond)
	return stop()
}

// plantedSignIns signs in to victim@acme.com with the planted password as
// fast as it can until the returned stop is called, which reports how many
// of those sign-ins got tokens.
func plantedSignIns(h *harness) (stop func() int64) {
	var got atomic.Int64
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true}
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := h.z.client().SignIn(context.Background(), cfg, Credentials{LoginName: "victim@acme.com", Password: attackerPassword}); err == nil {
					got.Add(1)
				}
			}
		}()
	}
	return func() int64 {
		close(done)
		wg.Wait()
		return got.Load()
	}
}

func TestTakeover_NoTokensWhileTheProofIsInFlight(t *testing.T) {
	for name, proof := range map[string]func(*testing.T, *harness, *fakeUser){
		"verify": func(t *testing.T, h *harness, u *fakeUser) {
			if status, body := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
				t.Fatalf("verify: %d %v", status, body)
			}
		},
		"magic link": func(t *testing.T, h *harness, u *fakeUser) {
			b := h.browser()
			b.post("/auth/magic/request", map[string]any{"email": "victim@acme.com"})
			if status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status != 200 {
				t.Fatalf("consume: %d %v", status, body)
			}
		},
		"invitation": func(t *testing.T, h *harness, u *fakeUser) {
			h.z.mu.Lock()
			u.inviteCode = "inv-race"
			h.z.mu.Unlock()
			if status, body := h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-race", "method": MethodPasskey}); status != 200 {
				t.Fatalf("accept: %d %v", status, body)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupOpen, allMethods))
			u := preRegister(t, h)
			if n := raceProof(t, h, func() { proof(t, h, u) }); n != 0 {
				t.Fatalf("TAKEOVER RACE: the planted password got tokens %d time(s) while the proof was in flight", n)
			}
			if h.z.user(u.id).locked {
				t.Fatal("the account was left locked after a successful proof")
			}
			if signsIn(t, h, "victim@acme.com", attackerPassword) {
				t.Fatal("the planted password works after the proof")
			}
		})
	}
}

func TestProof_AWrongCodeUnlocksAndAPreviouslyLockedAccountStaysLocked(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
		t.Fatalf("wrong code: %d", status)
	}
	if h.z.user(u.id).locked {
		t.Fatal("a wrong code left the account locked")
	}
	h.z.mu.Lock()
	u.locked = true
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("a proof unlocked an account the issuer had locked")
	}
}

// The security re-review at 9703d20c: an attacker who knows the user id (the
// legacy sign-up answers it as `subject`) keeps a WRONG-code proof in flight
// while the owner proves the address. The failing proof must not lift the
// lock under the succeeding one — on one replica (the per-account proof
// lock) or across two (the MetaProofLock mark).
func TestTakeover_AConcurrentFailedProofNeverUnlocksTheRealOne(t *testing.T) {
	owner := map[string]func(*testing.T, *harness, string){
		"verify": func(t *testing.T, h *harness, code string) {
			if status, body := h.browser().post("/auth/verify", map[string]any{"userID": h.z.userByEmail("victim@acme.com").id, "code": code}); status != 200 {
				t.Errorf("owner's verify: %d %v", status, body)
			}
		},
		"magic link": func(t *testing.T, h *harness, _ string) {
			b := h.browser()
			b.post("/auth/magic/request", map[string]any{"email": "victim@acme.com"})
			if status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status != 200 {
				t.Errorf("owner's magic link: %d %v", status, body)
			}
		},
	}
	for _, replicas := range []int{1, 2} {
		for name, prove := range owner {
			t.Run(fmt.Sprintf("%d replica(s)/%s", replicas, name), func(t *testing.T) {
				policy := policyWith(SignupOpen, allMethods)
				attacker := newHarness(t, policy)
				ownerSide := attacker
				if replicas == 2 {
					ownerSide = newHarnessOn(t, attacker.z, policy)
				}
				u := preRegister(t, attacker)
				code := attacker.z.lastMail(t).code
				attacker.z.mu.Lock()
				attacker.z.refusedProofDelay = 150 * time.Millisecond
				attacker.z.proofDelay = 150 * time.Millisecond
				// The first proof to reach the issuer is the attacker's
				// wrong code; it is then held for refusedProofDelay.
				attackerAtIssuer := make(chan struct{})
				var atIssuer sync.Once
				attacker.z.beforeRequest = func(r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/email/verify") {
						atIssuer.Do(func() { close(attackerAtIssuer) })
					}
				}
				attacker.z.mu.Unlock()

				stop := plantedSignIns(attacker)
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					if status, _ := attacker.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
						t.Errorf("attacker's wrong code: %d, want 401", status)
					}
				}()
				// The owner proves while the attacker's failing proof is in
				// flight at the issuer.
				select {
				case <-attackerAtIssuer:
				case <-time.After(30 * time.Second):
					t.Fatal("the attacker's wrong-code proof never reached the issuer")
				}
				prove(t, ownerSide, code)
				wg.Wait()
				// forge:lint-disable-next-line forge-test-fixed-sleep: a negative wait — keeps the attacker signing in just past both proofs; a correct fix passes however long this takes
				time.Sleep(20 * time.Millisecond)
				if n := stop(); n != 0 {
					t.Fatalf("TAKEOVER RACE: the planted password got tokens %d time(s) while a failed proof ran beside the real one", n)
				}
				got := attacker.z.user(u.id)
				if !got.verified {
					t.Fatal("the owner's proof did not verify the address")
				}
				if got.locked {
					t.Fatal("the account was left locked after the owner's proof")
				}
				if _, marked := got.metadata[MetaProofLock]; marked {
					t.Error("the proof lock mark survived a successful proof")
				}
				if signsIn(t, attacker, "victim@acme.com", attackerPassword) {
					t.Fatal("the planted password works after the proof: revocation did not run")
				}
			})
		}
	}
}

// The re-review at dbbe6f9a, B1, forced deterministically: replica A's
// failed proof reads its own mark and pauses there; replica B's proof marks,
// locks and verifies; only then does A clear the mark and unlock — so the
// account is verified and unlocked before B re-locks and revokes. No lock
// ordering can prevent that without a compare-and-swap at the issuer, so the
// planted password must already be gone at that moment.
func TestTakeover_AFailedProofsLateUnlockExposesNoPlantedCredential(t *testing.T) {
	policy := policyWith(SignupOpen, allMethods)
	a := newHarness(t, policy)
	b := newHarnessOn(t, a.z, policy)
	u := preRegister(t, a)
	code := a.z.lastMail(t).code
	c := a.z.client()
	ctx := context.Background()
	before, err := c.FindUserByID(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}

	// A's first delete of its proof mark is releaseFailed's, right after it
	// read its own mark: hold it until B has verified the address.
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.z.mu.Lock()
	a.z.beforeRequest = func(r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/metadata") && r.URL.Query().Get("keys") == MetaProofLock {
			once.Do(func() { close(paused); <-resume })
		}
	}
	a.z.mu.Unlock()

	aDone := make(chan error, 1)
	go func() {
		_, err := a.routes.proveAddress(ctx, before, func() error { return ErrInvalidCode })
		aDone <- err
	}()
	select {
	case <-paused:
	case err := <-aDone:
		t.Fatalf("A's failed proof never reached its unlock (returned %v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("A's failed proof never reached its unlock")
	}

	exposed := false
	if _, err := b.routes.proveAddress(ctx, before, func() error {
		if err := c.VerifyEmail(ctx, u.id, code); err != nil {
			close(resume)
			return err
		}
		close(resume) // A's unlock lands now, before B re-locks and revokes
		if err := <-aDone; !errors.Is(err, ErrInvalidCode) {
			t.Errorf("A's proof returned %v, want ErrInvalidCode", err)
		}
		a.z.mu.Lock()
		verified, locked := u.verified, u.locked
		a.z.mu.Unlock()
		if !verified || locked {
			t.Errorf("interleaving not forced: verified=%v locked=%v, want verified and unlocked", verified, locked)
		}
		exposed = signsIn(t, a, "victim@acme.com", attackerPassword)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if exposed {
		t.Fatal("TAKEOVER RACE: the planted password signed in to the verified account while A's late unlock held it open")
	}
	got := a.z.user(u.id)
	a.z.mu.Lock()
	defer a.z.mu.Unlock()
	if !got.verified || got.locked {
		t.Fatalf("after both proofs: verified=%v locked=%v, want verified and unlocked", got.verified, got.locked)
	}
}

// A lock with no mark is the issuer's (or a failed revocation's): neither a
// wrong code nor a proof lifts it.
func TestProof_AnUnmarkedLockIsNeverLifted(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	u.locked = true
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
		t.Fatalf("wrong code: %d", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("a wrong code lifted a lock that was not a proof's")
	}
	// A FRESH mark from a proof that died mid-way is a proof's lock: the
	// next failed proof takes it over and may lift it.
	h.z.mu.Lock()
	u.metadata = map[string]string{MetaProofLock: newProofMark(time.Now().Add(-time.Minute))}
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
		t.Fatalf("wrong code: %d", status)
	}
	if h.z.user(u.id).locked {
		t.Fatal("a fresh proof lock was never lifted")
	}
}

// The re-review at ce91073b, finding 4: a mark some earlier proof failed to
// clear must not make a later issuer lockout look like a proof's. A mark
// older than proofLockTTL names no proof; the lock under it stays, through
// a wrong code and through a successful proof alike.
func TestProof_AStaleMarkLeavesTheIssuersLockoutInPlace(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	u.locked = true
	u.metadata = map[string]string{MetaProofLock: newProofMark(time.Now().Add(-proofLockTTL - time.Minute))}
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
		t.Fatalf("wrong code: %d", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("a wrong code lifted an issuer lockout under a stale mark")
	}
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("a proof lifted an issuer lockout under a stale mark")
	}
	for mark, fresh := range map[string]bool{
		"": false, "stale": false, "x.nonce": false, "123.": false,
		newProofMark(time.Now()): true, newProofMark(time.Now().Add(-proofLockTTL)): false,
		newProofMark(time.Now().Add(time.Minute)): true, newProofMark(time.Now().Add(proofLockTTL + time.Minute)): false,
	} {
		if got := freshProofMark(mark, time.Now()); got != fresh {
			t.Errorf("freshProofMark(%q) = %v, want %v", mark, got, fresh)
		}
	}
}

// Finding 4, the other half: a mark that fails to clear once is retried.
func TestProof_AMarkThatFailsToClearOnceIsRetried(t *testing.T) {
	// The failed deletes also keep this proof's window: its own wait for it
	// is short here.
	shrinkProofWindow(t, 100*time.Millisecond, 50*time.Millisecond)
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	h.z.failMetadataDeletes = 1
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d", status)
	}
	if _, marked := h.z.user(u.id).metadata[MetaProofLock]; marked {
		t.Fatal("one failed delete left the proof lock mark on the account")
	}
}

// Finding 1: on another replica, a failed proof cleared its mark, this
// proof read "locked, no mark" (so not its lock to lift), and then the
// failed proof's unlock landed before this proof's re-lock. The re-lock
// locked the account, so this call lifts it: before, it stayed locked for
// good (locked, unmarked — read by every later proof as the issuer's).
func TestProof_ARelockThatLockedTheAccountIsLiftedAfterwards(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	code := h.z.lastMail(t).code
	h.z.mu.Lock()
	u.locked = true // the other replica's lock, its mark already cleared
	h.z.mu.Unlock()
	c := h.z.client()
	ctx := context.Background()
	before, err := c.FindUserByID(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.routes.proveAddress(ctx, before, func() error {
		if err := c.UnlockUser(ctx, u.id); err != nil { // the other replica's unlock lands
			return err
		}
		return c.VerifyEmail(ctx, u.id, code)
	}); err != nil {
		t.Fatal(err)
	}
	got := h.z.user(u.id)
	if got.locked {
		t.Fatal("OWNER LOCKED OUT: the proof re-locked the account and never lifted that lock")
	}
	if !got.verified || signsIn(t, h, "victim@acme.com", attackerPassword) {
		t.Fatal("the proof did not verify and revoke")
	}
}

// Finding 1, the other half: an unlock that fails after a refused proof puts
// the mark back, so the lock stays a proof's and the owner's proof lifts it.
func TestProof_AFailedUnlockPutsTheMarkBack(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	h.z.failUnlock = true
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 {
		t.Fatalf("wrong code: %d", status)
	}
	got := h.z.user(u.id)
	if !got.locked || !freshProofMark(got.metadata[MetaProofLock], time.Now()) {
		t.Fatalf("after a failed unlock: locked=%v mark=%q; want locked under a fresh mark", got.locked, got.metadata[MetaProofLock])
	}
	h.z.mu.Lock()
	h.z.failUnlock = false
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d", status)
	}
	if h.z.user(u.id).locked {
		t.Fatal("OWNER LOCKED OUT: the owner's proof did not lift a lock a failed unlock left")
	}
}

// Finding 2: a client that hangs up right after the proof succeeded must not
// leave the account verified, locked and marked: the steps after the proof
// run detached from the request.
func TestProof_TheCleanupOutlivesTheRequest(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	code := h.z.lastMail(t).code
	c := h.z.client()
	before, err := c.FindUserByID(context.Background(), u.id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := h.routes.proveAddress(ctx, before, func() error {
		defer cancel() // the client goes away the moment the proof lands
		return c.VerifyEmail(ctx, u.id, code)
	}); err != nil {
		t.Fatal(err)
	}
	got := h.z.user(u.id)
	if _, marked := got.metadata[MetaProofLock]; got.locked || marked {
		t.Fatalf("after a cancelled request: locked=%v marked=%v; want neither", got.locked, marked)
	}
	if signsIn(t, h, "victim@acme.com", attackerPassword) {
		t.Fatal("the planted password survived: the revocation stopped with the request")
	}
}

func TestRevokeCredentials_AnyOtherRefusalFailsTheRevocation(t *testing.T) {
	if !isAbsent(&apiError{status: 404, body: `{"code":5,"details":[{"id":"COMMAND-Hd9sd"}]}`}) ||
		!isAbsent(&apiError{status: 404, body: `{"code":5,"details":[{"id":"COMMAND-Sr3h3"}]}`}) {
		t.Fatal("the issuer's \"no such factor\" is not read as absent")
	}
	for _, body := range []string{`{"code":3,"details":[{"id":"COMMAND-SF3fg"}]}`, `{"code":9}`, `not json`} {
		if isAbsent(&apiError{status: 400, body: body}) {
			t.Errorf("isAbsent(400 %s) = true: an unrelated refusal would pass for \"no factor\"", body)
		}
	}
}

func TestRevokeCredentials_SecondFactorsAndSessions(t *testing.T) {
	z := newFakeZitadel(t)
	u := z.addUser("v@acme.com", false, "planted-password")
	z.mu.Lock()
	u.totp = true
	z.sessions["s-old"] = &fakeSession{token: "t", userID: u.id, verified: []string{"password"}}
	z.mu.Unlock()
	if _, err := z.client().RevokeCredentials(context.Background(), u.id); err != nil {
		t.Fatal(err)
	}
	if z.user(u.id).totp {
		t.Error("a planted TOTP survived the revocation")
	}
	if !z.sessions["s-old"].deleted {
		t.Error("a session from before the revocation was not ended")
	}
}

func TestTakeover_RevokeAndLockFailingStillReplacesThePassword(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	h.z.failLinks = true
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != http.StatusBadGateway {
		t.Fatalf("verify answered %d, want 502", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("the account is not locked")
	}
	if h.z.user(u.id).password == attackerPassword {
		t.Fatal("the planted password was not replaced on the last attempt")
	}
}

// ── B. The pending password's sealed size depends only on its length ───

func TestRegister_HTMLPasswordsSealForEveryAddressAlike(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	h.z.addUser("verified@acme.com", true, "x-password-1")
	h.z.addUser("pending@acme.com", false, "")
	html := strings.Repeat("<>&", 85) + "x" // 256 bytes; 1531 if HTML-escaped
	lengths := map[string]int{}
	for _, email := range []string{"verified@acme.com", "pending@acme.com", "brand-new@acme.com"} {
		b := h.browser()
		status, body := b.post("/auth/register", map[string]any{"email": email, "password": html})
		if status != 202 {
			t.Fatalf("%s: %d %v", email, status, body)
		}
		lengths[email] = len(cookieValue(b, "app_pwp"))
		if email == "brand-new@acme.com" {
			u := h.z.userByEmail(email)
			if len(u.id) != 18 {
				t.Fatalf("fake ids must be issuer-shaped (18 digits), got %q", u.id)
			}
			if status, body := b.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 || body["password"] != nil {
				t.Fatalf("verify: %d %v", status, body)
			}
			if !signsIn(t, h, email, html) {
				t.Fatal("the HTML-character password was lost between sign-up and verification")
			}
		}
	}
	if lengths["verified@acme.com"] == 0 || lengths["verified@acme.com"] != lengths["pending@acme.com"] || lengths["pending@acme.com"] != lengths["brand-new@acme.com"] {
		t.Fatalf("sealed password cookie lengths differ by account state: %v", lengths)
	}
}

func TestRegister_TooLongIsOneAnswerBeforeTheLookup(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	h.z.addUser("verified@acme.com", true, "")
	long := strings.Repeat("a", maxPasswordBytes+1)
	for _, email := range []string{"verified@acme.com", "brand-new@acme.com"} {
		before := h.z.callCount("POST /v2/users")
		status, body := h.browser().post("/auth/register", map[string]any{"email": email, "password": long})
		want(t, status, body, 400, CodePasswordTooLong)
		if h.z.callCount("POST /v2/users") != before {
			t.Fatalf("%s: the address was looked up for a password that was refused anyway", email)
		}
	}
}

func TestRegister_ASealFailureIsNeverACheckYourEmail(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods), func(c *SignInRoutesConfig) { c.Client.Token = "" })
	status, _ := h.browser().post("/auth/register", map[string]any{"email": "new@acme.com", "password": "a-long-password"})
	if status == 202 {
		t.Fatal("register answered 202 though the password could not be kept")
	}
}

// ── Low ────────────────────────────────────────────────────────────────

func TestVerifyResendHasTheResponseFloor(t *testing.T) {
	const floor = 120 * time.Millisecond
	h := newHarness(t, policyWith(SignupOpen, allMethods), func(c *SignInRoutesConfig) { c.ResponseFloor = floor })
	start := time.Now()
	h.browser().post("/auth/verify/resend", map[string]any{"email": "nobody@acme.com"})
	if took := time.Since(start); took < floor {
		t.Fatalf("verify/resend answered in %v, under the %v floor", took, floor)
	}
}

func TestSignupRoleIsGrantedOnceOnVerify(t *testing.T) {
	p := policyWith(SignupOpen, allMethods)
	p.SignupRole = "member"
	h := newHarness(t, p)
	b := h.browser()
	b.post("/auth/register", map[string]any{"email": "self@acme.com", "password": "self-password-1"})
	u := h.z.userByEmail("self@acme.com")
	b.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if n := h.z.callCount("POST /management/v1/users/" + u.id + "/grants"); n != 1 {
		t.Fatalf("the sign-up role was asked for %d times, want 1", n)
	}
}

func TestClientKeyCutsIPv6ToItsSlash64(t *testing.T) {
	key := func(remote string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		return ClientKey(req, 0)
	}
	a, b := key("[2001:db8:1:2:aaaa::1]:443"), key("[2001:db8:1:2:ffff:ffff:ffff:ffff]:443")
	if a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("one /64 gave keys %q and %q", a, b)
	}
	if key("[2001:db8:1:3::1]:443") == a {
		t.Fatal("another /64 shares the key")
	}
	if got := key("198.51.100.7:443"); got != "198.51.100.7" {
		t.Fatalf("IPv4 key %q", got)
	}
}

func TestKeyedMutex_SerialisesAKeyAndForgetsIt(t *testing.T) {
	var k keyedMutex
	release, err := k.lock(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := k.lock(context.Background(), "u2")
	if err != nil {
		t.Fatal("another key waited on u1")
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := k.lock(ctx, "u1"); err == nil {
		t.Fatal("a second holder of u1 got in while the first held it")
	}
	release()
	again, err := k.lock(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if len(k.keys) != 0 {
		t.Fatalf("keys outlive their holders: %v", k.keys)
	}
}

// The re-review at 832d5afc, B1: with mail "none" and no verified-email
// requirement, an unverified account is a working one. A wrong verification
// or invitation code — from anyone who knows the user id — must leave its
// password, passkeys, links, second factor and sessions exactly as they were.
func TestProof_AWrongCodeLeavesAWorkingUnverifiedAccountIntact(t *testing.T) {
	p := LoginPolicy{Present: true, Version: 1, Signup: SignupOpen,
		Methods: []string{MethodPassword}, Mail: MailNone, RequireVerifiedEmail: false}
	h := newHarness(t, p)
	owner := h.browser()
	if status, body := owner.post("/auth/register", map[string]any{"email": "owner@else.com", "password": "owner-password-1"}); status != 200 {
		t.Fatalf("register: %d %v", status, body)
	}
	u := h.z.userByEmail("owner@else.com")
	h.z.mu.Lock()
	u.passkeys = []Passkey{{ID: "pk-1", Name: "laptop"}}
	u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-1", UserName: "owner@else.com"}}
	u.totp = true
	h.z.sessions["s-owner"] = &fakeSession{token: "s-owner-token", userID: u.id}
	h.z.mu.Unlock()

	for _, attempt := range []struct{ path, proof string }{
		{"/auth/verify", "/email/verify"},
		{"/auth/invite/accept", "/invite_code/verify"},
	} {
		proofs := h.z.callCount("POST /v2/users/" + u.id + attempt.proof)
		status, body := h.browser().post(attempt.path, map[string]any{
			"userID": u.id, "code": "a-wrong-code", "method": MethodPassword, "password": "attacker-password-9"})
		want(t, status, body, 401, CodeInvalidCode)
		if h.z.callCount("POST /v2/users/"+u.id+attempt.proof) == proofs {
			t.Fatalf("%s: the issuer never checked the code, so this proves nothing", attempt.path)
		}
	}

	if !signsIn(t, h, "owner@else.com", "owner-password-1") {
		t.Fatal("a wrong code replaced the owner's password")
	}
	got := h.z.user(u.id)
	h.z.mu.Lock()
	defer h.z.mu.Unlock()
	if got.verified || got.locked {
		t.Errorf("verified=%v locked=%v, want unverified and unlocked", got.verified, got.locked)
	}
	if len(got.passkeys) != 1 || len(got.links) != 1 || !got.totp {
		t.Errorf("a wrong code took credentials: passkeys=%v links=%v totp=%v", got.passkeys, got.links, got.totp)
	}
	if h.z.sessions["s-owner"].deleted {
		t.Error("a wrong code ended the owner's session")
	}
	if got.metadata[MetaProofLock] != "" {
		t.Errorf("a wrong code left a proof mark: %q", got.metadata[MetaProofLock])
	}
}

// shrinkProofWindow shortens the pre-proof window and its slack for one
// test, so a successful proof's wait for another proof's window is short.
func shrinkProofWindow(t *testing.T, window, slack time.Duration) {
	t.Helper()
	w, s := preProofWindow, proofFenceSlack
	preProofWindow, proofFenceSlack = window, slack
	t.Cleanup(func() { preProofWindow, proofFenceSlack = w, s })
}

// The forge#1821 review, B1, forced deterministically: replica A's
// wrong-code proof has announced its pre-proof window and is held right
// before it takes the account's credentials back; replica B's proof takes the
// lock over, verifies the address and hands the account back; the owner then
// sets a password, as HandleVerify does after a proof. Only then is A's
// request let through. A's cleanup must not land on the owner's credential:
// every call it sends is bounded by its window, and B waits that window out
// before handing the account back.
func TestTakeover_AStaleProofsCleanupNeverRevokesTheOwnersNewCredential(t *testing.T) {
	shrinkProofWindow(t, 300*time.Millisecond, 100*time.Millisecond)
	policy := policyWith(SignupOpen, allMethods)
	a := newHarness(t, policy)
	b := newHarnessOn(t, a.z, policy)
	u := preRegister(t, a)
	code := a.z.lastMail(t).code
	c := a.z.client()
	ctx := context.Background()
	before, err := c.FindUserByID(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}

	// A's first credential read is its revocation's (whether a password is
	// set): hold it until the owner has a new password. Only the first: a
	// sync.Once would hold B's read too, until A's returned.
	paused, resume := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	a.z.mu.Lock()
	a.z.beforeRequest = func(r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/authentication_methods") && first.CompareAndSwap(false, true) {
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
	select {
	case <-paused:
	case err := <-aDone:
		t.Fatalf("A's proof never reached its pre-proof revocation (returned %v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("A's proof never reached its pre-proof revocation")
	}

	if _, err := b.routes.proveAddress(ctx, before, func() error { return c.VerifyEmail(ctx, u.id, code) }); err != nil {
		t.Fatalf("the owner's proof: %v", err)
	}
	const ownerPassword = "owner-password-1"
	if err := c.SetPassword(ctx, u.id, ownerPassword); err != nil {
		t.Fatal(err)
	}
	close(resume) // A's held request goes through now, after the owner's password
	select {
	case err := <-aDone:
		if err == nil {
			t.Error("A's wrong-code proof succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("A's proof never returned")
	}
	// Let anything A could still send arrive.
	// forge:lint-disable-next-line forge-test-fixed-sleep: a negative wait — gives a late cleanup from A the chance to land; a correct fix passes however long this takes
	time.Sleep(50 * time.Millisecond)
	if !signsIn(t, a, "victim@acme.com", ownerPassword) {
		t.Fatal("PROOF RACE: a wrong-code proof's late cleanup revoked the password the owner set after proving the address")
	}
	got := a.z.user(u.id)
	a.z.mu.Lock()
	defer a.z.mu.Unlock()
	if !got.verified || got.locked {
		t.Fatalf("after both proofs: verified=%v locked=%v, want verified and unlocked", got.verified, got.locked)
	}
}

// A proof that announces its window and then finds the address verified by
// another proof takes nothing from the account: what it holds now was set
// after a proof.
func TestProof_AnAddressVerifiedSinceTheFirstReadIsLeftAlone(t *testing.T) {
	policy := policyWith(SignupOpen, allMethods)
	h := newHarness(t, policy)
	u := preRegister(t, h)
	code := h.z.lastMail(t).code
	c := h.z.client()
	ctx := context.Background()
	before, err := c.FindUserByID(ctx, u.id) // unverified
	if err != nil {
		t.Fatal(err)
	}
	if err := c.VerifyEmail(ctx, u.id, code); err != nil {
		t.Fatal(err)
	}
	const ownerPassword = "owner-password-1"
	if err := c.SetPassword(ctx, u.id, ownerPassword); err != nil {
		t.Fatal(err)
	}
	// The stale read says unverified; the first re-read already sees the
	// proof, so force the announcement path by locking the account under a
	// fresh foreign mark, as another proof would leave it.
	if err := c.SetMetadata(ctx, u.id, MetaProofLock, newProofMark(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := h.routes.proveAddress(ctx, before, func() error { return ErrInvalidCode }); err == nil {
		t.Fatal("a wrong code was accepted")
	}
	if !signsIn(t, h, "victim@acme.com", ownerPassword) {
		t.Fatal("a wrong-code proof on a verified address revoked its password")
	}
}

// Windows older than proofLockTTL are left over: a successful proof neither
// waits for them nor keeps them; a live one is waited out.
func TestAwaitPreProofWindows_WaitsOutLiveWindowsAndDropsStaleOnes(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	c := h.z.client()
	ctx := context.Background()
	stale := MetaProofWindowPrefix + "stale"
	if err := c.SetMetadata(ctx, u.id, stale, strconv.FormatInt(time.Now().Add(-2*proofLockTTL).UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	// At the real window (15s + 5s slack) a wait for the stale window would
	// take 20s: far past this bound, which load cannot reach.
	start := time.Now()
	h.routes.awaitPreProofWindows(ctx, u.id)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("waited %v for a stale window", d)
	}
	if _, ok, _ := c.Metadata(ctx, u.id, stale); ok {
		t.Fatal("a stale window was kept")
	}
	shrinkProofWindow(t, 200*time.Millisecond, 50*time.Millisecond)
	live := MetaProofWindowPrefix + "live"
	opened := time.Now()
	if err := c.SetMetadata(ctx, u.id, live, strconv.FormatInt(opened.UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	h.routes.awaitPreProofWindows(ctx, u.id)
	if d := time.Since(opened); d < 240*time.Millisecond {
		t.Fatalf("returned %v after a live window opened, want at least the window plus its slack", d)
	}
}
