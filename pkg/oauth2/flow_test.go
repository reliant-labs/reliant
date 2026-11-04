package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestFullFlowThroughDiscovery drives every piece of the package the way a real
// callback route would: discover the provider, build the authorization URL,
// have the "provider" parse that URL and record the challenge, then exchange
// the code. The provider recomputes the challenge from the verifier itself, so
// nothing in the flow is asserted against a value this package produced.
func TestFullFlowThroughDiscovery(t *testing.T) {
	type grant struct {
		challenge   Challenge
		redirectURI string
		clientID    string
	}
	grants := map[string]grant{}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                           srv.URL,
				"authorization_endpoint":           srv.URL + "/oidc/auth",
				"token_endpoint":                   srv.URL + "/oidc/token",
				"code_challenge_methods_supported": []string{"S256"},
			})

		case "/oidc/auth":
			// Behave like an authorization server: validate the request,
			// record the challenge, redirect back with a code.
			q := r.URL.Query()
			if q.Get("response_type") != "code" {
				http.Error(w, "bad response_type", http.StatusBadRequest)
				return
			}
			method := q.Get("code_challenge_method")
			challenge := q.Get("code_challenge")
			if method == "" || challenge == "" {
				http.Error(w, "missing PKCE parameters", http.StatusBadRequest)
				return
			}
			code := "code-for-" + q.Get("state")
			grants[code] = grant{
				challenge:   Challenge{Value: challenge, Method: Method(method)},
				redirectURI: q.Get("redirect_uri"),
				clientID:    q.Get("client_id"),
			}
			redirect, err := url.Parse(q.Get("redirect_uri"))
			if err != nil {
				http.Error(w, "bad redirect_uri", http.StatusBadRequest)
				return
			}
			rq := redirect.Query()
			rq.Set("code", code)
			rq.Set("state", q.Get("state"))
			redirect.RawQuery = rq.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)

		case "/oidc/token":
			if err := r.ParseForm(); err != nil {
				writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "bad form")
				return
			}
			g, ok := grants[r.PostForm.Get("code")]
			if !ok {
				writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "unknown code")
				return
			}
			if r.PostForm.Get("client_id") != g.clientID {
				writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidClient, "client_id does not match the authorization request")
				return
			}
			if r.PostForm.Get("redirect_uri") != g.redirectURI {
				writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "redirect_uri does not match the authorization request")
				return
			}
			// Independent PKCE verification.
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge.Value {
				writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "PKCE verification failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "flow-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := srv.Client()

	// 1. Discovery.
	meta, err := Discover(ctx, client, srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !meta.SupportsS256() {
		t.Fatal("provider does not advertise S256; the flow below would be testing the wrong thing")
	}

	// 2. Mint the PKCE pair and state.
	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	state, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// 3. Simulate persisting across the redirect, as a session would.
	persistedVerifier := verifier.Secret()
	persistedState := state

	// 4. Build the authorization URL and "follow" it.
	const redirectURI = "https://app.example.test/auth/callback"
	authURL, err := AuthRequest{
		Endpoint:    meta.AuthorizationEndpoint,
		ClientID:    "flow-client",
		RedirectURI: redirectURI,
		Scopes:      []string{"openid", "profile"},
		State:       state,
		Challenge:   verifier.Challenge(),
	}.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}

	// Do not follow the redirect; capture it, as a browser landing on the
	// callback would.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Get(authURL)
	if err != nil {
		t.Fatalf("GET authorization URL: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorization endpoint returned %d, want a redirect", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse callback location: %v", err)
	}

	// 5. The callback route's first act: check state.
	restoredVerifier, err := ParseVerifier(persistedVerifier)
	if err != nil {
		t.Fatalf("ParseVerifier on the persisted verifier: %v", err)
	}
	if err := CompareState(persistedState, location.Query().Get("state")); err != nil {
		t.Fatalf("CompareState on a legitimate callback: %v", err)
	}

	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("callback carried no authorization code")
	}

	// 6. Exchange.
	tok, err := (&Exchanger{Client: client}).Exchange(ctx, TokenRequest{
		Endpoint:    meta.TokenEndpoint,
		ClientID:    "flow-client",
		RedirectURI: redirectURI,
		Code:        code,
		Verifier:    restoredVerifier,
	})
	if err != nil {
		t.Fatalf("Exchange at the end of a full flow: %v", err)
	}
	if tok.AccessToken != "flow-access-token" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}

	// The verifier survived the round trip intact — the property that makes
	// persistence across the redirect work.
	if restoredVerifier.Secret() != verifier.Secret() {
		t.Error("the verifier changed across persistence and restoration")
	}

	// And a forged callback is rejected at the state check, before any code
	// is exchanged.
	if err := CompareState(persistedState, "forged-state"); !errors.Is(err, ErrStateMismatch) {
		t.Errorf("a forged state produced %v, want ErrStateMismatch", err)
	}

	// The verifier never appeared in anything the user agent saw.
	for what, s := range map[string]string{
		"authorization URL": authURL,
		"callback location": location.String(),
	} {
		if strings.Contains(s, verifier.Secret()) {
			t.Errorf("the %s contained the code verifier: %s", what, s)
		}
	}
}

// TestFlowRejectsCodeFromADifferentSession is the attack this whole package
// exists to prevent: an intercepted authorization code is useless without the
// verifier that produced the challenge.
func TestFlowRejectsCodeFromADifferentSession(t *testing.T) {
	idp := newFakeIdP(t)

	// Session A starts a login.
	victim, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	const interceptedCode = "victims-code"
	idp.authorize(interceptedCode, victim.Challenge())

	// An attacker who intercepted the code runs their own verifier.
	attacker, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	tok, err := (&Exchanger{Client: idp.server.Client()}).Exchange(context.Background(), TokenRequest{
		Endpoint:    idp.tokenEndpoint(),
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        interceptedCode,
		Verifier:    attacker,
	})
	if err == nil {
		t.Fatalf("an intercepted code was redeemed with a foreign verifier: %v", tok)
	}
	var oerr *Error
	if !errors.As(err, &oerr) || oerr.Code != ErrCodeInvalidGrant {
		t.Errorf("error = %v, want invalid_grant from the PKCE check", err)
	}

	// The legitimate session still works, proving the rejection above was
	// about the verifier and not a broken fixture.
	if _, err := (&Exchanger{Client: idp.server.Client()}).Exchange(context.Background(), TokenRequest{
		Endpoint:    idp.tokenEndpoint(),
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        interceptedCode,
		Verifier:    victim,
	}); err != nil {
		t.Fatalf("the legitimate exchange failed: %v", err)
	}
}
