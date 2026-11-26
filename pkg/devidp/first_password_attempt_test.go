package devidp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// forge#2179 review (password.go:385): first-password install attempts are
// records safe to leave. An ambiguous failure keeps them, a wrong code removes
// only its own, and only a password set at or after an attempt — never a
// squatter's earlier one — lets a later reset verify the address, however
// much later it comes.

func provenBroker(z *fakeZitadel) *Client {
	c := z.client() // shares the issuer's clock
	c.ProvenCredentialsOnly = true
	return c
}

// withClock returns the clock the fake issuer and its clients share.
func withClock(z *fakeZitadel) *fakeClock { return z.clk }

func resetCode(t *testing.T, z *fakeZitadel, id string) string {
	t.Helper()
	code, err := z.client().MintPasswordResetCode(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// recoverWith redeems a fresh code and reports whether the address ended
// verified with that password.
func recoverWith(t *testing.T, z *fakeZitadel, id, password string) bool {
	t.Helper()
	if err := provenBroker(z).ResetPassword(context.Background(), PasswordReset{UserID: id, Code: resetCode(t, z, id), NewPassword: password}); err != nil {
		t.Fatalf("the recovery reset: %v", err)
	}
	verified, pw := z.passwordState(id)
	return verified && pw == password
}

func TestFirstPasswordAttempt_ALostAnswerAfterTheInstallStaysRecoverable(t *testing.T) {
	z := newFakeZitadel(t)
	u := z.addUser("invitee@acme.com", false, "")
	z.mu.Lock()
	z.passwordSetAnswerLost = true
	z.mu.Unlock()
	err := provenBroker(z).ResetPassword(context.Background(), PasswordReset{UserID: u.id, Code: resetCode(t, z, u.id), NewPassword: "first-password-1"})
	if err == nil || errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("a lost answer: %v, want an ambiguous failure", err)
	}
	if verified, pw := z.passwordState(u.id); verified || pw != "first-password-1" {
		t.Fatalf("precondition: want the password installed and the address unverified (verified=%v)", verified)
	}
	if !recoverWith(t, z, u.id, "first-password-2") {
		t.Fatal("after a lost answer, a new reset code did not verify the account it installed a first password on")
	}
}

// A valid first password whose verification fails, and a wrong code, on the
// same account at once — in both orders, held deterministically at the
// issuer's password write.
func TestFirstPasswordAttempt_AWrongCodeBesideAValidOneNeverBreaksRecovery(t *testing.T) {
	for _, validFirst := range []bool{true, false} {
		name := "the wrong code lands while the valid write is held"
		if !validFirst {
			name = "the valid write lands while the wrong code is held"
		}
		t.Run(name, func(t *testing.T) {
			z := newFakeZitadel(t)
			u := z.addUser("invitee@acme.com", false, "")
			code := resetCode(t, z, u.id)
			held, release := make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			z.mu.Lock()
			z.beforeRequest = func(r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/users/"+u.id+"/password") && first.CompareAndSwap(false, true) {
					close(held)
					<-release
				}
			}
			z.failEmailResends = 1 // the valid one's verification fails
			z.mu.Unlock()
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			valid := func() error {
				return provenBroker(z).ResetPassword(context.Background(), PasswordReset{UserID: u.id, Code: code, NewPassword: "first-password-1"})
			}
			wrong := func() error {
				return provenBroker(z).ResetPassword(context.Background(), PasswordReset{UserID: u.id, Code: "a-wrong-code", NewPassword: "attacker-password-9"})
			}
			heldOne, other := valid, wrong
			if !validFirst {
				heldOne, other = wrong, valid
			}
			done := make(chan error, 1)
			go func() { done <- heldOne() }()
			testkit.Receive(t, "the first password write at the issuer", held, testkit.Within(10*time.Second))
			otherErr := other()
			close(release)
			heldErr := testkit.Receive(t, "the held request", done, testkit.Within(10*time.Second))
			validErr, wrongErr := heldErr, otherErr
			if !validFirst {
				validErr, wrongErr = otherErr, heldErr
			}
			if !errors.Is(wrongErr, ErrInvalidResetCode) {
				t.Fatalf("the wrong code: %v, want ErrInvalidResetCode", wrongErr)
			}
			if validErr == nil {
				t.Fatal("precondition: the valid first password's verification was meant to fail")
			}
			if verified, pw := z.passwordState(u.id); verified || pw != "first-password-1" {
				t.Fatalf("precondition: want the valid password installed, unverified (verified=%v)", verified)
			}
			if !recoverWith(t, z, u.id, "first-password-2") {
				t.Fatal("a wrong code beside a valid one broke the valid one's recovery")
			}
		})
	}
}

// A squatter's password, set before any attempt, never qualifies — whether
// the attempt record came minutes or days after it.
func TestFirstPasswordAttempt_AnEarlierPasswordNeverQualifies(t *testing.T) {
	for _, gap := range []time.Duration{10 * time.Minute, 72 * time.Hour} {
		t.Run(gap.String(), func(t *testing.T) {
			ctx := context.Background()
			z := newFakeZitadel(t)
			clk := withClock(z)
			s := z.addUser(victimEmail, false, attackerPassword) // passwordChanged: now
			clk.advance(gap)
			if err := z.client().SetMetadata(ctx, s.id, MetaFirstPasswordPrefix+"later", strconv.FormatInt(clk.now().UnixMilli(), 10)); err != nil {
				t.Fatal(err)
			}
			if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: "owner-password-1"}); err != nil {
				t.Fatal(err)
			}
			if verified, _ := z.passwordState(s.id); verified {
				t.Fatal("TAKEOVER: an attempt recorded after the squatter's password made a reset verify the account")
			}
		})
	}
}

// forge#2179 review (password.go:494): an invitee whose verification failed
// during an outage and who comes back a day later recovers the SAME
// account, with its grant. An attempt has no expiry.
func TestFirstPasswordAttempt_RecoveryADayLaterKeepsTheSameAccount(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	clk := withClock(z)
	u := z.addUser("invitee@acme.com", false, "")
	z.mu.Lock()
	z.grants = append(z.grants, u.id+":member")
	z.failEmailResends = 1 // the first install's verification fails
	z.mu.Unlock()
	if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: u.id, Code: resetCode(t, z, u.id), NewPassword: "first-password-1"}); err == nil {
		t.Fatal("precondition: the first install's verification was meant to fail")
	}
	if verified, pw := z.passwordState(u.id); verified || pw != "first-password-1" {
		t.Fatalf("precondition: want the password installed and the address unverified (verified=%v)", verified)
	}

	clk.advance(26 * time.Hour)
	if !recoverWith(t, z, u.id, "first-password-2") {
		t.Fatal("a day after a failed verification, a new reset code did not verify the account it installed a first password on")
	}
	if got := z.userByEmail("invitee@acme.com"); got == nil || got.id != u.id {
		t.Fatal("recovery did not keep the same account")
	}
	z.mu.Lock()
	granted := slices.Contains(z.grants, u.id+":member")
	z.mu.Unlock()
	if !granted {
		t.Fatal("recovery lost the account's grant")
	}
	if attempts, err := z.client().MetadataWithPrefix(ctx, u.id, MetaFirstPasswordPrefix); err != nil || len(attempts) != 0 {
		t.Fatalf("attempt records left on a verified account: %v (err %v)", attempts, err)
	}
}

// A malformed record, or one dated past the skew in the future, is absent:
// it never qualifies a password.
func TestFirstPasswordAttempt_AMalformedRecordIsAbsent(t *testing.T) {
	for name, value := range map[string]func(now time.Time) string{
		"not a number": func(time.Time) string { return "yesterday" },
		"zero":         func(time.Time) string { return "0" },
		"negative":     func(time.Time) string { return "-5" },
		"in the future": func(now time.Time) string {
			return strconv.FormatInt(now.Add(firstPasswordClockSkew+time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			z := newFakeZitadel(t)
			clk := withClock(z)
			s := z.addUser(victimEmail, false, attackerPassword)
			if err := z.client().SetMetadata(ctx, s.id, MetaFirstPasswordPrefix+"bad", value(clk.now())); err != nil {
				t.Fatal(err)
			}
			if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: "owner-password-1"}); err != nil {
				t.Fatal(err)
			}
			if verified, _ := z.passwordState(s.id); verified {
				t.Fatal("a malformed attempt record made a reset verify the account")
			}
		})
	}
}

// squatterWithALaterAttempt is a pre-registered, unverified account holding
// the squatter's password, a session that passed it, and an attempt record
// dated after that password: the state forge#2179's review (password.go:408)
// names.
func squatterWithALaterAttempt(t *testing.T, z *fakeZitadel) *fakeUser {
	t.Helper()
	clk := withClock(z)
	s := z.addUser(victimEmail, false, attackerPassword)
	z.mu.Lock()
	z.sessions["s-attacker"] = &fakeSession{token: "s-attacker-token", userID: s.id, verified: []string{"password"}}
	z.mu.Unlock()
	clk.advance(10 * time.Minute)
	if err := z.client().SetMetadata(context.Background(), s.id, MetaFirstPasswordPrefix+"later", strconv.FormatInt(clk.now().UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	return s
}

// assertSquatterStillOut fails when the address got verified or the
// squatter's retained session completes a sign-in.
func assertSquatterStillOut(t *testing.T, z *fakeZitadel, s *fakeUser, after string) {
	t.Helper()
	if verified, _ := z.passwordState(s.id); verified {
		t.Fatalf("TAKEOVER: %s verified the squatter's account", after)
	}
	_, err := provenBroker(z).CompleteSignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true},
		Session{ID: "s-attacker", Token: "s-attacker-token"})
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("TAKEOVER: the squatter's retained session completed sign-in after %s (err %v)", after, err)
	}
}

// forge#2179 review (password.go:408): a reset that does not qualify still
// replaces the password, and that replacement is dated after the attempt
// record. It must not make the NEXT valid reset qualify and verify the
// squatter's account, which would let the squatter's retained session in.
func TestFirstPasswordAttempt_ADisqualifiedResetNeverQualifiesTheNext(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	clk := withClock(z)
	s := squatterWithALaterAttempt(t, z)
	for i, pw := range []string{"owner-password-1", "owner-password-2", "owner-password-3"} {
		if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: pw}); err != nil {
			t.Fatalf("reset %d: %v", i+1, err)
		}
		if _, got := z.passwordState(s.id); got != pw {
			t.Fatalf("reset %d did not replace the password", i+1)
		}
		assertSquatterStillOut(t, z, s, "reset "+strconv.Itoa(i+1))
		clk.advance(time.Hour)
	}
	if _, ok, _ := z.client().Metadata(ctx, s.id, MetaFirstPasswordDisqualified); !ok {
		t.Fatal("the account was not marked disqualified")
	}
}

// The mark is written BEFORE the replacing password, so a reset on another
// replica that reads the replacement also reads the mark.
func TestFirstPasswordAttempt_TheMarkPrecedesTheReplacement(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	s := squatterWithALaterAttempt(t, z)
	var markedAtWrite atomic.Bool
	var seen atomic.Bool
	z.mu.Lock()
	z.beforeRequest = func(r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/users/"+s.id+"/password") && seen.CompareAndSwap(false, true) {
			z.mu.Lock()
			_, ok := z.users[s.id].metadata[MetaFirstPasswordDisqualified]
			z.mu.Unlock()
			markedAtWrite.Store(ok)
		}
	}
	z.mu.Unlock()
	if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: "owner-password-1"}); err != nil {
		t.Fatal(err)
	}
	if !seen.Load() || !markedAtWrite.Load() {
		t.Fatalf("the password write reached the issuer before the mark (write seen %v, marked %v)", seen.Load(), markedAtWrite.Load())
	}
}

// A reset that cannot write the mark does not replace the password: it
// fails closed rather than leave a replacement nothing disqualifies.
func TestFirstPasswordAttempt_AMarkThatCannotBeWrittenStopsTheReset(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	s := squatterWithALaterAttempt(t, z)
	z.mu.Lock()
	z.failMetadataWrites = 1
	z.mu.Unlock()
	if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: "owner-password-1"}); err == nil {
		t.Fatal("a reset whose mark could not be written succeeded")
	}
	if _, pw := z.passwordState(s.id); pw != attackerPassword {
		t.Fatal("a reset whose mark could not be written replaced the password")
	}
	if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: s.id, Code: resetCode(t, z, s.id), NewPassword: "owner-password-2"}); err != nil {
		t.Fatal(err)
	}
	assertSquatterStillOut(t, z, s, "the retried reset")
}

// A marked account that holds no credential (an administrator removed its
// password) takes a first password without being verified, and records no
// attempt that could qualify a later reset.
func TestFirstPasswordAttempt_AMarkedAccountIsNeverVerifiedByAFirstPassword(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	u := z.addUser(victimEmail, false, "")
	if err := z.client().SetMetadata(ctx, u.id, MetaFirstPasswordDisqualified, "1"); err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"owner-password-1", "owner-password-2"} {
		if err := provenBroker(z).ResetPassword(ctx, PasswordReset{UserID: u.id, Code: resetCode(t, z, u.id), NewPassword: pw}); err != nil {
			t.Fatal(err)
		}
		if verified, got := z.passwordState(u.id); verified || got != pw {
			t.Fatalf("want %q installed and the address unverified (verified=%v)", pw, verified)
		}
	}
	if attempts, _ := z.client().MetadataWithPrefix(ctx, u.id, MetaFirstPasswordPrefix); len(attempts) != 0 {
		t.Fatalf("a marked account recorded first-password attempts: %v", attempts)
	}
}
