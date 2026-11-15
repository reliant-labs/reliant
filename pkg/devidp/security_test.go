package devidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// The findings of the independent security review of forge#1821, one test
// (or table) each. Every test here failed against the reviewed head.

const attackerPassword = "attacker-password-1"

// signsIn reports whether a password signs in to an address.
func signsIn(t *testing.T, h *harness, email, password string) bool {
	t.Helper()
	_, err := h.z.client().SignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "r"},
		Credentials{LoginName: email, Password: password})
	return err == nil
}

func cookieValue(b *browser, name string) string {
	u, _ := url.Parse(b.h.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// ── 1. Pre-registration account takeover ────────────────────────────────

// preRegister is the attack: the attacker's own browser registers the
// victim's address with the attacker's password. The sign-up route itself no
// longer puts a password on the account before its proof (it waits sealed in
// the attacker's browser); the account an older broker — or the legacy
// sign-up — created DID carry it, so the test then plants it, and every
// proof below must revoke it.
func preRegister(t *testing.T, h *harness) *fakeUser {
	t.Helper()
	status, body := h.browser().post("/auth/register", map[string]any{"email": "victim@acme.com", "password": attackerPassword})
	if status != 202 {
		t.Fatalf("attacker register: %d %v", status, body)
	}
	u := h.z.userByEmail("victim@acme.com")
	if u == nil || u.verified {
		t.Fatalf("precondition: want an unverified account, got %+v", u)
	}
	if u.password != "" {
		t.Fatal("TAKEOVER SETUP: sign-up put the registrant's password on the account before its proof")
	}
	h.z.mu.Lock()
	u.password = attackerPassword
	h.z.mu.Unlock()
	return u
}

func TestTakeover_MagicLinkVerificationRevokesThePlantedPassword(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	victim := h.browser()
	victim.post("/auth/magic/request", map[string]any{"email": "victim@acme.com"})
	status, body := victim.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code})
	if status != 200 || body["subject"] != u.id {
		t.Fatalf("victim's magic link: %d %v", status, body)
	}
	if signsIn(t, h, "victim@acme.com", attackerPassword) {
		t.Fatal("TAKEOVER: the attacker's password works on the victim's verified account")
	}
}

func TestTakeover_RegisteringAnExistingAddressReplacesThePlantedPasswordOnlyAfterProof(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	victim := h.browser()
	if status, _ := victim.post("/auth/register", map[string]any{"email": "victim@acme.com", "password": "victim-password-1"}); status != 202 {
		t.Fatalf("victim register: %d", status)
	}
	if h.z.user(u.id).password != attackerPassword {
		t.Fatal("registering an existing account changed its password before any proof")
	}
	status, body := victim.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if status != 200 || body["credentialsReset"] != false {
		t.Fatalf("victim verify: %d %v", status, body)
	}
	if signsIn(t, h, "victim@acme.com", attackerPassword) {
		t.Fatal("TAKEOVER: the attacker's password survived the victim's verification")
	}
	if !signsIn(t, h, "victim@acme.com", "victim-password-1") {
		t.Fatal("the victim's own password, chosen at their registration, was not set after their proof")
	}
}

func TestTakeover_VerifyingFromAnotherBrowserRevokesAndSaysSo(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	status, body := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if status != 200 || body["credentialsReset"] != true {
		t.Fatalf("verify: %d %v", status, body)
	}
	if signsIn(t, h, "victim@acme.com", attackerPassword) {
		t.Fatal("TAKEOVER: a verification from a browser that set no credential kept them")
	}
}

func TestTakeover_TheRegistrantsOwnBrowserKeepsItsPassword(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	alice := h.browser()
	alice.post("/auth/register", map[string]any{"email": "alice@acme.com", "password": "alice-password-1"})
	u := h.z.userByEmail("alice@acme.com")
	status, body := alice.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if status != 200 || body["credentialsReset"] != false || !signsIn(t, h, "alice@acme.com", "alice-password-1") {
		t.Fatalf("same-browser verify lost the registrant's password: %d %v", status, body)
	}
}

func TestTakeover_InvitationRevokesThePlantedPassword(t *testing.T) {
	for _, method := range []string{MethodPasskey, MethodMagicLink, MethodPassword} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupOpen, allMethods))
			u := preRegister(t, h)
			planted := Passkey{ID: "planted", Name: "attacker"}
			h.z.mu.Lock()
			u.inviteCode = "inv-1"
			u.passkeys = []Passkey{planted}
			h.z.mu.Unlock()
			body := map[string]any{"userID": u.id, "code": "inv-1", "method": method}
			if method == MethodPassword {
				body["password"] = "invitee-password-1"
			}
			if status, resp := h.browser().post("/auth/invite/accept", body); status >= 300 && status != 202 {
				t.Fatalf("accept: %d %v", status, resp)
			}
			if signsIn(t, h, "victim@acme.com", attackerPassword) {
				t.Fatal("TAKEOVER: the attacker's password survived the invitation")
			}
			for _, k := range h.z.user(u.id).passkeys {
				if k.ID == planted.ID {
					t.Fatal("a passkey set before the invitation survived it")
				}
			}
		})
	}
}

func TestTakeover_AnUnverifiedSocialLinkDoesNotSurviveTheOwnersProof(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	attacker := h.browser()
	id := startSocial(t, attacker)
	h.z.addIntent(id, "tok", intentResult("victim@acme.com", false))
	if status, body := attacker.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 202 {
		t.Fatalf("attacker social sign-up: %d %v", status, body)
	}
	u := h.z.userByEmail("victim@acme.com")
	if u == nil || len(u.links) != 0 {
		t.Fatalf("precondition: sign-up must not link an unverified provider before the proof, got %+v", u)
	}
	// An account an older broker created DID carry the link: plant it.
	h.z.mu.Lock()
	u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123", UserName: "victim@acme.com"}}
	h.z.mu.Unlock()
	victim := h.browser()
	if status, body := victim.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("victim verify: %d %v", status, body)
	}
	if n := len(h.z.user(u.id).links); n != 0 {
		t.Fatalf("TAKEOVER: %d identity-provider link(s) planted before the proof survived it", n)
	}
	// The attacker's provider identity is no longer linked: their next
	// social sign-in reaches the "both verified" rule and is refused.
	again := h.browser()
	id2 := startSocial(t, again)
	h.z.addIntent(id2, "tok2", intentResult("victim@acme.com", false))
	status, body := again.post("/auth/social/callback", map[string]any{"id": id2, "token": "tok2"})
	want(t, status, body, 409, CodeSocialLinkRequired)
}

func TestTakeover_ARevocationThatFailsLocksTheAccount(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	u := preRegister(t, h)
	h.z.mu.Lock()
	h.z.failLinks = true
	h.z.mu.Unlock()
	status, _ := h.browser().post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
	if status != http.StatusBadGateway {
		t.Fatalf("verify answered %d, want 502", status)
	}
	if !h.z.user(u.id).locked {
		t.Fatal("a verified account was left holding credentials nobody proved, and was not locked")
	}
}

// ── signup_role only for self-signups, once ────────────────────────────

func TestSignupRole_OnlySelfSignupsAndOnlyOnce(t *testing.T) {
	p := policyWith(SignupOpen, allMethods)
	p.SignupRole = "member"
	h := newHarness(t, p)
	invited := h.z.addUser("invited@acme.com", false, "")
	h.z.mu.Lock()
	invited.emailCode = "ev-x"
	h.z.mu.Unlock()
	if status, _ := h.browser().post("/auth/verify", map[string]any{"userID": invited.id, "code": "ev-x"}); status != 200 {
		t.Fatal("verify of an invited account failed")
	}
	if len(h.z.grants) != 0 {
		t.Fatalf("an account that did not sign up got the sign-up role: %v", h.z.grants)
	}

	b := h.browser()
	b.post("/auth/register", map[string]any{"email": "self@acme.com", "password": "self-password-1"})
	self := h.z.userByEmail("self@acme.com")
	b.post("/auth/verify", map[string]any{"userID": self.id, "code": h.z.lastMail(t).code})
	if len(h.z.grants) != 1 {
		t.Fatalf("grants %v, want the self-signup's one", h.z.grants)
	}
	// A later re-verification (an address change) grants nothing again.
	h.z.mu.Lock()
	self.verified, self.emailCode, h.z.grants = false, "ev-again", nil
	h.z.mu.Unlock()
	b.post("/auth/verify", map[string]any{"userID": self.id, "code": "ev-again"})
	if len(h.z.grants) != 0 {
		t.Fatalf("a re-verification granted the sign-up role again: %v", h.z.grants)
	}
}

// ── 2. Sealed-cookie length and response time ──────────────────────────

func TestSealedCookiesAreOneLength(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	h.z.addUser("verified@acme.com", true, "")
	h.z.addUser("pending@acme.com", false, "pending-password")
	seedPasskeyUser(h, true)

	lengths := map[string]int{}
	for _, email := range []string{"verified@acme.com", "pending@acme.com", "nobody@else.com", "new@acme.com"} {
		b := h.browser()
		b.post("/auth/magic/request", map[string]any{"email": email})
		lengths["magic "+email] = len(cookieValue(b, "app_ml"))
	}
	limited := h.browser()
	for i := 0; i < 4; i++ {
		limited.post("/auth/magic/request", map[string]any{"email": "verified@acme.com"})
	}
	lengths["magic rate-limited"] = len(cookieValue(limited, "app_ml"))
	for _, email := range []string{"pk@acme.com", "nobody@acme.com"} {
		b := h.browser()
		b.post("/auth/passkey/begin", map[string]any{"email": email})
		lengths["passkey "+email] = len(cookieValue(b, "app_pk"))
	}
	for _, email := range []string{"brand-new@acme.com", "pending@acme.com", "verified@acme.com"} {
		b := h.browser()
		b.post("/auth/register", map[string]any{"email": email, "password": strings.Repeat("p", maxPasswordBytes)})
		lengths["register "+email] = len(cookieValue(b, "app_pwp"))
	}
	var first int
	for k, n := range lengths {
		if n == 0 {
			t.Errorf("%s: no binding cookie", k)
		}
		if first == 0 {
			first = n
		}
		if n != first {
			t.Errorf("cookie lengths differ by account state: %v", lengths)
			break
		}
	}
}

func TestResponseFloorHidesTheSlowerPath(t *testing.T) {
	const floor = 150 * time.Millisecond
	h := newHarness(t, policyWith(SignupOpen, allMethods), func(c *SignInRoutesConfig) { c.ResponseFloor = floor })
	h.z.addUser("verified@acme.com", true, "")
	seedPasskeyUser(h, true)
	for _, tc := range []struct{ path, email string }{
		{"/auth/magic/request", "verified@acme.com"}, {"/auth/magic/request", "nobody@else.com"},
		{"/auth/passkey/begin", "pk@acme.com"}, {"/auth/passkey/begin", "nobody@else.com"},
	} {
		start := time.Now()
		h.browser().post(tc.path, map[string]any{"email": tc.email})
		if took := time.Since(start); took < floor {
			t.Errorf("%s %s answered in %v, under the %v floor", tc.path, tc.email, took, floor)
		}
	}
}

// ── SHOULD: the decoy passkey challenge is the issuer's shape ───────────

func TestDecoyChallengeMatchesTheIssuersShape(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	keys := func(raw json.RawMessage) (top, pk []string, opts map[string]any) {
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		for k := range doc {
			top = append(top, k)
		}
		opts, _ = doc["publicKey"].(map[string]any)
		for k := range opts {
			pk = append(pk, k)
		}
		sort.Strings(top)
		sort.Strings(pk)
		return top, pk, opts
	}
	// What Zitadel v4.19.5 (go-webauthn v0.10.2) returns for a UV-required login.
	issuer := json.RawMessage(`{"publicKey":{"challenge":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) +
		`","timeout":300000,"rpId":"app.test","allowCredentials":[{"type":"public-key","id":"a2V5"}],"userVerification":"required"}}`)
	it, ipk, _ := keys(normalizeJSON(issuer))
	for _, email := range []string{"a@acme.com", "b@acme.com", "c@acme.com", "d@acme.com", "e@acme.com"} {
		decoy := h.routes.decoyOptions(email)
		dt, dpk, opts := keys(decoy)
		if fmt.Sprint(dt) != fmt.Sprint(it) || fmt.Sprint(dpk) != fmt.Sprint(ipk) {
			t.Fatalf("decoy keys %v/%v, issuer's %v/%v", dt, dpk, it, ipk)
		}
		if opts["timeout"] != float64(300000) || opts["userVerification"] != "required" || opts["rpId"] != "app.test" {
			t.Errorf("decoy fields %v", opts)
		}
		ch, _ := base64.RawURLEncoding.DecodeString(opts["challenge"].(string))
		if len(ch) != 32 {
			t.Errorf("decoy challenge is %d bytes, the issuer's are 32", len(ch))
		}
		creds := opts["allowCredentials"].([]any)
		for _, c := range creds {
			m := c.(map[string]any)
			id, _ := base64.RawURLEncoding.DecodeString(m["id"].(string))
			if n := len(id); n != 16 && n != 20 && n != 32 && n != 64 {
				t.Errorf("decoy credential id of %d bytes", n)
			}
			if len(m) != 2 || m["type"] != "public-key" {
				t.Errorf("decoy credential %v", m)
			}
		}
		again := h.routes.decoyOptions(email)
		_, _, opts2 := keys(again)
		if fmt.Sprint(opts2["allowCredentials"]) != fmt.Sprint(opts["allowCredentials"]) {
			t.Error("a second decoy for the same address names other credentials")
		}
	}
}

// ── 4. Cross-site requests ──────────────────────────────────────────────

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	reg := map[string]any{"email": "csrf@acme.com", "password": "csrf-password-1"}
	for name, tc := range map[string]struct {
		headers map[string]string
		status  int
	}{
		"cross-site fetch":       {map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.test"}, 403},
		"foreign origin, no SFS": {map[string]string{"Origin": "https://evil.test"}, 403},
		"text/plain form":        {map[string]string{"Content-Type": "text/plain"}, 415},
		"urlencoded form":        {map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
	} {
		status, _ := h.browser().doWith(http.MethodPost, "/auth/register", reg, tc.headers)
		if status != tc.status {
			t.Errorf("%s: %d, want %d", name, status, tc.status)
		}
	}
	if h.z.userByEmail("csrf@acme.com") != nil {
		t.Fatal("a cross-site request created an account")
	}
	for name, headers := range map[string]map[string]string{
		"same-origin":                 {"Sec-Fetch-Site": "same-origin"},
		"the app's own frontend":      {"Sec-Fetch-Site": "same-site", "Origin": testAppBase},
		"no browser headers (server)": {},
	} {
		if status, body := h.browser().doWith(http.MethodPost, "/auth/verify/resend", map[string]any{"email": "x@acme.com"}, headers); status != 202 {
			t.Errorf("%s: %d %v", name, status, body)
		}
	}
}

// Hosted sites are <name>.sites.reliantlabs.dev, a parent domain that is not
// on the Public Suffix List: every tenant's site is SAME-SITE with every
// other, so SameSite cookies separate nothing. Only the app's exact origin
// may make a state-changing request.
func TestGuardRefusesASameSiteSibling(t *testing.T) {
	g := NewCrossOriginProtection("https://acme.sites.reliantlabs.dev/base")
	for origin, wantOK := range map[string]bool{
		"https://evil.sites.reliantlabs.dev": false,
		"https://sites.reliantlabs.dev":      false,
		"http://acme.sites.reliantlabs.dev":  false,
		"https://acme.sites.reliantlabs.dev": true,
	} {
		req := httptest.NewRequest(http.MethodPost, "https://acme.reliantlabs.dev/auth/register", nil)
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Origin", origin)
		if err := g.Check(req); (err == nil) != wantOK {
			t.Errorf("same-site from %s: allowed = %v, want %v", origin, err == nil, wantOK)
		}
	}
	// No Sec-Fetch-Site (an old browser): the Origin must be this host or the app's.
	req := httptest.NewRequest(http.MethodPost, "https://acme.reliantlabs.dev/auth/register", nil)
	req.Header.Set("Origin", "https://evil.sites.reliantlabs.dev")
	if g.Check(req) == nil {
		t.Error("a sibling Origin without Sec-Fetch-Site was allowed")
	}
}

func TestGuardMuxRefusesCrossSiteOnEveryRoute(t *testing.T) {
	mux := http.NewServeMux()
	g := GuardMux(mux, "https://app.example.com/base")
	g.Handle("POST /auth/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	for origin, wantStatus := range map[string]int{"https://evil.test": 403, "https://app.example.com": 200} {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com/auth/login", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Errorf("origin %s: %d, want %d", origin, rec.Code, wantStatus)
		}
	}
}

// ── 5. Limiter floods and the client address ───────────────────────────

func TestLimiterEvictsRatherThanLocksOut(t *testing.T) {
	l := newLimiter(5, time.Hour)
	for i := 0; i < limiterMaxKeys; i++ {
		l.reserve(fmt.Sprintf("junk-%d", i))
	}
	if !l.reserve("real-user") {
		t.Fatal("a full table refused a new key: anyone can lock every user out with made-up ids")
	}
	if len(l.entries) > limiterMaxKeys {
		t.Fatalf("table grew to %d", len(l.entries))
	}
}

func TestCodeFloodFromOneClientDoesNotLockOthersOut(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods), func(c *SignInRoutesConfig) { c.TrustedProxyHops = 1 })
	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	attacker := h.browser()
	var refused bool
	for i := 0; i < 40; i++ {
		status, _ := attacker.doWith(http.MethodPost, "/auth/verify", map[string]any{"userID": fmt.Sprintf("u-made-up-%d", i), "code": "x"}, from("203.0.113.9"))
		if status == 429 {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("one client tried 40 made-up user ids without being limited")
	}
	b := h.browser()
	b.doWith(http.MethodPost, "/auth/register", map[string]any{"email": "real@acme.com", "password": "real-password-1"}, from("198.51.100.7"))
	u := h.z.userByEmail("real@acme.com")
	status, body := b.doWith(http.MethodPost, "/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}, from("198.51.100.7"))
	if status != 200 {
		t.Fatalf("another client's verification was refused after the flood: %d %v", status, body)
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:4321"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7")
	for hops, want := range map[int]string{0: "10.0.0.5", 1: "198.51.100.7", 2: "6.6.6.6", 3: "10.0.0.5"} {
		if got := ClientIP(req, hops); got != want {
			t.Errorf("hops %d: %s, want %s", hops, got, want)
		}
	}
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(req, 1); got != "10.0.0.5" {
		t.Errorf("an unparseable entry: %s, want the peer", got)
	}
}

// ── SHOULD: enrolment, cookie prefix, return paths ─────────────────────

func TestEnrolmentRefusesASessionForAnotherUser(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	other := h.z.addUser("other@acme.com", true, "other-password-1")
	inv := invitee(h)
	b := h.browser()
	if status, _ := b.post("/auth/invite/accept", map[string]any{"userID": inv.id, "code": "inv-1", "method": "passkey"}); status != 200 {
		t.Fatal("accept failed")
	}
	srvURL, _ := url.Parse(h.srv.URL)
	b.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "app_session", Value: "at-" + other.id, Path: "/"}})
	if status, _ := b.post("/auth/passkey/register/begin", map[string]any{}); status != 401 {
		t.Fatalf("enrolment with a session for someone else answered %d, want 401", status)
	}
	if other.pendingPasskey != "" || inv.pendingPasskey != "" {
		t.Fatal("a registration was started")
	}
}

func TestSecureBindingCookiesUseTheHostPrefix(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods), func(c *SignInRoutesConfig) { c.SecureCookie = true })
	if got := h.routes.cookieName(magicCookie); got != "__Host-app_ml" {
		t.Fatalf("secure cookie name %q", got)
	}
	rec := httptest.NewRecorder()
	if err := h.routes.setBinding(rec, magicCookie, magicBinding{Kind: "n"}); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly {
		t.Fatalf("a __Host- cookie must be Secure, Path=/, host-only: %+v", c)
	}
}

func TestSafeReturnToRefusesWhatABrowserReadsAsAnotherSite(t *testing.T) {
	for _, hostile := range []string{
		"//evil.com", "/\\evil.com", "/\tevil.com", "/\t/evil.com", "/\n/evil.com", "/.//evil.com", "/./\\evil.com",
		"/%2e//evil.com", "/a/..//evil.com", "https://evil.com", "javascript:alert(1)", "", "evil.com",
	} {
		if got := SafeReturnTo(hostile); got != "/" {
			t.Errorf("SafeReturnTo(%q) = %q, want /", hostile, got)
		}
	}
	for in, out := range map[string]string{"/projects/7?tab=a#top": "/projects/7?tab=a#top", "/a/../b": "/"} {
		if got := SafeReturnTo(in); got != out {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, out)
		}
	}
}

// Two replicas of the broker (one issuer, one broker token) answer an unknown
// address with the same decoy credential ids, as they would a real account's,
// while each challenge stays fresh. A per-process decoy secret would let
// requests that land on different replicas, or straddle a restart, tell an
// unknown address from a real one by its changing ids.
func TestDecoyCredentialsAreStableAcrossReplicas(t *testing.T) {
	z := newFakeZitadel(t)
	a := newHarnessOn(t, z, policyWith(SignupInvite, allMethods))
	b := newHarnessOn(t, z, policyWith(SignupInvite, allMethods))
	parse := func(raw json.RawMessage) (creds, challenge string) {
		var doc struct {
			PublicKey struct {
				Challenge        string          `json:"challenge"`
				AllowCredentials json.RawMessage `json:"allowCredentials"`
			} `json:"publicKey"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return string(doc.PublicKey.AllowCredentials), doc.PublicKey.Challenge
	}
	for _, email := range []string{"nobody@acme.com", "ghost@acme.com"} {
		ca, cha := parse(a.routes.decoyOptions(email))
		cb, chb := parse(b.routes.decoyOptions(email))
		if ca != cb {
			t.Errorf("%s: replicas name different decoy credentials: %s vs %s", email, ca, cb)
		}
		if cha == chb {
			t.Errorf("%s: two decoys share a challenge", email)
		}
		if a.routes.magicAttemptKey(email) != b.routes.magicAttemptKey(email) {
			t.Errorf("%s: replicas key the magic-link budget differently", email)
		}
	}
	other := newHarnessOn(t, z, policyWith(SignupInvite, allMethods), func(c *SignInRoutesConfig) {
		c.Client = &Client{Base: z.srv.URL, Token: "another-token"}
	})
	mine, _ := parse(a.routes.decoyOptions("nobody@acme.com"))
	if theirs, _ := parse(other.routes.decoyOptions("nobody@acme.com")); mine == theirs {
		t.Error("the decoy secret does not depend on the broker token")
	}
}
