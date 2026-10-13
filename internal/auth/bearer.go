// Copyright (c) 2025 Reliant Labs
package auth

import "strings"

// BearerToken strips the "Bearer " prefix from an Authorization header,
// yielding the raw token: the session JWT a handler forwards to the control
// plane, or the credential it classifies (IsAccessTokenFormat) before doing so.
func BearerToken(authHeader string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
}
