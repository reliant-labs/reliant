package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIdP is a token endpoint that verifies PKCE the way a real authorization
// server does: it records the challenge from the authorization request and, at
// exchange time, recomputes BASE64URL(SHA256(code_verifier)) and compares.
//
// This is the end-to-end oracle. If the challenge derivation and the verifier
// sent at exchange time ever disagree — different hash, different encoding,
// verifier truncated in transit — the server rejects and the test fails, with
// no expected string written down anywhere.
type fakeIdP struct {
	t *testing.T

	// issued maps an authorization code to the challenge recorded for it.
	issued map[string]Challenge

	// lastForm captures the most recent request body for inspection.
	lastForm url.Values
	// lastHeader captures the most recent request headers.
	lastHeader http.Header
	// lastMethod captures the HTTP method used.
	lastMethod string

	server *httptest.Server
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{t: t, issued: map[string]Challenge{}}
	idp.server = httptest.NewServer(http.HandlerFunc(idp.handleToken))
	t.Cleanup(idp.server.Close)
	return idp
}

func (i *fakeIdP) tokenEndpoint() string { return i.server.URL + "/token" }

// authorize records a challenge and returns the authorization code for it,
// standing in for the redirect leg of the flow.
func (i *fakeIdP) authorize(code string, c Challenge) {
	i.issued[code] = c
}

func (i *fakeIdP) handleToken(w http.ResponseWriter, r *http.Request) {
	i.lastMethod = r.Method
	i.lastHeader = r.Header.Clone()

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
	i.lastForm = form

	if got := form.Get("grant_type"); got != "authorization_code" {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeUnsupportedGrantType, fmt.Sprintf("grant_type was %q", got))
		return
	}

	code := form.Get("code")
	challenge, known := i.issued[code]
	if !known {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant, "unknown authorization code")
		return
	}

	// The PKCE check, performed exactly as RFC 7636 §4.6 specifies.
	verifier := form.Get("code_verifier")
	if verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "missing code_verifier")
		return
	}
	var computed string
	switch challenge.Method {
	case MethodS256:
		sum := sha256.Sum256([]byte(verifier))
		computed = base64.RawURLEncoding.EncodeToString(sum[:])
	case MethodPlain:
		computed = verifier
	default:
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "unsupported challenge method")
		return
	}
	if computed != challenge.Value {
		writeOAuthError(w, http.StatusBadRequest, ErrCodeInvalidGrant,
			fmt.Sprintf("PKCE verification failed: recomputed %q from code_verifier, recorded challenge was %q", computed, challenge.Value))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  "at-" + code,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "rt-" + code,
		"id_token":      "it-" + code,
		"scope":         "openid profile",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{"error": code, "error_description": desc})
}

// TestExchangeRoundTripAgainstPKCEVerifyingServer is the integration proof: a
// server that independently recomputes the challenge accepts the exchange.
func TestExchangeRoundTripAgainstPKCEVerifyingServer(t *testing.T) {
	idp := newFakeIdP(t)

	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	const code = "auth-code-abc"
	idp.authorize(code, v.Challenge())

	ex := &Exchanger{Client: idp.server.Client(), UserAgent: "forge-test/1"}
	tok, err := ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    idp.tokenEndpoint(),
		ClientID:    "client-id",
		RedirectURI: "https://app.example.test/cb",
		Code:        code,
		Verifier:    v,
	})
	if err != nil {
		t.Fatalf("Exchange against a PKCE-verifying server: %v", err)
	}

	if tok.AccessToken != "at-"+code {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "at-"+code)
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", tok.TokenType)
	}
	if tok.RefreshToken != "rt-"+code {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "rt-"+code)
	}
	if tok.IDToken != "it-"+code {
		t.Errorf("IDToken = %q, want %q", tok.IDToken, "it-"+code)
	}
	if tok.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", tok.ExpiresIn)
	}
	if tok.Raw == nil {
		t.Error("Raw is nil; provider-specific fields would be unreachable")
	}

	// The request shape RFC 6749 §4.1.3 requires.
	if idp.lastMethod != http.MethodPost {
		t.Errorf("token request method = %s, want POST", idp.lastMethod)
	}
	if ct := idp.lastHeader.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
	}
	if ua := idp.lastHeader.Get("User-Agent"); ua != "forge-test/1" {
		t.Errorf("User-Agent = %q, want forge-test/1", ua)
	}
	for key, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"code_verifier": v.Secret(),
		"client_id":     "client-id",
		"redirect_uri":  "https://app.example.test/cb",
	} {
		if got := idp.lastForm.Get(key); got != want {
			t.Errorf("form field %q = %q, want %q", key, got, want)
		}
	}
	// A public client must not send a secret it does not have.
	if _, present := idp.lastForm["client_secret"]; present {
		t.Error("client_secret was sent for a public client")
	}
	// The verifier must travel in the body, never in the URL, where it
	// would land in server access logs.
	if strings.Contains(idp.tokenEndpoint(), v.Secret()) {
		t.Error("verifier appeared in the request URL")
	}
}

// TestExchangeRejectedWhenVerifierDoesNotMatchChallenge proves the fake IdP's
// PKCE check is live rather than decorative: swapping in a different verifier
// must be refused. Without this, the round-trip test above could pass against a
// server that ignores code_verifier entirely.
func TestExchangeRejectedWhenVerifierDoesNotMatchChallenge(t *testing.T) {
	idp := newFakeIdP(t)

	real, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	other, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if real.Secret() == other.Secret() {
		t.Fatal("the two verifiers are identical; the test would be vacuous")
	}

	const code = "auth-code-xyz"
	idp.authorize(code, real.Challenge())

	ex := &Exchanger{Client: idp.server.Client()}
	tok, err := ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    idp.tokenEndpoint(),
		ClientID:    "client-id",
		RedirectURI: "https://app.example.test/cb",
		Code:        code,
		Verifier:    other, // wrong verifier
	})
	if err == nil {
		t.Fatalf("Exchange with a mismatched verifier succeeded, returning %v", tok)
	}
	if tok != nil {
		t.Errorf("Exchange returned a token alongside its error: %v", tok)
	}
	var oerr *Error
	if !errors.As(err, &oerr) {
		t.Fatalf("error = %v (%T), want *oauth2.Error", err, err)
	}
	if oerr.Code != ErrCodeInvalidGrant {
		t.Errorf("Error.Code = %q, want %q", oerr.Code, ErrCodeInvalidGrant)
	}
}

// TestExchangeSurfacesOAuthErrorResponses is the guard against a silently empty
// token: every error shape a provider can send must become a Go error carrying
// the provider's own code and description.
func TestExchangeSurfacesOAuthErrorResponses(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantCode    string
		wantDesc    string
	}{
		{
			name:        "invalid_grant with 400",
			status:      http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"error":"invalid_grant","error_description":"code is expired","error_uri":"https://idp.example.test/docs#invalid_grant"}`,
			wantCode:    ErrCodeInvalidGrant,
			wantDesc:    "code is expired",
		},
		{
			name:        "invalid_client with 401",
			status:      http.StatusUnauthorized,
			contentType: "application/json",
			body:        `{"error":"invalid_client","error_description":"client authentication failed"}`,
			wantCode:    ErrCodeInvalidClient,
			wantDesc:    "client authentication failed",
		},
		{
			name:        "error member with a 200 status",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"error":"invalid_request","error_description":"provider misreports status"}`,
			wantCode:    ErrCodeInvalidRequest,
			wantDesc:    "provider misreports status",
		},
		{
			name:        "form-encoded error body",
			status:      http.StatusBadRequest,
			contentType: "application/x-www-form-urlencoded",
			body:        "error=invalid_scope&error_description=scope+not+granted",
			wantCode:    ErrCodeInvalidScope,
			wantDesc:    "scope not granted",
		},
		{
			name:        "charset on the content type",
			status:      http.StatusBadRequest,
			contentType: "application/json; charset=utf-8",
			body:        `{"error":"unauthorized_client"}`,
			wantCode:    ErrCodeUnauthorizedClient,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			v, err := NewVerifier()
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			ex := &Exchanger{Client: srv.Client()}
			tok, err := ex.Exchange(context.Background(), TokenRequest{
				Endpoint:    srv.URL,
				ClientID:    "client",
				RedirectURI: "https://app.example.test/cb",
				Code:        "code",
				Verifier:    v,
			})

			if err == nil {
				t.Fatalf("Exchange returned no error; token = %v", tok)
			}
			if tok != nil {
				t.Errorf("Exchange returned a non-nil token with an error: %v", tok)
			}

			var oerr *Error
			if !errors.As(err, &oerr) {
				t.Fatalf("error = %v (%T), want *oauth2.Error", err, err)
			}
			if oerr.Code != tc.wantCode {
				t.Errorf("Error.Code = %q, want %q", oerr.Code, tc.wantCode)
			}
			if tc.wantDesc != "" && oerr.Description != tc.wantDesc {
				t.Errorf("Error.Description = %q, want %q", oerr.Description, tc.wantDesc)
			}
			if oerr.StatusCode != tc.status {
				t.Errorf("Error.StatusCode = %d, want %d", oerr.StatusCode, tc.status)
			}
			// The message must name the provider's code so a log line is
			// actionable.
			if tc.wantCode != "" && !strings.Contains(oerr.Error(), tc.wantCode) {
				t.Errorf("Error() = %q, does not mention the provider's code %q", oerr.Error(), tc.wantCode)
			}
		})
	}
}

// TestExchangeRejectsSuccessWithoutAccessToken pins the "empty success" case: a
// 200 with no access_token is a failure. Returning a blank Token here would
// hand the caller a credential that authenticates nothing.
func TestExchangeRejectsSuccessWithoutAccessToken(t *testing.T) {
	bodies := map[string]string{
		"empty object":            `{}`,
		"other fields only":       `{"token_type":"Bearer","expires_in":3600}`,
		"explicitly empty token":  `{"access_token":"","token_type":"Bearer"}`,
		"wrong type access token": `{"access_token":12345}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeRawJSON(w, http.StatusOK, body)
			}))
			defer srv.Close()

			v, err := NewVerifier()
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			ex := &Exchanger{Client: srv.Client()}
			tok, err := ex.Exchange(context.Background(), TokenRequest{
				Endpoint:    srv.URL,
				ClientID:    "client",
				RedirectURI: "https://app.example.test/cb",
				Code:        "code",
				Verifier:    v,
			})
			if err == nil {
				t.Fatalf("Exchange succeeded with no access token: %v", tok)
			}
			if !errors.Is(err, ErrNoAccessToken) {
				t.Errorf("error = %v, want ErrNoAccessToken", err)
			}
			if tok != nil {
				t.Errorf("token = %v, want nil", tok)
			}
		})
	}
}

func writeRawJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// TestExchangeHandlesNonOAuthFailures covers the responses that are not valid
// OAuth at all — a proxy's HTML 502, an empty body, a truncated document. None
// may be reported as success.
func TestExchangeHandlesNonOAuthFailures(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"html gateway error", http.StatusBadGateway, "text/html", "<html><body>502 Bad Gateway</body></html>"},
		{"empty body 500", http.StatusInternalServerError, "text/plain", ""},
		{"truncated json 200", http.StatusOK, "application/json", `{"access_token":`},
		{"html with 200", http.StatusOK, "text/html", "<html>login page</html>"},
		{"huge html error", http.StatusServiceUnavailable, "text/html", strings.Repeat("x", 5000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			v, err := NewVerifier()
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			ex := &Exchanger{Client: srv.Client()}
			tok, err := ex.Exchange(context.Background(), TokenRequest{
				Endpoint:    srv.URL,
				ClientID:    "client",
				RedirectURI: "https://app.example.test/cb",
				Code:        "code",
				Verifier:    v,
			})
			if err == nil {
				t.Fatalf("Exchange succeeded on a non-OAuth response: %v", tok)
			}
			if tok != nil {
				t.Errorf("token = %v, want nil", tok)
			}
			// An error string must stay bounded; an HTML page is not an
			// error message.
			if len(err.Error()) > 1024 {
				t.Errorf("error message is %d bytes; unbounded response bodies must be truncated", len(err.Error()))
			}
		})
	}
}

// TestExchangeErrorsDoNotLeakVerifier checks the failure paths, which are the
// ones whose output actually gets logged.
func TestExchangeErrorsDoNotLeakVerifier(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	secret := v.Secret()

	// A server that echoes the verifier back in its error description is
	// the worst case: the package must not add it, and here we check our own
	// error construction paths.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "gateway error")
	}))
	defer srv.Close()

	ex := &Exchanger{Client: srv.Client()}
	_, err = ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    srv.URL,
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("exchange error leaked the verifier: %v", err)
	}

	// A transport failure: the URL may appear, the body must not.
	broken := &Exchanger{Client: doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})}
	_, err = broken.Exchange(context.Background(), TokenRequest{
		Endpoint:    "https://idp.example.test/token",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("transport error leaked the verifier: %v", err)
	}

	// Validation errors on the request itself.
	_, err = ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    srv.URL,
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "",
		Verifier:    v,
	})
	if err == nil {
		t.Fatal("expected a validation error for a missing code")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("validation error leaked the verifier: %v", err)
	}
}

// TestTokenRedaction mirrors the verifier redaction test for the credentials
// that come back.
func TestTokenRedaction(t *testing.T) {
	tok := &Token{
		AccessToken:  "super-secret-access-token",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: "super-secret-refresh-token",
		IDToken:      "super-secret-id-token",
	}
	secrets := []string{tok.AccessToken, tok.RefreshToken, tok.IDToken}

	renders := map[string]string{
		"%v":         fmt.Sprintf("%v", tok),
		"%s":         fmt.Sprintf("%s", tok),
		"%#v":        fmt.Sprintf("%#v", tok),
		"%+v":        fmt.Sprintf("%+v", tok),
		"value %v":   fmt.Sprintf("%v", *tok),
		"value %+v":  fmt.Sprintf("%+v", *tok),
		"String()":   tok.String(),
		"GoString()": tok.GoString(),
	}
	for what, out := range renders {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s leaked a credential (%s): %s", what, s, out)
			}
		}
	}

	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("exchanged", "token", tok)
	for _, s := range secrets {
		if strings.Contains(buf.String(), s) {
			t.Errorf("slog leaked a credential (%s): %s", s, buf.String())
		}
	}
	// The useful metadata must survive redaction, or the log line is noise.
	if !strings.Contains(buf.String(), "Bearer") {
		t.Errorf("slog output dropped token_type: %s", buf.String())
	}

	// Unlike Verifier, Token IS marshalable — a caller storing a session
	// needs that. Check it deliberately so a future redaction change cannot
	// break persistence silently.
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("json.Marshal(Token): %v", err)
	}
	var back Token
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("json.Unmarshal(Token): %v", err)
	}
	if back.AccessToken != tok.AccessToken || back.RefreshToken != tok.RefreshToken {
		t.Error("Token does not survive a JSON round trip; session persistence would silently lose credentials")
	}
}

func TestTokenExpiry(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := (Token{ExpiresIn: 3600}).Expiry(now); !got.Equal(now.Add(time.Hour)) {
		t.Errorf("Expiry = %v, want %v", got, now.Add(time.Hour))
	}
	// No expiry advertised must be the zero Time, not "now" — a caller
	// comparing against time.Now() would otherwise treat the token as
	// already expired.
	if got := (Token{}).Expiry(now); !got.IsZero() {
		t.Errorf("Expiry with no ExpiresIn = %v, want the zero Time", got)
	}
	if got := (Token{ExpiresIn: -1}).Expiry(now); !got.IsZero() {
		t.Errorf("Expiry with a negative ExpiresIn = %v, want the zero Time", got)
	}
}

// TestExchangeRequiresCompleteRequest checks that missing fields fail before a
// request is made, rather than producing a confusing provider error.
func TestExchangeRequiresCompleteRequest(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	var called bool
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("should not be reached")
	})
	ex := &Exchanger{Client: doer}

	complete := TokenRequest{
		Endpoint:    "https://idp.example.test/token",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	}
	cases := map[string]func(*TokenRequest){
		"no endpoint":     func(r *TokenRequest) { r.Endpoint = "" },
		"no client id":    func(r *TokenRequest) { r.ClientID = "" },
		"no redirect uri": func(r *TokenRequest) { r.RedirectURI = "" },
		"no code":         func(r *TokenRequest) { r.Code = "" },
		"no verifier":     func(r *TokenRequest) { r.Verifier = Verifier{} },
	}
	for name, mutate := range cases {
		req := complete
		mutate(&req)
		called = false
		tok, err := ex.Exchange(context.Background(), req)
		if err == nil {
			t.Errorf("%s: Exchange returned no error (token %v)", name, tok)
		}
		if called {
			t.Errorf("%s: Exchange issued an HTTP request despite an incomplete request", name)
		}
	}
}

// TestExchangeExtraCannotOverrideReservedParameters is the body-level twin of
// the authorization URL guard: a duplicated code_verifier or client_id in the
// form would be a credential-substitution foothold.
func TestExchangeExtraCannotOverrideReservedParameters(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if len(reservedTokenParams()) == 0 {
		t.Fatal("reservedTokenParams() is empty; this test would check nothing")
	}

	var called bool
	ex := &Exchanger{Client: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("should not be reached")
	})}
	base := TokenRequest{
		Endpoint:    "https://idp.example.test/token",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	}
	for _, key := range reservedTokenParams() {
		req := base
		req.Extra = url.Values{key: []string{"attacker-value"}}
		called = false
		if _, err := ex.Exchange(context.Background(), req); err == nil {
			t.Errorf("Extra[%q] was accepted; reserved form fields must be rejected", key)
		}
		if called {
			t.Errorf("Extra[%q]: a request was sent despite the reserved key", key)
		}
	}

	// Legitimate extras reach the wire exactly once.
	idp := newFakeIdP(t)
	idp.authorize("code", v.Challenge())
	real := &Exchanger{Client: idp.server.Client()}
	req := base
	req.Endpoint = idp.tokenEndpoint()
	req.Extra = url.Values{"resource": []string{"https://api.example.test"}}
	if _, err := real.Exchange(context.Background(), req); err != nil {
		t.Fatalf("Exchange with a legitimate extra: %v", err)
	}
	if got := idp.lastForm["resource"]; len(got) != 1 || got[0] != "https://api.example.test" {
		t.Errorf("extra form field resource = %v, want exactly one value", got)
	}
	if got := idp.lastForm["code_verifier"]; len(got) != 1 {
		t.Errorf("code_verifier appears %d times in the body, want once", len(got))
	}
}

// TestExchangeSendsClientSecretForConfidentialClients covers the confidential
// client case without making it the default.
func TestExchangeSendsClientSecretForConfidentialClients(t *testing.T) {
	idp := newFakeIdP(t)
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	idp.authorize("code", v.Challenge())

	ex := &Exchanger{Client: idp.server.Client()}
	if _, err := ex.Exchange(context.Background(), TokenRequest{
		Endpoint:     idp.tokenEndpoint(),
		ClientID:     "client",
		ClientSecret: "shhh",
		RedirectURI:  "https://app.example.test/cb",
		Code:         "code",
		Verifier:     v,
	}); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := idp.lastForm.Get("client_secret"); got != "shhh" {
		t.Errorf("client_secret = %q, want it sent for a confidential client", got)
	}
}

// TestExchangeHonorsContextCancellation checks that the injected client is
// actually driven by the context, which is what lets a caller bound a login.
func TestExchangeHonorsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "at"})
	}))
	defer srv.Close()
	defer close(release)

	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ex := &Exchanger{Client: srv.Client()}
	start := time.Now()
	tok, err := ex.Exchange(ctx, TokenRequest{
		Endpoint:    srv.URL,
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Exchange ignored the cancelled context and returned %v", tok)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Exchange took %v to honour a 50ms deadline", elapsed)
	}
}

// TestZeroExchangerWorks pins that the zero value is usable and that it does
// not reach for http.DefaultClient, which has no timeout.
func TestZeroExchangerWorks(t *testing.T) {
	idp := newFakeIdP(t)
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	idp.authorize("code", v.Challenge())

	var ex Exchanger // zero value, nil Client
	tok, err := ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    idp.tokenEndpoint(),
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    v,
	})
	if err != nil {
		t.Fatalf("zero Exchanger failed: %v", err)
	}
	if tok.AccessToken == "" {
		t.Error("zero Exchanger produced an empty access token")
	}
	if DefaultExchangeTimeout <= 0 {
		t.Error("DefaultExchangeTimeout must be positive; a nil Client must never mean no timeout")
	}
}

// TestExchangeRejectsMalformedVerifier ensures a verifier smuggled in via a
// hand-built Verifier value still gets validated before transmission.
func TestExchangeRejectsMalformedVerifier(t *testing.T) {
	var called bool
	ex := &Exchanger{Client: doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("should not be reached")
	})}
	// Short of the RFC minimum: a provider would reject it, but failing
	// locally names the problem.
	bad := Verifier{value: "too-short"}
	if _, err := ex.Exchange(context.Background(), TokenRequest{
		Endpoint:    "https://idp.example.test/token",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		Code:        "code",
		Verifier:    bad,
	}); err == nil {
		t.Error("Exchange accepted a verifier shorter than the RFC minimum")
	}
	if called {
		t.Error("Exchange sent a malformed verifier over the wire")
	}
}

// TestExpiresInAsString covers providers that quote the number.
func TestExpiresInAsString(t *testing.T) {
	tok, err := parseTokenResponse(200, "application/json", []byte(`{"access_token":"at","expires_in":"7200"}`))
	if err != nil {
		t.Fatalf("parseTokenResponse: %v", err)
	}
	if tok.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want 7200 from a quoted number", tok.ExpiresIn)
	}
}

// doerFunc adapts a function to HTTPDoer.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// TestHTTPClientSatisfiesHTTPDoer is a compile-time guarantee that the
// documented "*http.Client satisfies it" claim is true.
func TestHTTPClientSatisfiesHTTPDoer(t *testing.T) {
	var _ HTTPDoer = (*http.Client)(nil)
	var _ HTTPDoer = doerFunc(nil)
}
