package devidp

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// forge#2121, settled by removing its cause: no credential is ever put on an
// unverified account, and a proof never verifies an account that holds one
// (its owner proves into a fresh account instead). These tests pin both
// halves end to end against the fake issuer.

const victimEmail = "victim@acme.com"

// signInGated signs in as the scaffolded broker does under a verified-email
// policy — its client is ProvenCredentialsOnly — and the session is refused
// while the account's address is unverified.
func signInGated(h *harness, login, password string) error {
	c := h.z.client()
	c.ProvenCredentialsOnly = true
	_, err := c.SignIn(context.Background(),
		FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true},
		Credentials{LoginName: login, Password: password})
	return err
}

// squat leaves the state an older broker (or a hand-made account) could:
// an UNVERIFIED account for the victim's address holding a password, a
// passkey, a social link, a TOTP and a live session, none of them proven.
func squat(t *testing.T, h *harness) *fakeUser {
	t.Helper()
	u := h.z.addUser(victimEmail, false, attackerPassword)
	h.z.mu.Lock()
	defer h.z.mu.Unlock()
	u.passkeys = []Passkey{{ID: "pk-attacker", Name: "attacker"}}
	u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-attacker", UserName: victimEmail}}
	u.totp = true
	h.z.sessions["s-attacker"] = &fakeSession{token: "s-attacker-token", userID: u.id, verified: []string{"password"}}
	return u
}

// credentialsOf is what the issuer reports the account can sign in with.
func credentialsOf(t *testing.T, h *harness, id string) []string {
	t.Helper()
	kinds, err := h.z.client().credentialKinds(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return kinds
}

// usersWithEmail counts the accounts the issuer holds for an address.
func usersWithEmail(h *harness, email string) []*fakeUser {
	h.z.mu.Lock()
	defer h.z.mu.Unlock()
	var out []*fakeUser
	for _, u := range h.z.users {
		if u.email == email {
			out = append(out, u)
		}
	}
	return out
}

// ── 1. A sign-up that fails or is abandoned adds no credential ─────────

func TestEmailFirst_AFailedOrAbandonedSignUpAddsNoCredential(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	attacker := h.browser()
	if status, body := attacker.post("/auth/register", map[string]any{"email": victimEmail, "password": attackerPassword}); status != 202 {
		t.Fatalf("register: %d %v", status, body)
	}
	u := h.z.userByEmail(victimEmail)
	// Abandoned: the account exists so the issuer could mail its code, and
	// holds nothing.
	if kinds := credentialsOf(t, h, u.id); len(kinds) != 0 || u.verified {
		t.Fatalf("an unproven sign-up holds %v (verified=%v), want no credential", kinds, u.verified)
	}

	// Failed: a wrong code, by verification link and by magic link, changes
	// nothing on the account — not a credential, session or metadata key.
	before := h.z.snapshot(u.id)
	status, body := attacker.post("/auth/verify", map[string]any{"userID": u.id, "code": "a-wrong-code"})
	want(t, status, body, 401, CodeInvalidCode)
	if after := h.z.snapshot(u.id); after != before {
		t.Fatalf("a failed verification changed the account:\n before %s\n after  %s", before, after)
	}
	attacker.post("/auth/magic/request", map[string]any{"email": victimEmail})
	before = h.z.snapshot(u.id) // the request mailed a fresh code; the proof below must change nothing
	status, body = attacker.post("/auth/magic/consume", map[string]any{"code": "a-wrong-code"})
	want(t, status, body, 401, CodeInvalidCode)
	if after := h.z.snapshot(u.id); after != before {
		t.Fatalf("a failed magic link changed the account:\n before %s\n after  %s", before, after)
	}
	if err := signInGated(h, victimEmail, attackerPassword); err == nil {
		t.Fatal("the registrant's password signs in to an address they never proved")
	}
}

// ── 2. No credential is ever installed on an unverified account ────────

func TestEmailFirst_NoCredentialCanBeAddedToAnUnverifiedAccount(t *testing.T) {
	z := newFakeZitadel(t)
	c := z.client()
	c.ProvenCredentialsOnly = true
	ctx := context.Background()
	u := z.addUser("pending@acme.com", false, "")
	// A reset code the person presents is not here: redeeming one proves
	// the address (TestEmailFirst_APresentedResetCodeProvesTheAddress).
	before := z.snapshot(u.id)
	for name, add := range map[string]func() error{
		"password (SetPassword)": func() error { return c.SetPassword(ctx, u.id, "a-long-password") },
		"social link": func() error {
			return c.AddIDPLink(ctx, u.id, IDPLink{IdPID: "idp-google", UserID: "g-1", UserName: "x"})
		},
		"passkey code": func() error {
			_, err := c.MintPasskeyRegistrationCode(ctx, u.id)
			return err
		},
		"passkey registration": func() error {
			_, err := c.RegisterPasskey(ctx, u.id, "app.test", nil)
			return err
		},
		"passkey verification": func() error {
			return c.VerifyPasskeyRegistration(ctx, u.id, "pk-1", []byte(`{}`), "laptop")
		},
		"one-time-code method": func() error { return c.EnsureOTPEmail(ctx, u.id) },
	} {
		if err := add(); !errors.Is(err, ErrUnverifiedAccount) {
			t.Errorf("%s on an unverified account: %v, want ErrUnverifiedAccount", name, err)
		}
	}
	if after := z.snapshot(u.id); after != before {
		t.Fatalf("a refused install wrote to the account:\n before %s\n after  %s", before, after)
	}

	// Created unverified, an account carries nothing either.
	for name, nu := range map[string]NewUser{
		"with a password":    {Email: "new@acme.com", Password: "a-long-password"},
		"with a social link": {Email: "new@acme.com", IDPLinks: []IDPLink{{IdPID: "idp-google", UserID: "g-2", UserName: "new@acme.com"}}},
		"contact-only, with a link": {Email: "new@acme.com", Password: "a-long-password", ContactOnly: true,
			IDPLinks: []IDPLink{{IdPID: "idp-google", UserID: "g-3", UserName: "new@acme.com"}}},
	} {
		if _, err := c.CreateUser(ctx, nu); !errors.Is(err, ErrUnverifiedAccount) {
			t.Errorf("CreateUser %s, unverified: %v, want ErrUnverifiedAccount", name, err)
		}
	}
	if z.userByEmail("new@acme.com") != nil {
		t.Fatal("a refused CreateUser created an account")
	}

	// Once verified, the same calls work: the rule is the address, nothing else.
	z.mu.Lock()
	u.verified = true
	z.mu.Unlock()
	if err := c.SetPassword(ctx, u.id, "a-long-password"); err != nil {
		t.Fatalf("a verified account's first password: %v", err)
	}
}

// A reset code reaches a person only in a mail to the account's address or
// from an administrator who issued it (a console invitation), so redeeming
// one for an unverified account holding NO credential sets the password and
// marks the address verified. One holding a credential nobody proved is
// refused, and nothing is written.
func TestEmailFirst_APresentedResetCodeProvesTheAddress(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	c := z.client()
	c.ProvenCredentialsOnly = true
	u := z.addUser("pending@acme.com", false, "")
	reset, err := z.client().MintPasswordResetCode(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: "wrong-code", NewPassword: "a-long-password"}); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("a wrong code: %v, want ErrInvalidResetCode", err)
	}
	if verified, pw := z.passwordState(u.id); verified || pw != "" {
		t.Fatal("a wrong code verified the address or set a password")
	}
	if err := c.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: reset, NewPassword: "a-long-password"}); err != nil {
		t.Fatalf("the presented code: %v", err)
	}
	if verified, pw := z.passwordState(u.id); !verified || pw != "a-long-password" {
		t.Fatalf("after the presented code: verified=%v password set=%v", verified, pw != "")
	}

	held := z.addUser("held@acme.com", false, "")
	z.mu.Lock()
	held.links = []IDPLink{{IdPID: "idp-google", UserID: "g-held", UserName: "held@acme.com"}}
	z.mu.Unlock()
	code, err := z.client().MintPasswordResetCode(ctx, held.id)
	if err != nil {
		t.Fatal(err)
	}
	before := z.snapshot(held.id)
	if err := c.ResetPassword(ctx, PasswordReset{UserID: held.id, Code: code, NewPassword: "a-long-password"}); !errors.Is(err, ErrUnverifiedAccount) {
		t.Fatalf("an account holding an unproven link: %v, want ErrUnverifiedAccount", err)
	}
	if after := z.snapshot(held.id); after != before {
		t.Fatalf("a refused redemption wrote to the account:\n before %s\n after  %s", before, after)
	}
}

// A first password whose verification fails after the issuer installed it
// (the code is consumed by then) is recoverable on the SAME account: a new
// presented reset code sets the password again and marks the address
// verified. A code the broker mints itself (SetPassword) never does.
func TestEmailFirst_AFailedVerificationAfterTheFirstPasswordIsRecoverable(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	c := z.client()
	c.ProvenCredentialsOnly = true
	u := z.addUser("pending@acme.com", false, "")
	reset, err := z.client().MintPasswordResetCode(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	z.mu.Lock()
	z.failEmailResends = 1
	z.mu.Unlock()
	if err := c.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: reset, NewPassword: "a-long-password"}); err == nil {
		t.Fatal("a failed verification after the password was set reported success")
	}
	if verified, pw := z.passwordState(u.id); verified || pw != "a-long-password" {
		t.Fatalf("after the failed verification: verified=%v password set=%v, want an unverified account with the password", verified, pw != "")
	}

	// The broker's own code proves nothing: replacing the password leaves
	// the address unverified.
	if err := c.SetPassword(ctx, u.id, "another-long-password"); err != nil {
		t.Fatalf("replacing the password of a password-only account: %v", err)
	}
	if verified, _ := z.passwordState(u.id); verified {
		t.Fatal("a broker-minted code marked the address verified")
	}

	next, err := z.client().MintPasswordResetCode(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: next, NewPassword: "a-third-long-password"}); err != nil {
		t.Fatalf("the recovery code: %v", err)
	}
	if verified, pw := z.passwordState(u.id); !verified || pw != "a-third-long-password" {
		t.Fatalf("after recovery: verified=%v password set=%v, want the same account verified", verified, pw != "")
	}
}

// ── 3. Social sign-in with an unverified provider address ──────────────

func TestEmailFirst_AnUnverifiedProviderAddressLinksNothingUntilItsOwnBrowserProvesIt(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	attacker := h.browser()
	id := startSocial(t, attacker)
	h.z.addIntent(id, "tok", intentResult(victimEmail, false))
	if status, body := attacker.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 202 {
		t.Fatalf("social sign-up with an unverified address: %d %v", status, body)
	}
	u := h.z.userByEmail(victimEmail)
	if kinds := credentialsOf(t, h, u.id); len(kinds) != 0 {
		t.Fatalf("the account holds %v before any proof, want nothing", kinds)
	}
	// The victim proves the address in their own browser: the attacker's
	// pending link stays in the attacker's browser, unattached.
	if status, body := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("the owner's verification: %d %v", status, body)
	}
	if n := len(h.z.user(u.id).links); n != 0 {
		t.Fatalf("TAKEOVER: %d link(s) from an unverified provider address reached the proven account", n)
	}
	// And the attacker's provider identity cannot be linked now either: an
	// unverified provider address never links to an existing account.
	again := h.browser()
	id2 := startSocial(t, again)
	h.z.addIntent(id2, "tok2", intentResult(victimEmail, false))
	status, body := again.post("/auth/social/callback", map[string]any{"id": id2, "token": "tok2"})
	want(t, status, body, 409, CodeSocialLinkRequired)
	if n := len(h.z.user(u.id).links); n != 0 {
		t.Fatalf("an unverified provider address was linked: %d", n)
	}
}

// ── 4. The pre-registration takeover, end to end ──────────────────────

// Against the CURRENT broker: whatever the attacker does before the owner
// proves the address, the owner's account holds only what the owner set.
func TestEmailFirst_ThePreRegistrationTakeoverIsImpossible(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	attacker := h.browser()
	attacker.post("/auth/register", map[string]any{"email": victimEmail, "password": attackerPassword})
	id := startSocial(t, attacker)
	h.z.addIntent(id, "tok", intentResult(victimEmail, false))
	attacker.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	u := h.z.userByEmail(victimEmail)
	if kinds := credentialsOf(t, h, u.id); len(kinds) != 0 {
		t.Fatalf("the attacker's sign-ups put %v on an unproven account", kinds)
	}

	victim := h.browser()
	if status, body := victim.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"}); status != 202 {
		t.Fatalf("victim register: %d %v", status, body)
	}
	mail := h.z.lastMail(t)
	if status, body := victim.post("/auth/verify", map[string]any{"userID": mail.userID, "code": mail.code}); status != 200 {
		t.Fatalf("victim verify: %d %v", status, body)
	}
	if err := signInGated(h, victimEmail, attackerPassword); err == nil {
		t.Fatal("TAKEOVER: the attacker's password signs in to the victim's proven account")
	}
	if err := signInGated(h, victimEmail, "victim-password-1"); err != nil {
		t.Fatalf("the victim's own password: %v", err)
	}
	if got := credentialsOf(t, h, u.id); len(got) != 1 || got[0] != "AUTHENTICATION_METHOD_TYPE_PASSWORD" {
		t.Fatalf("the proven account holds %v, want only the victim's password", got)
	}
}

// Against an account an older broker LEFT holding credentials: the owner
// proves into a fresh account, which takes the address over; the old one is
// renamed out of the way with everything on it intact.
func TestEmailFirst_AnAccountHoldingUnprovenCredentialsIsReplacedNotProven(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	s := squat(t, h)

	// It cannot sign in: the address is unverified.
	if err := signInGated(h, victimEmail, attackerPassword); !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("the squatted account signed in: %v", err)
	}
	squatted := h.z.snapshot(s.id)

	victim := h.browser()
	if status, body := victim.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"}); status != 202 {
		t.Fatalf("victim register: %d %v", status, body)
	}
	// The request touched nothing on the squatted account; the code went to
	// a fresh, credential-less account with a username of its own.
	if got := h.z.snapshot(s.id); got != squatted {
		t.Fatalf("an unproven request changed the squatted account:\n before %s\n after  %s", squatted, got)
	}
	mail := h.z.lastMail(t)
	n := h.z.user(mail.userID)
	if n == nil || n.id == s.id || n.verified || n.metadata[MetaSupersedes] != s.id || n.name() == victimEmail {
		t.Fatalf("the code went to %+v, want a fresh account superseding %s", n, s.id)
	}
	if kinds := credentialsOf(t, h, n.id); len(kinds) != 0 {
		t.Fatalf("the fresh account holds %v before its proof", kinds)
	}
	// Asking again reuses it.
	victim.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"})
	if got := len(usersWithEmail(h, victimEmail)); got != 2 {
		t.Fatalf("%d accounts for the address after two sign-ups, want the squatted one and one fresh one", got)
	}
	mail = h.z.lastMail(t)

	if status, body := victim.post("/auth/verify", map[string]any{"userID": n.id, "code": mail.code}); status != 200 || body["credentialsReset"] != false {
		t.Fatalf("victim verify: %d %v", status, body)
	}
	got := h.z.user(n.id)
	h.z.mu.Lock()
	nVerified, nName, nPending := got.verified, got.name(), got.metadata[MetaSupersedes]
	sName, sEmail, sVerified := s.name(), s.email, s.verified
	sMeta := map[string]string{MetaSupersededBy: s.metadata[MetaSupersededBy], MetaSupersededEmail: s.metadata[MetaSupersededEmail]}
	sCreds := []any{s.password, s.passkeys, s.links, s.totp}
	h.z.mu.Unlock()
	if !nVerified || nName != victimEmail || nPending != "" {
		t.Fatalf("the fresh account: verified=%v username=%q supersedes=%q, want verified, the address, done", nVerified, nName, nPending)
	}
	if sName != supersededUsername(s.id) || sEmail != supersededEmail(s.id) || sVerified {
		t.Fatalf("the squatted account: username=%q email=%q verified=%v, want renamed out of the way and still unverified", sName, sEmail, sVerified)
	}
	if sMeta[MetaSupersededBy] != n.id || sMeta[MetaSupersededEmail] != victimEmail {
		t.Fatalf("the squatted account's record: %v", sMeta)
	}
	// Nothing was deleted from it.
	if sCreds[0] != attackerPassword || len(sCreds[1].([]Passkey)) != 1 || len(sCreds[2].([]IDPLink)) != 1 || sCreds[3] != true {
		t.Fatalf("credentials were removed from the squatted account: %v", sCreds)
	}

	if err := signInGated(h, victimEmail, "victim-password-1"); err != nil {
		t.Fatalf("the owner's password on the fresh account: %v", err)
	}
	if err := signInGated(h, victimEmail, attackerPassword); err == nil {
		t.Fatal("TAKEOVER: the attacker's password signs in by the victim's address")
	}
	if err := signInGated(h, supersededUsername(s.id), attackerPassword); !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("the superseded account signed in: %v", err)
	}
	if u, err := h.z.client().FindUserByEmail(context.Background(), victimEmail); err != nil || u.ID != n.id {
		t.Fatalf("the address resolves to %+v (%v), want the fresh account", u, err)
	}
}

// A magic link and an old verification link reach the fresh account too, and
// an invitation naming such an account is an administrator's.
func TestEmailFirst_EveryProofOfAHeldAccountGoesToAFreshOne(t *testing.T) {
	t.Run("magic link", func(t *testing.T) {
		h := newHarness(t, policyWith(SignupOpen, allMethods))
		s := squat(t, h)
		squatted := h.z.snapshot(s.id)
		victim := h.browser()
		victim.post("/auth/magic/request", map[string]any{"email": victimEmail})
		mail := h.z.lastMail(t)
		if mail.userID == s.id {
			t.Fatal("the magic link proves the squatted account")
		}
		status, body := victim.post("/auth/magic/consume", map[string]any{"code": mail.code})
		if status != 200 || body["subject"] != mail.userID {
			t.Fatalf("magic link: %d %v", status, body)
		}
		if got := h.z.user(mail.userID); got.name() != victimEmail {
			t.Fatalf("the fresh account did not take the address over: %q", got.name())
		}
		if h.z.snapshot(s.id) == squatted {
			t.Fatal("the squatted account was not moved out of the way")
		}
	})
	t.Run("a verification link mailed for the held account", func(t *testing.T) {
		h := newHarness(t, policyWith(SignupOpen, allMethods))
		s := squat(t, h)
		h.z.mu.Lock()
		s.emailCode = "ev-old"
		h.z.mu.Unlock()
		squatted := h.z.snapshot(s.id)
		status, body := h.browser().post("/auth/verify", map[string]any{"userID": s.id, "code": "ev-old"})
		want(t, status, body, 409, CodeNewLinkRequired)
		if got := h.z.snapshot(s.id); got != squatted {
			t.Fatalf("the old link changed the squatted account:\n before %s\n after  %s", squatted, got)
		}
		if mail := h.z.lastMail(t); mail.userID == s.id || h.z.user(mail.userID).metadata[MetaSupersedes] != s.id {
			t.Fatalf("no fresh link was mailed: %+v", mail)
		}
	})
	t.Run("an invitation naming the held account", func(t *testing.T) {
		h := newHarness(t, policyWith(SignupInvite, allMethods))
		s := squat(t, h)
		h.z.mu.Lock()
		s.inviteCode = "inv-1"
		h.z.mu.Unlock()
		squatted := h.z.snapshot(s.id)
		status, body := h.browser().post("/auth/invite/accept", map[string]any{"userID": s.id, "code": "inv-1", "method": MethodPassword, "password": "invitee-password-1"})
		want(t, status, body, 409, CodeNeedsAdmin)
		if got := h.z.snapshot(s.id); got != squatted {
			t.Fatalf("a refused invitation changed the account:\n before %s\n after  %s", squatted, got)
		}
	})
}

// ── 5. A takeover that stops part-way resumes and converges ────────────

func TestEmailFirst_ATakeoverCutShortResumes(t *testing.T) {
	for name, resume := range map[string]func(h *harness, b *browser, nID, code string){
		"on the next lookup of the address": func(h *harness, b *browser, _, _ string) {
			h.browser().post("/auth/magic/request", map[string]any{"email": victimEmail})
		},
		"on a retried verification link": func(h *harness, b *browser, nID, code string) {
			b.post("/auth/verify", map[string]any{"userID": nID, "code": code})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupOpen, allMethods))
			s := squat(t, h)
			victim := h.browser()
			victim.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"})
			mail := h.z.lastMail(t)
			h.z.mu.Lock()
			h.z.failUsernameChanges = 1 // the broker dies renaming the squatted account
			h.z.mu.Unlock()
			if status, body := victim.post("/auth/verify", map[string]any{"userID": mail.userID, "code": mail.code}); status != 200 {
				t.Fatalf("verify: %d %v", status, body)
			}
			if h.z.user(s.id).name() != victimEmail {
				t.Fatal("precondition: the takeover was meant to stop before the rename")
			}
			// Cut short, the address already resolves to the proven
			// account, never the squatted one.
			if err := signInGated(h, victimEmail, "victim-password-1"); err != nil {
				t.Fatalf("mid-takeover, the owner cannot sign in: %v", err)
			}
			if err := signInGated(h, victimEmail, attackerPassword); err == nil {
				t.Fatal("mid-takeover, the address signs in to the squatted account")
			}

			resume(h, victim, mail.userID, mail.code)
			n, sq := h.z.user(mail.userID), h.z.user(s.id)
			h.z.mu.Lock()
			defer h.z.mu.Unlock()
			if n.name() != victimEmail || n.metadata[MetaSupersedes] != "" || sq.name() != supersededUsername(s.id) || sq.email != supersededEmail(s.id) {
				t.Fatalf("not converged: fresh=%q (supersedes %q), squatted=%q <%s>", n.name(), n.metadata[MetaSupersedes], sq.name(), sq.email)
			}
		})
	}
}

// ── 6. A no-mail sign-up is a contact field no proof claims ────────────

func TestEmailFirst_ANoMailSignUpIsNeverClaimedByAProof(t *testing.T) {
	noMail := LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, Methods: []string{MethodPassword}, Mail: MailNone}
	h := newHarness(t, noMail)
	if status, body := h.browser().post("/auth/register", map[string]any{"email": victimEmail, "password": "holder-password-1"}); status != 200 {
		t.Fatalf("no-mail register: %d %v", status, body)
	}
	c := h.z.userByEmail(victimEmail)
	if c.verified || c.metadata[MetaContactOnly] != "1" || c.password == "" {
		t.Fatalf("the no-mail account: %+v, want unverified, contact-only, with its password", c)
	}
	held := h.z.snapshot(c.id)

	// Where nothing is mailed, a code names nothing to prove and creates
	// nothing.
	status, body := h.browser().post("/auth/verify", map[string]any{"userID": c.id, "code": "any"})
	want(t, status, body, 401, CodeInvalidCode)
	if got := len(usersWithEmail(h, victimEmail)); got != 1 || h.z.snapshot(c.id) != held {
		t.Fatalf("a no-mail verification changed something: %d accounts", got)
	}

	// The project later delivers mail: the address's owner proves into a
	// fresh account; the contact-only one is never verified.
	mailing := newHarnessOn(t, h.z, policyWith(SignupOpen, allMethods))
	owner := mailing.browser()
	owner.post("/auth/magic/request", map[string]any{"email": victimEmail})
	mail := h.z.lastMail(t)
	if mail.userID == c.id {
		t.Fatal("the magic link proves the contact-only account")
	}
	if status, body := owner.post("/auth/magic/consume", map[string]any{"code": mail.code}); status != 200 || body["subject"] != mail.userID {
		t.Fatalf("the owner's magic link: %d %v", status, body)
	}
	h.z.mu.Lock()
	defer h.z.mu.Unlock()
	if c.verified || c.password != "holder-password-1" {
		t.Fatalf("the contact-only account was claimed: verified=%v password kept=%v", c.verified, c.password == "holder-password-1")
	}
}

// ── 7. The count of accounts to decide about ───────────────────────────

func TestUnprovenAccounts_ListsUnverifiedAccountsHoldingCredentialsOnly(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	s := squat(t, h)
	contact := h.z.addUser("contact@acme.com", false, "holder-password-1")
	h.z.addUser("pending@acme.com", false, "")
	h.z.addUser("proven@acme.com", true, "proven-password-1")
	h.z.mu.Lock()
	contact.metadata = map[string]string{MetaContactOnly: "1"}
	h.z.mu.Unlock()
	before := h.z.totalCalls()
	got, err := h.z.client().UnprovenAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]UnprovenAccount{}
	for _, a := range got {
		byID[a.UserID] = a
	}
	if len(got) != 2 || len(byID[s.id].Credentials) != 4 || byID[s.id].ContactOnly || !byID[contact.id].ContactOnly {
		t.Fatalf("UnprovenAccounts = %+v, want the squatted account (4 kinds) and the contact-only one", got)
	}
	for _, call := range h.z.calls[before:] {
		if call[:4] != "GET " && call != "POST /v2/users" && !hasSuffix(call, "/metadata/search") {
			t.Fatalf("UnprovenAccounts wrote: %s", call)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// FindUserByEmail names the one proven account where a fresh account and the
// one it supersedes share the address, and none where nothing is proven.
func TestFindUserByEmail_PrefersTheOneVerifiedAccount(t *testing.T) {
	z := newFakeZitadel(t)
	ctx := context.Background()
	z.addUser("a@acme.com", false, "x-password-1")
	z.addUser("a@acme.com", false, "")
	if _, err := z.client().FindUserByEmail(ctx, "a@acme.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("two unverified accounts: %v, want ErrUserNotFound", err)
	}
	v := z.addUser("a@acme.com", true, "")
	proven := z.client()
	proven.ProvenCredentialsOnly = true
	if u, err := proven.FindUserByEmail(ctx, "a@acme.com"); err != nil || u.ID != v.id {
		t.Fatalf("= %+v, %v; want the verified account", u, err)
	}
	// Off (the default), several matches are still none, as before.
	if _, err := z.client().FindUserByEmail(ctx, "a@acme.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("a default client picked one of several accounts: %v", err)
	}
}

// forge is semver v1: a client that does not ask for ProvenCredentialsOnly
// behaves exactly as before — the calls that add a credential do not read
// the account first and do not refuse an unverified one.
func TestProvenCredentialsOnly_OffByDefaultChangesNothing(t *testing.T) {
	z := newFakeZitadel(t)
	c := z.client()
	ctx := context.Background()
	u := z.addUser("legacy@acme.com", false, "")
	before := z.callCount("GET /v2/users/" + u.id)
	if err := c.AddIDPLink(ctx, u.id, IDPLink{IdPID: "idp-google", UserID: "g-1", UserName: "x"}); err != nil {
		t.Fatalf("AddIDPLink on a default client: %v", err)
	}
	if err := c.SetPassword(ctx, u.id, "a-long-password"); err != nil {
		t.Fatalf("SetPassword on a default client: %v", err)
	}
	if _, err := c.CreateUser(ctx, NewUser{Email: "other@acme.com", Password: "a-long-password"}); err != nil {
		t.Fatalf("CreateUser with a password on a default client: %v", err)
	}
	if got := z.callCount("GET /v2/users/" + u.id); got != before {
		t.Fatalf("a default client read the account %d time(s) before adding a credential", got-before)
	}
}

// A password is never set on an account that could not be read: a failed
// read refuses the write before a reset code is minted or redeemed (fail
// closed), and an unknown account answers like a wrong code (no
// enumeration at a reset form).
func TestProvenCredentialsOnly_AFailedReadNeverAuthorizesAPassword(t *testing.T) {
	ctx := context.Background()
	for name, fault := range map[string]func(z *fakeZitadel){
		"the account read fails":        func(z *fakeZitadel) { z.failUserReads = 1 },
		"the sign-in-method read fails": func(z *fakeZitadel) { z.failMethodReads = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			z := newFakeZitadel(t)
			c := z.client()
			c.ProvenCredentialsOnly = true
			u := z.addUser("pending@acme.com", false, "")
			reset, err := c.MintPasswordResetCode(ctx, u.id)
			if err != nil {
				t.Fatal(err)
			}
			for call, set := range map[string]func() error{
				"SetPassword": func() error { return c.SetPassword(ctx, u.id, "a-long-password") },
				"ResetPassword": func() error {
					return c.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: reset, NewPassword: "a-long-password"})
				},
			} {
				z.mu.Lock()
				fault(z)
				z.mu.Unlock()
				mints, writes := z.callCount("POST /v2/users/"+u.id+"/password_reset"), z.callCount("POST /v2/users/"+u.id+"/password")
				if err := set(); err == nil || errors.Is(err, ErrUnverifiedAccount) {
					t.Fatalf("%s with %s: %v, want the read's error", call, name, err)
				}
				if z.callCount("POST /v2/users/"+u.id+"/password_reset") != mints || z.callCount("POST /v2/users/"+u.id+"/password") != writes {
					t.Fatalf("%s minted or redeemed a reset code for an account it could not read", call)
				}
			}
			if z.user(u.id).password != "" {
				t.Fatal("a password was set on an unreadable unverified account")
			}
		})
	}
	t.Run("an unknown account", func(t *testing.T) {
		z := newFakeZitadel(t)
		c := z.client()
		c.ProvenCredentialsOnly = true
		err := c.ResetPassword(ctx, PasswordReset{UserID: "300000000000000999", Code: "x", NewPassword: "a-long-password"})
		if !errors.Is(err, ErrInvalidResetCode) {
			t.Fatalf("an unknown account: %v, want it answered like a wrong code", err)
		}
		if z.callCount("POST /v2/users/300000000000000999/password") != 0 {
			t.Fatal("a reset was sent for an unknown account")
		}
	})
}

// forge#2179 review B1: two replicas asked for the same address at once —
// both past the "is there a fresh account already" read before either
// creates — converge on ONE fresh account. Its username is derived from the
// held account, so the issuer takes one create and refuses the other, which
// then adopts the winner.
func TestEmailFirst_ConcurrentReplicasConvergeOnOneFreshAccount(t *testing.T) {
	policy := policyWith(SignupOpen, allMethods)
	a := newHarness(t, policy)
	b := newHarnessOn(t, a.z, policy)
	s := squat(t, a)

	// Hold every account create until both replicas have sent theirs.
	var arrived atomic.Int32
	both := make(chan struct{})
	a.z.mu.Lock()
	a.z.beforeRequest = func(r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v2/users/human" {
			if arrived.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(10 * time.Second): // a guard, not a wait: the test fails below if only one arrived
			}
		}
	}
	a.z.mu.Unlock()

	type answer struct{ status int }
	done := make(chan answer, 2)
	for _, h := range []*harness{a, b} {
		br := h.browser()
		go func() {
			status, _ := br.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"})
			done <- answer{status}
		}()
	}
	for range 2 {
		if got := testkit.Receive(t, "a replica's sign-up", done, testkit.Within(15*time.Second)); got.status != 202 {
			t.Fatalf("sign-up answered %d, want 202", got.status)
		}
	}
	if n := arrived.Load(); n != 2 {
		t.Fatalf("%d create(s) reached the issuer, want both replicas racing", n)
	}
	accounts := usersWithEmail(a, victimEmail)
	if len(accounts) != 2 {
		t.Fatalf("%d accounts for the address, want the squatted one and ONE fresh one", len(accounts))
	}
	var fresh *fakeUser
	for _, u := range accounts {
		if u.id != s.id {
			fresh = u
		}
	}
	if fresh.name() != freshUsername(s.id) {
		t.Fatalf("the fresh account is %q, want %q", fresh.name(), freshUsername(s.id))
	}
	if _, err := a.routes.accountFor(context.Background(), victimEmail); err != nil {
		t.Fatalf("the address is ambiguous after the race: %v", err)
	}
	mail := a.z.lastMail(t)
	if mail.userID != fresh.id {
		t.Fatalf("the last link proves %s, want the one fresh account %s", mail.userID, fresh.id)
	}
	if status, body := a.browser().post("/auth/verify", map[string]any{"userID": fresh.id, "code": mail.code}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	verified := 0
	a.z.mu.Lock()
	for _, u := range a.z.users {
		if u.verified {
			verified++
		}
	}
	a.z.mu.Unlock()
	if verified != 1 {
		t.Fatalf("%d verified accounts after the race, want 1", verified)
	}
}

// A takeover cut short AFTER the old account's email moved — at the rename
// of the fresh account, or at the clearing of its marker — leaves the address
// with ONE match. The owner can sign in meanwhile, and the next lookup of the
// address finishes it.
func TestEmailFirst_ATakeoverCutShortAfterTheEmailMovedResumes(t *testing.T) {
	for name, fault := range map[string]func(z *fakeZitadel, freshID string){
		"renaming the fresh account":        func(z *fakeZitadel, freshID string) { z.failUsernameChangeOf = freshID },
		"clearing the fresh account's mark": func(z *fakeZitadel, _ string) { z.failMetadataDeletes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupOpen, allMethods))
			s := squat(t, h)
			victim := h.browser()
			victim.post("/auth/register", map[string]any{"email": victimEmail, "password": "victim-password-1"})
			mail := h.z.lastMail(t)
			h.z.mu.Lock()
			fault(h.z, mail.userID)
			h.z.mu.Unlock()
			if status, body := victim.post("/auth/verify", map[string]any{"userID": mail.userID, "code": mail.code}); status != 200 {
				t.Fatalf("verify: %d %v", status, body)
			}
			h.z.mu.Lock()
			moved, pending := s.email == supersededEmail(s.id), h.z.users[mail.userID].metadata[MetaSupersedes] != ""
			h.z.mu.Unlock()
			if !moved || !pending {
				t.Fatalf("precondition: want the old email moved and the takeover unfinished (moved=%v pending=%v)", moved, pending)
			}
			if got := len(usersWithEmail(h, victimEmail)); got != 1 {
				t.Fatalf("%d accounts have the address, want the fresh one alone", got)
			}
			if err := signInGated(h, victimEmail, "victim-password-1"); err != nil {
				t.Fatalf("the owner cannot sign in mid-takeover: %v", err)
			}

			h.browser().post("/auth/magic/request", map[string]any{"email": victimEmail}) // the next lookup
			n := h.z.user(mail.userID)
			h.z.mu.Lock()
			defer h.z.mu.Unlock()
			if n.name() != victimEmail || n.metadata[MetaSupersedes] != "" || s.name() != supersededUsername(s.id) {
				t.Fatalf("not converged: fresh=%q (supersedes %q), old=%q", n.name(), n.metadata[MetaSupersedes], s.name())
			}
		})
	}
}

// forge#2179 review (password.go:468): a pre-registered, unverified account
// holding only a password — the squatter's — and a session that passed that
// password before the owner ever acted. The inbox owner redeems a reset code
// mailed to the address. The password is replaced (the holder's recovery),
// but the address is NOT verified: verifying it would let the squatter's
// retained session through the verified-email gate. The owner proves into a
// fresh account instead.
func TestEmailFirst_AResetNeverVerifiesAPreRegisteredPasswordAccount(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	broker := z.client()
	broker.ProvenCredentialsOnly = true
	s := z.addUser(victimEmail, false, attackerPassword)
	z.mu.Lock()
	z.sessions["s-attacker"] = &fakeSession{token: "s-attacker-token", userID: s.id, verified: []string{"password"}}
	z.mu.Unlock()

	code, err := z.client().MintPasswordResetCode(ctx, s.id) // mailed to the address: the owner holds it
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.ResetPassword(ctx, PasswordReset{UserID: s.id, Code: code, NewPassword: "owner-password-1"}); err != nil {
		t.Fatalf("the owner's reset: %v", err)
	}
	if verified, pw := z.passwordState(s.id); verified || pw != "owner-password-1" {
		t.Fatalf("after the owner's reset: verified=%v password replaced=%v, want the password replaced and the address still unverified", verified, pw == "owner-password-1")
	}
	_, err = broker.CompleteSignIn(ctx, FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true},
		Session{ID: "s-attacker", Token: "s-attacker-token"})
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("TAKEOVER: the squatter's retained session completed sign-in after the owner's reset (err %v)", err)
	}
}

// The first-password mark exists only while a first password is being
// installed: a wrong code installs nothing and leaves no mark, and a
// successful first password clears it once the address is verified.
func TestEmailFirst_TheFirstPasswordMarkNeverOutlivesItsInstall(t *testing.T) {
	ctx := context.Background()
	z := newFakeZitadel(t)
	broker := z.client()
	broker.ProvenCredentialsOnly = true
	u := z.addUser("pending@acme.com", false, "")
	if err := broker.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: "a-wrong-code", NewPassword: "a-long-password"}); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("a wrong code: %v, want ErrInvalidResetCode", err)
	}
	if left, _ := z.client().MetadataWithPrefix(ctx, u.id, MetaFirstPasswordPrefix); len(left) != 0 {
		t.Fatalf("a wrong code left its first-password attempt on the account: %v", left)
	}
	code, err := z.client().MintPasswordResetCode(ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: code, NewPassword: "a-long-password"}); err != nil {
		t.Fatal(err)
	}
	if verified, _ := z.passwordState(u.id); !verified {
		t.Fatal("the first password did not verify the address")
	}
	if left, _ := z.client().MetadataWithPrefix(ctx, u.id, MetaFirstPasswordPrefix); len(left) != 0 {
		t.Fatalf("first-password attempts outlived the verification: %v", left)
	}
}
