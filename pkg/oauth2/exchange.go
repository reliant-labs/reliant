package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// maxTokenResponseBytes caps how much of a token response is read. A token
// response is a small JSON object; anything larger is a misconfigured endpoint
// or a hostile one, and reading it unbounded would be a memory sink.
const maxTokenResponseBytes = 1 << 20

// TokenRequest describes an authorization-code redemption (RFC 6749 §4.1.3
// plus the code_verifier of RFC 7636 §4.5).
type TokenRequest struct {
	// Endpoint is the provider's token endpoint. Required.
	Endpoint string

	// ClientID must be the same client the code was issued to. Required.
	ClientID string

	// ClientSecret authenticates a confidential client. Leave it empty for
	// a public client (browser SPA, native app, CLI) — that is what PKCE
	// is for. When set it is sent in the request body as client_secret.
	ClientSecret string

	// RedirectURI must byte-for-byte match the one in the authorization
	// request. Required: providers verify it, and a mismatch is a common
	// cause of an otherwise unexplained invalid_grant.
	RedirectURI string

	// Code is the authorization code from the callback. Required.
	Code string

	// Verifier is the code verifier whose challenge was sent with the
	// authorization request. Required.
	Verifier Verifier

	// Extra carries provider-specific body parameters. Keys that collide
	// with a parameter this package sets are rejected.
	Extra url.Values
}

// reservedTokenParams returns the form fields Exchange sets itself. It is a
// function rather than a package-level slice so there is no global mutable
// state; see [reservedAuthParams].
func reservedTokenParams() []string {
	return []string{
		"grant_type",
		"code",
		"code_verifier",
		"client_id",
		"client_secret",
		"redirect_uri",
	}
}

// Token is a successful token endpoint response (RFC 6749 §5.1).
//
// Like [Verifier], Token redacts itself under fmt and log/slog so that logging
// a whole response does not spill the credentials. Unlike Verifier it *is*
// JSON-marshalable, because a caller storing a session legitimately needs to
// serialize it; the fields are exported and carry their wire names.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`

	// Raw holds every field of the response, including provider-specific
	// ones this struct does not name.
	Raw map[string]any `json:"-"`
}

// String implements [fmt.Stringer] and reports the shape of the token without
// its contents, which is what is actually useful in a log line.
func (t Token) String() string {
	kind := t.TokenType
	if kind == "" {
		kind = "unknown"
	}
	return fmt.Sprintf("oauth2.Token{type:%s access:%s refresh:%t id:%t expires_in:%d}",
		kind, redacted, t.RefreshToken != "", t.IDToken != "", t.ExpiresIn)
}

// GoString implements [fmt.GoStringer] so %#v does not print the secrets.
func (t Token) GoString() string { return t.String() }

// LogValue implements [slog.LogValuer].
func (t Token) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("token_type", t.TokenType),
		slog.String("access_token", redacted),
		slog.Bool("has_refresh_token", t.RefreshToken != ""),
		slog.Bool("has_id_token", t.IDToken != ""),
		slog.Int64("expires_in", t.ExpiresIn),
	)
}

// Expiry converts ExpiresIn to an absolute instant relative to now, or returns
// the zero Time when the provider sent no expiry.
func (t Token) Expiry(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// Error is an OAuth 2.0 error response (RFC 6749 §5.2) rendered as a Go error.
//
// The provider's machine-readable code is preserved in Code so callers can
// branch on it — invalid_grant means the code is spent or expired and the user
// should start over, while invalid_client is a deployment problem.
type Error struct {
	// Code is the OAuth error identifier, e.g. "invalid_grant".
	Code string
	// Description is the provider's human-readable explanation, if any.
	Description string
	// URI optionally points at provider documentation.
	URI string
	// StatusCode is the HTTP status the error arrived with.
	StatusCode int
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("oauth2: token endpoint returned error")
	if e.Code != "" {
		fmt.Fprintf(&b, " %q", e.Code)
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.StatusCode)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, ": %s", e.Description)
	}
	if e.URI != "" {
		fmt.Fprintf(&b, " [%s]", e.URI)
	}
	return b.String()
}

// Standard OAuth 2.0 error codes (RFC 6749 §5.2), for comparison against
// [Error.Code]. Providers may return codes outside this set.
const (
	ErrCodeInvalidRequest       = "invalid_request"
	ErrCodeInvalidClient        = "invalid_client"
	ErrCodeInvalidGrant         = "invalid_grant"
	ErrCodeUnauthorizedClient   = "unauthorized_client"
	ErrCodeUnsupportedGrantType = "unsupported_grant_type"
	ErrCodeInvalidScope         = "invalid_scope"
)

// ErrNoAccessToken reports a token endpoint that answered 200 with no
// access_token. Such a response is a failure, not an empty success: returning
// a blank Token would hand the caller a credential-shaped value that
// authenticates nothing, and the failure would surface later as an
// unexplained 401.
var ErrNoAccessToken = errors.New("oauth2: token response contained no access_token")

// Exchanger redeems authorization codes at a token endpoint.
//
// The zero Exchanger is usable and exchanges over a client with a 30 second
// timeout. Set Client to control timeouts, transport, or proxying, or to point
// tests at an [net/http/httptest] server.
type Exchanger struct {
	// Client performs the request. When nil, a client with
	// DefaultExchangeTimeout is used — never [http.DefaultClient], which
	// has no timeout at all and would let a wedged provider hang a login
	// forever.
	Client HTTPDoer

	// UserAgent is sent as the User-Agent header when non-empty.
	UserAgent string
}

// DefaultExchangeTimeout bounds a token exchange made by an [Exchanger] with
// no Client of its own.
const DefaultExchangeTimeout = 30 * time.Second

// Exchange redeems req.Code for a token, proving possession of the PKCE
// verifier.
//
// A provider error response becomes an [*Error] (use [errors.As]); a 200 with
// no access token becomes [ErrNoAccessToken]. Neither is reported as success.
func (e *Exchanger) Exchange(ctx context.Context, req TokenRequest) (*Token, error) {
	var missing []string
	if req.Endpoint == "" {
		missing = append(missing, "Endpoint")
	}
	if req.ClientID == "" {
		missing = append(missing, "ClientID")
	}
	if req.RedirectURI == "" {
		missing = append(missing, "RedirectURI")
	}
	if req.Code == "" {
		missing = append(missing, "Code")
	}
	if req.Verifier.Secret() == "" {
		missing = append(missing, "Verifier")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("oauth2: TokenRequest is missing required field(s): %s", strings.Join(missing, ", "))
	}
	if err := validateVerifier(req.Verifier.Secret()); err != nil {
		return nil, err
	}

	form := url.Values{}
	reserved := reservedTokenParams()
	for key := range req.Extra {
		for _, reserved := range reserved {
			if key == reserved {
				return nil, fmt.Errorf("oauth2: TokenRequest.Extra may not set %q; use the corresponding TokenRequest field", key)
			}
		}
		for _, v := range req.Extra[key] {
			form.Add(key, v)
		}
	}
	form.Set("grant_type", "authorization_code")
	form.Set("code", req.Code)
	form.Set("code_verifier", req.Verifier.Secret())
	form.Set("client_id", req.ClientID)
	form.Set("redirect_uri", req.RedirectURI)
	if req.ClientSecret != "" {
		form.Set("client_secret", req.ClientSecret)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth2: build token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	if e.UserAgent != "" {
		httpReq.Header.Set("User-Agent", e.UserAgent)
	}

	doer := e.Client
	if doer == nil {
		doer = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: DefaultExchangeTimeout})
	}
	resp, err := doer.Do(httpReq)
	if err != nil {
		// The URL is safe to include; the body held the verifier and is
		// not part of err.
		return nil, fmt.Errorf("oauth2: post to token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("oauth2: read token response: %w", err)
	}

	return parseTokenResponse(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

// parseTokenResponse turns a token endpoint reply into a Token or an error.
//
// It is split out from Exchange so the response-handling rules — which is
// where the security-relevant decisions live — are testable without an HTTP
// round trip.
func parseTokenResponse(status int, contentType string, body []byte) (*Token, error) {
	// Some providers answer errors with a form-encoded body, and RFC 6749
	// §5.2 mandates 400 for most error codes, so decide by shape rather
	// than trusting either the status or the Content-Type alone.
	mediaType := contentType
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		mediaType = mt
	}

	var raw map[string]any
	jsonErr := json.Unmarshal(body, &raw)
	if jsonErr != nil && mediaType == "application/x-www-form-urlencoded" {
		if vals, err := url.ParseQuery(string(body)); err == nil {
			raw = make(map[string]any, len(vals))
			for k := range vals {
				raw[k] = vals.Get(k)
			}
			jsonErr = nil
		}
	}

	if jsonErr != nil {
		if status < 200 || status > 299 {
			return nil, &Error{
				Code:        "",
				Description: summarizeBody(body),
				StatusCode:  status,
			}
		}
		return nil, fmt.Errorf("oauth2: decode token response (HTTP %d, Content-Type %q): %w", status, contentType, jsonErr)
	}

	// An "error" member is authoritative regardless of status: a provider
	// that reports invalid_grant with a 200 is still refusing.
	if code, _ := raw["error"].(string); code != "" {
		desc, _ := raw["error_description"].(string)
		uri, _ := raw["error_uri"].(string)
		return nil, &Error{Code: code, Description: desc, URI: uri, StatusCode: status}
	}

	if status < 200 || status > 299 {
		return nil, &Error{
			Description: summarizeBody(body),
			StatusCode:  status,
		}
	}

	tok := &Token{Raw: raw}
	tok.AccessToken, _ = raw["access_token"].(string)
	tok.TokenType, _ = raw["token_type"].(string)
	tok.RefreshToken, _ = raw["refresh_token"].(string)
	tok.IDToken, _ = raw["id_token"].(string)
	tok.Scope, _ = raw["scope"].(string)
	switch v := raw["expires_in"].(type) {
	case float64: // encoding/json's number
		tok.ExpiresIn = int64(v)
	case string: // a few providers send it quoted
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			tok.ExpiresIn = n
		}
	}

	if tok.AccessToken == "" {
		return nil, ErrNoAccessToken
	}
	return tok, nil
}

// summarizeBody renders an unparseable error body for a message, truncated so
// that an HTML error page does not become the error string.
func summarizeBody(body []byte) string {
	const max = 256
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "empty response body"
	}
	if len(s) > max {
		return s[:max] + "…(truncated)"
	}
	return s
}
