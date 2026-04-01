// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/permission"
)

// WindowState represents the persisted window state for the current workspace
type WindowState struct {
	// Window bounds
	X      int `json:"x,omitempty"`
	Y      int `json:"y,omitempty"`
	Width  int `json:"width"`
	Height int `json:"height"`

	// Window state
	IsMaximized  bool `json:"isMaximized"`
	IsFullScreen bool `json:"isFullScreen"`

	// Timestamps
	UpdatedAt time.Time `json:"updatedAt"`
}

// WindowStateHandler handles window state persistence
type WindowStateHandler struct {
	dataDir   string
	statePath string
	mu        sync.RWMutex
	state     *WindowState
}

// NewWindowStateHandler creates a new window state handler
func NewWindowStateHandler(dataDir string) ReliantHandler {
	statePath := filepath.Join(dataDir, "window-state.json")
	h := &WindowStateHandler{
		dataDir:   dataDir,
		statePath: statePath,
	}
	// Load initial state
	h.loadState()
	return h
}

// RoutePrefix returns the base path for window state routes
func (h *WindowStateHandler) RoutePrefix() string {
	return ""
}

// Routes returns all routes for the window state handler
func (h *WindowStateHandler) Routes() []Route {
	return []Route{
		{Path: "/window-state", Method: http.MethodGet, Handler: h.GetWindowState, RequireAuth: false},
		{Path: "/window-state", Method: http.MethodPost, Handler: h.SaveWindowState, RequireAuth: false},
		{Path: "/window-state", Method: http.MethodDelete, Handler: h.ClearWindowState, RequireAuth: false},
	}
}

// Can checks if the current user can perform an action on a resource
// Window state endpoints don't require authentication for local-only access
func (h *WindowStateHandler) Can(r *http.Request, action permission.Action, resourceID string) error {
	return nil
}

// loadState loads window state from disk
func (h *WindowStateHandler) loadState() {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, err := os.ReadFile(h.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Warn("Failed to read window state file", "error", err, "path", h.statePath)
		}
		h.state = nil
		return
	}

	var state WindowState
	if err := json.Unmarshal(data, &state); err != nil {
		logging.Warn("Failed to parse window state file", "error", err, "path", h.statePath)
		h.state = nil
		return
	}

	h.state = &state
	logging.Debug("Loaded window state", "path", h.statePath)
}

// saveState saves window state to disk
func (h *WindowStateHandler) saveState(state *WindowState) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Ensure data directory exists
	if err := os.MkdirAll(h.dataDir, 0755); err != nil {
		return err
	}

	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(h.statePath, data, 0644); err != nil {
		return err
	}

	h.state = state
	logging.Debug("Saved window state", "path", h.statePath)
	return nil
}

// GetWindowState returns the current window state
func (h *WindowStateHandler) GetWindowState(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	state := h.state
	h.mu.RUnlock()

	if state == nil {
		// Return empty object, not null - indicates no saved state
		RespondJSON(w, http.StatusOK, map[string]interface{}{
			"state": nil,
		})
		return
	}

	RespondJSON(w, http.StatusOK, map[string]interface{}{
		"state": state,
	})
}

// SaveWindowState saves the window state
func (h *WindowStateHandler) SaveWindowState(w http.ResponseWriter, r *http.Request) {
	var state WindowState
	if err := DecodeJSON(r, &state); err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// Validate basic bounds
	if state.Width <= 0 {
		state.Width = 1400 // Default width
	}
	if state.Height <= 0 {
		state.Height = 900 // Default height
	}

	if err := h.saveState(&state); err != nil {
		logging.Error("Failed to save window state", "error", err)
		RespondError(w, http.StatusInternalServerError, "Failed to save window state")
		return
	}

	RespondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

// ClearWindowState clears the saved window state
func (h *WindowStateHandler) ClearWindowState(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Delete the file if it exists
	if err := os.Remove(h.statePath); err != nil && !os.IsNotExist(err) {
		logging.Error("Failed to delete window state file", "error", err)
		RespondError(w, http.StatusInternalServerError, "Failed to clear window state")
		return
	}

	h.state = nil
	logging.Debug("Cleared window state", "path", h.statePath)

	RespondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}
