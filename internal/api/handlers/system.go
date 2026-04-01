// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"net/http"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/permission"
)

// SystemHandler handles system-related HTTP requests
type SystemHandler struct {
	database    db.Repository
	NATSChecker func() bool // returns true if NATS is connected; nil means NATS not in use
}

// NewSystemHandler creates a new system handler
func NewSystemHandler(database db.Repository, natsChecker func() bool) ReliantHandler {
	return &SystemHandler{
		database:    database,
		NATSChecker: natsChecker,
	}
}

// RoutePrefix returns the base path for system routes
func (h *SystemHandler) RoutePrefix() string {
	return ""
}

// Routes returns all routes for the system handler
// NOTE: Health, Ready, Info, Version have been migrated to gRPC (SystemService)
// The /health endpoint below is a lightweight HTTP wrapper for Electron compatibility
func (h *SystemHandler) Routes() []Route {
	return []Route{
		// Lightweight HTTP health check for Electron (proxies to gRPC)
		{Path: "/health", Method: http.MethodGet, Handler: h.Health, RequireAuth: false},
		{Path: "/ready", Method: http.MethodGet, Handler: h.Ready, RequireAuth: false},
	}
}

// Can checks if the current user can perform an action on a resource
// System endpoints don't require authentication, so this always returns nil
func (h *SystemHandler) Can(r *http.Request, action permission.Action, resourceID string) error {
	return nil
}

// Health provides a lightweight HTTP wrapper for the gRPC health check
// This is primarily for Electron's backend-manager.js which uses HTTP
// Web clients should use the gRPC endpoint directly via systemGrpc.health()
func (h *SystemHandler) Health(w http.ResponseWriter, r *http.Request) {
	// Return simple OK response
	// This matches the gRPC health response format
	RespondJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": "v2",
	})
}

// Ready checks service dependencies and reports readiness
func (h *SystemHandler) Ready(w http.ResponseWriter, r *http.Request) {
	var failures []string

	if err := h.database.Ping(r.Context()); err != nil {
		failures = append(failures, "db: "+err.Error())
	}
	if h.NATSChecker != nil && !h.NATSChecker() {
		failures = append(failures, "nats: disconnected")
	}

	if len(failures) > 0 {
		RespondJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "not_ready",
			"failures": failures,
		})
		return
	}
	RespondJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
	})
}
