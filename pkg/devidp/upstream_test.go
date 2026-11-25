package devidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The provider's tokens as the fake issuer reports them. Distinctive, so a
// leak anywhere is found by a substring search.
const (
	upAccess  = "gho_UPSTREAM-ACCESS-0123456789"
	upRefresh = "ghr_UPSTREAM-REFRESH-0123456789"
	upID      = "UPSTREAM-ID-TOKEN-0123456789"
)

var googlePass = LoginPolicySocial{Type: "google", IdPID: "idp-google", Label: "Google", PassTokens: true,
	Scopes: "read:user repo"}

// withTokens adds Zitadel v2's idpInformation.oauth to an intent result.
func withTokens(res map[string]any) map[string]any {
	info := res["idpInformation"].(map[string]any)
	info["oauth"] = map[string]any{"accessToken": upAccess, "refreshToken": upRefresh, "idToken": upID}
	return res
}

// grants records what the hook received.
type grants struct {
	mu   sync.Mutex
	got  []UpstreamGrant
	fail error
}

func (g *grants) hook(_ context.Context, gr UpstreamGrant) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.got = append(g.got, gr)
	return g.fail
}

func (g *grants) all() []UpstreamGrant {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]UpstreamGrant(nil), g.got...)
}

// rawCallback posts the social callback and returns the raw response body
// and Set-Cookie headers, for the leak checks.
func rawCallback(t *testing.T, b *browser, id, token string) (int, string, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "token": token})
	req, _ := http.NewRequest(http.MethodPost, b.h.srv.URL+"/auth/social/callback", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), strings.Join(resp.Header.Values("Set-Cookie"), "\n")
}

func liveSessions(z *fakeZitadel, userID string) (live, deleted int) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for _, s := range z.sessions {
		if s.userID != userID {
			continue
		}
		if s.deleted {
			deleted++
		} else {
			live++
		}
	}
	return live, deleted
}

func upstreamHarness(t *testing.T, p LoginPolicy, g *grants, strict bool, logs io.Writer) *harness {
	t.Helper()
	return newHarness(t, p, func(c *SignInRoutesConfig) {
		c.UpstreamTokens, c.RequireUpstreamTokens = g.hook, strict
		if logs != nil {
			c.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		}
	})
}

func wantGrant(t *testing.T, got []UpstreamGrant, subject, event string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("hook called %d times, want once: %v", len(got), got)
	}
	g := got[0]
	if g.Subject != subject || g.Event != event || g.Provider != "google" || g.IdPID != "idp-google" ||
		g.ProviderUserID != "g-123" || g.Tokens.AccessToken != upAccess || g.Tokens.RefreshToken != upRefresh ||
		g.Tokens.IDToken != upID || strings.Join(g.Tokens.Scopes, " ") != "read:user repo" {
		t.Fatalf("grant = subject %q event %q provider %q idp %q user %q tokens %q/%q/%q scopes %v",
			g.Subject, g.Event, g.Provider, g.IdPID, g.ProviderUserID,
			g.Tokens.AccessToken, g.Tokens.RefreshToken, g.Tokens.IDToken, g.Tokens.Scopes)
	}
}

func TestRetrieveIDPIntent_DecodesTheProvidersTokens(t *testing.T) {
	z := newFakeZitadel(t)
	z.addIntent("intent-1", "tok", withTokens(intentResult("g@acme.com", true)))
	res, err := z.client().RetrieveIDPIntent(context.Background(), "intent-1", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if res.Upstream == nil || res.Upstream.AccessToken != upAccess || res.Upstream.RefreshToken != upRefresh || res.Upstream.IDToken != upID {
		t.Fatalf("upstream = %q %q %q", res.Upstream.AccessToken, res.Upstream.RefreshToken, res.Upstream.IDToken)
	}
	z.addIntent("intent-2", "tok", intentResult("g@acme.com", true))
	if res, _ := z.client().RetrieveIDPIntent(context.Background(), "intent-2", "tok"); res.Upstream != nil {
		t.Fatal("tokens invented for an intent without idpInformation.oauth")
	}
}

func TestUpstreamTokens_RedactAndRefuseToMarshal(t *testing.T) {
	tok := UpstreamTokens{AccessToken: upAccess, RefreshToken: upRefresh, IDToken: upID}
	g := UpstreamGrant{Subject: "u1", Provider: "github", Tokens: tok}
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok),
		fmt.Sprintf("%v %+v", g, g), fmt.Sprintf("%#v", g)} {
		if strings.Contains(s, "UPSTREAM") {
			t.Errorf("formatted tokens leak: %s", s)
		}
	}
	var logs bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&logs, nil))
	l.Info("x", "tokens", tok, "grant", g)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "tokens", tok, "grant", g)
	if strings.Contains(logs.String(), "UPSTREAM") || !strings.Contains(logs.String(), "[redacted]") {
		t.Errorf("slog output: %s", logs.String())
	}
	if _, err := json.Marshal(tok); !errors.Is(err, ErrUpstreamTokensNotSerializable) {
		t.Errorf("json.Marshal(tokens) = %v, want ErrUpstreamTokensNotSerializable", err)
	}
	if _, err := json.Marshal(g); err == nil {
		t.Error("a grant marshalled with its tokens")
	}
}

// IDPResult is public: adding Upstream must not change how a result without
// it marshals, nor stop it being a comparable value (==, a map key).
func TestIDPResult_StillMarshalsAndCompares(t *testing.T) {
	legacy := IDPResult{Email: "g@acme.com", EmailVerified: true}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal(legacy IDPResult) = %v", err)
	}
	if strings.Contains(string(raw), "Upstream") {
		t.Errorf("legacy result gained a field: %s", raw)
	}
	withTokens := legacy
	withTokens.Upstream = &UpstreamTokens{AccessToken: upAccess, RefreshToken: upRefresh, IDToken: upID}
	raw, err = json.Marshal(withTokens)
	if err != nil {
		t.Fatalf("json.Marshal(IDPResult with tokens) = %v", err)
	}
	if strings.Contains(string(raw), "UPSTREAM") {
		t.Errorf("tokens serialised with the result: %s", raw)
	}
	seen := map[IDPResult]bool{legacy: true}
	if !seen[IDPResult{Email: "g@acme.com", EmailVerified: true}] || legacy == withTokens {
		t.Error("IDPResult is no longer compared by value")
	}
	if s := fmt.Sprintf("%v %+v", withTokens, withTokens); strings.Contains(s, "UPSTREAM") {
		t.Errorf("formatted result leaks: %s", s)
	}
}

func TestUpstream_HookGetsTheSessionsSubject(t *testing.T) {
	t.Run("linked", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupInvite, allMethods, googlePass), g, false, nil)
		u := h.z.addUser("g@acme.com", true, "")
		u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123"}}
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("g@acme.com", true)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
			t.Fatalf("callback: %d %v", status, body)
		}
		wantGrant(t, g.all(), u.id, UpstreamEventSignIn)
	})
	t.Run("auto-linked", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupInvite, allMethods, googlePass), g, false, nil)
		u := h.z.addUser("g@acme.com", true, "pw-long-enough")
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("g@acme.com", true)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
			t.Fatalf("callback: %d %v", status, body)
		}
		wantGrant(t, g.all(), u.id, UpstreamEventSignIn)
	})
	t.Run("new account", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupOpen, allMethods, googlePass), g, false, nil)
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("new@else.com", true)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
			t.Fatalf("callback: %d %v", status, body)
		}
		u := h.z.userByEmail("new@else.com")
		if u == nil {
			t.Fatal("no account created")
		}
		wantGrant(t, g.all(), u.id, UpstreamEventSignUp)
	})
	t.Run("invitation", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupInvite, nil, googlePass), g, false, nil)
		u := invitee(h)
		b := h.browser()
		id := startInviteSocial(t, b, u.id, "inv-1")
		h.z.addIntent(id, "tok", withTokens(intentResult("someone@gmail.com", true)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
			t.Fatalf("callback: %d %v", status, body)
		}
		wantGrant(t, g.all(), u.id, UpstreamEventInviteAccept)
	})
}

func TestUpstream_HookNotCalled(t *testing.T) {
	t.Run("refused sign-in", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupInvite, allMethods, googlePass), g, false, nil)
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("nobody@acme.com", true)))
		status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"})
		want(t, status, body, 403, CodeSocialNoAccount)
		if n := len(g.all()); n != 0 {
			t.Fatalf("hook called %d times for a refused sign-in", n)
		}
	})
	t.Run("pending link", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupOpen, allMethods, googlePass), g, false, nil)
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("me@else.com", false)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 202 {
			t.Fatalf("callback: %d %v", status, body)
		}
		if n := len(g.all()); n != 0 {
			t.Fatalf("hook called %d times with no session", n)
		}
	})
	t.Run("pass_tokens off", func(t *testing.T) {
		g := &grants{}
		h := upstreamHarness(t, policyWith(SignupOpen, allMethods, google), g, true, nil)
		b := h.browser()
		id := startSocial(t, b)
		h.z.addIntent(id, "tok", withTokens(intentResult("new@else.com", true)))
		if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
			t.Fatalf("callback: %d %v", status, body)
		}
		if n := len(g.all()); n != 0 {
			t.Fatalf("hook called %d times for a provider without pass_tokens", n)
		}
	})
}

func TestUpstream_HookFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		strict     bool
		noToken    bool
		wantStatus int
	}{
		{"lenient: logged, signed in", false, false, 200},
		{"lenient: no token, signed in", false, true, 200},
		{"strict: refused, session deleted", true, false, 401},
		{"strict: no token, refused", true, true, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &grants{fail: errors.New("store unavailable")}
			if tc.noToken {
				g.fail = nil
			}
			var logs bytes.Buffer
			h := upstreamHarness(t, policyWith(SignupInvite, allMethods, googlePass), g, tc.strict, &logs)
			u := h.z.addUser("g@acme.com", true, "")
			u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123"}}
			b := h.browser()
			id := startSocial(t, b)
			res := withTokens(intentResult("g@acme.com", true))
			if tc.noToken {
				res = intentResult("g@acme.com", true)
			}
			h.z.addIntent(id, "tok", res)
			status, body, cookies := rawCallback(t, b, id, "tok")
			if status != tc.wantStatus {
				t.Fatalf("callback: %d %s", status, body)
			}
			for where, s := range map[string]string{"body": body, "Set-Cookie": cookies, "logs": logs.String()} {
				if strings.Contains(s, "UPSTREAM") {
					t.Errorf("the provider's token leaked into the %s: %s", where, s)
				}
			}
			live, deleted := liveSessions(h.z, u.id)
			if tc.strict {
				if !strings.Contains(body, CodeSocialFailed) || b.signedIn() != "" || live != 0 || deleted != 1 {
					t.Fatalf("strict refusal: body %s signed in %q, sessions live %d deleted %d", body, b.signedIn(), live, deleted)
				}
			} else if b.signedIn() == "" || live != 1 {
				t.Fatalf("lenient: signed in %q, live sessions %d", b.signedIn(), live)
			}
		})
	}
}

func TestUpstream_SuccessLeaksNothing(t *testing.T) {
	g := &grants{}
	var logs bytes.Buffer
	h := upstreamHarness(t, policyWith(SignupOpen, allMethods, googlePass), g, true, &logs)
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", withTokens(intentResult("new@else.com", true)))
	status, body, cookies := rawCallback(t, b, id, "tok")
	if status != 200 || len(g.all()) != 1 {
		t.Fatalf("callback: %d %s, grants %d", status, body, len(g.all()))
	}
	for where, s := range map[string]string{"body": body, "Set-Cookie": cookies, "logs": logs.String()} {
		if strings.Contains(s, "UPSTREAM") {
			t.Errorf("the provider's token leaked into the %s: %s", where, s)
		}
	}
	opts, _ := json.Marshal(OptionsFor(policyWith(SignupOpen, allMethods, googlePass), false))
	if strings.Contains(string(opts), "pass_tokens") || strings.Contains(string(opts), "repo") {
		t.Errorf("/auth/options exposes pass_tokens or its scopes: %s", opts)
	}
}

func TestNewSignInRoutes_RequireUpstreamTokensNeedsAHook(t *testing.T) {
	z := newFakeZitadel(t)
	_, err := NewSignInRoutes(SignInRoutesConfig{Client: z.client(), AppBase: testAppBase, RequireUpstreamTokens: true,
		SignedIn: func(http.ResponseWriter, Tokens) any { return nil }, SessionToken: func(*http.Request) string { return "" }})
	if err == nil || !strings.Contains(err.Error(), "UpstreamTokens") {
		t.Fatalf("err = %v", err)
	}
}
