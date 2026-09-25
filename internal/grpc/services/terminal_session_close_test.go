package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// These tests pin one invariant for every server-side terminal transport:
// a connection that creates a daemon terminal session closes that session
// when the connection ends, however it ends.
//
// Neither transport can reattach to an existing session — every connection
// sends terminal.create — so a session outliving its connection is
// unreachable. Before this was enforced, each dropped WebSocket (reload,
// sleep, API restart, network blip) left a login shell and its PTY alive on
// the user's machine, and the browser's reconnect created another. One
// developer daemon accumulated 501 idle shells, exhausting macOS's PTY pool
// (kern.tty.ptmx_max = 511) so no process on the machine could open a
// terminal: "forkpty: Device not configured".

const terminalTestUserID = "user-terminal-test"

// terminalCommandRouter records the daemon commands a terminal handler sends.
// It embeds fakeDaemonRouter for the rest of toolexec.DaemonRouter.
type terminalCommandRouter struct {
	fakeDaemonRouter

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

// startStreamTerminal serves TerminalProxyService over h2c (bidi streaming
// needs HTTP/2) with the test user injected, and opens a stream on it that
// has already received "created".
func startStreamTerminal(t *testing.T, router *terminalCommandRouter) *connect.BidiStreamForClient[reliantv1.TerminalStreamInput, reliantv1.TerminalStreamOutput] {
	t.Helper()
	path, handler := reliantv1connect.NewTerminalServiceHandler(NewTerminalProxyService(router))
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, terminalTestUserID)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := reliantv1connect.NewTerminalServiceClient(srv.Client(), srv.URL)
	stream := client.StreamTerminal(context.Background())
	require.NoError(t, stream.Send(&reliantv1.TerminalStreamInput{
		Input: &reliantv1.TerminalStreamInput_Create{Create: &reliantv1.TerminalCreateRequest{WorkingDir: "/tmp"}},
	}))
	first, err := stream.Receive()
	require.NoError(t, err)
	require.Equal(t, "daemon-session-1", first.GetCreated().GetSessionId())
	return stream
}

func TestStreamTerminal_ClosesDaemonSessionWhenClientHangsUp(t *testing.T) {
	router := newTerminalCommandRouter()
	stream := startStreamTerminal(t, router)

	// Hang up without sending CloseSession.
	require.NoError(t, stream.CloseRequest())
	require.NoError(t, stream.CloseResponse())

	requireSingleClose(t, router)
}

func TestStreamTerminal_ClosesDaemonSessionOnceOnExplicitClose(t *testing.T) {
	router := newTerminalCommandRouter()
	stream := startStreamTerminal(t, router)

	// An explicit CloseSession must still close exactly once — the
	// connection-scoped close must not double up with it.
	require.NoError(t, stream.Send(&reliantv1.TerminalStreamInput{
		Input: &reliantv1.TerminalStreamInput_CloseSession{CloseSession: &reliantv1.TerminalCloseRequest{}},
	}))

	requireSingleClose(t, router)
	require.NoError(t, stream.CloseRequest())
	require.NoError(t, stream.CloseResponse())
}
