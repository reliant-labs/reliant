// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type contextKey string

const (
	UserIDContextKey    contextKey = "user_id"
	UserRoleContextKey  contextKey = "user_role"
	UserEmailContextKey contextKey = "user_email"
	DaemonIDContextKey  contextKey = "daemon_id"
)

// userJWTs stores the latest JWT for each authenticated user.
// Updated on every gRPC request by the auth interceptor so that
// subsystems (e.g. the Reliant LLM driver) can forward the token
// without requiring the original request context.
var (
	userJWTs   = make(map[string]string)
	userJWTsMu sync.RWMutex
)

// SetUserJWT stores the latest JWT for the given user.
func SetUserJWT(userID, token string) {
	userJWTsMu.Lock()
	userJWTs[userID] = token
	userJWTsMu.Unlock()
}

// GetUserJWT returns the latest JWT for the given user, unless it has
// expired. The cache outlives the session that filled it, and every caller
// treats a present token as "the user is signed in": an expired one fails at
// the receiver instead (control plane: "invalid token: token expired") and,
// worse, wins over the fallback the caller would otherwise take, such as an
// unattended run's stored automation token (automationcred.BearerFor).
func GetUserJWT(userID string) (string, bool) {
	userJWTsMu.RLock()
	token, ok := userJWTs[userID]
	userJWTsMu.RUnlock()
	if ok && jwtExpired(token, time.Now()) {
		return "", false
	}
	return token, ok
}

// jwtExpired reports whether token is a JWT whose exp claim has passed. It
// reads the claim without verifying the signature: it only decides whether a
// token already accepted by the interceptor is still worth presenting. A value
// that is not a JWT, or carries no exp, is not judged expired.
func jwtExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return !now.Before(time.Unix(claims.Exp, 0))
}

// WithUser returns ctx carrying the authenticated caller under the keys the
// Get*FromContext readers use. The API auth interceptor installs every
// verified caller through it, and pkg/middleware.ContextWithClaims (the seam
// forge's generated handler tests authenticate through) does the same, so a
// handler test sees exactly the context a real request does.
func WithUser(ctx context.Context, userID, role, email string) context.Context {
	ctx = context.WithValue(ctx, UserIDContextKey, userID)
	ctx = context.WithValue(ctx, UserRoleContextKey, role)
	return context.WithValue(ctx, UserEmailContextKey, email)
}

// GetUserIDFromContext extracts the user ID from the context
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(UserIDContextKey).(string)
	return userID, ok
}

// MustGetUserID extracts user ID from context and panics if not found
// Use this in authenticated handlers where user_id is guaranteed to exist
func MustGetUserID(ctx context.Context) string {
	userID, ok := GetUserIDFromContext(ctx)
	if !ok {
		panic("user_id not found in context - auth middleware not applied?")
	}
	return userID
}

// GetUserEmailFromContext extracts the user email from the context.
func GetUserEmailFromContext(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(UserEmailContextKey).(string)
	return email, ok
}

// GetDaemonIDFromContext extracts the PAT-bound daemon ID from the context.
// Returns empty string if the PAT is not bound to a daemon.
func GetDaemonIDFromContext(ctx context.Context) string {
	daemonID, _ := ctx.Value(DaemonIDContextKey).(string)
	return daemonID
}
