package devidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// The account routes against the fake issuer (fakezitadel_test.go) and an
// in-memory session store. Every refusal below is the routes' own.

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type accountHarness struct {
	t        *testing.T
	z        *fakeZitadel
	clk      *testClock
	sessions *session.Manager
	routes   *AccountRoutes
	srv      *httptest.Server
	// signInCallbacks counts requests the wrapped sign-in callback got.
	signInCallbacks int
	grants          []UpstreamGrant
}

func newAccountHarness(t *testing.T, p LoginPolicy, opts ...func(*AccountRoutesConfig)) *accountHarness {
	t.Helper()
	return newAccountHarnessOn(t, session.NewMemoryStore(), p, opts...)
}

// newAccountHarnessOn is newAccountHarness over a session store of the
// test's choosing.
func newAccountHarnessOn(t *testing.T, store session.Store, p LoginPolicy, opts ...func(*AccountRoutesConfig)) *accountHarness {
	t.Helper()
	h := &accountHarness{t: t, z: newFakeZitadel(t), clk: &testClock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}}
	m, err := session.NewManager(session.Config{Store: store, Now: h.clk.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	h.sessions = m
	cfg := AccountRoutesConfig{
		Client: h.z.client(), Sessions: m, Policy: p, AppBase: testAppBase, CookiePrefix: "app",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: h.clk.Now,
		SessionID: func(r *http.Request) string {
			c, err := r.Cookie("app_session")
			if err != nil {
				return ""
			}
			return c.Value
		},
		PasswordPolicy: func(context.Context) (PasswordPolicy, error) { return PasswordPolicy{MinLength: 8}, nil },
		UpstreamTokens: func(_ context.Context, g UpstreamGrant) error {
			h.grants = append(h.grants, g)
			return nil
		},
	}
	for _, o := range opts {
		o(&cfg)
	}
	routes, err := NewAccountRoutes(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.routes = routes
	mux := http.NewServeMux()
	for path, fn := range map[string]http.HandlerFunc{
		AccountPath: routes.HandleAccount, AccountEmailPath: routes.HandleEmailChange, AccountEmailConfirmPath: routes.HandleEmailConfirm,
		AccountPasswordPath: routes.HandlePasswordChange, AccountLinkStartPath: routes.HandleLinkStart, AccountUnlinkPath: routes.HandleUnlink,
		AccountPasskeyRemovePath: routes.HandlePasskeyRemove, AccountSessionsPath: routes.HandleSessions,
		AccountSessionRevokePath: routes.HandleSessionRevoke, AccountSessionsRevokeOthersPath: routes.HandleRevokeOthers,
		ReauthPath: routes.HandleReauth,
	} {
		mux.Handle(path, fn)
	}
	mux.Handle(SocialCallbackPath, routes.SocialCallback(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.signInCallbacks++
		writeJSON(w, http.StatusTeapot, map[string]any{"signIn": true})
	})))
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

// signIn stores a session for u, as the broker does at sign-in, and returns
// a browser holding its cookie.
func (h *accountHarness) signIn(u *fakeUser) *accountBrowser {
	h.t.Helper()
	issued, err := h.sessions.Create(context.Background(),
		session.Tokens{AccessToken: "at-" + u.id, RefreshToken: "rt", Expiry: h.clk.Now().Add(time.Hour)},
		session.Meta{Subject: u.id, UserAgent: "test"})
	if err != nil {
		h.t.Fatal(err)
	}
	b := h.browser()
	b.setSession(issued.ID)
	return b
}

func (h *accountHarness) browser() *accountBrowser {
	jar, _ := cookiejar.New(nil)
	return &accountBrowser{h: h, client: &http.Client{Jar: jar}}
}

type accountBrowser struct {
	h      *accountHarness
	client *http.Client
}

func (b *accountBrowser) setSession(id string) {
	u, _ := url.Parse(b.h.srv.URL)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: "app_session", Value: id, Path: "/"}})
}

func (b *accountBrowser) session() string {
	u, _ := url.Parse(b.h.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == "app_session" {
			return c.Value
		}
	}
	return ""
}

func (b *accountBrowser) do(method, path string, body any, headers ...string) (int, map[string]any) {
	b.h.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
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

func TestAccount_OnlyACookieSessionIsSignedIn(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	id := b.session()

	status, body := h.browser().do(http.MethodGet, "/auth/account", nil)
	want(t, status, body, 401, CodeNotSignedIn)
	// The same session id as a bearer token: not a browser session.
	status, body = h.browser().do(http.MethodGet, "/auth/account", nil, "Authorization", "Bearer "+id)
	want(t, status, body, 401, CodeNotSignedIn)
	// A cookie holding an access token instead of a session id.
	other := h.browser()
	other.setSession("at-" + u.id)
	status, body = other.do(http.MethodGet, "/auth/account", nil)
	want(t, status, body, 401, CodeNotSignedIn)

	status, body = b.do(http.MethodGet, "/auth/account", nil)
	if status != 200 || body["email"] != "ada@acme.com" || body["subject"] != u.id || body["hasPassword"] != true {
		t.Fatalf("account = %d %v", status, body)
	}
	if list, _ := body["sessions"].([]any); len(list) != 1 || list[0].(map[string]any)["current"] != true {
		t.Errorf("sessions = %v, want this one, flagged current", body["sessions"])
	}

	// A revoked session is signed out at once.
	if _, _, err := h.sessions.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	status, body = b.do(http.MethodGet, "/auth/account", nil)
	want(t, status, body, 401, CodeNotSignedIn)
}

func TestAccount_CrossSiteMutationsAreRefused(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	before := h.z.totalCalls()
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/auth/reauth"}, {http.MethodPost, "/auth/account/password"}, {http.MethodPost, "/auth/account/email"},
		{http.MethodPost, "/auth/account/sessions/revoke-others"}, {http.MethodDelete, "/auth/account/links/google"},
	} {
		status, body := b.do(rt.method, rt.path, map[string]any{"password": "Password1!", "newPassword": "Another1!", "email": "x@acme.com"},
			"Sec-Fetch-Site", "cross-site")
		want(t, status, body, 403, CodeCrossSite)
	}
	if h.z.totalCalls() != before {
		t.Error("a cross-site request reached the issuer")
	}
}

func TestAccount_AStaleProofNeedsReauthentication(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	h.clk.Advance(DefaultReauthWindow + time.Second)

	status, body := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "new@acme.com"})
	want(t, status, body, 401, CodeReauthRequired)
	if m, _ := body["methods"].([]any); len(m) != 2 || m[0] != MethodPassword || m[1] != "sign_in" {
		t.Errorf("methods = %v, want [password sign_in]", body["methods"])
	}
	if u := h.z.user(u.id); u.email != "ada@acme.com" {
		t.Fatal("a stale session changed the address")
	}
	status, body = b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "wrong-password"})
	want(t, status, body, 401, CodeWrongPassword)
	status, _ = b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "Password1!"})
	if status != 200 {
		t.Fatalf("reauth with the right password = %d", status)
	}
	status, body = b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "new@acme.com"})
	if status != 202 {
		t.Fatalf("after reauth: %d %v", status, body)
	}
	// The proof's issuer session was only a proof.
	h.z.mu.Lock()
	for _, s := range h.z.sessions {
		if !s.deleted {
			t.Error("the re-authentication left an issuer session behind")
		}
	}
	h.z.mu.Unlock()
}

func TestAccount_ReauthenticationIsRateLimited(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	for i := 0; i < 5; i++ {
		status, body := b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "wrong-password"})
		want(t, status, body, 401, CodeWrongPassword)
	}
	status, body := b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "Password1!"})
	want(t, status, body, 429, CodeRateLimited)
}

func TestAccount_EmailChangeMailsTheNewAddressAndOnlyTheCodeVerifiesIt(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	h.z.addUser("taken@acme.com", true, "Password1!")
	b := h.signIn(u)

	status, body := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "taken@acme.com"})
	want(t, status, body, 409, CodeAddressInUse)
	status, body = b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "Ada@acme.com"})
	want(t, status, body, 400, CodeSameAddress)
	status, body = b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "not an address"})
	want(t, status, body, 400, CodeInvalidAddress)

	status, body = b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "ada.new@acme.com"})
	if status != 202 {
		t.Fatalf("change = %d %v", status, body)
	}
	if strings.Contains(mustJSON(body), "ev") && strings.Contains(mustJSON(body), h.z.user(u.id).emailCode) {
		t.Fatal("the response carried the code")
	}
	m := h.z.lastMail(t)
	if m.to != "ada.new@acme.com" || m.kind != "verify" || !strings.HasPrefix(m.link, testAppBase+"/auth/account/email/confirm?code=") {
		t.Fatalf("mail = %+v, want a code to the NEW address linking to the confirm page", m)
	}
	if got := h.z.user(u.id); got.verified {
		t.Fatal("the new address is verified before its code was confirmed")
	}

	// Another account cannot spend the code, and a wrong code does nothing.
	other := h.signIn(h.z.addUser("eve@acme.com", true, "Password1!"))
	status, body = other.do(http.MethodPost, "/auth/account/email/confirm", map[string]any{"code": m.code})
	want(t, status, body, 401, CodeInvalidCode)
	status, body = b.do(http.MethodPost, "/auth/account/email/confirm", map[string]any{"code": "wrong"})
	want(t, status, body, 401, CodeInvalidCode)
	status, body = b.do(http.MethodPost, "/auth/account/email/confirm", map[string]any{"code": m.code})
	if status != 200 || body["emailVerified"] != true {
		t.Fatalf("confirm = %d %v", status, body)
	}
	got := h.z.user(u.id)
	if !got.verified || got.email != "ada.new@acme.com" || got.name() != "ada.new@acme.com" {
		t.Errorf("after confirm: email=%q verified=%v username=%q, want the new address, verified, as the username",
			got.email, got.verified, got.name())
	}
	if _, found := got.metadata[MetaEmailChangeFrom]; found {
		t.Error("the address change's marker was left behind")
	}
}

func TestAccount_EmailChangeInDevelopmentLogsTheLinkNeverReturnsIt(t *testing.T) {
	var logs bytes.Buffer
	h := newAccountHarness(t, LegacyLoginPolicy(), func(c *AccountRoutesConfig) {
		c.DevMode = true
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	status, body := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "ada.new@acme.com"})
	if status != 202 {
		t.Fatalf("change = %d %v", status, body)
	}
	code := h.z.user(u.id).emailCode
	if code == "" || strings.Contains(mustJSON(body), code) || h.z.mailCount() != 0 {
		t.Fatalf("dev mode: code %q, body %v, %d mails", code, body, h.z.mailCount())
	}
	if !strings.Contains(logs.String(), "/auth/account/email/confirm?code="+code) {
		t.Errorf("the confirm link is not in the log:\n%s", logs.String())
	}
}

func TestAccount_PasswordChangeSignsOutEveryOtherSession(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	laptop := h.signIn(u)
	theirs := h.signIn(h.z.addUser("eve@acme.com", true, "Password1!"))
	h.clk.Advance(DefaultReauthWindow + time.Second)

	status, body := b.do(http.MethodPost, "/auth/account/password", map[string]any{"newPassword": "Another1!"})
	want(t, status, body, 401, CodeReauthRequired)
	status, body = b.do(http.MethodPost, "/auth/account/password", map[string]any{"currentPassword": "nope-nope", "newPassword": "Another1!"})
	want(t, status, body, 401, CodeWrongPassword)
	status, body = b.do(http.MethodPost, "/auth/account/password", map[string]any{"currentPassword": "Password1!", "newPassword": "short"})
	want(t, status, body, 400, "password_policy")
	if _, pw := h.z.passwordState(u.id); pw != "Password1!" {
		t.Fatal("a refused change set the password")
	}
	status, body = b.do(http.MethodPost, "/auth/account/password", map[string]any{"newPassword": "Another1!"})
	if status != 200 || body["otherSessionsEnded"] != float64(1) {
		t.Fatalf("change (proved by the refused attempt's right password) = %d %v", status, body)
	}
	if _, pw := h.z.passwordState(u.id); pw != "Another1!" {
		t.Fatalf("password = %q", pw)
	}
	if status, _ := b.do(http.MethodGet, "/auth/account", nil); status != 200 {
		t.Errorf("the session that changed it: %d, want 200", status)
	}
	status, body = laptop.do(http.MethodGet, "/auth/account", nil)
	want(t, status, body, 401, CodeNotSignedIn)
	if status, _ := theirs.do(http.MethodGet, "/auth/account", nil); status != 200 {
		t.Errorf("another account's session: %d, want 200", status)
	}
}

func TestAccount_PasswordRoutesRefusedWhenThePolicyOffersNone(t *testing.T) {
	h := newAccountHarness(t, policyWith(SignupOpen, []string{MethodPasskey}))
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	status, body := b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "Password1!"})
	want(t, status, body, 403, CodeMethodNotOffered)
	status, body = b.do(http.MethodPost, "/auth/account/password", map[string]any{"newPassword": "Another1!"})
	want(t, status, body, 403, CodeMethodNotOffered)
}

func TestAccount_TheLastCredentialIsNeverRemoved(t *testing.T) {
	// No magic links: they would be a way in whatever else is removed.
	h := newAccountHarness(t, policyWith(SignupOpen, []string{MethodPassword, MethodPasskey}, google))
	u := h.z.addUser("ada@acme.com", true, "")
	h.z.mu.Lock()
	h.z.users[u.id].links = []IDPLink{{IdPID: "idp-google", UserID: "g-1", UserName: "ada"}}
	h.z.mu.Unlock()
	b := h.signIn(u)

	status, body := b.do(http.MethodDelete, "/auth/account/links/google", nil)
	want(t, status, body, 409, CodeLastCredential)
	status, body = b.do(http.MethodDelete, "/auth/account/links/github", nil)
	want(t, status, body, 404, CodeNotLinked)

	h.z.mu.Lock()
	h.z.users[u.id].passkeys = []Passkey{{ID: "pk-1", Name: "laptop"}}
	h.z.mu.Unlock()
	if status, body := b.do(http.MethodDelete, "/auth/account/links/google", nil); status != 200 {
		t.Fatalf("unlink beside a passkey = %d %v", status, body)
	}
	if got := h.z.user(u.id); len(got.links) != 0 {
		t.Fatalf("links = %v", got.links)
	}
	status, body = b.do(http.MethodDelete, "/auth/account/passkeys/pk-1", nil)
	want(t, status, body, 409, CodeLastCredential)
	status, body = b.do(http.MethodDelete, "/auth/account/passkeys/pk-other", nil)
	want(t, status, body, 404, CodeNotLinked)
}

func startLink(t *testing.T, b *accountBrowser) string {
	t.Helper()
	status, body := b.do(http.MethodPost, "/auth/account/links/start", map[string]any{"provider": "google", "returnTo": "/settings"})
	if status != 200 {
		t.Fatalf("link start: %d %v", status, body)
	}
	u, _ := url.Parse(body["redirect"].(string))
	return u.Query().Get("state")
}

func TestAccount_ALinkConnectsTheStartingSessionsAccountAndSignsNoOneIn(t *testing.T) {
	g := google
	g.PassTokens = true
	h := newAccountHarness(t, policyWith(SignupOpen, allMethods, g))
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	id := b.session()

	intent := startLink(t, b)
	result := intentResult("someone-else@gmail.com", true)
	result["idpInformation"].(map[string]any)["oauth"] = map[string]any{"accessToken": "gh-at"}
	h.z.addIntent(intent, "tok", result)
	status, body := b.do(http.MethodPost, "/auth/social/callback", map[string]any{"id": intent, "token": "tok"})
	if status != 200 || body["linked"] != true || body["returnTo"] != "/settings" {
		t.Fatalf("link callback = %d %v", status, body)
	}
	if got := h.z.user(u.id); len(got.links) != 1 || got.links[0].UserID != "g-123" {
		t.Fatalf("links = %v", got.links)
	}
	if b.session() != id || h.signInCallbacks != 0 {
		t.Error("the link touched the session cookie or reached the sign-in callback")
	}
	if list, _ := h.sessions.List(context.Background(), u.id); len(list) != 1 {
		t.Errorf("sessions = %d, want the one that was there", len(list))
	}
	if len(h.grants) != 1 || h.grants[0].Event != UpstreamEventLink || h.grants[0].Subject != u.id {
		t.Errorf("grants = %+v, want one link grant for the account", h.grants)
	}
	// The binding is spent.
	status, _ = b.do(http.MethodPost, "/auth/social/callback", map[string]any{"id": intent, "token": "tok"})
	if status != http.StatusTeapot {
		t.Errorf("a replayed link callback = %d, want it handed to the sign-in callback", status)
	}
}

func TestAccount_ALinkRefusesAnotherUsersProviderAccountAndAnotherSession(t *testing.T) {
	h := newAccountHarness(t, policyWith(SignupOpen, allMethods, google))
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	eve := h.z.addUser("eve@acme.com", true, "Password1!")

	b := h.signIn(u)
	intent := startLink(t, b)
	taken := intentResult("eve@gmail.com", true)
	taken["userId"] = eve.id
	h.z.addIntent(intent, "tok", taken)
	status, body := b.do(http.MethodPost, "/auth/social/callback", map[string]any{"id": intent, "token": "tok"})
	want(t, status, body, 409, CodeSocialTaken)

	// The browser signs in as someone else before the provider returns.
	b2 := h.signIn(u)
	intent = startLink(t, b2)
	h.z.addIntent(intent, "tok", intentResult("x@gmail.com", true))
	b2.setSession(h.signIn(eve).session())
	status, body = b2.do(http.MethodPost, "/auth/social/callback", map[string]any{"id": intent, "token": "tok"})
	want(t, status, body, 401, CodeSocialFailed)
	for _, who := range []*fakeUser{u, eve} {
		if got := h.z.user(who.id); len(got.links) != 0 {
			t.Errorf("%s gained a link: %v", who.email, got.links)
		}
	}

	// A callback with no link binding is the sign-in's.
	status, _ = h.browser().do(http.MethodPost, "/auth/social/callback", map[string]any{"id": "x", "token": "y"})
	if status != http.StatusTeapot {
		t.Errorf("a sign-in callback = %d, want it handed through", status)
	}
}

func TestAccount_SessionsAreListedAndRevokedForTheirOwnerOnly(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	laptop := h.signIn(u)
	theirs := h.signIn(h.z.addUser("eve@acme.com", true, "Password1!"))
	ctx := context.Background()
	mine, _ := h.sessions.Info(ctx, b.session())
	lap, _ := h.sessions.Info(ctx, laptop.session())
	their, _ := h.sessions.Info(ctx, theirs.session())

	status, body := b.do(http.MethodGet, "/auth/account/sessions", nil)
	if list, _ := body["sessions"].([]any); status != 200 || len(list) != 2 {
		t.Fatalf("sessions = %d %v", status, body)
	}
	status, body = b.do(http.MethodDelete, "/auth/account/sessions/"+their.Handle, nil)
	want(t, status, body, 404, CodeNotLinked)
	status, body = b.do(http.MethodDelete, "/auth/account/sessions/"+mine.Handle, nil)
	want(t, status, body, 409, CodeCurrentSession)
	if status, _ := b.do(http.MethodDelete, "/auth/account/sessions/"+lap.Handle, nil); status != 200 {
		t.Fatalf("revoke the laptop = %d", status)
	}
	if status, _ := laptop.do(http.MethodGet, "/auth/account", nil); status != 401 {
		t.Errorf("the revoked laptop still signed in: %d", status)
	}
	if status, _ := theirs.do(http.MethodGet, "/auth/account", nil); status != 200 {
		t.Errorf("another account's session: %d", status)
	}

	phone := h.signIn(u)
	h.clk.Advance(DefaultReauthWindow + time.Second)
	status, body = b.do(http.MethodPost, "/auth/account/sessions/revoke-others", nil)
	want(t, status, body, 401, CodeReauthRequired)
	if status, _ := b.do(http.MethodPost, "/auth/reauth", map[string]any{"password": "Password1!"}); status != 200 {
		t.Fatal("reauth")
	}
	status, body = b.do(http.MethodPost, "/auth/account/sessions/revoke-others", nil)
	if status != 200 || body["revoked"] != float64(1) {
		t.Fatalf("revoke-others = %d %v", status, body)
	}
	if status, _ := phone.do(http.MethodGet, "/auth/account", nil); status != 401 {
		t.Errorf("the phone still signed in: %d", status)
	}
}

func TestNewAccountRoutes_RefusesAnIncompleteConfig(t *testing.T) {
	z := newFakeZitadel(t)
	m, _ := session.NewManager(session.Config{Store: session.NewMemoryStore()})
	sid := func(*http.Request) string { return "" }
	for name, cfg := range map[string]AccountRoutesConfig{
		"no sessions":   {Client: z.client(), SessionID: sid, AppBase: testAppBase},
		"no SessionID":  {Client: z.client(), Sessions: m, AppBase: testAppBase},
		"no origin":     {Client: z.client(), Sessions: m, SessionID: sid},
		"require alone": {Client: z.client(), Sessions: m, SessionID: sid, AppBase: testAppBase, RequireUpstreamTokens: true},
	} {
		if _, err := NewAccountRoutes(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Only credentials the policy in force serves count as a way in: a stored
// password under a passkey-only policy, or a link to a provider the policy no
// longer declares, does not keep the account reachable. A magic link to a
// verified address does.
func TestAccount_OnlyUsableCredentialsCountAsTheOneThatRemains(t *testing.T) {
	passkeyOnly := policyWith(SignupOpen, []string{MethodPasskey}, google)
	h := newAccountHarness(t, passkeyOnly)
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	h.z.mu.Lock()
	h.z.users[u.id].passkeys = []Passkey{{ID: "pk-1"}}
	h.z.users[u.id].links = []IDPLink{{IdPID: "idp-retired", UserID: "r-1"}}
	h.z.mu.Unlock()
	b := h.signIn(u)
	status, body := b.do(http.MethodDelete, "/auth/account/passkeys/pk-1", nil)
	want(t, status, body, 409, CodeLastCredential)
	if got := h.z.user(u.id); len(got.passkeys) != 1 {
		t.Fatal("the only usable passkey was removed")
	}
	// The undeclared link itself is removable: it was no way in.
	if status, body := b.do(http.MethodDelete, "/auth/account/links/idp-retired", nil); status != 200 {
		t.Fatalf("removing a link to an undeclared provider = %d %v", status, body)
	}

	magic := newAccountHarness(t, policyWith(SignupOpen, []string{MethodPasskey, MethodMagicLink}))
	v := magic.z.addUser("ada@acme.com", true, "")
	magic.z.mu.Lock()
	magic.z.users[v.id].passkeys = []Passkey{{ID: "pk-1"}}
	magic.z.mu.Unlock()
	if status, body := magic.signIn(v).do(http.MethodDelete, "/auth/account/passkeys/pk-1", nil); status != 200 {
		t.Fatalf("a verified address that can receive magic links keeps the account reachable: %d %v", status, body)
	}
}

// A magic link the instance cannot deliver is no way in: under a paused or
// absent mailer, the only passkey stays, even beside a verified address and
// a policy that offers magic links.
func TestAccount_AnUndeliverableMagicLinkDoesNotCount(t *testing.T) {
	for _, mail := range []string{MailNone, MailPaused} {
		t.Run(mail, func(t *testing.T) {
			p := policyWith(SignupOpen, []string{MethodPasskey, MethodMagicLink})
			p.Mail = mail
			h := newAccountHarness(t, p)
			u := h.z.addUser("ada@acme.com", true, "")
			h.z.mu.Lock()
			h.z.users[u.id].passkeys = []Passkey{{ID: "pk-1"}}
			h.z.mu.Unlock()
			status, body := h.signIn(u).do(http.MethodDelete, "/auth/account/passkeys/pk-1", nil)
			want(t, status, body, 409, CodeLastCredential)
			if len(h.z.user(u.id).passkeys) != 1 {
				t.Fatal("the only passkey was removed though no magic link can be delivered")
			}
		})
	}
}

// Two removals that start together are serialized on the account: each
// counts what the other left. The issuer hook holds every credential read
// until a second one arrives (or 300ms pass), which is exactly the
// interleaving that, unserialized, lets both see two passkeys and remove
// both.
func TestAccount_ConcurrentRemovalsCannotRemoveTheLastCredential(t *testing.T) {
	h := newAccountHarness(t, policyWith(SignupOpen, []string{MethodPasskey}))
	u := h.z.addUser("ada@acme.com", true, "")
	h.z.mu.Lock()
	h.z.users[u.id].passkeys = []Passkey{{ID: "pk-1"}, {ID: "pk-2"}}
	h.z.mu.Unlock()
	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	h.z.beforeRequest = func(r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/passkeys/_search") {
			return
		}
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(both)
		}
		mu.Unlock()
		select {
		case <-both:
		case <-time.After(300 * time.Millisecond):
		}
	}
	b := h.signIn(u)
	statuses := make(chan int, 2)
	for _, id := range []string{"pk-1", "pk-2"} {
		go func() {
			status, _ := b.do(http.MethodDelete, "/auth/account/passkeys/"+id, nil)
			statuses <- status
		}()
	}
	got := []int{<-statuses, <-statuses}
	if left := len(h.z.user(u.id).passkeys); left != 1 {
		t.Fatalf("passkeys left = %d (statuses %v), want exactly 1", left, got)
	}
	if !(got[0] == 200 && got[1] == 409 || got[0] == 409 && got[1] == 200) {
		t.Errorf("statuses = %v, want one 200 and one 409", got)
	}
}

// noLockStore is a Store that cannot lock a subject.
type noLockStore struct{ session.Store }

func TestAccount_AStoreThatCannotLockRefusesRemovals(t *testing.T) {
	h := newAccountHarnessOn(t, noLockStore{session.NewMemoryStore()}, policyWith(SignupOpen, []string{MethodPassword, MethodPasskey}))
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	h.z.mu.Lock()
	h.z.users[u.id].passkeys = []Passkey{{ID: "pk-1"}}
	h.z.mu.Unlock()
	if status, _ := h.signIn(u).do(http.MethodDelete, "/auth/account/passkeys/pk-1", nil); status != http.StatusBadGateway {
		t.Fatalf("removal without a lock = %d, want 502", status)
	}
	if len(h.z.user(u.id).passkeys) != 1 {
		t.Fatal("removed without the account's lock")
	}
}

// failingDeletes is a MemoryStore whose Delete fails once armed.
type failingDeletes struct {
	*session.MemoryStore
	mu   sync.Mutex
	fail bool
}

func (f *failingDeletes) Delete(ctx context.Context, key []byte) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return errors.New("the store is down")
	}
	return f.MemoryStore.Delete(ctx, key)
}

func TestAccount_APasswordChangeNeverClaimsSessionsEndedWhenTheyDidNot(t *testing.T) {
	store := &failingDeletes{MemoryStore: session.NewMemoryStore()}
	h := newAccountHarnessOn(t, store, LegacyLoginPolicy())
	u := h.z.addUser("ada@acme.com", true, "Password1!")
	b := h.signIn(u)
	laptop := h.signIn(u)
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	status, body := b.do(http.MethodPost, "/auth/account/password", map[string]any{"newPassword": "Another1!"})
	if status != http.StatusBadGateway || body["code"] != CodeSessionsNotRevoked || body["changed"] != true || body["otherSessionsRevoked"] != false {
		t.Fatalf("password change with a failing store = %d %v, want 502 sessions_not_revoked, changed", status, body)
	}
	if status, _ := laptop.do(http.MethodGet, "/auth/account", nil); status != 200 {
		t.Fatal("test premise: the laptop's session was not ended")
	}
	// The recovery the answer names.
	store.mu.Lock()
	store.fail = false
	store.mu.Unlock()
	status, body = b.do(http.MethodPost, "/auth/account/sessions/revoke-others", nil)
	if status != 200 || body["revoked"] != float64(1) {
		t.Fatalf("revoke-others after the failure = %d %v", status, body)
	}
	if status, _ := laptop.do(http.MethodGet, "/auth/account", nil); status != 401 {
		t.Errorf("the laptop after the retry: %d, want 401", status)
	}
}

// The address that named the account before a change is kept across a
// second change and a failed one, so the confirmation renames from it.
func TestAccount_TheRenameSurvivesASecondAndAFailedChange(t *testing.T) {
	h := newAccountHarness(t, LegacyLoginPolicy())
	u := h.z.addUser("a@acme.com", true, "Password1!")
	b := h.signIn(u)
	if status, body := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "b@acme.com"}); status != 202 {
		t.Fatalf("A→B = %d %v", status, body)
	}
	h.z.mu.Lock()
	h.z.failSetEmail = true
	h.z.mu.Unlock()
	if status, _ := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "x@acme.com"}); status != http.StatusBadGateway {
		t.Fatalf("a failed change = %d, want 502", status)
	}
	h.z.mu.Lock()
	h.z.failSetEmail = false
	h.z.mu.Unlock()
	if status, body := b.do(http.MethodPost, "/auth/account/email", map[string]any{"email": "c@acme.com"}); status != 202 {
		t.Fatalf("B→C = %d %v", status, body)
	}
	if got := h.z.user(u.id); got.metadata[MetaEmailChangeFrom] != "a@acme.com" {
		t.Fatalf("marker = %q, want the address that named the account before any change", got.metadata[MetaEmailChangeFrom])
	}
	if status, body := b.do(http.MethodPost, "/auth/account/email/confirm", map[string]any{"code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("confirm C = %d %v", status, body)
	}
	got := h.z.user(u.id)
	if got.name() != "c@acme.com" || got.email != "c@acme.com" || !got.verified {
		t.Errorf("after confirming C: username=%q email=%q verified=%v, want c@acme.com throughout", got.name(), got.email, got.verified)
	}
	if _, left := got.metadata[MetaEmailChangeFrom]; left {
		t.Error("the marker outlived a settled confirmation")
	}
}
