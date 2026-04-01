// Copyright (c) 2025 Reliant Labs
package permission

import "errors"

var (
	// ErrPermissionDenied is returned when user doesn't have permission
	ErrPermissionDenied = errors.New("permission denied")

	// ErrResourceNotFound is returned when resource doesn't exist or user doesn't own it
	// We return "not found" instead of "access denied" to avoid leaking resource existence
	ErrResourceNotFound = errors.New("resource not found")

	// ErrUnauthorized is returned when no user_id in context
	ErrUnauthorized = errors.New("unauthorized")
)
