package devidp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The sign-in routes against a fake Zitadel (fakezitadel_test.go) that is as
// permissive as the real one: it finalizes any session and accepts any passkey
// origin. Every refusal below is therefore the broker's own.

const testAppBase = "http://app.test"

type harness struct {
	t      *testing.T
	z      *fakeZitadel
	routes *SignInRoutes
	srv    *httptest.Server
}

// browser is one cookie jar: a person's browser.
type browser struct {
	h      *harness
	client *http.Client
}

func policyWith(signup string, methods []string, social ...LoginPolicySocial) LoginPolicy {
	p := LoginPolicy{Present: true, Version: 1, Signup: signup, Methods: methods, Social: social,
		Mail: MailDev, RequireVerifiedEmail: true}
	if signup == SignupDomain {
		p.SignupDomains = []string{"acme.com"}
	}
	return p
}

var allMethods = []string{MethodPassword, MethodPasskey, MethodMagicLink}

var google = LoginPolicySocial{Type: "google", IdPID: "idp-google", Label: "Google"}

func newHarness(t *testing.T, p LoginPolicy, opts ...func(*SignInRoutesConfig)) *harness {
	t.Helper()
	return newHarnessOn(t, newFakeZitadel(t), p, opts...)
}

// newHarnessOn serves the routes against an existing fake issuer: a second
// call on the same z is a second replica of the broker.
func newHarnessOn(t *testing.T, z *fakeZitadel, p LoginPolicy, opts ...func(*SignInRoutesConfig)) *harness {
	t.Helper()
	cfg := SignInRoutesConfig{
		Client:        z.client(),
		Flow:          FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"},
		Policy:        p,
		AppBase:       testAppBase,
		ProjectID:     "proj",
		CookiePrefix:  "app",
		ResponseFloor: -1,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		SignedIn: func(w http.ResponseWriter, tokens Tokens) any {
			http.SetCookie(w, &http.Cookie{Name: "app_session", Value: tokens.AccessToken, Path: "/"})
			return map[string]any{"authenticated": true, "subject": strings.TrimPrefix(tokens.AccessToken, "at-")}
		},
		SessionToken: func(r *http.Request) string {
			c, err := r.Cookie("app_session")
			if err != nil {
				return ""
			}
			return c.Value
		},
		PasswordPolicy: func(context.Context) (PasswordPolicy, error) {
			return PasswordPolicy{MinLength: 8}, nil
		},
	}
	for _, o := range opts {
		o(&cfg)
	}
	routes, err := NewSignInRoutes(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for path, h := range map[string]http.HandlerFunc{
		PasskeyBeginPath: routes.HandlePasskeyBegin, PasskeyFinishPath: routes.HandlePasskeyFinish,
		PasskeyRegisterBeginPath: routes.HandlePasskeyRegisterBegin, PasskeyRegisterFinishPath: routes.HandlePasskeyRegisterFinish,
		PasskeysPath: routes.HandlePasskeys, MagicRequestPath: routes.HandleMagicRequest, MagicConsumePath: routes.HandleMagicConsume,
		SocialStartPath: routes.HandleSocialStart, SocialCallbackPath: routes.HandleSocialCallback,
		InviteAcceptPath: routes.HandleInviteAccept, VerifyPath: routes.HandleVerify, VerifyResendPath: routes.HandleVerifyResend,
		"POST /auth/register": routes.HandleRegister,
	} {
		mux.Handle(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, z: z, routes: routes, srv: srv}
}

func (h *harness) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{h: h, client: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path string, body any) (int, map[string]any) {
	b.h.t.Helper()
	return b.doWith(method, path, body, nil)
}

// doWith sends a request with extra headers (a "Content-Type" among them
// replaces the JSON default).
func (b *browser) doWith(method, path string, body any, headers map[string]string) (int, map[string]any) {
	b.h.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (b *browser) post(path string, body any) (int, map[string]any) {
	b.h.t.Helper()
	return b.do(http.MethodPost, path, body)
}

func (b *browser) signedIn() string {
	u, _ := url.Parse(b.h.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == "app_session" {
			return c.Value
		}
	}
	return ""
}

func (b *browser) cookieNames() []string {
	u, _ := url.Parse(b.h.srv.URL)
	var names []string
	for _, c := range b.client.Jar.Cookies(u) {
		names = append(names, c.Name)
	}
	return names
}

// credential builds a browser PublicKeyCredential JSON; signature "good" is
// what the fake issuer accepts.
func credential(origin, ceremony, signature string) map[string]any {
	cd, _ := json.Marshal(map[string]any{"type": ceremony, "origin": origin, "challenge": "Y2hhbGxlbmdl"})
	return map[string]any{"id": "a2V5", "rawId": "a2V5", "type": "public-key", "response": map[string]any{
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
		"authenticatorData": "YXV0aA", "signature": signature, "attestationObject": "YXR0",
	}}
}

func codeOf(t *testing.T, body map[string]any) string {
	t.Helper()
	c, _ := body["code"].(string)
	return c
}

func want(t *testing.T, gotStatus int, body map[string]any, status int, code string) {
	t.Helper()
	if gotStatus != status || codeOf(t, body) != code {
		t.Fatalf("got %d %v, want %d code %q", gotStatus, body, status, code)
	}
}

// ── LOGIN_POLICY ───────────────────────────────────────────────────────

func TestParseLoginPolicy_AbsentMalformedAndFutureFallBackToLegacy(t *testing.T) {
	for name, raw := range map[string]string{"absent": "", "blank": "  \n"} {
		p, err := ParseLoginPolicy(raw)
		if err != nil || p.Present || !p.Offers(MethodPassword) || p.Offers(MethodPasskey) || p.SelfSignup() {
			t.Errorf("%s: got %+v, %v; want the legacy policy and no error", name, p, err)
		}
	}
	for name, raw := range map[string]string{
		"malformed":      `{"version":1,`,
		"future version": `{"version":2,"signup":"open","methods":["passkey"]}`,
		"unknown method": `{"version":1,"signup":"open","methods":["sms"]}`,
		"unknown signup": `{"version":1,"signup":"anyone","methods":["password"]}`,
		"domain no list": `{"version":1,"signup":"domain","methods":["password"]}`,
	} {
		p, err := ParseLoginPolicy(raw)
		if !errors.Is(err, ErrLoginPolicyUnreadable) {
			t.Errorf("%s: err = %v, want ErrLoginPolicyUnreadable", name, err)
		}
		if p.Present || p.SelfSignup() || p.Offers(MethodPasskey) || !p.Offers(MethodPassword) {
			t.Errorf("%s: did not fail closed to the legacy policy: %+v", name, p)
		}
	}
}

func TestParseLoginPolicy_DomainMatchingIsExactCaseInsensitiveIDNA(t *testing.T) {
	p, err := ParseLoginPolicy(`{"version":1,"signup":"domain","signup_domains":["Acme.com","bücher.example"],"methods":["password"],"require_verified_email":true}`)
	if err != nil || !p.Present || !p.RequireVerifiedEmail {
		t.Fatalf("parse: %+v %v", p, err)
	}
	for email, allowed := range map[string]bool{
		"a@acme.com": true, "a@ACME.COM": true, "a@eu.acme.com": false, "a@acme.com.evil": false,
		"a@xn--bcher-kva.example": true, "a@BÜCHER.example": true, "no-at-sign": false,
	} {
		if got := p.SignupAllows(email); got != allowed {
			t.Errorf("SignupAllows(%q) = %v, want %v", email, got, allowed)
		}
	}
}

func TestOptionsFor_LegacyAndPresent(t *testing.T) {
	legacy := OptionsFor(LegacyLoginPolicy(), true)
	if !legacy.SelfSignup || legacy.Signup != SignupOpen || len(legacy.Methods) != 1 || legacy.Mail {
		t.Errorf("legacy dev options: %+v", legacy)
	}
	if o := OptionsFor(LegacyLoginPolicy(), false); o.SelfSignup || o.Signup != SignupInvite {
		t.Errorf("legacy deployed options: %+v", o)
	}
	o := OptionsFor(policyWith(SignupInvite, allMethods, google), true)
	if o.SelfSignup || len(o.Methods) != 3 || len(o.Social) != 1 || o.Social[0].Label != "Google" || !o.Mail {
		t.Errorf("present options: %+v (a present policy ignores the legacy switch)", o)
	}
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "idp-google") {
		t.Error("options leak the issuer's IdP id")
	}
}

func TestNewSignInRoutes_RefusesMissingOriginAndForeignRPID(t *testing.T) {
	base := SignInRoutesConfig{Client: &Client{}, SignedIn: func(http.ResponseWriter, Tokens) any { return nil },
		SessionToken: func(*http.Request) string { return "" }}
	c := base
	if _, err := NewSignInRoutes(c); err == nil {
		t.Error("accepted no app origin")
	}
	c.AppBase = "https://app.acme.com"
	c.Policy = LoginPolicy{Present: true, PasskeyRPID: "evil.com"}
	if _, err := NewSignInRoutes(c); err == nil {
		t.Error("accepted an RP id that is not the app's host or a parent of it")
	}
	c.Policy.PasskeyRPID = "acme.com"
	if r, err := NewSignInRoutes(c); err != nil || r.RPID() != "acme.com" {
		t.Errorf("a parent-domain RP id: %v", err)
	}
}

// ── WebAuthn origin check ──────────────────────────────────────────────

func TestCheckClientData(t *testing.T) {
	good := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	if err := CheckClientData(good(credential(testAppBase, WebAuthnGet, "x")), testAppBase, WebAuthnGet); err != nil {
		t.Fatalf("a correct credential was refused: %v", err)
	}
	cases := map[string]json.RawMessage{
		"other origin":     good(credential("https://evil.test", WebAuthnGet, "x")),
		"subdomain origin": good(credential("http://x.app.test", WebAuthnGet, "x")),
		"other port":       good(credential("http://app.test:8080", WebAuthnGet, "x")),
		"create for get":   good(credential(testAppBase, WebAuthnCreate, "x")),
		"not json":         json.RawMessage(`nope`),
	}
	cross := credential(testAppBase, WebAuthnGet, "x")
	cd, _ := json.Marshal(map[string]any{"type": WebAuthnGet, "origin": testAppBase, "challenge": "c", "crossOrigin": true})
	cross["response"].(map[string]any)["clientDataJSON"] = base64.RawURLEncoding.EncodeToString(cd)
	cases["cross origin"] = good(cross)
	for name, cred := range cases {
		if err := CheckClientData(cred, testAppBase, WebAuthnGet); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ── Passkey sign-in ────────────────────────────────────────────────────

func seedPasskeyUser(h *harness, verified bool) *fakeUser {
	u := h.z.addUser("pk@acme.com", verified, "")
	u.passkeys = []Passkey{{ID: "pk1", Name: "laptop"}}
	return u
}

func TestPasskeySignIn_SignsIn(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := seedPasskeyUser(h, true)
	b := h.browser()
	status, body := b.post("/auth/passkey/begin", map[string]any{"email": "PK@acme.com"})
	if status != 200 || body["options"] == nil {
		t.Fatalf("begin: %d %v", status, body)
	}
	if rp := body["options"].(map[string]any)["publicKey"].(map[string]any)["rpId"]; rp != "app.test" {
		t.Errorf("rpId = %v, want the app's host", rp)
	}
	status, body = b.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")})
	if status != 200 || body["subject"] != u.id || b.signedIn() != "at-"+u.id {
		t.Fatalf("finish: %d %v", status, body)
	}
}

func TestPasskeySignIn_OriginMismatchNeverReachesTheIssuer(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	seedPasskeyUser(h, true)
	b := h.browser()
	b.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	for _, cred := range []map[string]any{
		credential("https://phish.test", WebAuthnGet, "good"),
		credential(testAppBase, WebAuthnCreate, "good"),
	} {
		status, body := b.post("/auth/passkey/finish", map[string]any{"credential": cred})
		want(t, status, body, 401, CodePasskeyFailed)
	}
	if n := h.z.callCount("PATCH /v2/sessions"); n != 0 {
		t.Fatalf("a mismatched origin reached the issuer %d times", n)
	}
	if b.signedIn() != "" {
		t.Fatal("signed in")
	}
}

func TestPasskeySignIn_UnknownAddressGetsADecoyOfTheSameShape(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	seedPasskeyUser(h, true)
	real := h.browser()
	_, realBody := real.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	decoy := h.browser()
	status, decoyBody := decoy.post("/auth/passkey/begin", map[string]any{"email": "nobody@acme.com"})
	if status != 200 {
		t.Fatalf("decoy begin: %d", status)
	}
	keys := func(m map[string]any) string {
		pk := m["options"].(map[string]any)["publicKey"].(map[string]any)
		var ks []string
		for k := range pk {
			ks = append(ks, k)
		}
		return strings.Join(sortStrings(ks), ",")
	}
	if keys(realBody) != keys(decoyBody) {
		t.Errorf("decoy shape %s differs from the real %s", keys(decoyBody), keys(realBody))
	}
	if strings.Join(real.cookieNames(), ",") != strings.Join(decoy.cookieNames(), ",") {
		t.Errorf("cookies differ: %v vs %v", real.cookieNames(), decoy.cookieNames())
	}
	before := h.z.totalCalls()
	status, body := decoy.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")})
	want(t, status, body, 401, CodePasskeyFailed)
	if h.z.totalCalls() != before {
		t.Fatal("finishing a decoy called the issuer")
	}
}

func TestPasskeySignIn_FinishWithoutBeginIsRefusedBeforeTheIssuer(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	status, body := h.browser().post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")})
	want(t, status, body, 401, CodePasskeyFailed)
	if h.z.totalCalls() != 0 {
		t.Fatal("the issuer was called")
	}
}

func TestPasskeySignIn_ABadAssertionIsNotFinalized(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	seedPasskeyUser(h, true)
	b := h.browser()
	b.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	status, body := b.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "forged")})
	want(t, status, body, 401, CodePasskeyFailed)
	if h.z.callCount("POST /v2/oidc/auth_requests") != 0 {
		t.Fatal("a rejected assertion was finalized")
	}
}

func TestPasskeySignIn_UnverifiedAddressGetsNoSession(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	seedPasskeyUser(h, false)
	b := h.browser()
	b.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	status, body := b.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")})
	want(t, status, body, 403, CodeEmailUnverified)
	if b.signedIn() != "" || h.z.callCount("POST /v2/oidc/auth_requests") != 0 {
		t.Fatal("an unverified address got a session")
	}
}

// ── Passkey registration ───────────────────────────────────────────────

func TestPasskeyRegister_SignedInUserFromTheTokenNotTheBody(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := h.z.addUser("me@acme.com", true, "longpassword")
	victim := h.z.addUser("victim@acme.com", true, "longpassword")
	b := h.browser()
	if status, body := b.post("/auth/passkey/register/begin", map[string]any{"userID": victim.id}); status != 401 {
		t.Fatalf("signed-out register: %d %v", status, body)
	}
	srvURL, _ := url.Parse(h.srv.URL)
	b.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "app_session", Value: "at-" + u.id, Path: "/"}})
	status, body := b.post("/auth/passkey/register/begin", map[string]any{"userID": victim.id})
	if status != 200 || body["options"] == nil {
		t.Fatalf("begin: %d %v", status, body)
	}
	if victim.pendingPasskey != "" || u.pendingPasskey == "" {
		t.Fatal("the registration was started for the body's user, not the session's")
	}
	status, body = b.post("/auth/passkey/register/finish", map[string]any{"credential": credential("https://phish.test", WebAuthnCreate, "x"), "name": "x"})
	want(t, status, body, 400, CodePasskeyFailed)
	if len(u.passkeys) != 0 {
		t.Fatal("a mismatched origin registered a passkey")
	}
	status, body = b.post("/auth/passkey/register/finish", map[string]any{"credential": credential(testAppBase, WebAuthnCreate, "x"), "name": "laptop"})
	if status != 200 || body["registered"] != true || len(u.passkeys) != 1 || u.passkeys[0].Name != "laptop" {
		t.Fatalf("finish: %d %v %+v", status, body, u.passkeys)
	}
	status, body = b.do(http.MethodGet, "/auth/passkeys", nil)
	if status != 200 || len(body["passkeys"].([]any)) != 1 {
		t.Fatalf("list: %d %v", status, body)
	}
}

// ── Magic link ─────────────────────────────────────────────────────────

func TestMagicLink_SignsInInTheRequestingBrowserOnly(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := h.z.addUser("ml@acme.com", true, "")
	asker := h.browser()
	status, body := asker.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
	if status != 202 || body["message"] != msgMagicSent {
		t.Fatalf("request: %d %v", status, body)
	}
	mail := h.z.lastMail(t)
	if !strings.HasPrefix(mail.link, testAppBase+"/auth/magic?code=") || mail.kind != "otp" {
		t.Fatalf("mailed link %q (%s)", mail.link, mail.kind)
	}

	// Another device, or a link scanner: no binding cookie, no issuer call.
	before := h.z.totalCalls()
	status, body = h.browser().post("/auth/magic/consume", map[string]any{"code": mail.code})
	want(t, status, body, 400, CodeMagicOtherBrowser)
	if h.z.totalCalls() != before {
		t.Fatal("a consume from another browser reached the issuer (it would burn the code)")
	}

	status, body = asker.post("/auth/magic/consume", map[string]any{"code": "wrong"})
	want(t, status, body, 401, CodeInvalidCode)
	status, body = asker.post("/auth/magic/consume", map[string]any{"code": mail.code})
	if status != 200 || body["subject"] != u.id || asker.signedIn() == "" {
		t.Fatalf("consume: %d %v", status, body)
	}
	// Single use: the binding is gone, and the issuer burned the code.
	status, body = asker.post("/auth/magic/consume", map[string]any{"code": mail.code})
	want(t, status, body, 400, CodeMagicOtherBrowser)
}

func TestMagicLink_AConsumedCodeIsRefusedInAFreshRequest(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	h.z.addUser("ml@acme.com", true, "")
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
	first := h.z.lastMail(t)
	b.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
	// The first link's code belongs to a session this browser no longer holds.
	status, body := b.post("/auth/magic/consume", map[string]any{"code": first.code})
	want(t, status, body, 401, CodeInvalidCode)
}

func TestMagicLink_UniformForUnknownAndRateLimitedAddresses(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	h.z.addUser("ml@acme.com", true, "")
	known, unknown := h.browser(), h.browser()
	ks, kb := known.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
	us, ub := unknown.post("/auth/magic/request", map[string]any{"email": "nobody@acme.com"})
	if ks != us || kb["message"] != ub["message"] || strings.Join(known.cookieNames(), ",") != strings.Join(unknown.cookieNames(), ",") {
		t.Fatalf("known %d %v %v vs unknown %d %v %v", ks, kb, known.cookieNames(), us, ub, unknown.cookieNames())
	}
	if h.z.userByEmail("nobody@acme.com") != nil {
		t.Fatal("invite-only sign-up created an account from a magic link")
	}
	status, body := unknown.post("/auth/magic/consume", map[string]any{"code": "123"})
	want(t, status, body, 401, CodeInvalidCode)

	mails := h.z.mailCount()
	for i := 0; i < 4; i++ {
		status, body = known.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
		if status != 202 || body["message"] != msgMagicSent {
			t.Fatalf("rate-limited request answered differently: %d %v", status, body)
		}
	}
	if sent := h.z.mailCount() - mails; sent != 2 {
		t.Errorf("sent %d more mails, want 2 (3 per address per 15 min)", sent)
	}
}

func TestMagicLink_OpenSignupVerifiesTheAddressByTheClick(t *testing.T) {
	h := newHarness(t, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodMagicLink}, Mail: MailDev, RequireVerifiedEmail: true})
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "new@else.com"})
	u := h.z.userByEmail("new@else.com")
	if u == nil || u.verified || u.password != "" {
		t.Fatalf("want an unverified, credential-less account: %+v", u)
	}
	if len(h.z.grants) != 0 {
		t.Fatal("the sign-up role was granted before the address was verified")
	}
	mail := h.z.lastMail(t)
	if mail.kind != "verify" || !strings.HasPrefix(mail.link, testAppBase+"/auth/magic?code=") {
		t.Fatalf("mail %+v", mail)
	}
	status, body := b.post("/auth/magic/consume", map[string]any{"code": mail.code})
	if status != 200 || body["subject"] != u.id {
		t.Fatalf("consume: %d %v", status, body)
	}
	if !h.z.user(u.id).verified || len(h.z.grants) != 1 || h.z.grants[0] != u.id+":member" {
		t.Fatalf("verified=%v grants=%v", h.z.user(u.id).verified, h.z.grants)
	}
}

func TestMagicLink_ConsumeAttemptsAreCapped(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	h.z.addUser("ml@acme.com", true, "")
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "ml@acme.com"})
	for i := 0; i < 5; i++ {
		b.post("/auth/magic/consume", map[string]any{"code": "guess"})
	}
	status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code})
	want(t, status, body, 429, CodeRateLimited)
}

func TestBindingCookie_TamperedOrForeignIsRefused(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	s := h.routes.seal
	v, err := s.seal(cookieMagic, magicBinding{Kind: "v", UserID: "u1"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var out magicBinding
	if err := s.open(cookieMagic, v, &out); err != nil || out.UserID != "u1" {
		t.Fatalf("round trip: %v %+v", err, out)
	}
	if s.open(cookiePasskey, v, &out) == nil {
		t.Error("a magic-link cookie opened as a passkey cookie")
	}
	raw := []byte(v)
	raw[len(raw)-2] ^= 1
	if s.open(cookieMagic, string(raw), &out) == nil {
		t.Error("a tampered cookie opened")
	}
	other := &sealer{key: func() []byte { return []byte("another-token") }, now: s.now}
	if other.open(cookieMagic, v, &out) == nil {
		t.Error("a cookie sealed under another broker token opened")
	}
	expired, _ := s.seal(cookieMagic, magicBinding{Kind: "v"}, -time.Second)
	if s.open(cookieMagic, expired, &out) == nil {
		t.Error("an expired cookie opened")
	}
}

// ── Social ─────────────────────────────────────────────────────────────

func startSocial(t *testing.T, b *browser) string {
	t.Helper()
	status, body := b.post("/auth/social/start", map[string]any{"provider": "google", "returnTo": "/projects"})
	if status != 200 {
		t.Fatalf("start: %d %v", status, body)
	}
	u, _ := url.Parse(body["redirect"].(string))
	return u.Query().Get("state")
}

func intentResult(email string, verified bool) map[string]any {
	return map[string]any{
		"idpInformation": map[string]any{"idpId": "idp-google", "userId": "g-123", "userName": email},
		"addHumanUser": map[string]any{"profile": map[string]any{"givenName": "G", "familyName": "User"},
			"email": map[string]any{"email": email, "isVerified": verified}},
	}
}

func TestSocial_CallbackRequiresTheStartingBrowser(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", intentResult("g@acme.com", true))
	before := h.z.totalCalls()
	status, body := h.browser().post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	want(t, status, body, 401, CodeSocialFailed)
	status, body = b.post("/auth/social/callback", map[string]any{"id": "intent-other", "token": "tok"})
	want(t, status, body, 401, CodeSocialFailed)
	if h.z.totalCalls() != before {
		t.Fatal("a callback without the binding reached the issuer")
	}
}

func TestSocial_UnknownProviderAndUnsafeReturnTo(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	status, body := h.browser().post("/auth/social/start", map[string]any{"provider": "github"})
	want(t, status, body, 404, CodeUnknownProvider)
	for in, out := range map[string]string{"/a?b=1": "/a?b=1", "https://evil.test/": "/", "//evil.test": "/",
		"/\\evil.test": "/", "": "/", "javascript:alert(1)": "/"} {
		if got := SafeReturnTo(in); got != out {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestSocial_LinkedIdentitySignsIn(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods, google))
	u := h.z.addUser("g@acme.com", true, "")
	u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123"}}
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", intentResult("g@acme.com", true))
	status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	if status != 200 || body["returnTo"] != "/projects" || body["identity"].(map[string]any)["subject"] != u.id {
		t.Fatalf("callback: %d %v", status, body)
	}
}

func TestSocial_LinksOnlyWhenBothAddressesAreVerified(t *testing.T) {
	for _, tc := range []struct {
		name             string
		accountVerified  bool
		providerEmail    string
		providerVerified bool
		wantLinked       bool
		wantStatus       int
		wantCode         string
	}{
		{"both verified", true, "g@acme.com", true, true, 200, ""},
		{"provider unverified", true, "g@acme.com", false, false, 409, CodeSocialLinkRequired},
		{"account unverified", false, "g@acme.com", true, false, 409, CodeSocialLinkRequired},
		// Zitadel's GitHub provider reports "verified" with no address at all.
		{"no provider address", true, "", true, false, 403, CodeSocialNoAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupInvite, allMethods, google))
			u := h.z.addUser("g@acme.com", tc.accountVerified, "pw-long-enough")
			b := h.browser()
			id := startSocial(t, b)
			h.z.addIntent(id, "tok", intentResult(tc.providerEmail, tc.providerVerified))
			status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
			want(t, status, body, tc.wantStatus, tc.wantCode)
			if linked := len(h.z.user(u.id).links) == 1; linked != tc.wantLinked {
				t.Fatalf("linked = %v, want %v", linked, tc.wantLinked)
			}
			if !tc.wantLinked && b.signedIn() != "" {
				t.Fatal("signed in without a link")
			}
		})
	}
}

func TestSocial_NoAccountPerSignupMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		signup     string
		email      string
		verified   bool
		wantStatus int
		wantCode   string
		wantUser   bool
	}{
		{"invite refuses", SignupInvite, "new@acme.com", true, 403, CodeSocialNoAccount, false},
		{"open creates verified", SignupOpen, "new@else.com", true, 200, "", true},
		{"open unverified creates without a session", SignupOpen, "new@else.com", false, 202, "", true},
		{"domain allows a listed verified domain", SignupDomain, "new@acme.com", true, 200, "", true},
		{"domain refuses an unlisted domain", SignupDomain, "new@else.com", true, 403, CodeSignupNotAllowed, false},
		{"domain refuses an unverified address", SignupDomain, "new@acme.com", false, 403, CodeSignupNotAllowed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, policyWith(tc.signup, allMethods, google))
			b := h.browser()
			id := startSocial(t, b)
			h.z.addIntent(id, "tok", intentResult(tc.email, tc.verified))
			status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
			want(t, status, body, tc.wantStatus, tc.wantCode)
			u := h.z.userByEmail(tc.email)
			if (u != nil) != tc.wantUser {
				t.Fatalf("account created = %v, want %v", u != nil, tc.wantUser)
			}
			// A provider link is persisted at creation only for an address the
			// provider asserted; an unverified one waits for the address's code.
			wantLinks := 0
			if tc.verified {
				wantLinks = 1
			}
			if u != nil && (u.verified != tc.verified || len(u.links) != wantLinks) {
				t.Fatalf("account %+v", u)
			}
			if (b.signedIn() != "") != (tc.wantStatus == 200) {
				t.Fatalf("session = %q at status %d", b.signedIn(), status)
			}
		})
	}
}

// ── Invitations ────────────────────────────────────────────────────────

func invitee(h *harness) *fakeUser {
	u := h.z.addUser("invitee@acme.com", false, "")
	u.inviteCode = "inv-1"
	return u
}

func TestInvite_PasswordSetsItAndSignsIn(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	b := h.browser()
	status, body := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "a-long-password"})
	if status != 200 || body["subject"] != u.id || !u.verified || u.password != "a-long-password" {
		t.Fatalf("accept: %d %v %+v", status, body, u)
	}
	// Single use.
	status, body = h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "another-long-one"})
	want(t, status, body, 401, CodeInvalidCode)
}

func TestInvite_WrongCodeMintsNothing(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	status, body := h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "guess", "method": "password", "password": "a-long-password"})
	want(t, status, body, 401, CodeInvalidCode)
	if h.z.callCount("POST /v2/users/"+u.id+"/password") != 0 || u.password != "" {
		t.Fatal("a password was set without a valid invitation")
	}
	status, body = h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "guess", "method": "passkey"})
	want(t, status, body, 401, CodeInvalidCode)
	// Listing passkeys is the proof's revocation (proveAddress), not a mint.
	if h.z.callCount("POST /v2/users/"+u.id+"/passkeys") != h.z.callCount("POST /v2/users/"+u.id+"/passkeys/_search") {
		t.Fatal("a passkey registration code was minted without a valid invitation")
	}
}

func TestInvite_AWeakPasswordDoesNotSpendTheInvitation(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	status, body := h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "short"})
	want(t, status, body, 400, "password_policy")
	if u.inviteCode != "inv-1" {
		t.Fatal("the invitation was spent on a password the policy refuses")
	}
}

func TestInvite_PasskeyEnrolsThenSignsIn(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	b := h.browser()
	status, body := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "passkey"})
	if status != 200 || body["enroll"] != true {
		t.Fatalf("accept: %d %v", status, body)
	}
	if status, body = b.post("/auth/passkey/register/begin", map[string]any{}); status != 200 {
		t.Fatalf("register begin: %d %v", status, body)
	}
	if status, body = b.post("/auth/passkey/register/finish", map[string]any{"credential": credential(testAppBase, WebAuthnCreate, "x")}); status != 200 {
		t.Fatalf("register finish: %d %v", status, body)
	}
	// The follow-up sign-in names no address: the enrolment cookie does.
	if status, body = b.post("/auth/passkey/begin", map[string]any{}); status != 200 {
		t.Fatalf("begin: %d %v", status, body)
	}
	status, body = b.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")})
	if status != 200 || body["subject"] != u.id {
		t.Fatalf("finish: %d %v", status, body)
	}
	// An enrolment cookie from one browser does not enrol in another.
	if status, _ = h.browser().post("/auth/passkey/register/begin", map[string]any{}); status != 401 {
		t.Fatalf("a fresh browser could register: %d", status)
	}
}

func TestInvite_MagicLinkMailsThisBrowser(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	b := h.browser()
	status, _ := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "magic_link"})
	if status != 202 {
		t.Fatalf("accept: %d", status)
	}
	status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code})
	if status != 200 || body["subject"] != u.id {
		t.Fatalf("consume: %d %v", status, body)
	}
}

func TestInvite_MethodMustBeOffered(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, []string{MethodPassword}))
	u := invitee(h)
	status, body := h.browser().post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "passkey"})
	want(t, status, body, 400, CodeMethodNotOffered)
	if u.inviteCode != "inv-1" {
		t.Fatal("the invitation was spent on a method the policy does not offer")
	}
}

// ── Verification and sign-up ───────────────────────────────────────────

func TestRegister_OpenAnswersTheSameForNewAndExistingAddresses(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	h.z.addUser("taken@acme.com", true, "existing-password")
	b := h.browser()
	ns, nb := b.post("/auth/register", map[string]any{"email": "fresh@acme.com", "password": "a-long-password"})
	es, eb := b.post("/auth/register", map[string]any{"email": "taken@acme.com", "password": "a-long-password"})
	if ns != 202 || es != 202 || nb["message"] != eb["message"] || nb["verify"] != "sent" {
		t.Fatalf("new %d %v vs existing %d %v", ns, nb, es, eb)
	}
	if b.signedIn() != "" {
		t.Fatal("sign-up issued a session before verification")
	}
	u := h.z.userByEmail("fresh@acme.com")
	if u == nil || u.verified {
		t.Fatalf("want an unverified account: %+v", u)
	}
	mail := h.z.lastMail(t)
	if mail.link != testAppBase+"/auth/verify?userID="+u.id+"&code="+mail.code {
		t.Fatalf("verify link %q", mail.link)
	}
	// A weak password is refused for every address alike, before the lookup.
	ws, _ := b.post("/auth/register", map[string]any{"email": "taken@acme.com", "password": "short"})
	ns2, _ := b.post("/auth/register", map[string]any{"email": "other@acme.com", "password": "short"})
	if ws != 400 || ns2 != 400 {
		t.Fatalf("weak password: existing %d, new %d", ws, ns2)
	}
}

func TestRegister_DomainAndPasswordRules(t *testing.T) {
	h := newHarness(t, policyWith(SignupDomain, []string{MethodPassword}))
	b := h.browser()
	status, body := b.post("/auth/register", map[string]any{"email": "a@else.com", "password": "a-long-password"})
	want(t, status, body, 403, CodeSignupNotAllowed)
	status, body = b.post("/auth/register", map[string]any{"email": "a@acme.com"})
	want(t, status, body, 400, CodePasswordRequired)
	if h.z.userByEmail("a@else.com") != nil || h.z.userByEmail("a@acme.com") != nil {
		t.Fatal("an account was created")
	}
}

func TestVerify_GrantsTheRoleAndOffersPasskeyEnrolment(t *testing.T) {
	h := newHarness(t, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodPasskey}, Mail: MailDev, RequireVerifiedEmail: true})
	b := h.browser()
	b.post("/auth/register", map[string]any{"email": "pk@else.com"})
	u := h.z.userByEmail("pk@else.com")
	mail := h.z.lastMail(t)
	status, body := b.post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"})
	want(t, status, body, 401, CodeInvalidCode)
	if len(h.z.grants) != 0 {
		t.Fatal("granted on a wrong code")
	}
	status, body = b.post("/auth/verify", map[string]any{"userID": u.id, "code": mail.code})
	if status != 200 || body["verified"] != true || body["enroll"] != true {
		t.Fatalf("verify: %d %v", status, body)
	}
	if len(h.z.grants) != 1 || b.signedIn() != "" {
		t.Fatalf("grants %v, session %q (verify grants the role and issues no session)", h.z.grants, b.signedIn())
	}
	if status, _ = b.post("/auth/passkey/register/begin", map[string]any{}); status != 200 {
		t.Fatalf("enrolment after verify: %d", status)
	}
}

func TestVerifyResend_IsUniform(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	h.z.addUser("pending@acme.com", false, "")
	h.z.addUser("done@acme.com", true, "")
	var bodies []string
	for _, e := range []string{"pending@acme.com", "done@acme.com", "nobody@acme.com"} {
		status, body := h.browser().post("/auth/verify/resend", map[string]any{"email": e})
		if status != 202 {
			t.Fatalf("%s: %d", e, status)
		}
		raw, _ := json.Marshal(body)
		bodies = append(bodies, string(raw))
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("answers differ: %v", bodies)
	}
	if h.z.mailCount() != 1 {
		t.Fatalf("sent %d mails, want 1 (only the pending address)", h.z.mailCount())
	}
}

// ── The gate, at the library boundary ──────────────────────────────────

func TestCompleteSignIn_RefusesASessionWithNoVerifiedFactor(t *testing.T) {
	// The bypass: a session with only a user check. The fake finalizes it,
	// as the real issuer does; CompleteSignIn must not.
	z := newFakeZitadel(t)
	u := z.addUser("pk@acme.com", true, "")
	u.passkeys = []Passkey{{ID: "pk1"}}
	c := z.client()
	cs, err := c.CreateChallengeSession(context.Background(), "", u.id, Challenge{WebAuthnDomain: "app.test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CompleteSignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "r"}, Session{ID: cs.ID, Token: cs.Token})
	if !errors.Is(err, ErrNoVerifiedFactor) {
		t.Fatalf("err = %v, want ErrNoVerifiedFactor", err)
	}
	if z.callCount("POST /v2/oidc/auth_requests") != 0 {
		t.Fatal("an unverified session was finalized")
	}
}

func TestSignIn_RequireVerifiedEmail(t *testing.T) {
	z := newFakeZitadel(t)
	z.addUser("unverified@acme.com", false, "a-long-password")
	z.addUser("verified@acme.com", true, "a-long-password")
	c := z.client()
	cfg := FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true}
	if _, err := c.SignIn(context.Background(), cfg, Credentials{LoginName: "unverified@acme.com", Password: "a-long-password"}); !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("err = %v, want ErrEmailUnverified", err)
	}
	if z.callCount("POST /v2/oidc/auth_requests") != 0 {
		t.Fatal("an unverified address was finalized")
	}
	// The gate runs only after the password: a wrong one says nothing about the address.
	if _, err := c.SignIn(context.Background(), cfg, Credentials{LoginName: "unverified@acme.com", Password: "wrong-password"}); errors.Is(err, ErrEmailUnverified) {
		t.Fatal("the address state leaked to someone without the password")
	}
	if _, err := c.SignIn(context.Background(), cfg, Credentials{LoginName: "verified@acme.com", Password: "a-long-password"}); err != nil {
		t.Fatalf("verified sign-in: %v", err)
	}
	cfg.RequireVerifiedEmail = false
	if _, err := c.SignIn(context.Background(), cfg, Credentials{LoginName: "unverified@acme.com", Password: "a-long-password"}); err != nil {
		t.Fatalf("legacy (no gate) sign-in: %v", err)
	}
}

func TestBuildChecks_IDPIntentIsVerifyingAndUserIDIsExclusive(t *testing.T) {
	checks, err := buildChecks(Credentials{UserID: "u1", IDPIntent: &IDPIntent{ID: "i", Token: "t"}})
	if err != nil || checks["idpIntent"] == nil || sub(checks, "user")["userId"] != "u1" {
		t.Fatalf("checks %v, %v", checks, err)
	}
	if _, err := buildChecks(Credentials{UserID: "u1", IDPIntent: &IDPIntent{ID: "i"}}); err == nil {
		t.Error("an intent with no token counted as a verifying factor")
	}
	if _, err := buildChecks(Credentials{UserID: "u1", LoginName: "x", Password: "p"}); err == nil {
		t.Error("a user check by both id and login name was accepted")
	}
}

// ── Review regressions ─────────────────────────────────────────────────

func (b *browser) cookie(name string) string {
	u, _ := url.Parse(b.h.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// Pre-registration takeover: an attacker whose provider reports a victim's
// address UNVERIFIED creates the account; the victim later confirms the
// address from their own browser. The attacker's provider account must not be
// linked, or it signs the attacker into the now-verified account.
func TestSocial_UnverifiedSignupLinksOnlyInTheBrowserThatProvesTheAddress(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	attacker := h.browser()
	id := startSocial(t, attacker)
	h.z.addIntent(id, "tok", intentResult("victim@else.com", false))
	status, body := attacker.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	if status != 202 || body["verify"] != "sent" {
		t.Fatalf("callback: %d %v", status, body)
	}
	u := h.z.userByEmail("victim@else.com")
	if u == nil || len(u.links) != 0 {
		t.Fatalf("the unverified provider account was linked at creation: %+v", u)
	}
	// The victim confirms the address in their own browser.
	victim := h.browser()
	mail := h.z.lastMail(t)
	if status, body = victim.post("/auth/verify", map[string]any{"userID": u.id, "code": mail.code}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if len(h.z.user(u.id).links) != 0 {
		t.Fatal("a verification in another browser attached the pending link")
	}
	// The attacker comes back through the provider: no link, so no way in.
	id = startSocial(t, attacker)
	h.z.addIntent(id, "tok2", intentResult("victim@else.com", false))
	status, body = attacker.post("/auth/social/callback", map[string]any{"id": id, "token": "tok2"})
	want(t, status, body, 409, CodeSocialLinkRequired)
	if attacker.signedIn() != "" {
		t.Fatal("the attacker signed into the victim's account")
	}
}

func TestSocial_UnverifiedSignupLinksOnceThisBrowserProvesTheAddress(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", intentResult("me@else.com", false))
	if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 202 {
		t.Fatalf("callback: %d %v", status, body)
	}
	u := h.z.userByEmail("me@else.com")
	if status, body := b.post("/auth/verify", map[string]any{"userID": u.id, "code": "wrong"}); status != 401 || len(u.links) != 0 {
		t.Fatalf("a wrong code: %d %v, links %v", status, body, u.links)
	}
	if status, body := b.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if links := h.z.user(u.id).links; len(links) != 1 || links[0].UserID != "g-123" {
		t.Fatalf("links after this browser verified: %+v", links)
	}
	// Now the provider account signs in.
	id = startSocial(t, b)
	h.z.addIntent(id, "tok2", intentResult("me@else.com", false))
	status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok2"})
	if status != 200 || body["identity"].(map[string]any)["subject"] != u.id {
		t.Fatalf("sign-in after the link: %d %v", status, body)
	}
}

// Sealing hides content, not length: every outcome of a ceremony must seal
// to the same cookie length.
func TestBindingCookie_LengthDoesNotTellTheOutcome(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	h.z.addUser("known@acme.com", true, "")
	h.z.addUser("pending@acme.com", false, "")
	magic := func(email string, times int) int {
		b := h.browser()
		for i := 0; i < times; i++ {
			if status, _ := b.post("/auth/magic/request", map[string]any{"email": email}); status != 202 {
				t.Fatalf("%s: %d", email, status)
			}
		}
		v := b.cookie("app" + cookieMagic)
		if v == "" {
			t.Fatalf("%s: no binding cookie", email)
		}
		return len(v)
	}
	lengths := map[string]int{
		"known":        magic("known@acme.com", 1),
		"unknown":      magic("nobody@acme.com", 1),
		"unverified":   magic("pending@acme.com", 1),
		"rate-limited": magic("known@acme.com", 3), // the 4th request for the address
	}
	for name, n := range lengths {
		if n != lengths["known"] {
			t.Errorf("magic-link cookie lengths differ by outcome: %v (%s)", lengths, name)
		}
	}

	seedPasskeyUser(h, true)
	real, decoy := h.browser(), h.browser()
	real.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	decoy.post("/auth/passkey/begin", map[string]any{"email": "nobody@acme.com"})
	if r, d := len(real.cookie("app"+cookiePasskey)), len(decoy.cookie("app"+cookiePasskey)); r == 0 || r != d {
		t.Errorf("passkey cookie lengths: real %d, decoy %d", r, d)
	}
}

// mail "none": nothing can deliver a verification link, and the converged
// policy does not require one. The sign-up form must still end in a session.
func TestRegister_NoMailSignsInAtOnce(t *testing.T) {
	p := LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodPassword}, Mail: MailNone, RequireVerifiedEmail: false}
	if !p.RegisterServed() || !OptionsFor(p, false).SelfSignup {
		t.Fatal("the no-mail open policy does not offer its sign-up form")
	}
	h := newHarness(t, p)
	b := h.browser()
	status, body := b.post("/auth/register", map[string]any{"email": "new@else.com", "password": "a-long-password"})
	u := h.z.userByEmail("new@else.com")
	if status != 200 || u == nil || body["subject"] != u.id || b.signedIn() == "" {
		t.Fatalf("register: %d %v", status, body)
	}
	if u.verified || len(h.z.grants) != 1 || h.z.grants[0] != u.id+":member" {
		t.Fatalf("verified=%v grants=%v", u.verified, h.z.grants)
	}
	status, body = h.browser().post("/auth/register", map[string]any{"email": "new@else.com", "password": "a-long-password"})
	want(t, status, body, 409, CodeAccountExists)
	status, body = h.browser().post("/auth/register", map[string]any{"email": "other@else.com"})
	want(t, status, body, 400, CodePasswordRequired)
}

func TestRegister_RefusedWhereItCouldNeverComplete(t *testing.T) {
	for name, p := range map[string]LoginPolicy{
		"no mail, verified email required": {Present: true, Version: 1, Signup: SignupOpen,
			Methods: []string{MethodPassword}, Mail: MailNone, RequireVerifiedEmail: true},
		"no mail, passwordless": {Present: true, Version: 1, Signup: SignupOpen,
			Methods: []string{MethodPasskey, MethodMagicLink}, Mail: MailNone},
		"no mail, domain": {Present: true, Version: 1, Signup: SignupDomain, SignupDomains: []string{"acme.com"},
			Methods: []string{MethodPassword}, Mail: MailPaused},
	} {
		t.Run(name, func(t *testing.T) {
			if p.RegisterServed() || OptionsFor(p, true).SelfSignup {
				t.Fatal("a sign-up form that cannot complete is offered")
			}
			h := newHarness(t, p)
			status, body := h.browser().post("/auth/register", map[string]any{"email": "new@acme.com", "password": "a-long-password"})
			want(t, status, body, 403, CodeSignupNotAllowed)
			if h.z.userByEmail("new@acme.com") != nil {
				t.Fatal("an account was created")
			}
		})
	}
}

func socialOnlyInvitePolicy() LoginPolicy {
	return policyWith(SignupInvite, nil, google)
}

func startInviteSocial(t *testing.T, b *browser, userID, code string) string {
	t.Helper()
	status, body := b.post("/auth/invite/accept", map[string]any{"userID": userID, "code": code, "method": "social", "provider": "google"})
	if status != 200 {
		t.Fatalf("accept: %d %v", status, body)
	}
	u, _ := url.Parse(body["redirect"].(string))
	return u.Query().Get("state")
}

func TestInvite_SocialOnlyPolicyAcceptsByConnectingAProvider(t *testing.T) {
	h := newHarness(t, socialOnlyInvitePolicy())
	u := invitee(h)
	b := h.browser()
	id := startInviteSocial(t, b, u.id, "inv-1")
	if u.inviteCode != "inv-1" {
		t.Fatal("the invitation was spent before the provider round trip")
	}
	// The provider's address need not match: the invitation is the proof.
	h.z.addIntent(id, "tok", intentResult("someone@gmail.com", false))
	status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	if status != 200 || body["identity"].(map[string]any)["subject"] != u.id {
		t.Fatalf("callback: %d %v", status, body)
	}
	if u.inviteCode != "" || !u.verified || len(u.links) != 1 || u.links[0].UserID != "g-123" {
		t.Fatalf("after acceptance: %+v", u)
	}
	// Another browser cannot finish this acceptance.
	id = startInviteSocial(t, b, u.id, "inv-1")
	h.z.addIntent(id, "tok2", intentResult("someone@gmail.com", true))
	status, body = h.browser().post("/auth/social/callback", map[string]any{"id": id, "token": "tok2"})
	want(t, status, body, 401, CodeSocialFailed)
}

func TestInvite_SocialChecksTheCodeBeforeLinking(t *testing.T) {
	h := newHarness(t, socialOnlyInvitePolicy())
	u := invitee(h)
	b := h.browser()
	id := startInviteSocial(t, b, u.id, "guess")
	h.z.addIntent(id, "tok", intentResult("someone@gmail.com", true))
	status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	want(t, status, body, 401, CodeInvalidCode)
	if len(u.links) != 0 || b.signedIn() != "" {
		t.Fatal("a provider account was linked without a valid invitation")
	}
}

func TestInvite_SocialRefusesAProviderAccountOfAnotherUser(t *testing.T) {
	h := newHarness(t, socialOnlyInvitePolicy())
	u := invitee(h)
	other := h.z.addUser("other@acme.com", true, "")
	other.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123"}}
	b := h.browser()
	id := startInviteSocial(t, b, u.id, "inv-1")
	h.z.addIntent(id, "tok", intentResult("other@acme.com", true))
	status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
	want(t, status, body, 409, CodeSocialTaken)
	if u.inviteCode != "inv-1" || b.signedIn() != "" {
		t.Fatal("the invitation was spent, or a session issued, for another user's provider account")
	}
	status, body = b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "social", "provider": "github"})
	want(t, status, body, 400, CodeMethodNotOffered)
}

// passwordSignsIn reports whether email and password sign in at the issuer.
func passwordSignsIn(h *harness, email, password string) bool {
	_, err := h.z.client().SignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "r", RequireVerifiedEmail: true},
		Credentials{LoginName: email, Password: password})
	return err == nil
}

// Pre-registration takeover by password: an attacker registers a victim's
// address with a password they know; the victim confirms the address from
// their own browser. The attacker's password must not open the account.
func TestRegister_PasswordIsSetOnlyInTheBrowserThatProvesTheAddress(t *testing.T) {
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	attacker := h.browser()
	if status, body := attacker.post("/auth/register", map[string]any{"email": "victim@else.com", "password": "attacker-password"}); status != 202 {
		t.Fatalf("register: %d %v", status, body)
	}
	u := h.z.userByEmail("victim@else.com")
	if u == nil || u.password != "" {
		t.Fatalf("the sign-up password was set before the address was proven: %+v", u)
	}
	victim := h.browser()
	if status, body := victim.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if h.z.user(u.id).password != "" || passwordSignsIn(h, "victim@else.com", "attacker-password") {
		t.Fatal("the attacker's password opens the account the victim verified")
	}

	// The victim registering the pending address in their own browser
	// sets THEIR password once that browser proves it.
	h2 := newHarness(t, policyWith(SignupOpen, allMethods))
	h2.browser().post("/auth/register", map[string]any{"email": "victim@else.com", "password": "attacker-password"})
	mine := h2.browser()
	if status, body := mine.post("/auth/register", map[string]any{"email": "victim@else.com", "password": "victim-password"}); status != 202 {
		t.Fatalf("register: %d %v", status, body)
	}
	u = h2.z.userByEmail("victim@else.com")
	if status, body := mine.post("/auth/verify", map[string]any{"userID": u.id, "code": h2.z.lastMail(t).code}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if !passwordSignsIn(h2, "victim@else.com", "victim-password") || passwordSignsIn(h2, "victim@else.com", "attacker-password") {
		t.Fatalf("after this browser verified, password is %q", h2.z.user(u.id).password)
	}
}

// A passkey finish fails alike for a decoy and a real challenge: status,
// body and Set-Cookie must not say which the address got.
func TestPasskeySignIn_FailuresDoNotTellADecoyFromARealChallenge(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	seedPasskeyUser(h, true)
	finish := func(email string, cred any) string {
		b := h.browser()
		b.post("/auth/passkey/begin", map[string]any{"email": email})
		raw, _ := json.Marshal(map[string]any{"credential": cred})
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/auth/passkey/finish", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("%d %s %q", resp.StatusCode, body, resp.Header.Values("Set-Cookie"))
	}
	for name, cred := range map[string]any{
		"malformed":    "not a credential",
		"wrong origin": credential("https://phish.test", WebAuthnGet, "good"),
		"wrong type":   credential(testAppBase, WebAuthnCreate, "good"),
	} {
		if r, d := finish("pk@acme.com", cred), finish("nobody@acme.com", cred); r != d {
			t.Errorf("%s: real %s, decoy %s", name, r, d)
		}
	}
	if n := h.z.callCount("PATCH /v2/sessions"); n != 0 {
		t.Fatalf("a refused assertion reached the issuer %d times", n)
	}
}

// A magic-link sign-up asked for twice before the first link is used still
// gets its signup_role from the replacement link, in any browser that asked.
func TestMagicLink_AReplacementLinkStillGrantsTheSignupRole(t *testing.T) {
	h := newHarness(t, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodMagicLink}, Mail: MailDev, RequireVerifiedEmail: true})
	h.browser().post("/auth/magic/request", map[string]any{"email": "new@else.com"})
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "new@else.com"})
	u := h.z.userByEmail("new@else.com")
	status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code})
	if status != 200 || body["subject"] != u.id {
		t.Fatalf("consume: %d %v", status, body)
	}
	if len(h.z.grants) != 1 || h.z.grants[0] != u.id+":member" {
		t.Fatalf("grants %v, want %s:member", h.z.grants, u.id)
	}
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

// A magic-link binding is charged its attempt budget whatever it holds, so
// exhausting it does not tell a known address from an unknown or unverified
// one: every outcome answers 401 five times, then 429.
func TestMagicLink_AttemptExhaustionDoesNotTellTheOutcome(t *testing.T) {
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	h.z.addUser("known@acme.com", true, "")
	h.z.addUser("unverified@acme.com", false, "")
	run := func(email string) string {
		b := h.browser()
		b.post("/auth/magic/request", map[string]any{"email": email})
		var seq []string
		for i := 0; i < 7; i++ {
			status, body := b.post("/auth/magic/consume", map[string]any{"code": "guess"})
			seq = append(seq, fmt.Sprintf("%d %v", status, body))
		}
		// Newline-separated: the error text itself contains "; ".
		return strings.Join(seq, "\n")
	}
	known := run("known@acme.com")
	if !strings.HasPrefix(strings.Split(known, "\n")[5], "429") {
		t.Fatalf("a known address was not rate limited after five wrong codes: %s", known)
	}
	for _, email := range []string{"nobody@acme.com", "unverified@acme.com"} {
		if got := run(email); got != known {
			t.Errorf("%s answered\n  %s\nknown answered\n  %s", email, got, known)
		}
	}
	// A second link for the same address does not buy fresh guesses, for a
	// decoy as for a real account.
	for _, email := range []string{"known@acme.com", "nobody@acme.com"} {
		b := h.browser()
		b.post("/auth/magic/request", map[string]any{"email": email})
		if status, body := b.post("/auth/magic/consume", map[string]any{"code": "guess"}); status != 429 {
			t.Errorf("%s: a replacement link answered %d %v, want 429", email, status, body)
		}
	}
}

// Password-only open sign-up: when the issuer refuses the chosen password
// at verification (a rule the broker cannot see) or fails to set it, the
// answer says the password was not set and hands this browser a reset code
// to choose another; the refused password is not kept.
func TestVerify_APasswordTheIssuerDoesNotSetIsRecoverable(t *testing.T) {
	for name, setup := range map[string]func(z *fakeZitadel){
		"issuer refuses": func(z *fakeZitadel) { z.refusedPassword = "no-symbol-password" },
		"issuer fails":   func(z *fakeZitadel) { z.passwordSetFails = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, policyWith(SignupOpen, []string{MethodPassword}))
			h.z.mu.Lock()
			setup(h.z)
			h.z.mu.Unlock()
			b := h.browser()
			if status, body := b.post("/auth/register", map[string]any{"email": "pw@else.com", "password": "no-symbol-password"}); status != 202 {
				t.Fatalf("register: %d %v", status, body)
			}
			u := h.z.userByEmail("pw@else.com")
			status, body := b.post("/auth/verify", map[string]any{"userID": u.id, "code": h.z.lastMail(t).code})
			if status != 200 || body["verified"] != true || body["password"] != passwordNotSet {
				t.Fatalf("verify: %d %v, want verified with password %q", status, body, passwordNotSet)
			}
			if h.z.user(u.id).password != "" {
				t.Fatal("the refused password was set")
			}
			reset, _ := body["reset"].(map[string]any)
			if reset == nil || reset["userID"] != u.id || reset["code"] == "" {
				t.Fatalf("no reset continuation: %v", body)
			}
			// The continuation sets a password chosen again.
			h.z.mu.Lock()
			h.z.refusedPassword, h.z.passwordSetFails = "", false
			h.z.mu.Unlock()
			if err := h.z.client().ResetPassword(context.Background(), PasswordReset{UserID: u.id,
				Code: reset["code"].(string), NewPassword: "a-better-password!"}); err != nil {
				t.Fatalf("the continuation's code does not set a password: %v", err)
			}
			if !passwordSignsIn(h, "pw@else.com", "a-better-password!") {
				t.Fatal("the password chosen again does not sign in")
			}
		})
	}
}
