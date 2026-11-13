package devidp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
)

// authEvent is one standard auth event as observe.TrackAuth wrote it.
type authEvent struct {
	name  string            // auth_signed_in, …
	user  string            // user.id
	props map[string]string // product.* without the prefix
}

func (e authEvent) String() string {
	return e.name + "(" + e.user + ")" + func() string { b, _ := json.Marshal(e.props); return string(b) }()
}

// recordAuthEvents captures what this package records, through the real
// observe.TrackAuth, so the assertions see the bounded record.
func recordAuthEvents(t *testing.T) func() []authEvent {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	prev := trackAuth
	trackAuth = func(ctx context.Context, ev observe.AuthEvent, userID string, opts ...observe.AuthOption) {
		mu.Lock()
		defer mu.Unlock()
		observe.TrackAuth(observe.WithLogger(ctx, logger), ev, userID, opts...)
	}
	t.Cleanup(func() { trackAuth = prev })
	return func() []authEvent {
		mu.Lock()
		defer mu.Unlock()
		var out []authEvent
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("bad line %q: %v", line, err)
			}
			e := authEvent{props: map[string]string{}}
			e.name = strings.TrimPrefix(m["event.name"].(string), "product.")
			e.user, _ = m["user.id"].(string)
			for k, v := range m {
				if strings.HasPrefix(k, "product.") {
					e.props[strings.TrimPrefix(k, "product.")] = v.(string)
				}
			}
			out = append(out, e)
		}
		return out
	}
}

func wantAuthEvents(t *testing.T, got []authEvent, want ...authEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i].String() {
			t.Errorf("event %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func signedIn(user, method string, extra ...string) authEvent {
	p := map[string]string{"kind": "credential", "method": method, "provider": "forge"}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	return authEvent{name: "auth_signed_in", user: user, props: p}
}

func signedUp(user, method, verified string, extra ...string) authEvent {
	p := map[string]string{"method": method, "provider": "forge", "verified": verified}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	return authEvent{name: "auth_signed_up", user: user, props: p}
}

func TestAuthEvents_PasswordSignInRecordsOneSignIn(t *testing.T) {
	events := recordAuthEvents(t)
	z := newFakeZitadel(t)
	u := z.addUser("ada@acme.com", true, "a-long-password")
	cfg := FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"}
	if _, err := z.client().SignIn(context.Background(), cfg, Credentials{LoginName: "ada@acme.com", Password: "wrong"}); err == nil {
		t.Fatal("a wrong password signed in")
	}
	if _, err := z.client().SignIn(context.Background(), cfg, Credentials{LoginName: "ada@acme.com", Password: "a-long-password"}); err != nil {
		t.Fatal(err)
	}
	// The refused attempt recorded nothing; the subject is the issuer's id,
	// never the address typed.
	wantAuthEvents(t, events(), signedIn(u.id, "password"))
}

func TestAuthEvents_WithAuthMethodNamesAResetSignIn(t *testing.T) {
	events := recordAuthEvents(t)
	z := newFakeZitadel(t)
	u := z.addUser("ada@acme.com", true, "a-long-password")
	ctx := WithAuthMethod(context.Background(), observe.AuthMethodReset)
	if _, err := z.client().SignIn(ctx, FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"},
		Credentials{LoginName: "ada@acme.com", Password: "a-long-password"}); err != nil {
		t.Fatal(err)
	}
	wantAuthEvents(t, events(), signedIn(u.id, "reset"))
}

func TestAuthEvents_PasskeySignIn(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := seedPasskeyUser(h, true)
	b := h.browser()
	b.post("/auth/passkey/begin", map[string]any{"email": "pk@acme.com"})
	if status, body := b.post("/auth/passkey/finish", map[string]any{"credential": credential(testAppBase, WebAuthnGet, "good")}); status != 200 {
		t.Fatalf("finish: %d %v", status, body)
	}
	wantAuthEvents(t, events(), signedIn(u.id, "passkey"))
}

func TestAuthEvents_MagicLinkSignUpThenSignIn(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodMagicLink}, Mail: MailDev, RequireVerifiedEmail: true})
	b := h.browser()
	b.post("/auth/magic/request", map[string]any{"email": "new@else.com"})
	u := h.z.userByEmail("new@else.com")
	if status, body := b.post("/auth/magic/consume", map[string]any{"code": h.z.lastMail(t).code}); status != 200 {
		t.Fatalf("consume: %d %v", status, body)
	}
	wantAuthEvents(t, events(), signedUp(u.id, "magic_link", "false"), signedIn(u.id, "magic_link"))
}

func TestAuthEvents_SocialSignInNamesTheProvider(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, policyWith(SignupInvite, allMethods, google))
	u := h.z.addUser("g@acme.com", true, "")
	u.links = []IDPLink{{IdPID: "idp-google", UserID: "g-123"}}
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", intentResult("g@acme.com", true))
	if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
		t.Fatalf("callback: %d %v", status, body)
	}
	wantAuthEvents(t, events(), signedIn(u.id, "social", "social_provider", "google"))
}

func TestAuthEvents_SocialSignUp(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, policyWith(SignupOpen, allMethods, google))
	b := h.browser()
	id := startSocial(t, b)
	h.z.addIntent(id, "tok", intentResult("g@acme.com", true))
	if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
		t.Fatalf("callback: %d %v", status, body)
	}
	u := h.z.userByEmail("g@acme.com")
	wantAuthEvents(t, events(),
		signedUp(u.id, "social", "true", "social_provider", "google"),
		signedIn(u.id, "social", "social_provider", "google"))
}

func TestAuthEvents_AnAcceptedInvitationIsTheSignUp(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, policyWith(SignupInvite, allMethods))
	u := invitee(h)
	b := h.browser()
	// A wrong code accepts nothing, so it records nothing.
	b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "nope", "method": "password", "password": "a-long-password"})
	if status, body := b.post("/auth/invite/accept", map[string]any{"userID": u.id, "code": "inv-1", "method": "password", "password": "a-long-password"}); status != 200 {
		t.Fatalf("accept: %d %v", status, body)
	}
	wantAuthEvents(t, events(), signedUp(u.id, "invite", "true"), signedIn(u.id, "password"))
}

func TestAuthEvents_RegisterWithoutMail(t *testing.T) {
	events := recordAuthEvents(t)
	h := newHarness(t, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodPassword}, Mail: MailNone})
	b := h.browser()
	if status, body := b.post("/auth/register", map[string]any{"email": "new@else.com", "password": "a-long-password"}); status != 200 {
		t.Fatalf("register: %d %v", status, body)
	}
	u := h.z.userByEmail("new@else.com")
	wantAuthEvents(t, events(), signedUp(u.id, "password", "false"), signedIn(u.id, "password"))
}

func TestAuthEvents_LegacySignUp(t *testing.T) {
	events := recordAuthEvents(t)
	z := newFakeZitadel(t)
	if _, err := z.client().SignUp(context.Background(), FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"},
		Registration{Email: "new@else.com", Password: "a-long-password"}); err != nil {
		t.Fatal(err)
	}
	u := z.userByEmail("new@else.com")
	wantAuthEvents(t, events(), signedUp(u.id, "password", "false"), signedIn(u.id, "password"))
}

func TestAuthEvents_TokenSubject(t *testing.T) {
	jwt := func(claims string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	}
	for name, tc := range map[string]struct {
		tokens Tokens
		want   string
	}{
		"id token":                 {Tokens{IDToken: jwt(`{"sub":"u1"}`), AccessToken: jwt(`{"sub":"u2"}`)}, "u1"},
		"jwt access token":         {Tokens{AccessToken: jwt(`{"sub":"u2"}`)}, "u2"},
		"opaque tokens":            {Tokens{AccessToken: "opaque"}, ""},
		"no sub":                   {Tokens{IDToken: jwt(`{"email":"x"}`)}, ""},
		"garbage payload":          {Tokens{IDToken: "h.!!!.s"}, ""},
		"opaque access, id has it": {Tokens{IDToken: jwt(`{"sub":"u3"}`), AccessToken: "opaque"}, "u3"},
	} {
		if got := tokenSubject(tc.tokens); got != tc.want {
			t.Errorf("%s: tokenSubject = %q, want %q", name, got, tc.want)
		}
	}
}

func TestAuthEvents_MethodFromFactors(t *testing.T) {
	for want, factors := range map[observe.AuthMethod][]string{
		observe.AuthMethodPasskey:   {"password", "webAuthN"},
		observe.AuthMethodSocial:    {"intent"},
		observe.AuthMethodMagicLink: {"otpEmail"},
		observe.AuthMethodPassword:  {"password", "totp"},
		observe.AuthMethodOther:     {"otpSms"},
	} {
		if got := methodFromFactors(factors); got != want {
			t.Errorf("methodFromFactors(%v) = %s, want %s", factors, got, want)
		}
	}
}
