package devidp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The second security re-review of forge#1821 (at 72d47a1c): the race in the
// takeover fix, the sealed-password size oracle, and the low findings.

// ── A. RevokeCredentials, which nothing in forge calls any more ──────────

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
