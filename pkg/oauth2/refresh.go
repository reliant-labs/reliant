package oauth2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// RefreshRequest describes a refresh-token redemption (RFC 6749 §6).
//
// There is no Verifier field. PKCE binds the AUTHORIZATION CODE to the client
// instance that started the flow (RFC 7636 §4.5); the refresh grant presents no
// code, so there is no challenge to satisfy. The refresh token is itself the
// credential, which is why [RefreshRequest.RefreshToken] is a [RefreshToken]
// rather than a bare string — see that type for what it refuses to disclose.
type RefreshRequest struct {
	// Endpoint is the provider's token endpoint. Required.
	Endpoint string

	// ClientID must be the client the refresh token was issued to.
	// Required: RFC 6749 §6 requires it for a public client, and a
	// provider that cannot identify the client cannot check the binding.
	ClientID string

	// ClientSecret authenticates a confidential client. Leave it empty for
	// a public client (browser SPA, native app, CLI). When set it is sent
	// in the request body as client_secret.
	ClientSecret string

	// RefreshToken is the credential being redeemed. Required.
	RefreshToken RefreshToken

	// Scopes optionally narrows the granted scope. RFC 6749 §6 permits a
	// scope no broader than the original grant, and providers reject a
	// widening. Leave it nil to keep the original scope, which is what
	// almost every caller wants — omitting the parameter is not the same
	// as sending an empty one, and this package omits it when nil.
	Scopes []string

	// ScopeSeparator joins Scopes, exactly as [AuthRequest.ScopeSeparator]:
	// empty means a space (RFC 6749 §3.3), "," for a comma-delimited
	// provider, and nothing else.
	ScopeSeparator string

	// Extra carries provider-specific body parameters. Keys that collide
	// with a parameter this package sets are rejected.
	Extra url.Values
}

// reservedRefreshParams returns the form fields Refresh sets itself. It is a
// function rather than a package-level slice so there is no global mutable
// state; see [reservedAuthParams].
func reservedRefreshParams() []string {
	return []string{
		"grant_type",
		"refresh_token",
		"client_id",
		"client_secret",
		"scope",
	}
}

// RefreshToken is a refresh token: a long-lived credential that mints access
// tokens without the user present.
//
// It is a distinct type from a plain string, and holds its value in an
// unexported field, for the same reason [Verifier] does — it is strictly more
// dangerous than the access token it produces, because it outlives every token
// it mints. It redacts itself under fmt and log/slog.
//
// Unlike Verifier, it does NOT fail to marshal: a caller legitimately needs to
// carry a refresh token in a session so that a refresh can happen later, and
// Verifier's "fail rather than emit a placeholder" rule exists precisely
// because a verifier is single-use within one login. See [RefreshToken.Secret]
// for taking the value out deliberately.
type RefreshToken struct {
	value string
}

// NewRefreshToken wraps a refresh token value received from a provider.
//
// It rejects the empty string. A zero RefreshToken is not a token, and letting
// one through would produce a refresh request whose credential is absent — a
// call the provider answers with invalid_grant, far from the missing
// assignment that caused it.
func NewRefreshToken(value string) (RefreshToken, error) {
	if value == "" {
		return RefreshToken{}, ErrNoRefreshToken
	}
	return RefreshToken{value: value}, nil
}

// Secret returns the refresh token in the clear. Every call site is a place
// the credential can escape: send it to the token endpoint, or hand it to the
// session store that will present it later. Do not log it.
func (t RefreshToken) Secret() string { return t.value }

// IsZero reports whether t holds no value, which is how a caller distinguishes
// "this session has no refresh token" from "this session has one" without
// reaching for [RefreshToken.Secret] and comparing against "".
func (t RefreshToken) IsZero() bool { return t.value == "" }

// String implements [fmt.Stringer] and returns a redacted placeholder.
func (t RefreshToken) String() string { return redacted }

// GoString implements [fmt.GoStringer] so that %#v does not reflect through
// the unexported field.
func (t RefreshToken) GoString() string { return "oauth2.RefreshToken{" + redacted + "}" }

// LogValue implements [slog.LogValuer] and returns a redacted placeholder.
func (t RefreshToken) LogValue() slog.Value { return slog.StringValue(redacted) }

// ErrNoRefreshToken reports a refresh token that is absent where one is
// required — either constructed from the empty string, or a [RefreshRequest]
// carrying the zero [RefreshToken].
var ErrNoRefreshToken = errors.New("oauth2: refresh token is empty")

// Refresh redeems req.RefreshToken for a new access token (RFC 6749 §6).
//
// # Rotation
//
// A provider MAY return a new refresh token, and RFC 6749 §6 says the client
// MUST then discard the old one. Many providers rotate on EVERY refresh and
// treat a re-presented old token as theft: they revoke the entire grant, which
// signs the user out completely rather than merely failing one call.
//
// Verified against Zitadel v4.19.4 (the IdP forge scaffolds for local dev): it
// rotates on every refresh and rejects a replayed token IMMEDIATELY, with no
// grace window — though it revokes only that token, leaving the newest one
// usable. Providers built on node oidc-provider are harsher and kill the whole
// grant. Write the client for the harsh case; it is correct under both.
//
// This function therefore reports what to present NEXT rather than leaving the
// caller to infer it. [Refreshed.RefreshToken] is ALWAYS the token to use for
// the following refresh: the rotated one when the provider sent one, otherwise
// the one just presented. A caller that stores it unconditionally is correct
// under both provider behaviours, and there is no branch to get wrong.
//
// A provider error response becomes an [*Error] (use [errors.As]); a 200 with
// no access token becomes [ErrNoAccessToken]. Neither is reported as success.
func (e *Exchanger) Refresh(ctx context.Context, req RefreshRequest) (*Refreshed, error) {
	var missing []string
	if req.Endpoint == "" {
		missing = append(missing, "Endpoint")
	}
	if req.ClientID == "" {
		missing = append(missing, "ClientID")
	}
	if req.RefreshToken.IsZero() {
		missing = append(missing, "RefreshToken")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("oauth2: RefreshRequest is missing required field(s): %s", strings.Join(missing, ", "))
	}

	form := url.Values{}
	reserved := reservedRefreshParams()
	for key := range req.Extra {
		for _, reserved := range reserved {
			if key == reserved {
				return nil, fmt.Errorf("oauth2: RefreshRequest.Extra may not set %q; use the corresponding RefreshRequest field", key)
			}
		}
		for _, v := range req.Extra[key] {
			form.Add(key, v)
		}
	}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", req.RefreshToken.Secret())
	form.Set("client_id", req.ClientID)
	if req.ClientSecret != "" {
		form.Set("client_secret", req.ClientSecret)
	}
	// Omitted entirely when nil: RFC 6749 §6 makes scope optional, and an
	// empty scope= is a request for no scopes at all rather than "keep what
	// I had", which some providers honour literally.
	if len(req.Scopes) > 0 {
		scope, err := joinScopes("RefreshRequest", req.Scopes, req.ScopeSeparator)
		if err != nil {
			return nil, err
		}
		form.Set("scope", scope)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth2: build refresh request: %w", err)
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
		// The URL is safe to include; the body held the refresh token and
		// is not part of err.
		return nil, fmt.Errorf("oauth2: post to token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("oauth2: read refresh response: %w", err)
	}

	tok, err := parseTokenResponse(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, err
	}

	// The rotation decision, made HERE so no caller has to make it. When the
	// provider rotated, the new token is what must be presented next; when it
	// did not, the presented one remains current.
	next := req.RefreshToken
	rotated := false
	if tok.RefreshToken != "" && tok.RefreshToken != req.RefreshToken.Secret() {
		next = RefreshToken{value: tok.RefreshToken}
		rotated = true
	}
	return &Refreshed{Token: tok, RefreshToken: next, Rotated: rotated}, nil
}

// Refreshed is the result of a successful [Exchanger.Refresh].
//
// It exists rather than returning a bare [*Token] so that the rotation rule is
// carried in the type: a caller cannot read the new access token without also
// being handed the refresh token to store, which is the step whose omission
// breaks the SECOND refresh against a rotating provider.
type Refreshed struct {
	// Token is the provider's response. Token.RefreshToken holds the raw
	// rotated value when there was one, and is empty when the provider sent
	// none — prefer the RefreshToken field below, which is never empty.
	Token *Token

	// RefreshToken is what to present on the NEXT refresh. It is always
	// populated: the rotated token when the provider issued one, otherwise
	// the token that was just presented. Store it unconditionally.
	RefreshToken RefreshToken

	// Rotated reports whether the provider issued a NEW refresh token and
	// therefore invalidated (or will invalidate) the previous one. It is
	// informational — RefreshToken is already correct either way — and is
	// exposed for callers that log or meter rotation.
	Rotated bool
}
