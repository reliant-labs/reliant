// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/reliant-labs/reliant/internal/permission"
)

// Route represents a single HTTP route
type Route struct {
	Path        string
	Method      string
	Handler     http.HandlerFunc
	RequireAuth bool // Whether this route requires authentication
}

// ReliantHandler defines the interface for resource-specific handlers
type ReliantHandler interface {
	// Routes returns all routes this handler provides
	Routes() []Route

	// RoutePrefix returns the base path for this handler's routes
	// e.g., "/api/v2/chats" or "/api/v2/projects"
	RoutePrefix() string

	// Can checks if the current user can perform an action on a resource
	// This delegates to the permission checker for this resource
	Can(r *http.Request, action permission.Action, resourceID string) error
}

// Utility helper functions for handlers

// RespondJSON writes a JSON response
func RespondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log error but don't expose to client
		return
	}
}

// RespondError writes an error response
func RespondError(w http.ResponseWriter, status int, message string) {
	RespondJSON(w, status, map[string]interface{}{
		"error":   http.StatusText(status),
		"message": message,
		"code":    status,
	})
}

// DecodeJSON decodes JSON from request body
func DecodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// GetPathVar gets a path variable from the request
func GetPathVar(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// GetQueryParam gets a query parameter from the request
func GetQueryParam(r *http.Request, name string) string {
	return r.URL.Query().Get(name)
}

// PermissionChecker is a minimal interface for permission checking
type PermissionChecker interface {
	Can(r *http.Request, action permission.Action, resourceID string) error
}

// CheckPermissionAndRespond checks permission and responds with error if denied
// Returns true if permission granted, false if denied (and error already sent)
func CheckPermissionAndRespond(w http.ResponseWriter, r *http.Request, checker PermissionChecker, action permission.Action, resourceID string) bool {
	if err := checker.Can(r, action, resourceID); err != nil {
		switch err {
		case permission.ErrResourceNotFound:
			RespondError(w, http.StatusNotFound, "Resource not found")
		case permission.ErrUnauthorized:
			RespondError(w, http.StatusUnauthorized, "Unauthorized")
		default:
			RespondError(w, http.StatusForbidden, "Access denied")
		}
		return false
	}
	return true
}
