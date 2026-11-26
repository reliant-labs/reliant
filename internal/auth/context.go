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
	// DaemonOrgIDContextKey is the org of the machine a daemon-bound
	// credential is bound to, as the token authority named it.
	DaemonOrgIDContextKey contextKey = "daemon_org_id"
	// CredentialOrgContextKey is the org a daemon's connecting credential
	// acts in, set by the daemon auth interceptor.
	CredentialOrgContextKey contextKey = "credential_org" //nolint:gosec // G101: a context key name, not a credential
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

// OfferUserJWT stores token for userID like SetUserJWT, except that it keeps
// the token already held when that one is a live JWT expiring no earlier.
// Workflow activities carry the token captured at the run's start
// (RuntimeContext.UserJWT) to whichever worker picks them up; offering it,
// rather than setting it, never replaces a fresher token a later turn already
// delivered to this process with an older one. Tokens whose expiry cannot be
// read are not compared: the offered one wins, as with SetUserJWT.
func OfferUserJWT(userID, token string) {
	if userID == "" || strings.TrimSpace(token) == "" {
		return
	}
	userJWTsMu.Lock()
	defer userJWTsMu.Unlock()
	if held, ok := userJWTs[userID]; ok && outlasts(held, token, time.Now()) {
		return
	}
	userJWTs[userID] = token
}

// outlasts reports whether held is a live JWT whose exp is no earlier than
// offered's. Both expiries must be readable.
func outlasts(held, offered string, now time.Time) bool {
	he, okh := jwtExp(held)
	oe, oko := jwtExp(offered)
	return okh && oko && now.Before(time.Unix(he, 0)) && he >= oe
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
	exp, ok := jwtExp(token)
	if !ok {
		return false
	}
	return !now.Before(time.Unix(exp, 0))
}

// jwtExp is token's exp claim, read without verifying the signature; false
// when token is not a JWT or carries no exp.
func jwtExp(token string) (int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return 0, false
	}
	return claims.Exp, true
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
//
//forge:resolves-caller
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(UserIDContextKey).(string)
	return userID, ok
}

// MustGetUserID extracts user ID from context and panics if not found
// Use this in authenticated handlers where user_id is guaranteed to exist
//
//forge:resolves-caller
func MustGetUserID(ctx context.Context) string {
	userID, ok := GetUserIDFromContext(ctx)
	if !ok {
		panic("user_id not found in context - auth middleware not applied?")
	}
	return userID
}

// GetUserEmailFromContext extracts the user email from the context.
//
//forge:resolves-caller
func GetUserEmailFromContext(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(UserEmailContextKey).(string)
	return email, ok
}

// GetDaemonIDFromContext extracts the PAT-bound daemon ID from the context.
// Returns empty string if the PAT is not bound to a daemon.
//
//forge:resolves-caller
func GetDaemonIDFromContext(ctx context.Context) string {
	daemonID, _ := ctx.Value(DaemonIDContextKey).(string)
	return daemonID
}

// GetDaemonOrgIDFromContext is the organization of the machine the daemon
// credential on ctx is bound to (control-plane's daemons.org_id, named at
// introspection), or "" when it names none: an unbound credential, a machine
// with no recorded org, a self-hosted authority, or a control plane that
// predates the field.
//
//forge:resolves-caller
func GetDaemonOrgIDFromContext(ctx context.Context) string {
	orgID, _ := ctx.Value(DaemonOrgIDContextKey).(string)
	return orgID
}

// GetCredentialOrgFromContext is the org the daemon's connecting credential
// acts in, or "" when the context carries none. Unlike
// GetDaemonOrgIDFromContext it is set for an unbound credential too: a
// self-paired machine's credential acts in the org chosen at pairing before
// any machine row records an org.
//
//forge:resolves-caller
func GetCredentialOrgFromContext(ctx context.Context) string {
	orgID, _ := ctx.Value(CredentialOrgContextKey).(string)
	return orgID
}
