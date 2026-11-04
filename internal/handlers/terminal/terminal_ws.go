// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reliant-labs/forge/pkg/serverkit"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/checkoutstate"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	// wsPingInterval is how often the server sends a websocket ping frame.
	wsPingInterval = 30 * time.Second
	// wsPongTimeout is how long the server waits for a pong before closing.
	wsPongTimeout = 45 * time.Second
	// wsDrainCloseGrace bounds how long a connection the server is closing
	// for shutdown waits for the browser's answering close frame.
	wsDrainCloseGrace = 2 * time.Second
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
//
// DaemonID names the machine the terminal went to, on "init" and on every
// machine-specific error, and MachineName is what to call it. The browser
// shows it on every terminal, closes the session there, and names that machine
// when it is not up, not answering, or the folder is not on it. Both are empty
// only when no machine resolved at all.
type wsMessage struct {
	Type        string `json:"type"`
	Data        string `json:"data,omitempty"`
	Code        string `json:"code,omitempty"`
	PID         int32  `json:"pid,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	DaemonID    string `json:"daemon_id,omitempty"`
	MachineName string `json:"machine_name,omitempty"`
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

// Codes of an "error" sent when the machine, not the command, is why no
// terminal opened. The browser keys on these, never on the message text
// (13-p9-frontend-plan.md §7, R3w2).
const (
	// wsErrorNoMachine: the account has no machine at all. Nothing to wait
	// for; the browser offers to connect one and stops retrying.
	wsErrorNoMachine = "no_machine"
	// wsErrorDaemonUnavailable: the machine is starting, asleep or not
	// connected. The browser shows the wait and retries.
	wsErrorDaemonUnavailable = "daemon_unavailable"
	// wsErrorMachineNotAnswering: the machine is connected but did not open
	// the shell within terminalCreateTimeout — saturated or wedged. The
	// browser says so, naming the machine, and offers a retry instead of
	// spinning: asking a machine at load 220 again every few seconds only
	// adds to its load.
	wsErrorMachineNotAnswering = "machine_not_answering"
	// wsErrorCheckoutMissing: the terminal's machine has no checkout of the
	// project. Retrying cannot create one; the message names the machine and
	// the way out (switch to a machine that has it, or clone it there).
	wsErrorCheckoutMissing = "checkout_missing"
)

// terminalCreateTimeout bounds terminal.create, the part of the browser's
// wait for "init" or "error" that depends on the machine. The browser's own
// bound (TERMINAL_CONNECT_TIMEOUT_MS in Terminal.tsx) sits above it. A var so
// tests can shrink it.
var terminalCreateTimeout = 30 * time.Second

// terminalCheckouts is what TerminalWSHandler reads to tell a working
// directory that may still appear from one that will not. The api-server
// cannot see the filesystem, so this is the workspace's recorded state.
//
// It is also where the terminal's machine comes from: GetChat with GetWorktree
// is daemonplacement.WorkspaceReader.
type terminalCheckouts interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*db.ProjectDaemon, error)
	// GetDaemon names the terminal's machine, and confirms a checkout's
	// machine is the caller's (projectcheckout).
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// TerminalWSOption configures TerminalWSHandler.
type TerminalWSOption func(*terminalWSConfig)

type terminalWSConfig struct {
	checkouts terminalCheckouts
}

// WithTerminalCheckouts lets the handler open the terminal on the machine that
// holds its workspace (see terminalMachine), and classify a missing working
// directory as one that may still appear (working_dir_unavailable) or one that
// will not (working_dir_missing). Without it the terminal goes to the default
// machine and every missing directory is working_dir_unavailable, which the
// browser waits on.
func WithTerminalCheckouts(checkouts terminalCheckouts) TerminalWSOption {
	return func(c *terminalWSConfig) { c.checkouts = checkouts }
}

// workingDirMayAppear reports whether a working directory the daemon could
// not find may still be created. When the answer cannot be known it is true:
// waiting on a directory that never appears costs a retry loop, while giving
// up on a clone that is about to land strands the user.
//
// daemonID is the machine the terminal reached ("" when default resolution
// picked it).
func (c terminalWSConfig) workingDirMayAppear(ctx context.Context, router toolexec.DaemonRouter, userID, daemonID, worktreeID string) bool {
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
	if daemonID == "" {
		daemonID, _ = router.ResolveDaemonID(ctx, userID)
	}
	state, _ := checkoutstate.On(rows, daemonID)
	return state == reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING
}

// terminalPlacement is where a terminal opens: the machine every command of
// the connection goes to, and the directory ON THAT MACHINE its shell starts
// in.
type terminalPlacement struct {
	daemonID   string
	workingDir string
}

// placementRefusal is a terminal that cannot open on its machine for a reason
// the user can act on: the project is not checked out there. It is said to
// the browser with its code and the machine, and is not a server fault.
type placementRefusal struct {
	code     string
	message  string
	daemonID string
}

func (r *placementRefusal) Error() string { return r.message }

// terminalCheckoutPurpose finishes projectcheckout's refusal sentence ("clone
// it onto <machine> to <purpose>").
const terminalCheckoutPurpose = "open a terminal in it"

// place picks the terminal's machine and working directory.
//
// The machine is the rule the Files tab reads by
// (daemonplacement.WorkspaceDaemon): an owned worktree's owner, else the
// chat's machine — where the chat's agent runs, so the terminal sees the same
// disk. A branch workspace exists on one machine's disk; opening its terminal
// on the default machine reported the workspace as archived or removed.
//
// The project's main checkout is on every machine that holds the project, at
// a path of THAT machine's (projects.path, which the browser sends, is the
// path on the machine that created it). So for the main checkout the machine
// and the directory come from that machine's project_daemons row
// (projectcheckout.ForMachine), and a machine without a checkout is refused by
// name rather than opened in a directory that is not there. With no chat, the
// machine is the one the project's work is placed on, never the account's
// arbitrary default: a terminal opened with no chat used to land on whichever
// of the user's machines answered first.
//
// Whatever decides it, the machine is resolved here and pinned, so the
// browser is told which machine the terminal is on and every later command
// goes to the same one.
func (c terminalWSConfig) place(ctx context.Context, router toolexec.DaemonRouter, userID, projectID, worktreeID, chatID, workingDir string) (terminalPlacement, error) {
	p := terminalPlacement{workingDir: workingDir}
	if c.checkouts != nil {
		ws, err := daemonplacement.WorkspaceDaemon(ctx, c.checkouts, userID, projectID, worktreeID, chatID)
		if err != nil {
			return terminalPlacement{}, err
		}
		p.daemonID = ws.DaemonID
		if projectID != "" && c.isMainCheckout(ctx, worktreeID) {
			if err := c.placeOnCheckout(ctx, router, userID, projectID, &p); err != nil {
				return terminalPlacement{}, err
			}
		}
	}
	if p.daemonID == "" {
		// Pinned to what default resolution names now. When nothing
		// resolves, the create below reports the machine's state.
		if resolved, err := router.ResolveDaemonID(ctx, userID); err == nil {
			p.daemonID = resolved
		}
	}
	return p, nil
}

// isMainCheckout reports whether worktreeID names the project's main checkout
// ("" is the main checkout too). A branch workspace, or one whose row cannot
// be read, is not.
func (c terminalWSConfig) isMainCheckout(ctx context.Context, worktreeID string) bool {
	if worktreeID == "" {
		return true
	}
	wt, err := c.checkouts.GetWorktree(ctx, worktreeID)
	return err == nil && wt != nil && wt.IsMain
}

// placeOnCheckout moves p onto the project's main checkout on p.daemonID ("":
// the machine the project's work is placed on), or refuses with the machine
// named.
func (c terminalWSConfig) placeOnCheckout(ctx context.Context, router toolexec.DaemonRouter, userID, projectID string, p *terminalPlacement) error {
	project, err := c.checkouts.GetProject(ctx, projectID)
	if err != nil || project == nil || project.UserID != userID {
		return daemonplacement.ErrForeignWorkspace
	}
	machines, _ := router.(daemonplacement.Machines)
	target, err := projectcheckout.ForMachine(ctx, c.checkouts, machines, userID, project, p.daemonID, terminalCheckoutPurpose)
	if err != nil {
		if !projectcheckout.IsMissing(err) {
			return err
		}
		code := wsErrorCheckoutMissing
		if rows, rowsErr := c.checkouts.ListProjectDaemonsForProject(ctx, projectID); rowsErr == nil {
			if state, _ := checkoutstate.On(rows, p.daemonID); state == reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING {
				// A clone landing will create it: the browser waits.
				code = wsErrorWorkingDirUnavailable
			}
		}
		return &placementRefusal{code: code, message: err.Error(), daemonID: p.daemonID}
	}
	p.daemonID = target.DaemonID
	p.workingDir = target.Rebase(p.workingDir)
	return nil
}

// machineName is what the browser calls the terminal's machine, by the rule
// every server-side sentence uses (daemonplacement.MachineName), as a label
// rather than inside a sentence: a name the owner gave is unquoted. Only the
// caller's own machines are named; anything else is its short id.
func (c terminalWSConfig) machineName(ctx context.Context, userID, daemonID string) string {
	if daemonID == "" {
		return ""
	}
	var names daemonplacement.DaemonReader
	if c.checkouts != nil {
		names = ownMachines{r: c.checkouts, userID: userID}
	}
	name := daemonplacement.MachineName(ctx, names, daemonID)
	if label, err := strconv.Unquote(name); err == nil {
		return label
	}
	return name
}

// ownMachines reads only userID's machine records.
type ownMachines struct {
	r      daemonplacement.DaemonReader
	userID string
}

func (o ownMachines) GetDaemon(ctx context.Context, id string) (*db.Daemon, error) {
	d, err := o.r.GetDaemon(ctx, id)
	if err != nil || d == nil || d.UserID != o.userID {
		return nil, err
	}
	return d, nil
}

// wsMessagePong answers a browser "ping". The browser cannot see protocol-level
// ping frames, and a laptop that slept can keep a dead socket in OPEN state
// until TCP notices; it pings after waking and replaces the socket when no
// answer (or anything else) arrives.
const (
	wsClientPing = "ping"
	wsServerPong = "pong"
)

// wsResizeMessage is the JSON message the browser sends for resize events and
// liveness pings (Type "resize" / "ping").
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
//   - projectId:  (optional) the project the terminal belongs to
//   - chatId:     (optional) the chat the terminal was opened beside; on the
//     main checkout its machine is the terminal's (see place)
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
		chatID := q.Get("chatId")

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
			logging.WarnContext(r.Context(), "[TerminalWS] Invalid token", "error", err)
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		userID := claims.Sub
		// Every line this connection logs says whose terminal it was.
		logFields := []any{"user_id", userID, "project_id", projectID, "worktree_id", worktreeID, "chat_id", chatID}

		// --- Upgrade to WebSocket ---
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			logging.ErrorContext(r.Context(), "[TerminalWS] WebSocket upgrade failed", append([]any{"error", err}, logFields...)...)
			return
		}
		defer conn.Close()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// The server is draining (the route is serverkit.LongLived): close
		// with 1012, service restart, which the browser reconnects on — not
		// 1000, which it reads as "the shell exited". The alternative was the
		// process exit dropping the TCP connection, with
		// closeDaemonTerminalSession below never running and the daemon's PTY
		// leaked. The read deadline bounds the wait for the browser's
		// answering close frame, which is what returns the input pump's read;
		// the drain waits for this handler to return, with ctx still live for
		// the session close.
		stopDrainClose := serverkit.AfterDrain(r.Context(), func() {
			deadline := time.Now().Add(wsDrainCloseGrace)
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server restarting"), deadline)
			_ = conn.SetReadDeadline(deadline)
		})
		defer stopDrainClose()

		// --- Pick the machine ---
		// Every command of this connection — create, input, resize, output,
		// close — goes to the one machine holding the workspace.
		placement, err := cfg.place(ctx, router, userID, projectID, worktreeID, chatID, workingDir)
		var refusal *placementRefusal
		switch {
		case errors.As(err, &refusal):
			// The user's state, said with the machine: INFO, not an error.
			logging.InfoContext(r.Context(), "[TerminalWS] Project not checked out on the terminal's machine",
				append([]any{"code", refusal.code, "machine", refusal.daemonID, "error", err}, logFields...)...)
			writeWSJSON(conn, wsMessage{Type: "error", Code: refusal.code, Data: refusal.message,
				DaemonID: refusal.daemonID, MachineName: cfg.machineName(ctx, userID, refusal.daemonID)})
			return
		case errors.Is(err, daemonplacement.ErrForeignChat):
			writeWSError(conn, "chat not found", logFields...)
			return
		case errors.Is(err, daemonplacement.ErrForeignWorkspace):
			writeWSError(conn, "worktree not found", logFields...)
			return
		case err != nil:
			writeWSError(conn, "resolve the workspace's machine", append([]any{"error", err}, logFields...)...)
			return
		}
		daemonID := placement.daemonID
		machineName := cfg.machineName(ctx, userID, daemonID)
		if daemonID != "" {
			logFields = append(logFields, "daemon_id", daemonID)
			ctx = toolexec.WithDaemonSelector(ctx, &toolexec.DaemonSelector{ID: daemonID})
		}

		// --- Create terminal session via daemon ---
		createReq := map[string]any{
			"working_dir": placement.workingDir,
		}
		if worktreeID != "" {
			createReq["worktree_id"] = worktreeID
		}
		payload, err := json.Marshal(createReq)
		if err != nil {
			writeWSError(conn, fmt.Sprintf("marshal create request: %v", err), logFields...)
			return
		}

		// Bounded as a whole: a saturated machine must end in an answer the
		// browser can show, not a spinner (see terminalCreateTimeout).
		createCtx, cancelCreate := context.WithTimeout(ctx, terminalCreateTimeout)
		respBytes, err := sendToMachine(createCtx, router, userID, daemonID, "terminal.create", payload, int32(terminalCreateTimeout/time.Millisecond))
		cancelCreate()
		if err != nil {
			if terminal.IsWorkingDirUnavailable(err) {
				// The user's own state — a directory that is not there — so
				// INFO, not an error.
				mayAppear := cfg.workingDirMayAppear(ctx, router, userID, daemonID, worktreeID)
				code := wsErrorWorkingDirMissing
				if mayAppear {
					code = wsErrorWorkingDirUnavailable
				}
				logging.InfoContext(r.Context(), "[TerminalWS] Working directory unavailable",
					append([]any{"requested_working_dir", workingDir, "working_dir", placement.workingDir, "may_appear", mayAppear, "error", err}, logFields...)...)
				writeWSJSON(conn, wsMessage{Type: "error", Code: code, Data: err.Error(), DaemonID: daemonID, MachineName: machineName})
				return
			}
			message := fmt.Sprintf("create terminal session: %v", err)
			// No machine, one that is not up yet, or one that is up but not
			// answering is the machine's state: the browser shows it, naming
			// the machine. Said once per window (see logTerminalMachineState).
			if state, ok := terminalMachineState(err); ok {
				logTerminalMachineState(ctx, router, userID, daemonID, state, err, logFields)
				writeWSJSON(conn, wsMessage{Type: "error", Code: terminalMachineStateCode(state), Data: message, DaemonID: daemonID, MachineName: machineName})
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
		defer closeDaemonTerminalSession(ctx, router, userID, daemonID, sessionID)
		// Both directories, so a terminal in the wrong place is diagnosable
		// from this line alone: this server cannot see the filesystem.
		logFields = append(logFields, "session_id", sessionID)
		logging.InfoContext(r.Context(), "[TerminalWS] Session created",
			append([]any{
				"pid", createResp.PID,
				"requested_working_dir", workingDir,
				"working_dir", createResp.WorkingDir,
			}, logFields...)...)

		// Send init message to browser, carrying the daemon's session id so the
		// browser can address this session on close. See wsMessage.SessionID.
		writeWSJSON(conn, wsMessage{Type: "init", PID: createResp.PID, SessionID: sessionID, DaemonID: daemonID, MachineName: machineName})

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
			// Once the connection is ending, a late pong must not push back
			// the drain's close deadline.
			if ctx.Err() != nil {
				return nil
			}
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

		// pongCh hands a browser ping to the output pump, the only goroutine
		// that writes data frames: gorilla allows one concurrent writer.
		pongCh := make(chan struct{}, 1)

		// Output pump: daemon -> WebSocket
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-pongCh:
					writeWSJSON(conn, wsMessage{Type: wsServerPong})
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
						logging.DebugContext(r.Context(), "[TerminalWS] WebSocket read error", append([]any{"error", err}, logFields...)...)
					}
					setPumpErr(nil)
					return
				}

				// Try to parse as a JSON control message.
				var ctl wsResizeMessage
				ctlOK := json.Unmarshal(raw, &ctl) == nil
				if ctlOK && ctl.Type == wsClientPing {
					select {
					case pongCh <- struct{}{}:
					default: // a pong is already queued; it answers this ping too
					}
					continue
				}
				if ctlOK && ctl.Type == "resize" {
					if err := router.SendTerminalResize(ctx, userID, sessionID, ctl.Cols, ctl.Rows); err != nil {
						logging.ErrorContext(r.Context(), "[TerminalWS] Send resize failed", append([]any{"error", err}, logFields...)...)
						setPumpErr(err)
						return
					}
					continue
				}

				// Otherwise treat as raw PTY input.
				if err := router.SendTerminalInput(ctx, userID, sessionID, raw); err != nil {
					logging.ErrorContext(r.Context(), "[TerminalWS] Send input failed", append([]any{"error", err}, logFields...)...)
					setPumpErr(err)
					return
				}
			}
		}()

		wg.Wait()
		if pumpErr != nil {
			logging.ErrorContext(r.Context(), "[TerminalWS] Session ended with error", append([]any{"error", pumpErr}, logFields...)...)
		} else {
			logging.InfoContext(r.Context(), "[TerminalWS] Session ended", logFields...)
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
// there, not being up, or not answering goes through logTerminalMachineState
// instead.
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

// terminalMachineNotAnswering is the machine state of a daemon that is
// connected but did not answer terminal.create before its deadline.
const terminalMachineNotAnswering = "not_answering"

// terminalMachineState names why a terminal could not be created when the
// reason is the machine's state rather than the command itself — the
// distinctions daemonerr.MachineState draws for every daemon-proxy
// RPC. ok is false for anything else, which is a real failure.
//
//   - no_machine, starting_or_asleep, not_connected (NATS had no responder):
//     the machine is not up. The UI already shows that; it is not an error.
//   - not_answering: the machine is connected — NATS delivered the request to
//     its live subscription — but did not answer in time
//     (toolexec.ErrDaemonTimeout). Nothing the user did explains a connected
//     daemon that cannot open a shell, so it stays an error.
func terminalMachineState(err error) (state string, ok bool) {
	switch {
	case toolexec.IsNoDaemon(err):
		return "no_machine", true
	case toolexec.IsDaemonPending(err):
		return "starting_or_asleep", true
	case daemonerr.Unreachable(err):
		return "not_connected", true
	case toolexec.IsDaemonTimeout(err), errors.Is(err, context.DeadlineExceeded):
		// The second: terminalCreateTimeout ended the wait somewhere the
		// router does not wrap as its own timeout.
		return terminalMachineNotAnswering, true
	default:
		return "", false
	}
}

// terminalMachineStateCode is the wsMessage code the browser reads for a
// machine state.
func terminalMachineStateCode(state string) string {
	switch state {
	case "no_machine":
		return wsErrorNoMachine
	case "starting_or_asleep", "not_connected":
		return wsErrorDaemonUnavailable
	case terminalMachineNotAnswering:
		return wsErrorMachineNotAnswering
	default:
		return ""
	}
}

// logTerminalMachineState logs a machine state at most once per window per
// user and state, with how many were suppressed since and the machine: the one
// the terminal was sent to, else the one default resolution names, when it
// names one.
//
// A machine that is not up is INFO. A connected machine that did not answer
// is ERROR, but it is still one machine's state, said once per window rather
// than once per browser retry: one such machine produced 24 "[TerminalWS]
// Error" events in five minutes (ELECTRON-CZ, 2026-10-11) — four terminals,
// each retrying every ~62s, the 30s timeout plus the browser's backoff.
func logTerminalMachineState(ctx context.Context, router toolexec.DaemonRouter, userID, daemonID, state string, err error, logFields []any) {
	suppressed, ok := terminalMachineStateLog.Allow(userID + "|" + state)
	if !ok {
		return
	}
	fields := append([]any{"machine_state", state, "error", err, "suppressed_since_last", suppressed}, logFields...)
	if daemonID == "" {
		if resolved, resolveErr := router.ResolveDaemonID(ctx, userID); resolveErr == nil {
			fields = append(fields, "daemon_id", resolved)
		}
	}
	if state == terminalMachineNotAnswering {
		logging.ErrorContext(ctx, "[TerminalWS] Machine is connected but did not answer; the browser retries", fields...)
		return
	}
	logging.InfoContext(ctx, "[TerminalWS] Machine not ready; the browser shows it and retries", fields...)
}
