// Copyright (c) 2026 Reliant Labs
package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// TerminalCLILoginWSPath is the websocket `reliant machine login <vendor>
// --machine <other machine>` signs a vendor agent CLI in through: a terminal
// session on that machine running the vendor's own sign-in, and nothing else.
//
// It is a route of its own, not a mode of /api/v2/terminal/ws, so that a
// server that predates it answers 404 — which the CLI reports as "this server
// is too old" — instead of ignoring an unknown parameter and opening a plain
// shell.
const TerminalCLILoginWSPath = "/api/v2/terminal/cli-login/ws"

// WSErrorDaemonOutdated is the "error" code of a machine whose daemon predates
// toolexec.TerminalCreateCLILoginCommand: the person must update Reliant
// there. The daemon is never asked for a shell instead.
const WSErrorDaemonOutdated = "daemon_outdated"

// outcomeDaemonOutdated is the session outcome of WSErrorDaemonOutdated.
const outcomeDaemonOutdated = WSErrorDaemonOutdated

// errCLILoginNeedsPerson is the refusal of a credential that is not a
// person's own sign-in: a machine's credential, or an access token without
// daemon:manage (made before `reliant auth login` asked for it, or minted for
// a script).
const errCLILoginNeedsPerson = "signing a vendor CLI in on a machine needs your own login with the daemon:manage permission: run 'reliant auth login' (a machine's own credential, or a token minted for a script, cannot)"

// TerminalCLILoginWSHandler returns the websocket at TerminalCLILoginWSPath.
//
// Query parameters:
//   - daemonId: the machine (required); the caller must own it
//   - vendor:   a vendorcli id (required)
//   - cols, rows: (optional) the person's terminal size
//
// The bearer is the Authorization header (`Bearer <token>`), or the token
// query parameter as the browser terminal sends it. It is the CLI login — an
// `rlat_` access token, resolved by accessTokens — which must be a person's
// (not bound to a machine) and carry daemon:manage, the permission the other
// machine commands need; or a session JWT or a self-host deployment's shared
// key (validator), both a person's.
//
// The daemon is sent toolexec.TerminalCreateCLILoginCommand with the vendor
// and the size only: it builds the program from its own recipe table. The
// session's bytes are a sign-in — a URL or device code out, perhaps a pasted
// one-time code in — and, as for every terminal, are moved and counted, never
// logged (pumpTerminalSession).
func TerminalCLILoginWSHandler(router toolexec.DaemonRouter, validator auth.TokenValidator, accessTokens auth.AccessTokenIntrospector, machines daemonplacement.DaemonReader, opts ...TerminalWSOption) http.HandlerFunc {
	cfg := terminalWSConfig{createTimeout: terminalCreateTimeout, pongTimeout: wsPongTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	tel := newTerminalWSTelemetry(cfg.telemetry)
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		daemonID := strings.TrimSpace(q.Get("daemonId"))
		vendorID := strings.TrimSpace(q.Get("vendor"))
		cols, rows := parseTerminalSize(q.Get("cols")), parseTerminalSize(q.Get("rows"))

		// --- Authenticate ---
		ctx, userID, status, msg := authenticateCLILogin(r, validator, accessTokens)
		if status != 0 {
			tel.reject(r.Context(), outcomeUnauthenticated)
			http.Error(w, msg, status)
			return
		}
		if machines == nil {
			http.Error(w, "machine records not configured", http.StatusInternalServerError)
			return
		}
		vendor, ok := vendorcli.Lookup(vendorID)
		if !ok {
			http.Error(w, fmt.Sprintf("unknown vendor CLI %q: want one of %s", vendorID, strings.Join(vendorcli.IDs(), ", ")), http.StatusBadRequest)
			return
		}
		if daemonID == "" {
			http.Error(w, "daemonId is required", http.StatusBadRequest)
			return
		}
		// Only the caller's own machine; any other reads as absent.
		if d, err := machines.GetDaemon(ctx, daemonID); err != nil || !authz.CanWriteMachine(ctx, userID, d) {
			http.Error(w, "no such machine", http.StatusNotFound)
			return
		}

		spanCtx, sess := tel.begin(r, false)
		defer sess.finish(spanCtx)
		spanCtx = telemetry.BindUser(spanCtx, userID)
		logFields := []any{"user_id", userID, "daemon_id", daemonID, "vendor", vendor.ID}

		// --- Upgrade to WebSocket ---
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			sess.end(outcomeUpgradeFailed, err)
			logging.ErrorContext(spanCtx, "[TerminalCLILoginWS] WebSocket upgrade failed", append([]any{"error", err}, logFields...)...)
			return
		}
		defer conn.Close()
		sess.opened(spanCtx)

		ctx, cancel := context.WithCancel(authzContext(spanCtx, ctx))
		defer cancel()

		var draining atomic.Bool
		stopDrainClose := serverkit.AfterDrain(r.Context(), func() {
			draining.Store(true)
			deadline := time.Now().Add(wsDrainCloseGrace)
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server restarting"), deadline)
			_ = conn.SetReadDeadline(deadline)
		})
		defer stopDrainClose()

		machineName := daemonplacement.MachineName(ctx, ownMachines{r: machines, userID: userID}, daemonID)
		if label, err := strconv.Unquote(machineName); err == nil {
			machineName = label
		}
		ctx = toolexec.WithDaemonSelector(ctx, &toolexec.DaemonSelector{ID: daemonID})

		// --- Create the sign-in session on the machine ---
		payload, err := json.Marshal(map[string]any{"vendor": vendor.ID, "cols": cols, "rows": rows})
		if err != nil {
			sess.end(outcomeCreateFailed, err)
			writeWSError(ctx, conn, fmt.Sprintf("marshal create request: %v", err), logFields...)
			return
		}
		createCtx, cancelCreate := context.WithTimeout(ctx, cfg.createTimeout)
		respBytes, err := sendToMachine(createCtx, router, userID, daemonID, toolexec.TerminalCreateCLILoginCommand, payload, int32(cfg.createTimeout/time.Millisecond))
		cancelCreate()
		if err != nil {
			message := fmt.Sprintf("start the %s sign-in: %v", vendor.Name, err)
			switch state, isState := terminalMachineState(err); {
			case isDaemonOutdated(err):
				// The machine's own state, said with its fix: INFO.
				sess.end(outcomeDaemonOutdated, nil)
				logging.InfoContext(ctx, "[TerminalCLILoginWS] Machine's daemon predates CLI sign-in", append([]any{"error", err}, logFields...)...)
				writeWSJSON(conn, wsMessage{Type: "error", Code: WSErrorDaemonOutdated, Data: fmt.Sprintf(
					"machine %s runs a Reliant too old for `reliant machine login`: update Reliant on it, then try again",
					daemonplacement.MachineArg(ctx, ownMachines{r: machines, userID: userID}, daemonID)),
					DaemonID: daemonID, MachineName: machineName})
			case isState:
				sess.end(outcomeMachineUnavailable, err)
				sess.machineState(state)
				logTerminalMachineState(ctx, router, userID, daemonID, state, err, logFields)
				writeWSJSON(conn, wsMessage{Type: "error", Code: terminalMachineStateCode(state), Data: message, DaemonID: daemonID, MachineName: machineName})
			default:
				sess.end(outcomeCreateFailed, err)
				writeWSError(ctx, conn, message, append([]any{"error", err}, logFields...)...)
			}
			return
		}
		var createResp struct {
			SessionID string `json:"session_id"`
			PID       int32  `json:"pid"`
		}
		if err := json.Unmarshal(respBytes, &createResp); err != nil || createResp.SessionID == "" {
			sess.end(outcomeCreateFailed, err)
			writeWSError(ctx, conn, "the machine's answer named no session", logFields...)
			return
		}
		sessionID := createResp.SessionID
		sess.created(sessionID)
		defer closeDaemonTerminalSession(ctx, router, userID, daemonID, sessionID)
		logFields = append(logFields, "session_id", sessionID)
		logging.InfoContext(ctx, "[TerminalCLILoginWS] Sign-in session created", append([]any{"pid", createResp.PID}, logFields...)...)

		writeWSJSON(conn, wsMessage{Type: "init", PID: createResp.PID, SessionID: sessionID, DaemonID: daemonID, MachineName: machineName})
		pumpTerminalSession(ctx, spanCtx, cancel, conn, router, cfg, sess, &draining, userID, sessionID, logFields)
	}
}

// authzContext is spanCtx carrying what authentication put on authCtx: the
// user and, for an access token, its principal (authz binds its org).
func authzContext(spanCtx, authCtx context.Context) context.Context {
	if userID, ok := auth.GetUserIDFromContext(authCtx); ok {
		spanCtx = auth.WithUser(spanCtx, userID, "authenticated", "")
	}
	spanCtx = auth.WithCredential(spanCtx, auth.CredentialFromContext(authCtx))
	if p, ok := auth.MachineTokenFromContext(authCtx); ok {
		spanCtx = auth.WithMachineToken(spanCtx, p)
	}
	return spanCtx
}

// authenticateCLILogin checks the request's bearer. status is 0 when it is a
// person's credential that may sign a vendor CLI in; otherwise it is the HTTP
// status to refuse with, and msg the reason.
func authenticateCLILogin(r *http.Request, validator auth.TokenValidator, accessTokens auth.AccessTokenIntrospector) (ctx context.Context, userID string, status int, msg string) {
	ctx = r.Context()
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return ctx, "", http.StatusUnauthorized, "missing bearer token"
	}
	if auth.IsAccessTokenFormat(token) {
		if accessTokens == nil {
			return ctx, "", http.StatusUnauthorized, "invalid or expired token"
		}
		claims, p, err := auth.ClaimsForAccessToken(ctx, accessTokens, token)
		switch {
		case err != nil && !auth.IsCredentialRejection(err):
			logging.ErrorContext(ctx, "[TerminalCLILoginWS] Token verification unavailable", "error", err)
			return ctx, "", http.StatusServiceUnavailable, "token verification unavailable"
		case err != nil:
			return ctx, "", http.StatusUnauthorized, "invalid or expired token: run 'reliant auth login'"
		case auth.MachineBound(p) || !p.Scopes.Has(fat.ScopeDaemonManage):
			return ctx, "", http.StatusForbidden, errCLILoginNeedsPerson
		}
		ctx = auth.WithUser(ctx, claims.Sub, claims.Role, claims.Email)
		ctx = auth.WithCredential(ctx, claims.Credential)
		ctx = auth.WithMachineToken(ctx, p)
		return ctx, claims.Sub, 0, ""
	}
	if validator == nil {
		return ctx, "", http.StatusInternalServerError, "auth not configured"
	}
	claims, err := validator.ValidateToken(token)
	if err != nil {
		return ctx, "", http.StatusUnauthorized, "invalid or expired token"
	}
	// Only person sessions and self-host shared keys may use this path.
	// Software-held credentials must not bypass access-token scope checks.
	if claims.Credential != auth.CredentialSession && claims.Credential != auth.CredentialSharedKey {
		return ctx, "", http.StatusForbidden, errCLILoginNeedsPerson
	}
	ctx = auth.WithUser(ctx, claims.Sub, claims.Role, claims.Email)
	ctx = auth.WithCredential(ctx, claims.Credential)
	return ctx, claims.Sub, 0, ""
}

// isDaemonOutdated reports a daemon that does not have the command asked
// for: today's wording (daemonerr.IsOutdated), or the bare registry miss of a
// daemon older than that wording.
func isDaemonOutdated(err error) bool {
	return daemonerr.IsOutdated(err) || (err != nil && strings.Contains(err.Error(), "unknown daemon command type"))
}

// parseTerminalSize reads a cols or rows parameter; anything that is not a
// size the PTY can take is 0 (the daemon's default).
func parseTerminalSize(s string) uint16 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n > 1000 {
		return 0
	}
	return uint16(n)
}
