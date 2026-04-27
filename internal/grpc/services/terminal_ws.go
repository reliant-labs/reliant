// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	// wsPingInterval is how often the server sends a websocket ping frame.
	wsPingInterval = 30 * time.Second
	// wsPongTimeout is how long the server waits for a pong before closing.
	wsPongTimeout = 45 * time.Second
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsMessage is the JSON envelope sent from server to browser.
type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	PID  int32  `json:"pid,omitempty"`
}

// wsResizeMessage is the JSON message the browser sends for resize events.
type wsResizeMessage struct {
	Type string `json:"type"`
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// TerminalWSHandler returns an http.HandlerFunc that upgrades to WebSocket and
// proxies terminal I/O through the DaemonRouter interface.
//
// Query parameters:
//   - token:      JWT token (used for auth when not in dev mode)
//   - workingDir: directory to start the shell in
//   - worktreeId: (optional) worktree identifier
//
// The handler works identically with NATSDaemonRouter (daemon-gateway) and
// LocalDaemonRouter (monolith).
func TerminalWSHandler(router toolexec.DaemonRouter, validator auth.TokenValidator) http.HandlerFunc {
	devMode := auth.GetAuthMode() == "dev"

	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		token := q.Get("token")
		workingDir := q.Get("workingDir")
		worktreeID := q.Get("worktreeId")

		// --- Authenticate ---
		var userID string
		if devMode {
			userID = auth.DevUser.Sub
			logging.Debug("[TerminalWS] Dev mode — using dev user", "user_id", userID)
		} else {
			if validator == nil {
				http.Error(w, "auth not configured", http.StatusInternalServerError)
				return
			}
			if token == "" {
				http.Error(w, "missing token query parameter", http.StatusUnauthorized)
				return
			}
			claims, err := validator.ValidateToken(token)
			if err != nil {
				logging.Warn("[TerminalWS] Invalid token", "error", err)
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
			userID = claims.Sub
		}

		// --- Upgrade to WebSocket ---
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			logging.Error("[TerminalWS] WebSocket upgrade failed", "error", err)
			return
		}
		defer conn.Close()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// --- Create terminal session via daemon ---
		createReq := map[string]any{
			"working_dir": workingDir,
		}
		if worktreeID != "" {
			createReq["worktree_id"] = worktreeID
		}
		payload, err := json.Marshal(createReq)
		if err != nil {
			writeWSError(conn, fmt.Sprintf("marshal create request: %v", err))
			return
		}

		respBytes, err := router.SendDaemonCommand(ctx, userID, "terminal.create", payload, 30000)
		if err != nil {
			writeWSError(conn, fmt.Sprintf("create terminal session: %v", err))
			return
		}

		var createResp struct {
			SessionID string `json:"session_id"`
			PID       int32  `json:"pid"`
		}
		if err := json.Unmarshal(respBytes, &createResp); err != nil {
			writeWSError(conn, fmt.Sprintf("unmarshal create response: %v", err))
			return
		}

		sessionID := createResp.SessionID
		logging.Info("[TerminalWS] Session created",
			"session_id", sessionID,
			"pid", createResp.PID,
			"user_id", userID,
			"working_dir", workingDir,
		)

		// Send init message to browser.
		writeWSJSON(conn, wsMessage{Type: "init", PID: createResp.PID})

		// --- Subscribe to terminal output ---
		outputCh, unsub, err := router.SubscribeTerminalOutput(ctx, userID, sessionID)
		if err != nil {
			writeWSError(conn, fmt.Sprintf("subscribe terminal output: %v", err))
			return
		}
		defer unsub()

		// --- WebSocket keepalive ---
		// Set initial pong deadline; each pong resets it.
		_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
			return nil
		})

		// --- Pump goroutines ---
		var wg sync.WaitGroup
		wg.Add(3)

		var pumpErr error
		var errOnce sync.Once
		setPumpErr := func(e error) {
			errOnce.Do(func() {
				pumpErr = e
				cancel()
			})
		}

		// Ping pump: sends periodic pings to keep the connection alive
		// through proxies and to detect dead clients.
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(wsPingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
						setPumpErr(nil)
						return
					}
				}
			}
		}()

		// Output pump: daemon -> WebSocket
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case evt, ok := <-outputCh:
					if !ok {
						setPumpErr(nil)
						return
					}
					if evt.Error != "" {
						writeWSJSON(conn, wsMessage{Type: "error", Data: evt.Error})
						setPumpErr(nil)
						return
					}
					if evt.Closed {
						writeWSJSON(conn, wsMessage{
							Type: "exit",
							Data: fmt.Sprintf("Process exited with code %d", evt.ExitCode),
						})
						setPumpErr(nil)
						return
					}
					if len(evt.Data) > 0 {
						writeWSJSON(conn, wsMessage{Type: "output", Data: string(evt.Data)})
					}
				}
			}
		}()

		// Input pump: WebSocket -> daemon
		go func() {
			defer wg.Done()
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
						logging.Debug("[TerminalWS] WebSocket read error", "error", err, "session_id", sessionID)
					}
					setPumpErr(nil)
					return
				}

				// Try to parse as JSON resize message.
				var resize wsResizeMessage
				if json.Unmarshal(raw, &resize) == nil && resize.Type == "resize" {
					if err := router.SendTerminalResize(ctx, userID, sessionID, resize.Cols, resize.Rows); err != nil {
						logging.Error("[TerminalWS] Send resize failed", "error", err, "session_id", sessionID)
						setPumpErr(err)
						return
					}
					continue
				}

				// Otherwise treat as raw PTY input.
				if err := router.SendTerminalInput(ctx, userID, sessionID, raw); err != nil {
					logging.Error("[TerminalWS] Send input failed", "error", err, "session_id", sessionID)
					setPumpErr(err)
					return
				}
			}
		}()

		wg.Wait()
		if pumpErr != nil {
			logging.Error("[TerminalWS] Session ended with error", "error", pumpErr, "session_id", sessionID)
		} else {
			logging.Info("[TerminalWS] Session ended", "session_id", sessionID, "user_id", userID)
		}
	}
}

// writeWSJSON marshals msg and writes it as a text message. Errors are logged but not returned
// because the WebSocket may already be closing.
func writeWSJSON(conn *websocket.Conn, msg wsMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		logging.Error("[TerminalWS] Failed to marshal WS message", "error", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		logging.Debug("[TerminalWS] Failed to write WS message", "error", err)
	}
}

// writeWSError sends an error message to the browser.
func writeWSError(conn *websocket.Conn, msg string) {
	logging.Error("[TerminalWS] Error", "message", msg)
	writeWSJSON(conn, wsMessage{Type: "error", Data: msg})
}
