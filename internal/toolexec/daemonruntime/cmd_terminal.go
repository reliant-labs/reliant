// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/terminal"
)

// terminalManager is the package-level terminal manager instance.
var terminalManager *terminal.Manager

// SetTerminalManager sets the terminal manager used by terminal command handlers.
func SetTerminalManager(m *terminal.Manager) {
	terminalManager = m
}

func init() {
	RegisterCommand("terminal.create", handleTerminalCreate)
	RegisterCommand("terminal.list", handleTerminalList)
	RegisterCommand("terminal.close", handleTerminalClose)
	RegisterCommand("terminal.resize", handleTerminalResize)
}

// =============================================================================
// terminal.create — create a new terminal session
// =============================================================================

type terminalCreateRequest struct {
	WorkingDir string `json:"working_dir"`
}

type terminalCreateResponse struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
}

func handleTerminalCreate(_ context.Context, payload []byte) ([]byte, error) {
	if terminalManager == nil {
		return nil, fmt.Errorf("terminal manager not initialized")
	}

	var req terminalCreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// DaemonCommand handlers are already scoped per-user: each tools-daemon
	// bidi stream serves exactly one user, so the router only delivers commands
	// to the correct daemon. We pass an empty userID here because access
	// control is enforced at the stream/routing level, not inside the manager.
	session, err := terminalManager.CreateSession(req.WorkingDir, "")
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	var pid int
	if session.CMD != nil && session.CMD.Process != nil {
		pid = session.CMD.Process.Pid
	}

	resp := terminalCreateResponse{
		SessionID: session.ID,
		PID:       pid,
	}
	return json.Marshal(resp)
}

// =============================================================================
// terminal.list — list all active terminal sessions
// =============================================================================

type terminalSessionInfo struct {
	ID         string `json:"id"`
	WorkingDir string `json:"working_dir"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
	PID        int    `json:"pid"`
}

type terminalListResponse struct {
	Sessions []terminalSessionInfo `json:"sessions"`
}

func handleTerminalList(_ context.Context, _ []byte) ([]byte, error) {
	if terminalManager == nil {
		return nil, fmt.Errorf("terminal manager not initialized")
	}

	// DaemonCommand handlers are already scoped per-user via the bidi stream.
	// Each daemon serves one user, so ListSessions() returns only that user's sessions.
	sessions := terminalManager.ListSessions()
	infos := make([]terminalSessionInfo, 0, len(sessions))

	for _, s := range sessions {
		var pid int
		if s.CMD != nil && s.CMD.Process != nil {
			pid = s.CMD.Process.Pid
		}
		infos = append(infos, terminalSessionInfo{
			ID:         s.ID,
			WorkingDir: s.WorkingDir,
			CreatedAt:  s.CreatedAt.Format(time.RFC3339),
			LastActive: s.LastActive.Format(time.RFC3339),
			PID:        pid,
		})
	}

	return json.Marshal(terminalListResponse{Sessions: infos})
}

// =============================================================================
// terminal.close — close a terminal session
// =============================================================================

type terminalCloseRequest struct {
	SessionID string `json:"session_id"`
}

type terminalCloseResponse struct {
	Success bool `json:"success"`
}

func handleTerminalClose(_ context.Context, payload []byte) ([]byte, error) {
	if terminalManager == nil {
		return nil, fmt.Errorf("terminal manager not initialized")
	}

	var req terminalCloseRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// DaemonCommand handlers are already scoped per-user via the bidi stream.
	// Each daemon serves one user, so CloseSession() can only reach that user's sessions.
	if err := terminalManager.CloseSession(req.SessionID); err != nil {
		return nil, fmt.Errorf("close session: %w", err)
	}

	return json.Marshal(terminalCloseResponse{Success: true})
}

// =============================================================================
// terminal.resize — resize a terminal session
// =============================================================================

type terminalResizeRequest struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type terminalResizeResponse struct {
	Success bool `json:"success"`
}

func handleTerminalResize(_ context.Context, payload []byte) ([]byte, error) {
	if terminalManager == nil {
		return nil, fmt.Errorf("terminal manager not initialized")
	}

	var req terminalResizeRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if err := terminalManager.Resize(req.SessionID, req.Cols, req.Rows); err != nil {
		return nil, fmt.Errorf("resize session: %w", err)
	}

	return json.Marshal(terminalResizeResponse{Success: true})
}
