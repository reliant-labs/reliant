package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// rotatingIdP is a refresh endpoint that behaves the way a STRICT rotating
// provider does. It is deliberately harsher than the dev IdP forge scaffolds
// (Zitadel v4.19.4 rotates on every refresh and rejects a replayed token
// immediately, but revokes only that token, not the grant) because the client
// has to be correct against the harshest behaviour in the wild:
//
//   - every successful refresh CONSUMES the presented token and issues a new
//     one;
//   - re-presenting a consumed token is treated as theft: the whole grant is
//     revoked, so every token in the chain — including the newest, never-reused
//     one — stops working, with invalid_grant.
//
// It is the oracle for rotation handling. Nothing here compares against a
// hardcoded token string: the server MINTS the tokens and remembers which it
// issued, so a client that replays a stale one is caught by the server's own
// bookkeeping rather than by a literal in a test.
type rotatingIdP struct {
	mu sync.Mutex

	// live is the single token currently redeemable, "" once the grant is
	// revoked.
	live string
	// consumed records every token this server has already redeemed.
	consumed map[string]bool
	// revoked latches once reuse is detected.
	revoked bool
	// minted counts tokens issued, and drives their values so no two are
	// equal.
	minted int
	// hits counts requests that reached the token endpoint. This is the
	// single-flight oracle.
	hits int
	// rotate controls whether a refresh issues a new token. A provider that
	// does NOT rotate is also legal, and the client must work with both.
	rotate bool

	server *httptest.Server
}

func newRotatingIdP(t *testing.T, rotate bool) *rotatingIdP {
	t.Helper()
	idp := &rotatingIdP{consumed: map[string]bool{}, rotate: rotate}
	idp.live = idp.mint()
	idp.server = httptest.NewServer(http.HandlerFunc(idp.handle))
	t.Cleanup(idp.server.Close)
	return idp
}

// mint issues a fresh, unique token value. Caller holds mu (or is the
// constructor, before the server exists).
func (i *rotatingIdP) mint() string {
	i.minted++
	return fmt.Sprintf("rt-%d", i.minted)
}

func (i *rotatingIdP) tokenEndpoint() string { return i.server.URL + "/token" }

// initialToken is the refresh token a caller starts with, as though it had
// come from the authorization-code exchange.
func (i *rotatingIdP) initialToken(t *testing.T) RefreshToken {
	t.Helper()
	i.mu.Lock()
	defer i.mu.Unlock()
	tok, err := NewRefreshToken(i.live)
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	return tok
}

func (i *rotatingIdP) hitCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.hits
}

func (i *rotatingIdP) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "unparseable body")
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.hits++

	if got := form.Get("grant_type"); got != "refresh_token" {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeUnsupportedGrantType,
			fmt.Sprintf("grant_type was %q", got))
		return
	}
	presented := form.Get("refresh_token")
	if presented == "" {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "missing refresh_token")
		return
	}
	if i.revoked {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "grant revoked")
		return
	}
	// Reuse detection: a token this server already redeemed is theft, and
	// the response is to kill the whole grant — the strictest reading of
	// RFC 6749 §6, and what a client must survive.
	if i.consumed[presented] {
		i.revoked = true
		i.live = ""
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "refresh token already used")
		return
	}
	if presented != i.live {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "refresh token not found")
		return
	}

	resp := map[string]any{
		"access_token": "at-for-" + presented,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        "openid profile offline_access",
	}
	if i.rotate {
		i.consumed[presented] = true
		i.live = i.mint()
		resp["refresh_token"] = i.live
	}
	writeJSON(w, http.StatusOK, resp)
}

// TestRefreshRotationIsCarriedForward is the property that breaks a naive
// implementation: against a rotating provider, the SECOND refresh must present
// the token the FIRST one returned.
//
// The assertion derives entirely from the server's own bookkeeping — it mints
// the tokens and revokes on reuse — so a client that replays the original is
// caught by the provider, not by a string comparison in this test.
func TestRefreshRotationIsCarriedForward(t *testing.T) {
	idp := newRotatingIdP(t, true)
	ex := &Exchanger{Client: idp.server.Client()}

	current := idp.initialToken(t)
	for round := 1; round <= 3; round++ {
		got, err := ex.Refresh(context.Background(), RefreshRequest{
			Endpoint:     idp.tokenEndpoint(),
			ClientID:     "spa-client",
			RefreshToken: current,
		})
		if err != nil {
			t.Fatalf("refresh round %d: %v", round, err)
		}
		if !got.Rotated {
			t.Errorf("round %d: Rotated = false against a rotating provider", round)
		}
		// The access token the server derived from what was PRESENTED. This
		// is what proves the right credential went on the wire, without this
		// test knowing any token's value.
		if want := "at-for-" + current.Secret(); got.Token.AccessToken != want {
			t.Errorf("round %d: AccessToken = %q, want %q — the server derived it from the token actually presented",
				round, got.Token.AccessToken, want)
		}
		if got.RefreshToken.IsZero() {
			t.Fatalf("round %d: Refreshed.RefreshToken is zero; there is nothing to present next", round)
		}
		if got.RefreshToken.Secret() == current.Secret() {
			t.Fatalf("round %d: Refreshed.RefreshToken is the token just presented, but the provider rotated", round)
		}
		// Not t.Fatalf above only: the loop CONTINUING with the carried-forward
		// token is what makes round 2 and 3 meaningful. If the implementation
		// stopped carrying rotation forward, round 2 would present a consumed
		// token and the provider's reuse detection would revoke the grant —
		// which is exactly the production failure this guards.
		current = got.RefreshToken
	}
}

// TestRefreshWithoutRotationKeepsPresentingTheSameToken pins the other legal
// provider behaviour. A provider that issues no new refresh token has NOT
// invalidated the old one, so Refreshed.RefreshToken must be the token just
// presented rather than a zero value the caller would store and then fail with.
func TestRefreshWithoutRotationKeepsPresentingTheSameToken(t *testing.T) {
	idp := newRotatingIdP(t, false)
	ex := &Exchanger{Client: idp.server.Client()}

	start := idp.initialToken(t)
	current := start
	for round := 1; round <= 3; round++ {
		got, err := ex.Refresh(context.Background(), RefreshRequest{
			Endpoint:     idp.tokenEndpoint(),
			ClientID:     "spa-client",
			RefreshToken: current,
		})
		if err != nil {
			t.Fatalf("refresh round %d: %v", round, err)
		}
		if got.Rotated {
			t.Errorf("round %d: Rotated = true, but the provider sent no new refresh token", round)
		}
		if got.RefreshToken.IsZero() {
			t.Fatalf("round %d: RefreshToken is zero; a caller storing this would lose the credential", round)
		}
		if got.RefreshToken.Secret() != start.Secret() {
			t.Errorf("round %d: RefreshToken = %q, want the originally presented token",
				round, got.RefreshToken.Secret())
		}
		current = got.RefreshToken
	}
}

// TestRefreshReuseIsSurfacedAsTypedError proves that when a client DOES replay
// a consumed token, the failure arrives as a typed *Error carrying
// invalid_grant, so a caller can tell "start a new login" from "the network is
// down".
func TestRefreshReuseIsSurfacedAsTypedError(t *testing.T) {
	idp := newRotatingIdP(t, true)
	ex := &Exchanger{Client: idp.server.Client()}

	stale := idp.initialToken(t)
	if _, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:     idp.tokenEndpoint(),
		ClientID:     "spa-client",
		RefreshToken: stale,
	}); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Deliberately present the now-consumed token again.
	_, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:     idp.tokenEndpoint(),
		ClientID:     "spa-client",
		RefreshToken: stale,
	})
	if err == nil {
		t.Fatal("replaying a consumed refresh token succeeded; the test provider's reuse detection is not wired up")
	}
	var oauthErr *Error
	if !errors.As(err, &oauthErr) {
		t.Fatalf("error is %T (%v), want *oauth2.Error so callers can branch on the code", err, err)
	}
	if oauthErr.Code != ErrCodeInvalidGrant {
		t.Errorf("Error.Code = %q, want %q", oauthErr.Code, ErrCodeInvalidGrant)
	}
	if oauthErr.StatusCode != http.StatusBadRequest {
		t.Errorf("Error.StatusCode = %d, want 400", oauthErr.StatusCode)
	}
}

// TestRefreshSendsTheGrantRFC6749Requires checks the wire shape: the form must
// carry grant_type=refresh_token and the credential, in the BODY (never the
// URL, where it would land in access logs), and must omit client_secret for a
// public client.
func TestRefreshSendsTheGrantRFC6749Requires(t *testing.T) {
	var gotForm url.Values
	var gotMethod, gotContentType, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotURL = r.URL.String()
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("the-refresh-secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	if _, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:     srv.URL + "/token",
		ClientID:     "spa-client",
		RefreshToken: rt,
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	for key, want := range map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": rt.Secret(),
		"client_id":     "spa-client",
	} {
		if got := gotForm.Get(key); got != want {
			t.Errorf("form field %q = %q, want %q", key, got, want)
		}
	}
	if _, present := gotForm["client_secret"]; present {
		t.Error("client_secret was sent for a public client")
	}
	// scope must be ABSENT, not empty: an empty scope= asks for no scopes.
	if _, present := gotForm["scope"]; present {
		t.Errorf("scope was sent with no Scopes set (value %q); an empty scope is a request for NO scopes, not the original grant", gotForm.Get("scope"))
	}
	if strings.Contains(gotURL, rt.Secret()) {
		t.Error("the refresh token appeared in the request URL, where it would be captured by access logs")
	}
}

// TestRefreshScopesAreSentWhenNarrowed complements the test above: when a
// caller DOES narrow scope, it must reach the wire space-delimited per RFC
// 6749 §3.3.
func TestRefreshScopesAreSentWhenNarrowed(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	if _, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:     srv.URL + "/token",
		ClientID:     "c",
		RefreshToken: rt,
		Scopes:       []string{"openid", "profile"},
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got, want := gotForm.Get("scope"), "openid profile"; got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

// TestRefreshRejectsSuccessWithoutAccessToken pins that a 200 carrying no
// access token is a failure. A blank access token is a credential-shaped value
// that authenticates nothing; returning it would surface as an unexplained 401
// far from here.
func TestRefreshRejectsSuccessWithoutAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A rotated refresh token but no access token — the shape that would
		// tempt an implementation into reporting success.
		writeJSON(w, http.StatusOK, map[string]any{
			"token_type": "Bearer", "expires_in": 3600, "refresh_token": "rt-new",
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	got, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	})
	if !errors.Is(err, ErrNoAccessToken) {
		t.Errorf("err = %v, want ErrNoAccessToken", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil on failure", got)
	}
}

// TestRefreshRequiresCompleteRequest checks each required field is named when
// missing, rather than the call reaching the network and failing obscurely.
func TestRefreshRequiresCompleteRequest(t *testing.T) {
	valid, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	cases := map[string]RefreshRequest{
		"Endpoint":     {ClientID: "c", RefreshToken: valid},
		"ClientID":     {Endpoint: "https://idp.test/token", RefreshToken: valid},
		"RefreshToken": {Endpoint: "https://idp.test/token", ClientID: "c"},
	}
	for field, req := range cases {
		t.Run(field, func(t *testing.T) {
			// A doer that fails the test if reached: a request missing a
			// required field must never hit the network.
			ex := &Exchanger{Client: doerFunc(func(*http.Request) (*http.Response, error) {
				t.Errorf("Refresh sent a request despite missing %s", field)
				return nil, errors.New("must not be called")
			})}
			_, err := ex.Refresh(context.Background(), req)
			if err == nil {
				t.Fatalf("Refresh with no %s succeeded", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("error %q does not name the missing field %q", err, field)
			}
		})
	}
}

// TestRefreshExtraCannotOverrideReservedParameters pins that Extra cannot
// smuggle a second grant_type or refresh_token. url.Values allows repeats, and
// a duplicated grant parameter is a request whose meaning depends on which one
// the server reads first.
func TestRefreshExtraCannotOverrideReservedParameters(t *testing.T) {
	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	for _, key := range reservedRefreshParams() {
		t.Run(key, func(t *testing.T) {
			ex := &Exchanger{Client: doerFunc(func(*http.Request) (*http.Response, error) {
				t.Errorf("Refresh sent a request despite Extra setting reserved %q", key)
				return nil, errors.New("must not be called")
			})}
			_, err := ex.Refresh(context.Background(), RefreshRequest{
				Endpoint:     "https://idp.test/token",
				ClientID:     "c",
				RefreshToken: rt,
				Extra:        url.Values{key: {"smuggled"}},
			})
			if err == nil {
				t.Fatalf("Extra[%q] was accepted", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q does not name the offending key %q", err, key)
			}
		})
	}

	// Guards the guard: an empty reserved set would make every case above
	// vacuous.
	if len(reservedRefreshParams()) == 0 {
		t.Fatal("reservedRefreshParams() is empty, so this test asserted nothing")
	}
}

// TestRefreshTokenRedaction pins that the credential does not leak through the
// formatting and logging paths that a caller reaches by accident.
func TestRefreshTokenRedaction(t *testing.T) {
	const secret = "super-secret-refresh-value"
	rt, err := NewRefreshToken(secret)
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}

	for _, tc := range []struct {
		name string
		got  string
	}{
		{"%v", fmt.Sprintf("%v", rt)},
		{"%s", fmt.Sprintf("%s", rt)},
		{"%q", fmt.Sprintf("%q", rt)},
		{"%#v", fmt.Sprintf("%#v", rt)},
		{"String", rt.String()},
		{"inside a struct", fmt.Sprintf("%v", struct{ T RefreshToken }{rt})},
		{"inside a slice", fmt.Sprintf("%v", []RefreshToken{rt})},
	} {
		if strings.Contains(tc.got, secret) {
			t.Errorf("%s rendered the secret: %s", tc.name, tc.got)
		}
		if !strings.Contains(tc.got, redacted) {
			t.Errorf("%s = %s, want it to contain %s", tc.name, tc.got, redacted)
		}
	}

	// slog is the path most likely to be reached without thinking.
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("refreshing", "token", rt)
	if strings.Contains(buf.String(), secret) {
		t.Errorf("slog leaked the secret: %s", buf.String())
	}

	// And the Refreshed wrapper must not leak it either — it is the value
	// callers actually hold.
	res := &Refreshed{Token: &Token{AccessToken: "at"}, RefreshToken: rt, Rotated: true}
	if s := fmt.Sprintf("%v", res); strings.Contains(s, secret) {
		t.Errorf("Refreshed rendered the refresh secret: %s", s)
	}
}

// TestNewRefreshTokenRejectsEmpty pins that a zero token cannot be constructed
// by accident. Letting one through would produce a request whose credential is
// absent, which the provider answers with invalid_grant far from the cause.
func TestNewRefreshTokenRejectsEmpty(t *testing.T) {
	got, err := NewRefreshToken("")
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}
	if !got.IsZero() {
		t.Error("a rejected RefreshToken is not zero")
	}
}

// TestRefreshHonorsContextCancellation pins that a wedged provider cannot hang
// a caller forever.
func TestRefreshHonorsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ex := &Exchanger{Client: srv.Client()}
	if _, err := ex.Refresh(ctx, RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestRefreshSendsClientSecretForConfidentialClients pins the confidential
// case, so the public-client assertion above is meaningful rather than
// vacuously true because the field is never sent.
func TestRefreshSendsClientSecretForConfidentialClients(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	if _, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:     srv.URL + "/token",
		ClientID:     "c",
		ClientSecret: "confidential",
		RefreshToken: rt,
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := gotForm.Get("client_secret"); got != "confidential" {
		t.Errorf("client_secret = %q, want %q", got, "confidential")
	}
}

// TestRefreshSurfacesNonJSONFailures pins that an HTML error page from a proxy
// does not become a confusing decode error with no status in it.
func TestRefreshSurfacesNonJSONFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	_, err = ex.Refresh(context.Background(), RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	})
	if err == nil {
		t.Fatal("a 502 HTML page was reported as success")
	}
	var oauthErr *Error
	if !errors.As(err, &oauthErr) {
		t.Fatalf("error is %T (%v), want *oauth2.Error carrying the status", err, err)
	}
	if oauthErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", oauthErr.StatusCode)
	}
}

// TestRefreshErrorsDoNotLeakTheCredential pins that the refresh token stays out
// of error strings. An error is the value most likely to be logged verbatim.
func TestRefreshErrorsDoNotLeakTheCredential(t *testing.T) {
	const secret = "leak-me-if-you-can"

	// Two failure shapes: a provider refusal, and a transport failure.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "no")
	}))
	t.Cleanup(refusing.Close)

	rt, err := NewRefreshToken(secret)
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}

	for name, endpoint := range map[string]string{
		"provider refusal": refusing.URL + "/token",
		"unreachable host": "http://127.0.0.1:1/token",
	} {
		ex := &Exchanger{Client: refusing.Client()}
		_, err := ex.Refresh(context.Background(), RefreshRequest{
			Endpoint: endpoint, ClientID: "c", RefreshToken: rt,
		})
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaked the refresh token: %v", name, err)
		}
	}
}

// TestRefreshDecodesRotationFromRawResponse pins that a provider-specific field
// stays reachable through Token.Raw, and that Refreshed.Token is the parsed
// response rather than a reconstruction.
func TestRefreshDecodesRotationFromRawResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  "at-1",
			"token_type":    "Bearer",
			"expires_in":    120,
			"refresh_token": "rt-2",
			"scope":         "openid offline_access",
			"provider_note": "something forge does not model",
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("rt-1")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	got, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Token.ExpiresIn != 120 {
		t.Errorf("ExpiresIn = %d, want 120", got.Token.ExpiresIn)
	}
	if got.Token.Scope != "openid offline_access" {
		t.Errorf("Scope = %q", got.Token.Scope)
	}
	if note, _ := got.Token.Raw["provider_note"].(string); note == "" {
		t.Error("Token.Raw lost the provider-specific field")
	}
	if !got.Rotated || got.RefreshToken.Secret() != "rt-2" {
		t.Errorf("Rotated = %t, RefreshToken = %q; want true and rt-2", got.Rotated, got.RefreshToken.Secret())
	}
}

// TestRefreshTreatsEchoedTokenAsNoRotation pins a subtle case: a provider that
// echoes back the SAME refresh token has not rotated, and reporting Rotated
// would make a caller log a rotation that did not happen.
func TestRefreshTreatsEchoedTokenAsNoRotation(t *testing.T) {
	const same = "rt-unchanged"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
			"refresh_token": same,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken(same)
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	got, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Rotated {
		t.Error("Rotated = true, but the provider echoed the same refresh token")
	}
	if got.RefreshToken.Secret() != same {
		t.Errorf("RefreshToken = %q, want the unchanged token", got.RefreshToken.Secret())
	}
}

// TestZeroExchangerRefreshes pins that the zero Exchanger is usable, matching
// Exchange's documented behaviour — a caller should not have to build a client
// to make one call.
func TestZeroExchangerRefreshes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	var ex Exchanger // zero value, no Client
	got, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint: srv.URL + "/token", ClientID: "c", RefreshToken: rt,
	})
	if err != nil {
		t.Fatalf("zero Exchanger Refresh: %v", err)
	}
	if got.Token.AccessToken != "at" {
		t.Errorf("AccessToken = %q", got.Token.AccessToken)
	}
}

// TestRefreshTokenJSONRoundTrip pins the DELIBERATE divergence from Verifier:
// a refresh token must survive being carried in a session, because that is the
// whole point of holding one. Verifier fails to marshal; this must not.
func TestRefreshTokenJSONRoundTrip(t *testing.T) {
	// The realistic shape: a Token as persisted by a caller.
	tok := &Token{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 60}
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshalling a Token must not fail: %v", err)
	}
	var back Token
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.RefreshToken != "rt" {
		t.Errorf("RefreshToken did not survive the round trip: %q", back.RefreshToken)
	}
}
