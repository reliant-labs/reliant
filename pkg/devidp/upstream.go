package devidp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// Upstream tokens: the social provider's OWN tokens from a sign-in.
//
// The issuer does the provider's code exchange (social.go), so it holds the
// tokens the provider issued, and Zitadel's retrieve-intent response carries
// them under idpInformation.oauth. An app that calls the provider's API as
// the user (with the scopes its declaration asks for) receives them through
// SignInRoutesConfig.UpstreamTokens, server-side, for each provider whose
// login policy entry has PassTokens. They never reach the browser: they are
// in no response body, cookie or redirect, they redact themselves in logs,
// and they refuse to marshal.

// redacted is what UpstreamTokens print as.
const redacted = "[redacted]"

// ErrUpstreamTokensNotSerializable is MarshalJSON's answer, always.
var ErrUpstreamTokensNotSerializable = errors.New("devidp: upstream tokens are never serialised; store them sealed, field by field")

// UpstreamTokens are the provider's own tokens from a social sign-in.
//
// They print as "[redacted]" (String, GoString, LogValue) and MarshalJSON
// refuses them, so a log line or a response that carries one by mistake
// leaks nothing. Read the fields themselves to store them, sealed.
type UpstreamTokens struct {
	// AccessToken is the provider's access token. "" when the issuer
	// reported none.
	AccessToken string
	// RefreshToken is "" when the provider issued none: a GitHub OAuth app's
	// token does not expire and has none; a GitHub App's user token does.
	// The issuer does not ask Google for offline access, so Google gives
	// none.
	RefreshToken string
	// IDToken is the provider's ID token, for an OIDC provider (Google).
	IDToken string
	// ExpiresAt is when AccessToken expires. Zero is unknown or
	// non-expiring: the issuer does not report the provider's expiry.
	ExpiresAt time.Time
	// Scopes are the scopes the provider was declared with (the policy
	// entry's Scopes). The issuer does not report the scopes the user
	// actually granted.
	Scopes []string
}

// String redacts the tokens.
func (UpstreamTokens) String() string { return redacted }

// GoString redacts the tokens, for %#v.
func (UpstreamTokens) GoString() string { return redacted }

// LogValue redacts the tokens in slog output.
func (UpstreamTokens) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON refuses: upstream tokens are never serialised whole.
func (UpstreamTokens) MarshalJSON() ([]byte, error) {
	return nil, ErrUpstreamTokensNotSerializable
}

// Events of an UpstreamGrant: what the sign-in that produced it was.
const (
	// UpstreamEventSignIn is a sign-in to an existing account: one already
	// linked to the provider identity, or one auto-linked to it.
	UpstreamEventSignIn = "sign_in"
	// UpstreamEventSignUp is a sign-in to an account created just now.
	UpstreamEventSignUp = "sign_up"
	// UpstreamEventInviteAccept is an invitation accepted by connecting the
	// provider.
	UpstreamEventInviteAccept = "invite_accept"
)

// UpstreamGrant is what SignInRoutesConfig.UpstreamTokens receives after a
// social sign-in SUCCEEDED.
type UpstreamGrant struct {
	// Subject is the issuer user id the session was created for, never the
	// provider's user id. Key the stored credential by (Subject, Provider).
	Subject string
	// Provider is the policy entry's type ("github", "google").
	Provider string
	// IdPID is the issuer's id of the identity provider.
	IdPID string
	// ProviderUserID and ProviderUserName are the user at the provider.
	ProviderUserID   string
	ProviderUserName string
	// Event is UpstreamEventSignIn, UpstreamEventSignUp or
	// UpstreamEventInviteAccept.
	Event  string
	Tokens UpstreamTokens
}

// LogValue keeps the tokens out of slog output even when the whole grant is
// logged.
func (g UpstreamGrant) LogValue() slog.Value {
	return slog.GroupValue(slog.String("subject", g.Subject), slog.String("provider", g.Provider),
		slog.String("event", g.Event), slog.String("tokens", redacted))
}

// handUpstreamTokens hands a successful social sign-in's upstream tokens to
// the app's hook, when the provider passes them and the app takes them. It
// reports false when the sign-in must be refused: RequireUpstreamTokens, and
// the hook failed or no access token came back.
func (r *SignInRoutes) handUpstreamTokens(ctx context.Context, social LoginPolicySocial, res IDPResult, subject, event string) bool {
	if !social.PassTokens || r.cfg.UpstreamTokens == nil {
		return true
	}
	strict := r.cfg.RequireUpstreamTokens
	if res.Upstream == nil || res.Upstream.AccessToken == "" {
		r.cfg.Logger.WarnContext(ctx, "social sign-in: the issuer reported no provider access token",
			"user_id", subject, "provider", social.Type, "required", strict)
		return !strict
	}
	tokens := *res.Upstream
	tokens.Scopes = strings.Fields(social.Scopes)
	err := r.cfg.UpstreamTokens(ctx, UpstreamGrant{
		Subject: subject, Provider: social.Type, IdPID: res.Link.IdPID,
		ProviderUserID: res.Link.UserID, ProviderUserName: res.Link.UserName,
		Event: event, Tokens: tokens,
	})
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "social sign-in: the app did not take the provider's tokens",
			"user_id", subject, "provider", social.Type, "required", strict, "error", err)
		return !strict
	}
	return true
}
