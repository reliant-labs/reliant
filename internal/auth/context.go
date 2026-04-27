// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
)

type contextKey string

const (
	UserIDContextKey    contextKey = "user_id"
	UserRoleContextKey  contextKey = "user_role"
	UserEmailContextKey contextKey = "user_email"
	DaemonIDContextKey  contextKey = "daemon_id"
)

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
