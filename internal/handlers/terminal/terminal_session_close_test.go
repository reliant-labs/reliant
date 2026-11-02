package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// These tests pin one invariant of the terminal websocket: a connection that
// creates a daemon terminal session closes that session when the connection
// ends, however it ends.
//
// A connection cannot reattach to an existing session — every connection
// sends terminal.create — so a session outliving its connection is
// unreachable. Before this was enforced, each dropped WebSocket (reload,
// sleep, API restart, network blip) left a login shell and its PTY alive on
// the user's machine, and the browser's reconnect created another. One
// developer daemon accumulated 501 idle shells, exhausting macOS's PTY pool
// (kern.tty.ptmx_max = 511) so no process on the machine could open a
// terminal: "forkpty: Device not configured".

const terminalTestUserID = "user-terminal-test"

// terminalCommandRouter records the daemon commands a terminal handler sends.
// It embeds toolexectest.Recorder for the rest of toolexec.DaemonRouter.
type terminalCommandRouter struct {
	toolexectest.Recorder

	mu       sync.Mutex
	commands []recordedDaemonCommand
}

type recordedDaemonCommand struct {
	commandType string
	sessionID   string
	// ctxErr is the command context's error at send time. A close sent on
	// the already-cancelled request context would never reach the daemon.
	ctxErr error
}

var _ toolexec.DaemonRouter = (*terminalCommandRouter)(nil)

func newTerminalCommandRouter() *terminalCommandRouter {
	return &terminalCommandRouter{}
}

func (r *terminalCommandRouter) SendDaemonCommand(ctx context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(payload, &req)

	r.mu.Lock()
	r.commands = append(r.commands, recordedDaemonCommand{commandType: commandType, sessionID: req.SessionID, ctxErr: ctx.Err()})
	r.mu.Unlock()

	if commandType == "terminal.create" {
		return json.Marshal(map[string]any{"session_id": "daemon-session-1", "pid": 4242})
	}
	return json.Marshal(map[string]any{"success": true})
}

func (r *terminalCommandRouter) SubscribeTerminalOutput(context.Context, string, string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	// Never closed: the session produces no output and never exits, so only
	// the connection ending can end the handler.
	return make(chan *toolexec.TerminalOutputEvent), func() {}, nil
}

// closeCommands returns the terminal.close commands sent so far.
func (r *terminalCommandRouter) closeCommands() []recordedDaemonCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	var closes []recordedDaemonCommand
	for _, cmd := range r.commands {
		if cmd.commandType == "terminal.close" {
			closes = append(closes, cmd)
		}
	}
	return closes
}

// requireSingleClose waits for the handler to finish tearing down, then
// asserts exactly one terminal.close was sent for the created session, on a
// context that was still live.
func requireSingleClose(t *testing.T, router *terminalCommandRouter) {
	t.Helper()
	require.Eventually(t, func() bool { return len(router.closeCommands()) > 0 }, 5*time.Second, 10*time.Millisecond,
		"connection ended without closing its daemon terminal session — the shell and its PTY leak")
	// A second close would come from the same teardown path; give it a
	// moment to show up before asserting there is exactly one.
	time.Sleep(50 * time.Millisecond)
	closes := router.closeCommands()
	require.Len(t, closes, 1, "expected exactly one terminal.close")
	require.Equal(t, "daemon-session-1", closes[0].sessionID)
	require.NoError(t, closes[0].ctxErr, "terminal.close was sent on a cancelled context and would never reach the daemon")
}

type staticTokenValidator struct{ userID string }

func (v staticTokenValidator) ValidateToken(string) (*auth.JWTClaims, error) {
	return &auth.JWTClaims{Sub: v.userID}, nil
}

// dialTerminalWS starts TerminalWSHandler behind a test server and dials it,
// returning the connection once the "init" message has arrived.
func dialTerminalWS(t *testing.T, router *terminalCommandRouter) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?token=test&workingDir=/tmp"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)
	require.Equal(t, "daemon-session-1", init.SessionID)
	return conn
}

func TestTerminalWS_ClosesDaemonSessionWhenClientDisconnects(t *testing.T) {
	router := newTerminalCommandRouter()
	conn := dialTerminalWS(t, router)

	// Drop the connection without a close handshake — the 1006 path every
	// reload, sleep, or network blip takes.
	require.NoError(t, conn.UnderlyingConn().Close())

	requireSingleClose(t, router)
}

func TestTerminalWS_ClosesDaemonSessionOnCleanClientClose(t *testing.T) {
	router := newTerminalCommandRouter()
	conn := dialTerminalWS(t, router)

	require.NoError(t, conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))

	requireSingleClose(t, router)
}

// sessionGoneRouter answers terminal.close the way a daemon does for a session
// it no longer has, verbatim from ELECTRON-97.
type sessionGoneRouter struct{ toolexectest.Recorder }

func (*sessionGoneRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, fmt.Errorf(`daemon command %q failed: close session: session not found: e0f413d9-94c0-44f2-aa5c-4b9f1ac1d167`, commandType)
}

// Closing a tab closes its session twice by design: the browser calls
// CloseSession and the terminal connection's teardown closes it too. The
// second close finds nothing, which is the outcome close asks for — not an
// INTERNAL error reported to Sentry from both server and browser.
func TestCloseSession_AlreadyClosedIsSuccess(t *testing.T) {
	svc := NewTestTerminal(t, WithTerminalDeps(Deps{Router: &sessionGoneRouter{}}))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, terminalTestUserID)

	resp, err := svc.CloseSession(ctx, connect.NewRequest(&reliantv1.CloseTerminalSessionRequest{
		SessionId: "e0f413d9-94c0-44f2-aa5c-4b9f1ac1d167",
	}))

	require.NoError(t, err)
	require.True(t, resp.Msg.GetSuccess())
}

// Any other close failure is still a failure.
func TestCloseSession_OtherDaemonFailureIsStillAnError(t *testing.T) {
	router := &closeFailsRouter{err: errors.New(`daemon command "terminal.close" failed: terminal manager not initialized`)}
	svc := NewTestTerminal(t, WithTerminalDeps(Deps{Router: router}))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, terminalTestUserID)

	_, err := svc.CloseSession(ctx, connect.NewRequest(&reliantv1.CloseTerminalSessionRequest{SessionId: "s1"}))

	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, svcerr.Code(err))
}

type closeFailsRouter struct {
	toolexectest.Recorder
	err error
}

func (r *closeFailsRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return nil, r.err
}
