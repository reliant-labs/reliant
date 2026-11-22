package oauth2

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// AuthRequest describes the authorization request a user agent is redirected
// to. It is a plain struct rather than a constructor with options so that a
// caller can see, in one place, every parameter that ends up in the URL.
type AuthRequest struct {
	// Endpoint is the provider's authorization endpoint, e.g.
	// https://idp.example.com/oidc/auth. Take it from
	// [ProviderMetadata.AuthorizationEndpoint] if you used [Discover].
	Endpoint string

	// ClientID identifies the client to the provider. Required.
	ClientID string

	// RedirectURI must match one registered with the provider. Required:
	// omitting it lets a provider fall back to a registered default, which
	// is how a callback ends up somewhere the caller did not intend.
	RedirectURI string

	// Scopes are joined with ScopeSeparator. For OIDC include "openid";
	// add "offline_access" to be issued a refresh token.
	Scopes []string

	// ScopeSeparator joins Scopes into the one scope parameter. Empty means
	// a space, which is RFC 6749 §3.3. Set "," only for a provider that
	// reads scope comma-delimited (Slack's OAuth v2 does); no other value
	// is accepted.
	ScopeSeparator string

	// State is the CSRF token from [NewState]. Required — a flow with no
	// state has no defence against a forged callback.
	State string

	// Challenge is the PKCE challenge from [Verifier.Challenge]. Required.
	Challenge Challenge

	// ResponseType defaults to "code". Set it only for a provider that
	// needs something else; this package can only complete "code".
	ResponseType string

	// Extra carries provider-specific parameters (prompt, audience,
	// resource, login_hint, nonce, ...). Keys that collide with a
	// parameter this package sets are rejected rather than silently
	// dropped or duplicated.
	Extra url.Values
}

// reservedAuthParams returns the query parameters AuthRequest.URL sets itself.
// Extra may not contain them: url.Values allows repeats, and a duplicated
// code_challenge or redirect_uri is a parameter-injection foothold rather
// than a convenience.
//
// This is a function rather than a package-level slice so there is no global
// mutable state for a caller (or a test) to scribble on.
func reservedAuthParams() []string {
	return []string{
		"response_type",
		"client_id",
		"redirect_uri",
		"scope",
		"state",
		"code_challenge",
		"code_challenge_method",
	}
}

// URL renders the authorization request as an absolute URL, with every
// parameter percent-encoded by [net/url].
//
// It fails rather than emitting a half-built URL: a request missing a
// challenge, a state, or a redirect URI is a flow with a security property
// silently switched off, and the provider is not obliged to complain.
func (r AuthRequest) URL() (string, error) {
	var missing []string
	if r.Endpoint == "" {
		missing = append(missing, "Endpoint")
	}
	if r.ClientID == "" {
		missing = append(missing, "ClientID")
	}
	if r.RedirectURI == "" {
		missing = append(missing, "RedirectURI")
	}
	if r.State == "" {
		missing = append(missing, "State")
	}
	if r.Challenge.Value == "" {
		missing = append(missing, "Challenge")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("oauth2: AuthRequest is missing required field(s): %s", strings.Join(missing, ", "))
	}
	if r.Challenge.Method == "" {
		return "", errors.New("oauth2: AuthRequest.Challenge has no Method; build it with Verifier.Challenge or Verifier.ChallengeWithMethod")
	}
	if r.Challenge.Method != MethodS256 && r.Challenge.Method != MethodPlain {
		return "", fmt.Errorf("oauth2: unknown code challenge method %q", string(r.Challenge.Method))
	}

	u, err := url.Parse(r.Endpoint)
	if err != nil {
		return "", fmt.Errorf("oauth2: parse authorization endpoint: %w", err)
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("oauth2: authorization endpoint %q is not absolute", r.Endpoint)
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return "", fmt.Errorf("oauth2: authorization endpoint %q must use https (loopback excepted for local development)", r.Endpoint)
	}

	responseType := r.ResponseType
	if responseType == "" {
		responseType = "code"
	}

	// Start from any query already on the endpoint so a provider that
	// publishes one (some multi-tenant issuers do) keeps it.
	q := u.Query()
	reserved := reservedAuthParams()
	for key := range r.Extra {
		for _, reserved := range reserved {
			if key == reserved {
				return "", fmt.Errorf("oauth2: AuthRequest.Extra may not set %q; use the corresponding AuthRequest field", key)
			}
		}
		for _, v := range r.Extra[key] {
			q.Add(key, v)
		}
	}
	q.Set("response_type", responseType)
	q.Set("client_id", r.ClientID)
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("state", r.State)
	q.Set("code_challenge", r.Challenge.Value)
	q.Set("code_challenge_method", string(r.Challenge.Method))
	if len(r.Scopes) > 0 {
		scope, err := joinScopes("AuthRequest", r.Scopes, r.ScopeSeparator)
		if err != nil {
			return "", err
		}
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// joinScopes renders scopes as the one scope parameter value.
//
// Only a space (RFC 6749 §3.3, the default) or a comma is a separator: those
// are the two providers actually read. Anything else is refused rather than
// encoded, because a caller that reached for it is misconfigured. A scope that
// contains the separator is refused too, since the provider would read it as
// two scopes and grant something other than what was listed.
func joinScopes(owner string, scopes []string, sep string) (string, error) {
	if sep == "" {
		sep = " "
	}
	if sep != " " && sep != "," {
		return "", fmt.Errorf("oauth2: %s.ScopeSeparator %q is not supported; use \"\" (space, RFC 6749) or \",\"", owner, sep)
	}
	for _, scope := range scopes {
		if strings.Contains(scope, sep) {
			return "", fmt.Errorf("oauth2: %s scope %q contains the scope separator %q and would be read as more than one scope", owner, scope, sep)
		}
	}
	return strings.Join(scopes, sep), nil
}
