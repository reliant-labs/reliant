// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/apikey"
	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/session"

	"github.com/reliant-labs/reliant/internal/auth/authmetrics"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// ErrSessionUnavailable is a server-side session that could not be read: its
// store or the issuer is unreachable. Verification never happened, so it is
// not a credential rejection, and the caller is told to retry rather than to
// sign in again.
var ErrSessionUnavailable = errors.New("the session could not be checked; try again")

// Authenticator checks the credentials of the user audience that are not
// `rlat_` access tokens — those the token authority resolves, before this is
// asked. It is forge's Authenticator (forge/pkg/auth) over the TokenChain: a
// server-side session id (forge/pkg/session, once Settings.Sessions is set)
// is resolved to its access token, which the chain then verifies like any
// other. The Connect interceptor and the HTTP routes share one.
type Authenticator struct {
	chain *TokenChain
	forge *forgeauth.Authenticator
	// sessions: a session store is installed (Settings.Sessions), so the
	// sign-in cookie can hold a session.
	sessions bool
	// devCookie: the session cookie's plain name counts too
	// (Settings.DevSessionCookie).
	devCookie bool
}

// NewAuthenticator builds the authenticator s describes (NewTokenChain).
func NewAuthenticator(ctx context.Context, s Settings) (*Authenticator, error) {
	chain, err := NewTokenChain(ctx, s)
	if err != nil {
		return nil, err
	}
	fa := forgeauth.NewAuthenticator(chain.ValidateToken)
	if s.Sessions != nil {
		fa.UseSessions(s.Sessions)
	}
	if issuer := chain.IdentityIssuer(); issuer != "" {
		// A managed identity's access token carries no email; forge reads it
		// from the issuer's userinfo endpoint, cached per subject.
		fa.UseUserInfo(forgeauth.NewUserInfo(issuer, func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, identityDiscoveryTimeout)
			defer cancel()
			meta, err := oauth2.Discover(ctx, nil, issuer)
			if err != nil {
				return "", err
			}
			return meta.UserinfoEndpoint, nil
		}, nil))
	}
	return &Authenticator{chain: chain, forge: fa, sessions: s.Sessions != nil, devCookie: s.DevSessionCookie}, nil
}

// Verified is a credential the user audience accepted: who it proves, how
// it was presented (forge's authn.Method), which validator proved it, and the
// Credential those two make it.
type Verified struct {
	Claims *JWTClaims
	Source Source
	// Method is how the caller authenticated, in forge's terms: the channel
	// the credential arrived on and the kind forge's authenticator proved it
	// as (a token, a server-side session, a key).
	Method authn.Method
	// Principal is the access token's principal; nil for anything else.
	Principal *fat.Principal
}

// CredentialFrom is the credential the request with header h presents, and
// the channel it arrived on (CredentialFromHeader, with the plain-named
// cookie only in local development).
func (a *Authenticator) CredentialFrom(h http.Header) (string, authn.Channel) {
	return CredentialFromHeader(h, a.devCookie)
}

// CheckChannel refuses a credential its channel may not carry: the sign-in
// cookie holds a server-side session id and nothing else. A token in it — a
// JWT, an `rlat_` access token — is a program replaying a bearer as a
// cookie: refused, never checked as a bearer. Every caller runs it before it
// dispatches on the credential's shape.
func (a *Authenticator) CheckChannel(credential string, channel authn.Channel) error {
	if channel == authn.ChannelCookie && (!a.sessions || !session.IsID(credential)) {
		return fmt.Errorf("%w: the sign-in cookie holds no session", ErrInvalidToken)
	}
	return nil
}

// Verify checks credential, which arrived on channel, and says what it
// proves. Its errors are credential rejections (IsCredentialRejection),
// except ErrSessionUnavailable and the errors of an issuer that could not be
// reached.
func (a *Authenticator) Verify(ctx context.Context, credential string, channel authn.Channel) (*Verified, error) {
	if IsAccessTokenFormat(credential) {
		// Never a token of the chain: the token authority resolves it, or no
		// one does.
		return nil, fmt.Errorf("%w: an access token is not accepted here", ErrInvalidToken)
	}
	if err := a.CheckChannel(credential, channel); err != nil {
		return nil, err
	}
	var claims *forgeauth.Claims
	var err error
	if strings.HasPrefix(credential, apikey.KeyPrefix) {
		// forge routes "fk_" to an API-key validator reliant does not
		// install; a self-host shared key may start that way, so it goes to
		// the chain as every other token did before.
		claims, err = a.chain.ValidateToken(credential)
	} else {
		claims, err = a.forge.Authenticate(ctx, credential)
	}
	switch {
	case errors.Is(err, forgeauth.ErrSessionUnavailable):
		return nil, fmt.Errorf("%w: %v", ErrSessionUnavailable, err)
	case errors.Is(err, forgeauth.ErrInvalidSession):
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	case err != nil:
		return nil, err
	}
	v, ok := verdictOf(claims)
	if !ok {
		return nil, fmt.Errorf("%w: claims the token chain did not verify", ErrInvalidToken)
	}
	method := authn.Method{Channel: channel, Credential: claims.Credential.Kind()}
	cred, err := credentialFor(v, method)
	if err != nil {
		return nil, err
	}
	jc := v.legacy
	if v.source == SourceIdentity {
		jc = identityClaims(claims)
	}
	out := *jc
	out.Credential = cred
	return &Verified{Claims: &out, Source: v.source, Method: method}, nil
}

// TokenValidator is a, as a TokenValidator, for a route that checks a token
// with no request context of its own (the terminal websocket): what it
// verifies is what Verify does, presented on no known channel.
func (a *Authenticator) TokenValidator() TokenValidator { return tokenValidator{a} }

type tokenValidator struct{ a *Authenticator }

func (t tokenValidator) ValidateToken(token string) (*JWTClaims, error) {
	v, err := t.a.Verify(context.Background(), token, authn.ChannelUnknown)
	if err != nil {
		return nil, err
	}
	return v.Claims, nil
}

// VerifyIdentityToken checks token as a token of the managed identity, and
// only of it: alg, iss, aud, a present and unexpired exp, and signature, by the identity's validator
// (TokenChain). A Supabase token, the shared key, a session id or an
// unconfigured identity is refused (ErrInvalidToken). The session resolver
// verifies what a server-side session holds before handing it out.
func (a *Authenticator) VerifyIdentityToken(token string) (*JWTClaims, error) {
	if a.chain.IdentityIssuer() == "" || unverifiedIssuer(token) != a.chain.IdentityIssuer() {
		return nil, fmt.Errorf("%w: not a token of the managed identity", ErrInvalidToken)
	}
	claims, err := a.chain.ValidateToken(token)
	if err != nil {
		return nil, err
	}
	v, ok := verdictOf(claims)
	if !ok || v.source != SourceIdentity {
		return nil, fmt.Errorf("%w: not a token of the managed identity", ErrInvalidToken)
	}
	jc := identityClaims(claims)
	if jc.Exp <= 0 {
		// The chain refuses this already; what is handed out on the
		// strength of this check must never be a token valid forever.
		return nil, fmt.Errorf("%w: the identity's token carries no expiry", ErrInvalidToken)
	}
	jc.Credential = CredentialIdentityToken
	return jc, nil
}

// VerifiedAccessToken is the Verified of an `rlat_` access token the token
// authority resolved to claims and p.
func VerifiedAccessToken(claims *JWTClaims, p *fat.Principal, channel authn.Channel) *Verified {
	return &Verified{
		Claims:    claims,
		Source:    SourceAccessToken,
		Method:    authn.Method{Channel: channel, Credential: forgeauth.CredentialAPIKey},
		Principal: p,
	}
}

// credentialFor is the Credential of a credential the chain accepted, from
// the validator that proved it and how it was presented (method). It is the
// one place this is decided.
//
//   - The managed identity's session, read from the sign-in cookie and
//     resolved through the session store (authn.Method.IsBrowserSession), is
//     a person's browser (CredentialBrowserSession).
//   - Any other managed identity credential is software's
//     (CredentialIdentityToken): the app's browser holds only its sign-in
//     cookie, so a program presenting the identity's token or a session id
//     itself is acting for the user, not as them.
//   - A Supabase or dev signer JWT is a person's session, or an OAuth
//     client's token when it names one (the legacy validator decided which).
//     A server-side session never holds one: one that does is refused.
//   - The shared key is CredentialSharedKey.
func credentialFor(v verdict, method authn.Method) (Credential, error) {
	switch v.source {
	case SourceIdentity:
		if method.IsBrowserSession() {
			return CredentialBrowserSession, nil
		}
		return CredentialIdentityToken, nil
	case SourceSupabase, SourceDevSigner, SourceSharedKey:
		if method.Credential == forgeauth.CredentialSession {
			return CredentialUnknown, fmt.Errorf("%w: a session holds a token of %s", ErrInvalidToken, v.source)
		}
		if v.legacy == nil {
			return CredentialUnknown, fmt.Errorf("%w: no claims for %s", ErrInvalidToken, v.source)
		}
		return v.legacy.Credential, nil
	}
	return CredentialUnknown, fmt.Errorf("%w: unknown validator %q", ErrInvalidToken, v.source)
}

// identityClaims maps the managed identity's claims to reliant's. The email
// rides only when the issuer vouches for it (email_verified), the rule
// verifiedEmailClaim applies to Supabase's.
func identityClaims(c *forgeauth.Claims) *JWTClaims {
	jc := &JWTClaims{Sub: c.UserID, Role: c.Role}
	if c.EmailVerified {
		jc.Email = c.Email
	}
	if iss, ok := c.Raw["iss"].(string); ok {
		jc.Iss = iss
	}
	if exp, ok := c.Raw["exp"].(float64); ok {
		jc.Exp = int64(exp)
	}
	return jc
}

// Bind returns ctx carrying v: the caller (WithUser), the Credential
// (WithCredential), forge's authn.Method (authn.AuthMethod reads it), the
// access token's principal, and the user on the request's telemetry. It also
// counts the credential in reliant_auth_credentials_total. Every serving path
// installs a verified caller through it.
func (v *Verified) Bind(ctx context.Context) context.Context {
	c := v.Claims
	ctx = WithUser(ctx, c.Sub, c.Role, c.Email)
	ctx = WithCredential(ctx, c.Credential)
	ctx = authn.ContextWithAuthMethod(ctx, v.Method)
	if v.Principal != nil {
		ctx = WithMachineToken(ctx, v.Principal)
	}
	authmetrics.CredentialsTotal.WithLabelValues(c.Credential.Label(), string(v.Source), v.Method.Channel.String()).Inc()
	return telemetry.BindUser(ctx, c.Sub)
}
