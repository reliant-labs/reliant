// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// TokenValidator defines the interface for token validation.
// Implementations include JWTValidator (Supabase JWTs) and apiKeyValidator
// (simple bearer-token auth for self-hosted deployments).
type TokenValidator interface {
	ValidateToken(token string) (*JWTClaims, error)
}

// DevUser is the hardcoded user for development mode auth bypass
// Sub is read from DEV_USER_ID env var, defaulting to the original dev UUID
var DevUser = &JWTClaims{
	Sub:   devUserID(),
	Role:  "authenticated",
	Email: "dev@localhost",
}

func devUserID() string {
	if id := os.Getenv("DEV_USER_ID"); id != "" {
		return id
	}
	return "530eb7d2-1f6a-4305-890e-c05becebcf03"
}

// GetAuthMode returns the configured auth mode from the AUTH_MODE env var.
// Recognized values: "dev", "apikey", "supabase". Defaults to "supabase"
// when unset.
func GetAuthMode() string {
	mode := strings.ToLower(os.Getenv("AUTH_MODE"))
	switch mode {
	case "dev", "apikey":
		return mode
	default:
		return "supabase"
	}
}

// Middleware provides token authentication for HTTP requests
type Middleware struct {
	validator        TokenValidator
	firstSeenTracker *firstSeenTracker
	devMode          bool
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

// NewMiddleware creates a new auth middleware.
// The auth mode is determined by GetAuthMode():
//   - "dev":      bypass auth entirely with a hardcoded dev user
//   - "apikey":   validate bearer tokens against AUTH_API_KEY env var
//   - "supabase": validate JWTs using publicKeyPEM or jwksURL (default)
func NewMiddleware(publicKeyPEM string, jwksURL string) (*Middleware, error) {
	mode := GetAuthMode()

	switch mode {
	case "dev":
		logging.Info("[HTTP Auth] Development mode detected - auth bypass enabled",
			"dev_user_id", DevUser.Sub,
			"dev_user_email", DevUser.Email)
		return &Middleware{
			validator:        nil,
			firstSeenTracker: newFirstSeenTracker(),
			devMode:          true,
		}, nil

	case "apikey":
		apiKey := os.Getenv("AUTH_API_KEY")
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
			devMode:          false,
		}, nil

	default: // "supabase"
		var validator *JWTValidator
		switch {
		case publicKeyPEM != "":
			var err error
			validator, err = NewJWTValidator(publicKeyPEM)
			if err != nil {
				return nil, err
			}
		case jwksURL != "":
			var err error
			validator, err = LoadJWKS(context.Background(), jwksURL)
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
			devMode:          false,
		}, nil
	}
}

// RequireAuth is a middleware that requires authentication
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Dev mode bypass - use hardcoded dev user
		if m.devMode {
			ctx := r.Context()
			ctx = context.WithValue(ctx, UserIDContextKey, DevUser.Sub)
			ctx = context.WithValue(ctx, UserRoleContextKey, DevUser.Role)
			ctx = context.WithValue(ctx, UserEmailContextKey, DevUser.Email)

			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

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

		claims, err := m.validator.ValidateToken(tokenString)
		if err != nil {
			logging.Warn("Invalid token", "error", err, "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error": "unauthorized", "message": "Invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		// Add claims to context
		ctx := r.Context()
		ctx = context.WithValue(ctx, UserIDContextKey, claims.Sub)
		ctx = context.WithValue(ctx, UserRoleContextKey, claims.Role)
		ctx = context.WithValue(ctx, UserEmailContextKey, claims.Email)

		// Update analytics client userID and Sentry user context on first authentication
		if m.firstSeenTracker.isFirstTimeSeen(claims.Sub) {
			analytics.SetUserID(claims.Sub)
			telemetry.GetReporter().SetUser(claims.Sub, claims.Email)
		}

		// Always update the JWT (it refreshes on each request)
		analytics.SetUserJWT(tokenString)

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
					"email", email,
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
		// Dev mode bypass - use hardcoded dev user
		if m.devMode {
			ctx := r.Context()
			ctx = context.WithValue(ctx, UserIDContextKey, DevUser.Sub)
			ctx = context.WithValue(ctx, UserRoleContextKey, DevUser.Role)
			ctx = context.WithValue(ctx, UserEmailContextKey, DevUser.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

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
		ctx := r.Context()
		ctx = context.WithValue(ctx, UserIDContextKey, claims.Sub)
		ctx = context.WithValue(ctx, UserRoleContextKey, claims.Role)
		ctx = context.WithValue(ctx, UserEmailContextKey, claims.Email)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
