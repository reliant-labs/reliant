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
	UserJWTContextKey   contextKey = "user_jwt"
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

// GetUserJWTFromContext extracts the raw user JWT from the context.
func GetUserJWTFromContext(ctx context.Context) (string, bool) {
	jwt, ok := ctx.Value(UserJWTContextKey).(string)
	return jwt, ok
}

// MustGetUserJWT extracts the raw user JWT from context and panics if not found.
func MustGetUserJWT(ctx context.Context) string {
	jwt, ok := GetUserJWTFromContext(ctx)
	if !ok || jwt == "" {
		panic("user_jwt not found in context - auth middleware not applied?")
	}
	return jwt
}