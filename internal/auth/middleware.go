// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
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
}

// ModeName is s's effective mode: ModeAPIKey or ModeSupabase.
func (s Settings) ModeName() string {
	if s.Mode == ModeAPIKey {
		return ModeAPIKey
	}
	return ModeSupabase
}

// JWTOptions are the JWT validator options s implies.
func (s Settings) JWTOptions() []ValidatorOption {
	return []ValidatorOption{RequireEmailVerifiedClaim(s.RequireEmailVerifiedClaim)}
}

// Middleware provides token authentication for HTTP requests
type Middleware struct {
	validator        TokenValidator
	firstSeenTracker *firstSeenTracker
}

// firstSeenTracker tracks which users have been seen to avoid redundant SetUserID calls
type firstSeenTracker struct {
	seen map[string]bool
	mu   sync.RWMutex
}

func newFirstSeenTracker() *firstSeenTracker {
	return &firstSeenTracker{
		seen: make(map[string]bool),
	}
}

func (t *firstSeenTracker) isFirstTimeSeen(userID string) bool {
	t.mu.RLock()
	result := !t.seen[userID]
	t.mu.RUnlock()
	if result {
		t.mu.Lock()
		t.seen[userID] = true
		t.mu.Unlock()
	}
	return result
}

// NewMiddleware creates a new auth middleware for settings' mode:
//   - "apikey":   validate bearer tokens against settings.APIKey
//   - "supabase": validate JWTs using settings.PublicKeyPEM or JWKSURL (default)
func NewMiddleware(settings Settings) (*Middleware, error) {
	publicKeyPEM, jwksURL := settings.PublicKeyPEM, settings.JWKSURL
	switch settings.ModeName() {
	case ModeAPIKey:
		apiKey := settings.APIKey
		if apiKey == "" {
			return nil, fmt.Errorf("AUTH_MODE=apikey requires AUTH_API_KEY to be set")
		}
		validator, err := NewAPIKeyValidator(apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create API key validator: %w", err)
		}
		logging.Info("[HTTP Auth] API key auth mode enabled",
			"user_id", validator.userID)
		return &Middleware{
			validator:        validator,
			firstSeenTracker: newFirstSeenTracker(),
		}, nil

	default: // "supabase"
		var validator *JWTValidator
		switch {
		case publicKeyPEM != "":
			var err error
			validator, err = NewJWTValidator(publicKeyPEM, settings.JWTOptions()...)
			if err != nil {
				return nil, err
			}
		case jwksURL != "":
			var err error
			validator, err = LoadJWKS(context.Background(), jwksURL, settings.JWTOptions()...)
			if err != nil {
				return nil, fmt.Errorf("failed to load JWKS from %s: %w", jwksURL, err)
			}
		default:
			return nil, ErrInvalidPublicKey
		}
		logging.Info("[HTTP Auth] Supabase JWT auth mode enabled")
		return &Middleware{
			validator:        validator,
			firstSeenTracker: newFirstSeenTracker(),
		}, nil
	}
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
		// Get token from Authorization header first
		var tokenString string
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// For WebSocket upgrade requests, allow query parameter as fallback
		// This is necessary because browser WebSocket API doesn't support custom headers
		if tokenString == "" && isWebSocketUpgrade(r) {
			tokenString = r.URL.Query().Get("token")
		}

		if tokenString == "" {
			logging.Warn("Missing authorization token", "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error": "unauthorized", "message": "Missing authorization token"}`, http.StatusUnauthorized)
			return
		}

		// Shape dispatch: `rlat_` bearers are access tokens (resolved by the
		// token authority, reliant:api only); anything else goes through the
		// JWT/apikey validator.
		isAccessToken := IsAccessTokenFormat(tokenString)
		var claims *JWTClaims
		var principal *fat.Principal
		var err error
		switch {
		case isAccessToken && tokens != nil:
			claims, principal, err = ClaimsForAPIToken(r.Context(), tokens, tokenString)
		case isAccessToken:
			err = fmt.Errorf("access tokens are not accepted on this endpoint")
		default:
			claims, err = m.validator.ValidateToken(tokenString)
		}
		if err != nil {
			// An authority that could not be reached has rejected nothing:
			// 503, so the client retries instead of discarding a good token.
			if isAccessToken && tokens != nil && !IsCredentialRejection(err) {
				logging.Error("Access token verification unavailable", "error", err, "path", r.URL.Path, "method", r.Method)
				http.Error(w, `{"error": "unavailable", "message": "Token verification unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			logging.Warn("Invalid token", "error", err, "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error": "unauthorized", "message": "Invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		// Add claims to context
		ctx := WithUser(r.Context(), claims.Sub, claims.Role, claims.Email)
		if principal != nil {
			ctx = WithMachineToken(ctx, principal)
		}

		// Update the Sentry user context on first authentication
		if m.firstSeenTracker.isFirstTimeSeen(claims.Sub) {
			telemetry.GetReporter().SetUser(claims.Sub, claims.Email)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// DomainWhitelist is HTTP middleware that rejects authenticated requests whose
// email domain is not in the allowed list. If allowedDomains is empty, all
// domains are allowed. Unauthenticated requests pass through unchanged.
func DomainWhitelist(allowedDomains []string) func(http.Handler) http.Handler {
	domainSet := make(map[string]bool, len(allowedDomains))
	for _, d := range allowedDomains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			domainSet[d] = true
		}
	}

	return func(next http.Handler) http.Handler {
		if len(domainSet) == 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			email, ok := GetUserEmailFromContext(r.Context())
			if !ok || email == "" {
				next.ServeHTTP(w, r)
				return
			}

			parts := strings.SplitN(email, "@", 2)
			if len(parts) != 2 {
				next.ServeHTTP(w, r)
				return
			}
			domain := strings.ToLower(parts[1])

			if !domainSet[domain] {
				logging.Warn("[Domain Whitelist] Rejected email domain",
					"domain", domain,
					"path", r.URL.Path,
				)
				http.Error(w, `{"error": "forbidden", "message": "email domain not allowed in this environment"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request
func isWebSocketUpgrade(r *http.Request) bool {
	// Connection header may contain multiple values like "Upgrade, keep-alive"
	connection := strings.ToLower(r.Header.Get("Connection"))
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))

	return strings.Contains(connection, "upgrade") && upgrade == "websocket"
}

// OptionalAuth is a middleware that extracts auth if present but doesn't require it
func (m *Middleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get token from Authorization header first
		var tokenString string
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// For WebSocket upgrade requests, allow query parameter as fallback
		if tokenString == "" && isWebSocketUpgrade(r) {
			tokenString = r.URL.Query().Get("token")
		}

		// If no token, continue without auth
		if tokenString == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Try to validate, but don't fail if invalid
		claims, err := m.validator.ValidateToken(tokenString)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		// Add claims to context
		ctx := WithUser(r.Context(), claims.Sub, claims.Role, claims.Email)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
