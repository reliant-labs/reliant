// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// tokenValidator defines the interface for JWT token validation
type tokenValidator interface {
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

// Middleware provides JWT authentication for HTTP requests
type Middleware struct {
	validator        tokenValidator
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

// NewMiddleware creates a new auth middleware using RSA public key
func NewMiddleware(publicKeyPEM string) (*Middleware, error) {
	// Check if we're in dev mode - bypass auth entirely
	devMode := config.IsDevelopmentEnvironment()
	if devMode {
		logging.Info("[HTTP Auth] Development mode detected - auth bypass enabled",
			"dev_user_id", DevUser.Sub,
			"dev_user_email", DevUser.Email)
		return &Middleware{
			validator:        nil,
			firstSeenTracker: newFirstSeenTracker(),
			devMode:          true,
		}, nil
	}

	if publicKeyPEM == "" {
		return nil, ErrInvalidPublicKey
	}

	validator, err := NewJWTValidator(publicKeyPEM)
	if err != nil {
		return nil, err
	}

	return &Middleware{
		validator:        validator,
		firstSeenTracker: newFirstSeenTracker(),
		devMode:          false,
	}, nil
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
