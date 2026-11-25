// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"

	"github.com/reliant-labs/reliant/internal/logging"
)

// TokenValidator defines the interface for token validation.
// Implementations include JWTValidator (Supabase JWTs) and apiKeyValidator
// (simple bearer-token auth for self-hosted deployments).
type TokenValidator interface {
	ValidateToken(token string) (*JWTClaims, error)
}

// Auth modes (AUTH_MODE).
const (
	// ModeSupabase validates Supabase JWTs: the default.
	ModeSupabase = "supabase"
	// ModeAPIKey validates one shared bearer key, for self-hosting.
	ModeAPIKey = "apikey"
)

// Settings is how the API server authenticates callers: the auth settings of
// its config (ApiServerConfig).
type Settings struct {
	// Mode is ModeSupabase or ModeAPIKey; anything else (the zero value
	// included) is ModeSupabase.
	Mode string
	// APIKey is the shared bearer key ModeAPIKey validates (AUTH_API_KEY).
	APIKey string
	// PublicKeyPEM, else JWKSURL, is what ModeSupabase validates JWTs
	// against.
	PublicKeyPEM string
	JWKSURL      string
	// RequireEmailVerifiedClaim: see the RequireEmailVerifiedClaim option.
	RequireEmailVerifiedClaim bool
	// DevJWTSecret (AUTH_DEV_JWT_SECRET) is the dev tier's HS256 signer,
	// accepted beside the session key in ModeSupabase; empty is none. See
	// WithDevSigner, which refuses it outside the dev tier.
	DevJWTSecret string
	// SupabaseIssuer and SupabaseAudience (SUPABASE_JWT_ISSUER,
	// SUPABASE_JWT_AUDIENCE) are the iss and aud a Supabase JWT must carry;
	// empty checks nothing (ExpectIssuer, ExpectAudience).
	SupabaseIssuer   string
	SupabaseAudience string
	// Identity is the managed identity (forge.ManagedIdentity) whose tokens
	// ModeSupabase accepts beside Supabase's. Inert until its Issuer is set.
	Identity IdentitySettings
	// Sessions resolves a server-side sign-in session id (forge/pkg/session)
	// to its access token. nil: a session id is an unknown token, and the
	// sign-in cookie is refused.
	Sessions forgeauth.SessionResolver
	// DevSessionCookie reads the sign-in cookie by its plain name
	// (SessionCookieName) too, as well as its __Host- name. Local
	// development only: it serves plain HTTP, where a browser refuses a
	// __Host- cookie.
	DevSessionCookie bool
}

// IdentitySettings is where the managed identity's tokens are verified:
// forge's standard jwt_* config fields, which forge.IdentityRef fills from
// the env's forge.ManagedIdentity.
type IdentitySettings struct {
	// Issuer is the exact iss of the identity's tokens (JWT_ISSUER). Empty
	// turns the identity validator off.
	Issuer string
	// Audience is an aud the token must carry (JWT_AUDIENCE): the identity
	// project's id, so a token another project of the same instance was
	// issued is refused. Required with Issuer.
	Audience string
	// JWKSURL is the issuer's key set (JWT_JWKS_URL); empty discovers it
	// from the issuer's OpenID configuration.
	JWKSURL string
	// SigningMethod is the alg the tokens are signed with (JWT_SIGNING_METHOD,
	// RS256 when empty).
	SigningMethod string
	// RolesClaim is where roles are read from (JWT_ROLES_CLAIM).
	RolesClaim string
	// Snapshot is the out-of-band key set the validator starts on when the
	// issuer is down at boot (JWT_JWKS_SNAPSHOT_FILE, JWT_JWKS_SNAPSHOT).
	Snapshot forgeauth.JWKSSnapshot
}

// Configured reports whether s names an identity to accept tokens from.
func (s IdentitySettings) Configured() bool { return s.Issuer != "" }

// ModeName is s's effective mode: ModeAPIKey or ModeSupabase.
func (s Settings) ModeName() string {
	if s.Mode == ModeAPIKey {
		return ModeAPIKey
	}
	return ModeSupabase
}

// JWTOptions are the JWT validator options s implies.
func (s Settings) JWTOptions() []ValidatorOption {
	return []ValidatorOption{
		RequireEmailVerifiedClaim(s.RequireEmailVerifiedClaim),
		ExpectIssuer(s.SupabaseIssuer),
		ExpectAudience(s.SupabaseAudience),
	}
}

// Middleware provides token authentication for HTTP requests
type Middleware struct {
	authenticator *Authenticator
}

// NewMiddleware creates a new auth middleware for settings' mode:
//   - "apikey":   validate bearer tokens against settings.APIKey
//   - "supabase": validate Supabase JWTs (settings.PublicKeyPEM or JWKSURL)
//     and, when settings.Identity names one, the managed identity's tokens
//     (default)
func NewMiddleware(settings Settings) (*Middleware, error) {
	a, err := NewAuthenticator(context.Background(), settings)
	if err != nil {
		return nil, err
	}
	logging.Info("[HTTP Auth] auth mode enabled", "mode", settings.ModeName(),
		"managed_identity", settings.Identity.Issuer)
	return &Middleware{authenticator: a}, nil
}

// RequireAuth is a middleware that requires authentication via the configured
// JWT/apikey validator. Access-token bearers are rejected here — surfaces that
// accept `rlat_` API tokens must use RequireAuthOrAccessToken.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return m.requireAuth(next, nil)
}

// RequireAuthOrAccessToken is RequireAuth extended with `rlat_` bearers
// carrying reliant:api, resolved by the token authority to the same
// claims/identity object a session produces — mirroring the gRPC
// AuthInterceptor so the same middleware path serves both credentials.
func (m *Middleware) RequireAuthOrAccessToken(v AccessTokenIntrospector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return m.requireAuth(next, v)
	}
}

// requireAuth authenticates the request and forwards it with identity on the
// context. When tokens is nil, access-token bearers are rejected outright
// (JWT-only surface) instead of falling through to JWT validation.
func (m *Middleware) requireAuth(next http.Handler, tokens AccessTokenIntrospector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Authorization header when the request has one, else the
		// sign-in session cookie (CredentialFromHeader).
		tokenString, channel := m.authenticator.CredentialFrom(r.Header)

		// For WebSocket upgrade requests, allow query parameter as fallback
		// This is necessary because browser WebSocket API doesn't support custom headers
		if tokenString == "" && channel != authn.ChannelAuthorization && isWebSocketUpgrade(r) {
			tokenString = r.URL.Query().Get("token")
			channel = authn.ChannelUnknown
		}

		if tokenString == "" {
			logging.WarnContext(r.Context(), "Missing authorization token", "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error": "unauthorized", "message": "Missing authorization token"}`, http.StatusUnauthorized)
			return
		}

		// Shape dispatch: `rlat_` bearers are access tokens (resolved by the
		// token authority, reliant:api only); anything else goes through the
		// authenticator's token chain.
		isAccessToken := IsAccessTokenFormat(tokenString)
		var verified *Verified
		err := m.authenticator.CheckChannel(tokenString, channel)
		switch {
		case err != nil:
		case isAccessToken && tokens != nil:
			var claims *JWTClaims
			var principal *fat.Principal
			claims, principal, err = ClaimsForAPIToken(r.Context(), tokens, tokenString)
			if err == nil {
				verified = VerifiedAccessToken(claims, principal, channel)
			}
		case isAccessToken:
			err = fmt.Errorf("%w: access tokens are not accepted on this endpoint", ErrInvalidToken)
		default:
			verified, err = m.authenticator.Verify(r.Context(), tokenString, channel)
		}
		if err != nil {
			// An authority that could not be reached has rejected nothing:
			// 503, so the client retries instead of discarding a good token.
			if !IsCredentialRejection(err) {
				logging.ErrorContext(r.Context(), "Token verification unavailable", "error", err, "path", r.URL.Path, "method", r.Method)
				http.Error(w, `{"error": "unavailable", "message": "Token verification unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			logging.WarnContext(r.Context(), "Invalid token", "error", err, "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error": "unauthorized", "message": "Invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := verified.Bind(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request
func isWebSocketUpgrade(r *http.Request) bool {
	// Connection header may contain multiple values like "Upgrade, keep-alive"
	connection := strings.ToLower(r.Header.Get("Connection"))
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))

	return strings.Contains(connection, "upgrade") && upgrade == "websocket"
}
