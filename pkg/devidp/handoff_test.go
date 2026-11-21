package devidp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// The sign-in handoff against the fake issuer and the in-memory session
// store. The fake finalizes any live session, as the real issuer does, so
// these tests prove the routes' own checks: the allowlist, PKCE, single use,
// expiry, the approving session still being live, and consent being an
// explicit same-site POST.

const (
	handoffLoopback = "http://127.0.0.1/cb"
	handoffWeb      = "https://console.test/auth/handoff/done"
	handoffApp      = "com.acme.desktop:/cb"
	handoffCookie   = "sid"
)

type handoffEnv struct {
	t      *testing.T
	z      *fakeZitadel
	user   *fakeUser
	store  *session.MemoryStore
	mgr    *session.Manager
	mux    *http.ServeMux
	routes *HandoffRoutes
	flow   FlowConfig

	mu  sync.Mutex
	now time.Time
}

func (e *handoffEnv) clock() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }

func (e *handoffEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func newHandoffEnv(t *testing.T, redirects ...string) *handoffEnv {
	t.Helper()
	if len(redirects) == 0 {
		redirects = []string{handoffLoopback, handoffWeb, handoffApp}
	}
	// A fixed start (the fake issuer's epoch), moved only by advance: every
	// TTL and ProvedAt assertion is against this clock, never the wall's.
	e := &handoffEnv{t: t, z: newFakeZitadel(t), now: fakeEpoch}
	e.user = e.z.addUser("ada@acme.com", true, "a-long-password")
	e.store = session.NewMemoryStore()
	mgr, err := session.NewManager(session.Config{Store: e.store, Now: e.clock, CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	e.mgr = mgr
	e.flow = FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"}
	routes, err := NewHandoffRoutes(HandoffConfig{
		Client: e.z.client(), Flow: e.flow, Sessions: mgr, Store: e.store, AppBase: testAppBase,
		Redirects: redirects,
		PrimarySession: func(r *http.Request) string {
			if c, err := r.Cookie(handoffCookie); err == nil {
				return c.Value
			}
			return ""
		},
		SignedIn: func(w http.ResponseWriter, _ *http.Request, issued session.Issued) error {
			http.SetCookie(w, &http.Cookie{Name: handoffCookie, Value: issued.ID, Path: "/", HttpOnly: true})
			return nil
		},
		Now: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.routes = routes
	e.mux = http.NewServeMux()
	g := GuardMux(e.mux, testAppBase)
	g.Handle(HandoffStartPath, http.HandlerFunc(routes.HandleStart))
	g.Handle(HandoffAuthorizePath, http.HandlerFunc(routes.HandleAuthorize))
	g.Handle(HandoffConsentPath, http.HandlerFunc(routes.HandleConsent))
	g.Handle(HandoffRedeemPath, http.HandlerFunc(routes.HandleRedeem))
	return e
}

// signIn signs ada in through the real flow and returns her session id: the
// issuer session the sign-in finalized is sealed with it.
func (e *handoffEnv) signIn() string {
	e.t.Helper()
	tokens, err := e.z.client().SignIn(context.Background(), e.flow, Credentials{LoginName: "ada@acme.com", Password: "a-long-password"})
	if err != nil {
		e.t.Fatal(err)
	}
	if tokens.IssuerSession.ID == "" || tokens.IssuerSession.Token == "" {
		e.t.Fatal("SignIn did not report the issuer session it finalized")
	}
	issued, err := e.mgr.Create(context.Background(), SessionTokens(tokens, e.clock()),
		session.Meta{Subject: e.user.id, IssuerSession: tokens.IssuerSession.ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return issued.ID
}

func (e *handoffEnv) do(method, target string, body any, cookie string, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *strings.Reader
	switch b := body.(type) {
	case nil:
		rd = strings.NewReader("")
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, testAppBase+target, rd)
	req.RemoteAddr = "192.0.2.1:1234"
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: handoffCookie, Value: cookie})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func handoffJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %d is not JSON: %s", rec.Code, rec.Body)
	}
	return m
}

type pkcePair struct{ verifier, challenge string }

func newPKCEPair(t *testing.T) pkcePair {
	t.Helper()
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	return pkcePair{p.Verifier, p.Challenge}
}

// start begins a handoff to redirect and returns the request id.
func (e *handoffEnv) start(redirect string, p pkcePair) string {
	e.t.Helper()
	rec := e.do("POST", "/auth/handoff/start", map[string]string{"code_challenge": p.challenge,
		"code_challenge_method": "S256", "redirect_uri": redirect, "state": "st-1"}, "", nil)
	if rec.Code != 200 {
		e.t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(handoffJSON(e.t, rec)["authorize_url"].(string))
	if err != nil || u.Path != HandoffPagePath || !strings.HasPrefix(u.String(), testAppBase) {
		e.t.Fatalf("authorize_url %v is not the app's consent page", u)
	}
	return u.Query().Get("req")
}

// consent approves request req as session primary and returns the code.
func (e *handoffEnv) consent(primary, req, redirect string) string {
	e.t.Helper()
	rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, primary, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != 200 {
		e.t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	target, err := url.Parse(handoffJSON(e.t, rec)["redirect"].(string))
	if err != nil {
		e.t.Fatal(err)
	}
	if got := strings.SplitN(target.String(), "?", 2)[0]; got != redirect {
		e.t.Fatalf("consent redirects to %q, want %q", got, redirect)
	}
	if target.Query().Get("state") != "st-1" {
		e.t.Fatalf("the state did not come back: %v", target)
	}
	return target.Query().Get("code")
}

func (e *handoffEnv) redeem(code, verifier, redirect string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/handoff/redeem", map[string]string{"code": code, "code_verifier": verifier, "redirect_uri": redirect}, "", nil)
}

// handoff runs start → consent and returns the code.
func (e *handoffEnv) handoff(primary, redirect string, p pkcePair) string {
	e.t.Helper()
	return e.consent(primary, e.start(redirect, p), redirect)
}

func TestHandoffNativeRoundTrip(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	primaryInfo, _ := e.mgr.Info(context.Background(), primary)
	e.advance(10 * time.Minute) // so a fresh ProvedAt would show

	p := newPKCEPair(t)
	redirect := "http://127.0.0.1:51234/cb"
	req := e.start(redirect, p)

	rec := e.do("GET", "/auth/handoff/authorize?req="+url.QueryEscape(req), nil, primary, nil)
	if rec.Code != 200 {
		t.Fatalf("authorize: %d %s", rec.Code, rec.Body)
	}
	got := handoffJSON(t, rec)
	if got["kind"] != HandoffNative || got["destination"] != "http://127.0.0.1:51234" || got["redirect"] != redirect {
		t.Errorf("authorize answer %v", got)
	}
	if acct, _ := got["account"].(map[string]any); acct["subject"] != e.user.id {
		t.Errorf("authorize names %v, want %s", got["account"], e.user.id)
	}
	if strings.Contains(rec.Body.String(), "fhc_") {
		t.Fatal("GET authorize minted a code")
	}

	code := e.consent(primary, req, redirect)
	if !strings.HasPrefix(code, handoffCodePrefix) {
		t.Fatalf("code %q", code)
	}
	rec = e.redeem(code, p.verifier, redirect)
	if rec.Code != 200 {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	out := handoffJSON(t, rec)
	newID, _ := out["session"].(string)
	if !session.IsID(newID) || out["token_type"] != "Bearer" || out["kind"] != HandoffNative || out["expires_at"] == "" {
		t.Fatalf("redeem answer %v", out)
	}
	if newID == primary {
		t.Fatal("the handoff returned the primary session")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a native redeem set a cookie")
	}
	if tok, err := e.mgr.AccessToken(context.Background(), newID); err != nil || tok != "at-"+e.user.id {
		t.Fatalf("the new session does not resolve: %q %v", tok, err)
	}
	info, err := e.mgr.Info(context.Background(), newID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Channel != session.ChannelHandoff || info.Subject != e.user.id {
		t.Errorf("new session info %+v", info)
	}
	if !info.ProvedAt.Equal(primaryInfo.ProvedAt.Truncate(time.Millisecond)) {
		t.Errorf("the new session's ProvedAt is %v, want the primary's %v (a click is not a proof)", info.ProvedAt, primaryInfo.ProvedAt)
	}
	// The new session can hand off in turn: it keeps the issuer session.
	if tok, _, err := e.mgr.Open(context.Background(), newID); err != nil || tok.IssuerSessionID == "" {
		t.Errorf("the new session keeps no issuer session: %v", err)
	}

	// Independent: revoking one leaves the other.
	if _, _, err := e.mgr.Revoke(context.Background(), newID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.AccessToken(context.Background(), primary); err != nil {
		t.Errorf("revoking the handoff session ended the primary: %v", err)
	}
	code = e.handoff(primary, redirect, p)
	out = handoffJSON(t, e.redeem(code, p.verifier, redirect))
	second, _ := out["session"].(string)
	if _, _, err := e.mgr.Revoke(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.AccessToken(context.Background(), second); err != nil {
		t.Errorf("revoking the primary ended the handoff session: %v", err)
	}
}

func TestHandoffWebRedeemSetsACookieAndNoCredential(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	rec := e.redeem(e.handoff(primary, handoffWeb, p), p.verifier, handoffWeb)
	if rec.Code != 200 {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	out := handoffJSON(t, rec)
	if _, leaked := out["session"]; leaked || out["kind"] != HandoffWeb {
		t.Fatalf("a web redeem answered %v: the id belongs in an HttpOnly cookie", out)
	}
	var id string
	for _, c := range rec.Result().Cookies() {
		if c.Name == handoffCookie {
			id = c.Value
		}
	}
	if !session.IsID(id) || id == primary {
		t.Fatalf("no new session cookie: %q", id)
	}
}

func TestHandoffPrivateUseSchemeIsNative(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	out := handoffJSON(t, e.redeem(e.handoff(primary, handoffApp, p), p.verifier, handoffApp))
	if out["kind"] != HandoffNative || !session.IsID(out["session"].(string)) {
		t.Fatalf("redeem answer %v", out)
	}
}

func TestHandoffConsentByFormRedirects(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	req := e.start(handoffApp, p)
	rec := e.do("POST", "/auth/handoff/authorize", "req="+url.QueryEscape(req), primary,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form consent: %d %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), handoffApp+"?") || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st-1" {
		t.Fatalf("Location %q", loc)
	}
}

func TestHandoffCodeIsSingleUse(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	code := e.handoff(primary, handoffLoopback, p)
	if rec := e.redeem(code, p.verifier, handoffLoopback); rec.Code != 200 {
		t.Fatalf("first redeem: %d %s", rec.Code, rec.Body)
	}
	if rec := e.redeem(code, p.verifier, handoffLoopback); rec.Code != 400 || handoffJSON(t, rec)["code"] != CodeHandoffInvalid {
		t.Fatalf("a replayed code answered %d %s", rec.Code, rec.Body)
	}
}

func TestHandoffFirstFailedRedeemBurnsTheCode(t *testing.T) {
	for name, bad := range map[string]func(p pkcePair) (verifier, redirect string){
		"wrong verifier":         func(pkcePair) (string, string) { return newPKCEPair(t).verifier, handoffLoopback },
		"malformed verifier":     func(pkcePair) (string, string) { return "short", handoffLoopback },
		"the challenge as plain": func(p pkcePair) (string, string) { return p.challenge, handoffLoopback },
		"another redirect":       func(p pkcePair) (string, string) { return p.verifier, handoffApp },
		"another loopback path":  func(p pkcePair) (string, string) { return p.verifier, "http://127.0.0.1/other" },
		"no redirect":            func(p pkcePair) (string, string) { return p.verifier, "" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newHandoffEnv(t)
			primary := e.signIn()
			p := newPKCEPair(t)
			code := e.handoff(primary, handoffLoopback, p)
			v, r := bad(p)
			if rec := e.redeem(code, v, r); rec.Code != 400 {
				t.Fatalf("a bad redeem answered %d %s", rec.Code, rec.Body)
			}
			if rec := e.redeem(code, p.verifier, handoffLoopback); rec.Code != 400 {
				t.Fatalf("the code survived a failed attempt: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestHandoffStartRefuses(t *testing.T) {
	e := newHandoffEnv(t)
	p := newPKCEPair(t)
	for name, body := range map[string]map[string]string{
		"plain method":       {"code_challenge": p.challenge, "code_challenge_method": "plain", "redirect_uri": handoffLoopback, "state": "s"},
		"no method":          {"code_challenge": p.challenge, "redirect_uri": handoffLoopback, "state": "s"},
		"short challenge":    {"code_challenge": "abc", "code_challenge_method": "S256", "redirect_uri": handoffLoopback, "state": "s"},
		"no state":           {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": handoffLoopback},
		"unlisted origin":    {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "https://evil.test/auth/handoff/done", "state": "s"},
		"listed host, path":  {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "https://console.test/other", "state": "s"},
		"listed + query":     {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": handoffWeb + "?x=1", "state": "s"},
		"listed + fragment":  {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": handoffWeb + "#x", "state": "s"},
		"http console":       {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "http://console.test/auth/handoff/done", "state": "s"},
		"localhost loopback": {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "http://localhost:4000/cb", "state": "s"},
		"unlisted ipv6":      {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "http://[::1]:4000/cb", "state": "s"},
		"other scheme":       {"code_challenge": p.challenge, "code_challenge_method": "S256", "redirect_uri": "com.evil.desktop:/cb", "state": "s"},
	} {
		rec := e.do("POST", "/auth/handoff/start", body, "", nil)
		if rec.Code != 400 {
			t.Errorf("%s: start answered %d %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "evil") || strings.Contains(rec.Body.String(), "localhost") {
			t.Errorf("%s: the refusal echoes the attacker's redirect: %s", name, rec.Body)
		}
	}
}

func TestHandoffLoopbackMatchesAnyPort(t *testing.T) {
	e := newHandoffEnv(t, handoffLoopback, "http://[::1]/cb")
	for _, ok := range []string{"http://127.0.0.1/cb", "http://127.0.0.1:1/cb", "http://127.0.0.1:65535/cb", "http://[::1]:8080/cb"} {
		if _, matched := e.routes.match(ok); !matched {
			t.Errorf("%s does not match", ok)
		}
	}
	for _, no := range []string{"http://127.0.0.1:8080/cb/", "http://127.0.0.1:8080/cb2", "http://127.0.0.2:8080/cb",
		"https://127.0.0.1:8080/cb", "http://localhost:8080/cb", "http://127.0.0.1:x/cb", "http://127.0.0.1:8080/CB"} {
		if _, matched := e.routes.match(no); matched {
			t.Errorf("%s matches", no)
		}
	}
}

func TestValidateHandoffRedirect(t *testing.T) {
	for _, ok := range []string{"https://console.acme.com", "https://console.acme.com/auth/done", "https://console.acme.com:8443/cb",
		"http://127.0.0.1/cb", "http://127.0.0.1:0/cb", "http://[::1]/cb", "com.acme.desktop:/cb", "io.example.app:/oauth/callback"} {
		if err := ValidateHandoffRedirect(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, no := range []string{"", "http://console.acme.com/cb", "http://localhost/cb", "http://10.0.0.1/cb",
		"https://*.acme.com/cb", "https://console.acme.com/cb#frag", "https://console.acme.com/cb?x=1",
		"https://user@console.acme.com/cb", "https:///cb", "HTTPS://console.acme.com/cb", "javascript:/alert(1)",
		"myapp:/cb", "com.acme.desktop://host/cb", "com.acme.desktop:cb", "file:///etc/passwd",
		"https://console.acme.com/a b", "data:text/html,x"} {
		if err := ValidateHandoffRedirect(no); err == nil {
			t.Errorf("%q accepted", no)
		}
	}
	if _, err := NewHandoffRoutes(HandoffConfig{}); err == nil {
		t.Error("an empty config built routes")
	}
}

func TestHandoffSignedOut(t *testing.T) {
	e := newHandoffEnv(t)
	p := newPKCEPair(t)
	req := e.start(handoffLoopback, p)
	for _, cookie := range []string{"", "fs_not-a-session", e.signedOutID()} {
		rec := e.do("GET", "/auth/handoff/authorize?req="+url.QueryEscape(req), nil, cookie, nil)
		got := handoffJSON(t, rec)
		if rec.Code != 401 || got["code"] != CodeNotSignedIn ||
			!strings.HasPrefix(got["sign_in"].(string), testAppBase+"/auth/sign-in?returnTo=") ||
			!strings.Contains(got["sign_in"].(string), url.QueryEscape(url.QueryEscape(req))) {
			t.Fatalf("signed-out authorize: %d %v", rec.Code, got)
		}
		if rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, cookie, nil); rec.Code != 401 {
			t.Fatalf("signed-out consent: %d %s", rec.Code, rec.Body)
		}
	}
	// The request waited: signed in, the same request completes.
	if code := e.consent(e.signIn(), req, handoffLoopback); code == "" {
		t.Fatal("no code after signing in")
	}
}

// signedOutID is a well-formed id for a session that was signed out.
func (e *handoffEnv) signedOutID() string {
	id := e.signIn()
	if _, _, err := e.mgr.Revoke(context.Background(), id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestHandoffConsentIsAPostNeverAGet(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	req := e.start(handoffLoopback, p)
	for i := 0; i < 3; i++ {
		rec := e.do("GET", "/auth/handoff/authorize?req="+url.QueryEscape(req), nil, primary, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "code=") || rec.Header().Get("Location") != "" {
			t.Fatalf("GET authorize: %d %s", rec.Code, rec.Body)
		}
	}
	if n := e.z.callCount("POST /v2/oidc/auth_requests"); n != 1 {
		// The one finalize is the sign-in's own: GET minted nothing.
		t.Fatalf("GET authorize finalized %d auth requests", n-1)
	}
	e.consent(primary, req, handoffLoopback)
	if rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, primary, nil); rec.Code != 404 {
		t.Fatalf("a request consented twice: %d %s", rec.Code, rec.Body)
	}
}

func TestHandoffCrossSiteConsentIsRefused(t *testing.T) {
	e := newHandoffEnv(t)
	primary := e.signIn()
	p := newPKCEPair(t)
	req := e.start(handoffLoopback, p)
	for name, h := range map[string]map[string]string{
		"cross-site":                {"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.test"},
		"same-site sibling":         {"Sec-Fetch-Site": "same-site", "Origin": "http://evil.app.test"},
		"old browser, other origin": {"Origin": "https://evil.test"},
	} {
		rec := e.do("POST", "/auth/handoff/authorize", "req="+url.QueryEscape(req), primary,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": h["Sec-Fetch-Site"], "Origin": h["Origin"]})
		if rec.Code != 403 || handoffJSON(t, rec)["code"] != CodeCrossSite {
			t.Errorf("%s consent answered %d %s", name, rec.Code, rec.Body)
		}
	}
	// Still pending: the refused posts consumed nothing.
	e.consent(primary, req, handoffLoopback)
}

func TestHandoffExpiry(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		e := newHandoffEnv(t)
		primary := e.signIn()
		p := newPKCEPair(t)
		req := e.start(handoffLoopback, p)
		e.advance(HandoffRequestTTL + time.Second)
		if rec := e.do("GET", "/auth/handoff/authorize?req="+url.QueryEscape(req), nil, primary, nil); rec.Code != 404 {
			t.Fatalf("expired request, authorize: %d", rec.Code)
		}
		if rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, primary, nil); rec.Code != 404 {
			t.Fatalf("expired request, consent: %d", rec.Code)
		}
	})
	t.Run("code", func(t *testing.T) {
		e := newHandoffEnv(t)
		primary := e.signIn()
		p := newPKCEPair(t)
		code := e.handoff(primary, handoffLoopback, p)
		e.advance(HandoffCodeTTL + time.Second)
		if rec := e.redeem(code, p.verifier, handoffLoopback); rec.Code != 400 {
			t.Fatalf("expired code: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("sweep", func(t *testing.T) {
		e := newHandoffEnv(t)
		p := newPKCEPair(t)
		req := e.start(handoffLoopback, p)
		e.advance(HandoffRequestTTL + time.Minute)
		e.start(handoffLoopback, p) // sweeps
		k, _ := deriveHandoffKeys(req, handoffRequestPrefix)
		if _, err := e.store.GetHandoff(context.Background(), k.lookup); err != session.ErrNoHandoff {
			t.Fatalf("the expired request was not swept: %v", err)
		}
	})
}

func TestHandoffRevokedPrimary(t *testing.T) {
	t.Run("before consent", func(t *testing.T) {
		e := newHandoffEnv(t)
		primary := e.signIn()
		p := newPKCEPair(t)
		req := e.start(handoffLoopback, p)
		if _, _, err := e.mgr.Revoke(context.Background(), primary); err != nil {
			t.Fatal(err)
		}
		if rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, primary, nil); rec.Code != 401 {
			t.Fatalf("a revoked session consented: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("before redeem", func(t *testing.T) {
		e := newHandoffEnv(t)
		primary := e.signIn()
		p := newPKCEPair(t)
		code := e.handoff(primary, handoffLoopback, p)
		if _, err := e.mgr.RevokeSubject(context.Background(), e.user.id); err != nil {
			t.Fatal(err)
		}
		if rec := e.redeem(code, p.verifier, handoffLoopback); rec.Code != 400 {
			t.Fatalf("a code outlived the session that approved it: %d %s", rec.Code, rec.Body)
		}
	})
}

func TestHandoffNeedsAnIssuerSession(t *testing.T) {
	e := newHandoffEnv(t)
	// A session created before handoffs existed: no issuer session sealed.
	old, err := e.mgr.Create(context.Background(), session.Tokens{AccessToken: "at-" + e.user.id, Expiry: e.clock().Add(time.Hour)},
		session.Meta{Subject: e.user.id})
	if err != nil {
		t.Fatal(err)
	}
	p := newPKCEPair(t)
	req := e.start(handoffLoopback, p)
	rec := e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, old.ID, nil)
	if rec.Code != 401 || handoffJSON(t, rec)["code"] != CodeReauthRequired {
		t.Fatalf("consent from a pre-handoff session: %d %s", rec.Code, rec.Body)
	}
	// The request is kept: after signing in again it completes.
	e.consent(e.signIn(), req, handoffLoopback)

	// An issuer session the issuer ended answers the same.
	primary := e.signIn()
	tok, _, _ := e.mgr.Open(context.Background(), primary)
	_ = e.z.client().DeleteSession(context.Background(), Session{ID: tok.IssuerSessionID, Token: tok.IssuerSessionToken})
	req = e.start(handoffLoopback, p)
	rec = e.do("POST", "/auth/handoff/authorize", map[string]string{"req": req}, primary, nil)
	if rec.Code != 401 || handoffJSON(t, rec)["code"] != CodeReauthRequired {
		t.Fatalf("consent from an ended issuer session: %d %s", rec.Code, rec.Body)
	}
}

func TestHandoffRedeemIsRateLimited(t *testing.T) {
	e := newHandoffEnv(t)
	limited := false
	for i := 0; i < 40; i++ {
		if rec := e.redeem("fhc_guess", "v", handoffLoopback); rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("40 redeem guesses from one address were never limited")
	}
	// Another address has its own budget.
	req := httptest.NewRequest("POST", testAppBase+"/auth/handoff/redeem", strings.NewReader(`{"code":"x"}`))
	req.RemoteAddr = "198.51.100.7:1"
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("the limit is global, not per address")
	}
}

func TestHandoffVerifierMatches(t *testing.T) {
	p := newPKCEPair(t)
	if !verifierMatches(p.verifier, p.challenge) {
		t.Fatal("the verifier does not match its own challenge")
	}
	sum := sha256.Sum256([]byte(p.verifier + "x"))
	if verifierMatches(p.verifier+"x", p.challenge) || !verifierMatches(p.verifier+"x", base64.RawURLEncoding.EncodeToString(sum[:])) {
		t.Fatal("S256 is compared wrongly")
	}
}
