package oauth2

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// newTestPair returns a verifier plus its challenge for URL-building tests.
func newTestPair(t *testing.T) (Verifier, Challenge) {
	t.Helper()
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v, v.Challenge()
}

// TestAuthRequestURLCarriesEveryParameter parses the produced URL back and
// compares each parameter against the input that produced it, so the
// expectations are derived from the request rather than from a literal URL
// string that would have to be kept in sync by hand.
func TestAuthRequestURLCarriesEveryParameter(t *testing.T) {
	v, challenge := newTestPair(t)
	state, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	req := AuthRequest{
		Endpoint:    "https://idp.example.test/oidc/auth",
		ClientID:    "client-id-123",
		RedirectURI: "https://app.example.test/auth/callback?tenant=acme",
		Scopes:      []string{"openid", "profile", "offline_access"},
		State:       state,
		Challenge:   challenge,
	}

	raw, err := req.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("produced URL does not parse: %v (%q)", err, raw)
	}

	if u.Scheme != "https" || u.Host != "idp.example.test" || u.Path != "/oidc/auth" {
		t.Errorf("URL target = %s://%s%s, want https://idp.example.test/oidc/auth", u.Scheme, u.Host, u.Path)
	}

	q := u.Query()
	if len(q) == 0 {
		t.Fatal("produced URL has no query parameters")
	}

	want := map[string]string{
		"response_type":         "code",
		"client_id":             req.ClientID,
		"redirect_uri":          req.RedirectURI,
		"state":                 req.State,
		"code_challenge":        challenge.Value,
		"code_challenge_method": string(challenge.Method),
		"scope":                 strings.Join(req.Scopes, " "),
	}
	for key, expected := range want {
		got, present := q[key]
		if !present {
			t.Errorf("parameter %q missing from authorization URL", key)
			continue
		}
		if len(got) != 1 {
			t.Errorf("parameter %q appears %d times, want exactly once: %v", key, len(got), got)
			continue
		}
		if got[0] != expected {
			t.Errorf("parameter %q = %q, want %q", key, got[0], expected)
		}
	}

	// The challenge on the wire must be the hash of the verifier that the
	// token exchange will later send. Derived here, not copied.
	sum := sha256.Sum256([]byte(v.Secret()))
	if got := q.Get("code_challenge"); got != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Errorf("code_challenge on the wire is not SHA256 of the verifier: got %q", got)
	}

	// The verifier itself must never appear in a URL the user agent sees.
	if strings.Contains(raw, v.Secret()) {
		t.Errorf("authorization URL contains the code verifier: %s", raw)
	}
}

// TestAuthRequestURLEncodesSpecialCharacters checks that percent-encoding
// happens by round-tripping values chosen to break naive concatenation.
func TestAuthRequestURLEncodesSpecialCharacters(t *testing.T) {
	_, challenge := newTestPair(t)

	nasty := map[string]string{
		"redirect with query and fragment-ish": "https://app.example.test/cb?a=1&b=2#frag",
		"redirect with space":                  "https://app.example.test/cb path",
		"redirect with plus":                   "https://app.example.test/cb+plus",
		"redirect with equals":                 "https://app.example.test/cb?x=a=b",
		"redirect with unicode":                "https://app.example.test/cb/ünïcøde",
	}
	for name, redirect := range nasty {
		req := AuthRequest{
			Endpoint:    "https://idp.example.test/authorize",
			ClientID:    "client&id=evil",
			RedirectURI: redirect,
			Scopes:      []string{"openid", "read:things"},
			State:       "state with spaces & ampersands",
			Challenge:   challenge,
		}
		raw, err := req.URL()
		if err != nil {
			t.Fatalf("%s: AuthRequest.URL: %v", name, err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: produced URL does not parse: %v", name, err)
		}
		q := u.Query()
		if got := q.Get("redirect_uri"); got != redirect {
			t.Errorf("%s: redirect_uri round-tripped as %q, want %q", name, got, redirect)
		}
		if got := q.Get("client_id"); got != req.ClientID {
			t.Errorf("%s: client_id round-tripped as %q, want %q", name, got, req.ClientID)
		}
		if got := q.Get("state"); got != req.State {
			t.Errorf("%s: state round-tripped as %q, want %q", name, got, req.State)
		}
		if u.Fragment != "" {
			t.Errorf("%s: a value's '#' escaped into the URL fragment: %q", name, u.Fragment)
		}
	}
}

// TestAuthRequestURLRequiresSecurityParameters is the anti-silent-downgrade
// guard: a request missing state, challenge, or redirect URI must fail rather
// than produce a URL that starts a flow with a protection switched off.
func TestAuthRequestURLRequiresSecurityParameters(t *testing.T) {
	_, challenge := newTestPair(t)
	complete := AuthRequest{
		Endpoint:    "https://idp.example.test/authorize",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "some-state",
		Challenge:   challenge,
	}
	if _, err := complete.URL(); err != nil {
		t.Fatalf("the complete request must succeed, or the negative cases prove nothing: %v", err)
	}

	// Each case removes exactly one field from a known-good request.
	cases := map[string]func(*AuthRequest){
		"no endpoint":         func(r *AuthRequest) { r.Endpoint = "" },
		"no client id":        func(r *AuthRequest) { r.ClientID = "" },
		"no redirect uri":     func(r *AuthRequest) { r.RedirectURI = "" },
		"no state":            func(r *AuthRequest) { r.State = "" },
		"no challenge":        func(r *AuthRequest) { r.Challenge = Challenge{} },
		"challenge no method": func(r *AuthRequest) { r.Challenge.Method = "" },
		"unknown method":      func(r *AuthRequest) { r.Challenge.Method = Method("S1") },
		"relative endpoint":   func(r *AuthRequest) { r.Endpoint = "/authorize" },
		"http endpoint":       func(r *AuthRequest) { r.Endpoint = "http://idp.example.test/authorize" },
	}
	for name, mutate := range cases {
		req := complete
		mutate(&req)
		got, err := req.URL()
		if err == nil {
			t.Errorf("%s: AuthRequest.URL returned %q, want an error", name, got)
		}
		if got != "" {
			t.Errorf("%s: AuthRequest.URL returned a non-empty URL alongside its error: %q", name, got)
		}
	}
}

// TestAuthRequestAllowsLoopbackHTTP covers the native-app and CLI case: the IdP
// is https, but a local dev IdP on loopback is legitimate.
func TestAuthRequestAllowsLoopbackHTTP(t *testing.T) {
	_, challenge := newTestPair(t)
	for _, endpoint := range []string{
		"http://localhost:3000/oidc/auth",
		"http://127.0.0.1:3000/oidc/auth",
	} {
		req := AuthRequest{
			Endpoint:    endpoint,
			ClientID:    "client",
			RedirectURI: "http://127.0.0.1:8080/cb",
			State:       "state",
			Challenge:   challenge,
		}
		if _, err := req.URL(); err != nil {
			t.Errorf("loopback endpoint %q was rejected: %v", endpoint, err)
		}
	}
}

// TestAuthRequestExtraCannotOverrideReservedParameters guards against
// parameter injection: url.Values permits repeats, and a second
// code_challenge or redirect_uri would let a caller (or a config value flowing
// into Extra) quietly redirect the flow.
func TestAuthRequestExtraCannotOverrideReservedParameters(t *testing.T) {
	_, challenge := newTestPair(t)
	base := AuthRequest{
		Endpoint:    "https://idp.example.test/authorize",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "state",
		Challenge:   challenge,
	}

	if len(reservedAuthParams()) == 0 {
		t.Fatal("reservedAuthParams() is empty; this test would check nothing")
	}
	for _, key := range reservedAuthParams() {
		req := base
		req.Extra = url.Values{key: []string{"attacker-value"}}
		got, err := req.URL()
		if err == nil {
			t.Errorf("Extra[%q] was accepted, producing %q; reserved parameters must be rejected", key, got)
		}
	}

	// Legitimate extras do come through, once each.
	req := base
	req.Extra = url.Values{
		"prompt":     []string{"consent"},
		"audience":   []string{"https://api.example.test"},
		"login_hint": []string{"user@example.test"},
	}
	raw, err := req.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL with legitimate extras: %v", err)
	}
	q, err := url.ParseQuery(mustQuery(t, raw))
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	for key, want := range map[string]string{
		"prompt":     "consent",
		"audience":   "https://api.example.test",
		"login_hint": "user@example.test",
	} {
		if got := q[key]; len(got) != 1 || got[0] != want {
			t.Errorf("extra %q = %v, want exactly [%q]", key, got, want)
		}
	}
	// And the reserved ones are still single-valued.
	for _, key := range reservedAuthParams() {
		if key == "scope" {
			continue // not set when Scopes is empty
		}
		if got := q[key]; len(got) > 1 {
			t.Errorf("reserved parameter %q appears %d times: %v", key, len(got), got)
		}
	}
}

// TestAuthRequestPreservesEndpointQuery covers multi-tenant issuers that
// publish an authorization endpoint with a query string already on it.
func TestAuthRequestPreservesEndpointQuery(t *testing.T) {
	_, challenge := newTestPair(t)
	req := AuthRequest{
		Endpoint:    "https://idp.example.test/authorize?tenant=acme",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "state",
		Challenge:   challenge,
	}
	raw, err := req.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}
	q, err := url.ParseQuery(mustQuery(t, raw))
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got := q.Get("tenant"); got != "acme" {
		t.Errorf("endpoint's own query parameter was dropped: tenant = %q", got)
	}
	if q.Get("code_challenge") != challenge.Value {
		t.Error("code_challenge missing when the endpoint carried its own query")
	}
}

func TestAuthRequestScopesAreSpaceDelimited(t *testing.T) {
	_, challenge := newTestPair(t)
	req := AuthRequest{
		Endpoint:    "https://idp.example.test/authorize",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "state",
		Challenge:   challenge,
		Scopes:      []string{"openid", "profile", "email"},
	}
	raw, err := req.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}
	q, _ := url.ParseQuery(mustQuery(t, raw))
	scope := q.Get("scope")
	fields := strings.Split(scope, " ")
	if len(fields) != len(req.Scopes) {
		t.Fatalf("scope %q split into %d fields, want %d", scope, len(fields), len(req.Scopes))
	}
	for i, want := range req.Scopes {
		if fields[i] != want {
			t.Errorf("scope field %d = %q, want %q", i, fields[i], want)
		}
	}
	// Space must be encoded as %20 or +, never left raw in the URL.
	if strings.Contains(raw, "openid profile") {
		t.Errorf("scope separator left unencoded in the URL: %s", raw)
	}

	// No scopes: the parameter is omitted rather than sent empty.
	req.Scopes = nil
	raw, err = req.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL without scopes: %v", err)
	}
	q, _ = url.ParseQuery(mustQuery(t, raw))
	if _, present := q["scope"]; present {
		t.Error("scope parameter present with no scopes configured")
	}
}

func mustQuery(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.RawQuery
}
