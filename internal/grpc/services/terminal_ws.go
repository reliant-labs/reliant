// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/terminal"
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
//
// SessionID is the DAEMON's session id, and the browser needs it to close the
// session later. The browser mints its own local id first (it needs a React key
// before the socket exists), so without this field it had no way to learn the
// real one — and CloseSession was called with the local id, which the daemon
// has never heard of. That failed every close and leaked the PTY.
//
// Code classifies an "error" the browser must handle differently from a
// broken session. See wsErrorWorkingDirUnavailable.
type wsMessage struct {
	Type      string `json:"type"`
	Data      string `json:"data,omitempty"`
	Code      string `json:"code,omitempty"`
	PID       int32  `json:"pid,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// Codes of an "error" sent when the daemon refused the requested working
// directory. Neither is a server fault, so neither is logged as an error.
const (
	// wsErrorWorkingDirUnavailable: the directory may still appear — most
	// often a project whose clone has not finished, a workspace still being
	// created, or one the server cannot classify. The browser waits for the
	// directory and retries, instead of spending its reconnect budget as it
	// would on a broken session.
	wsErrorWorkingDirUnavailable = "working_dir_unavailable"

	// wsErrorWorkingDirMissing: nothing is going to create the directory —
	// its workspace was archived or deleted, failed to set up, or finished
	// setting up and the directory has since been removed. Retrying cannot
	// succeed; the browser stops and says so. In prod one persisted terminal
	// for a removed worktree retried every ~12s for five hours (905 refusals
	// on 2026-10-08) while the server answered working_dir_unavailable.
	wsErrorWorkingDirMissing = "working_dir_missing"
)

// terminalCheckouts is what TerminalWSHandler reads to tell a working
// directory that may still appear from one that will not. The api-server
// cannot see the filesystem, so this is the workspace's recorded state.
type terminalCheckouts interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*db.ProjectDaemon, error)
}

// TerminalWSOption configures TerminalWSHandler.
type TerminalWSOption func(*terminalWSConfig)

type terminalWSConfig struct {
	checkouts terminalCheckouts
}

// WithTerminalCheckouts lets the handler classify a missing working directory
// as one that may still appear (working_dir_unavailable) or one that will not
// (working_dir_missing). Without it every missing directory is
// working_dir_unavailable, which the browser waits on.
func WithTerminalCheckouts(checkouts terminalCheckouts) TerminalWSOption {
	return func(c *terminalWSConfig) { c.checkouts = checkouts }
}

// workingDirMayAppear reports whether a working directory the daemon could
// not find may still be created. When the answer cannot be known it is true:
// waiting on a directory that never appears costs a retry loop, while giving
// up on a clone that is about to land strands the user.
func (c terminalWSConfig) workingDirMayAppear(ctx context.Context, router toolexec.DaemonRouter, userID, worktreeID string) bool {
	if c.checkouts == nil || worktreeID == "" {
		return true
	}
	wt, err := c.checkouts.GetWorktree(ctx, worktreeID)
	switch {
	case errors.Is(err, core.ErrWorktreeNotFound):
		return false
	case err != nil || wt == nil:
		return true
	case wt.DeletedAt != nil:
		return false // archived
	}
	switch reliantv1.WorktreeStatus(wt.Status) {
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING:
		return true
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED:
		return false
	}
	if !wt.IsMain {
		// Set up and since removed: nothing recreates a worktree directory.
		return false
	}
	// The main checkout is the project's clone; only a clone still installing
	// on the machine this terminal reached will create it.
	rows, err := c.checkouts.ListProjectDaemonsForProject(ctx, wt.ProjectID)
	if err != nil {
		return true
	}
	daemonID, _ := router.ResolveDaemonID(ctx, userID)
	state, _ := checkoutStateOn(rows, daemonID)
	return state == reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING
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
//   - token:      JWT token used for auth
//   - workingDir: directory to start the shell in
//   - worktreeId: (optional) worktree identifier
//   - projectId:  (optional) the project the terminal belongs to; logged only
//
// The handler works identically with NATSDaemonRouter (daemon-gateway) and
// LocalDaemonRouter.
func TerminalWSHandler(router toolexec.DaemonRouter, validator auth.TokenValidator, opts ...TerminalWSOption) http.HandlerFunc {
	var cfg terminalWSConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		token := q.Get("token")
		workingDir := q.Get("workingDir")
		worktreeID := q.Get("worktreeId")
		projectID := q.Get("projectId")

		// --- Authenticate ---
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
		userID := claims.Sub
		// Every line this connection logs says whose terminal it was.
		logFields := []any{"user_id", userID, "project_id", projectID, "worktree_id", worktreeID}

		// --- Upgrade to WebSocket ---
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			logging.Error("[TerminalWS] WebSocket upgrade failed", append([]any{"error", err}, logFields...)...)
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
			writeWSError(conn, fmt.Sprintf("marshal create request: %v", err), logFields...)
			return
		}

		respBytes, err := router.SendDaemonCommand(ctx, userID, "terminal.create", payload, 30000)
		if err != nil {
			if terminal.IsWorkingDirUnavailable(err) {
				// The user's own state — a directory that is not there — so
				// INFO, not an error.
				mayAppear := cfg.workingDirMayAppear(ctx, router, userID, worktreeID)
				code := wsErrorWorkingDirMissing
				if mayAppear {
					code = wsErrorWorkingDirUnavailable
				}
				logging.Info("[TerminalWS] Working directory unavailable",
					append([]any{"requested_working_dir", workingDir, "may_appear", mayAppear, "error", err}, logFields...)...)
				writeWSJSON(conn, wsMessage{Type: "error", Code: code, Data: err.Error()})
				return
			}
			message := fmt.Sprintf("create terminal session: %v", err)
			// No machine, or one that is not up yet, is the machine's state:
			// the browser shows it and retries. Said once, below ERROR.
			if state, ok := terminalMachineState(err); ok {
				logTerminalMachineState(ctx, router, userID, state, err, logFields)
				writeWSJSON(conn, wsMessage{Type: "error", Data: message})
				return
			}
			writeWSError(conn, message, append([]any{"error", err}, logFields...)...)
			return
		}

		var createResp struct {
			SessionID string `json:"session_id"`
			PID       int32  `json:"pid"`
			// Where the shell actually started. Empty only from a daemon
			// that predates reporting it.
			WorkingDir string `json:"working_dir"`
		}
		if err := json.Unmarshal(respBytes, &createResp); err != nil {
			writeWSError(conn, fmt.Sprintf("unmarshal create response: %v", err), logFields...)
			return
		}

		sessionID := createResp.SessionID
		// This connection owns the session, so it closes it however the
		// connection ends — including every early return below.
		defer closeDaemonTerminalSession(ctx, router, userID, sessionID)
		// Both directories, so a terminal in the wrong place is diagnosable
		// from this line alone: this server cannot see the filesystem.
		logFields = append(logFields, "session_id", sessionID)
		logging.Info("[TerminalWS] Session created",
			append([]any{
				"pid", createResp.PID,
				"requested_working_dir", workingDir,
				"working_dir", createResp.WorkingDir,
			}, logFields...)...)

		// Send init message to browser, carrying the daemon's session id so the
		// browser can address this session on close. See wsMessage.SessionID.
		writeWSJSON(conn, wsMessage{Type: "init", PID: createResp.PID, SessionID: sessionID})

		// --- Subscribe to terminal output ---
		outputCh, unsub, err := router.SubscribeTerminalOutput(ctx, userID, sessionID)
		if err != nil {
			writeWSError(conn, fmt.Sprintf("subscribe terminal output: %v", err), logFields...)
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
						logging.Debug("[TerminalWS] WebSocket read error", append([]any{"error", err}, logFields...)...)
					}
					setPumpErr(nil)
					return
				}

				// Try to parse as JSON resize message.
				var resize wsResizeMessage
				if json.Unmarshal(raw, &resize) == nil && resize.Type == "resize" {
					if err := router.SendTerminalResize(ctx, userID, sessionID, resize.Cols, resize.Rows); err != nil {
						logging.Error("[TerminalWS] Send resize failed", append([]any{"error", err}, logFields...)...)
						setPumpErr(err)
						return
					}
					continue
				}

				// Otherwise treat as raw PTY input.
				if err := router.SendTerminalInput(ctx, userID, sessionID, raw); err != nil {
					logging.Error("[TerminalWS] Send input failed", append([]any{"error", err}, logFields...)...)
					setPumpErr(err)
					return
				}
			}
		}()

		wg.Wait()
		if pumpErr != nil {
			logging.Error("[TerminalWS] Session ended with error", append([]any{"error", pumpErr}, logFields...)...)
		} else {
			logging.Info("[TerminalWS] Session ended", logFields...)
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

// writeWSError sends an error message to the browser and logs it at ERROR
// with the connection's context. Only for real failures: the machine not being
// there or not being up goes through logTerminalMachineState instead.
func writeWSError(conn *websocket.Conn, msg string, logFields ...any) {
	logging.Error("[TerminalWS] Error", append([]any{"message", msg}, logFields...)...)
	writeWSJSON(conn, wsMessage{Type: "error", Data: msg})
}

// terminalMachineStateLogWindow bounds how often one user's terminal logs the
// same machine state. The browser retries a terminal it could not open every
// 10–20 s for as long as the machine is down: one user's crash-looping machine
// produced 1,019 ERROR lines in five hours on 2026-10-09, each with no user,
// project or machine id to say whose.
const terminalMachineStateLogWindow = 5 * time.Minute

var terminalMachineStateLog = logging.NewThrottle(terminalMachineStateLogWindow, time.Now)

// terminalMachineState names why a terminal could not be created when the
// reason is the machine's state rather than a failure: the user has no
// machine, it is starting or asleep, or it is not connected (NATS had no
// responder). ok is false for anything else, which is a real failure.
func terminalMachineState(err error) (state string, ok bool) {
	switch {
	case toolexec.IsNoDaemon(err):
		return "no_machine", true
	case toolexec.IsDaemonPending(err):
		return "starting_or_asleep", true
	case machineUnreachable(err):
		return "not_connected", true
	default:
		return "", false
	}
}

// logTerminalMachineState logs an expected machine state at INFO, at most once
// per window per user and state, with how many were suppressed since and the
// machine default resolution names, when it names one.
func logTerminalMachineState(ctx context.Context, router toolexec.DaemonRouter, userID, state string, err error, logFields []any) {
	suppressed, ok := terminalMachineStateLog.Allow(userID + "|" + state)
	if !ok {
		return
	}
	fields := append([]any{"machine_state", state, "error", err, "suppressed_since_last", suppressed}, logFields...)
	if daemonID, resolveErr := router.ResolveDaemonID(ctx, userID); resolveErr == nil {
		fields = append(fields, "daemon_id", daemonID)
	}
	logging.Info("[TerminalWS] Machine not ready; the browser shows it and retries", fields...)
}
