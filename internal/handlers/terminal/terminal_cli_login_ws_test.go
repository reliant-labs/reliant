package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

const cliLoginMachine = "daemon-laptop-cloud"

// cliLoginRouter records every daemon command, the machine it went to and its
// payload, and the input forwarded to a session.
type cliLoginRouter struct {
	toolexectest.Recorder

	// createErr, when set, is the machine's answer to the sign-in create.
	createErr error

	mu       sync.Mutex
	commands []cliLoginCommand
	input    [][]byte
}

type cliLoginCommand struct {
	daemonID, commandType string
	payload               map[string]any
}

var _ toolexec.DaemonRouter = (*cliLoginRouter)(nil)

func (r *cliLoginRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	var body map[string]any
	_ = json.Unmarshal(payload, &body)
	r.mu.Lock()
	r.commands = append(r.commands, cliLoginCommand{daemonID: daemonID, commandType: commandType, payload: body})
	r.mu.Unlock()
	if commandType == toolexec.TerminalCreateCLILoginCommand {
		if r.createErr != nil {
			return nil, r.createErr
		}
		return json.Marshal(map[string]any{"session_id": "login-session-1", "pid": 7})
	}
	return json.Marshal(map[string]any{"success": true})
}

func (r *cliLoginRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, "", commandType, payload, timeoutMs)
}

func (r *cliLoginRouter) SubscribeTerminalOutput(context.Context, string, string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent, 1)
	ch <- &toolexec.TerminalOutputEvent{SessionID: "login-session-1", Data: []byte("Open https://example.test/device and enter ABCD-1234\r\n")}
	return ch, func() {}, nil
}

func (r *cliLoginRouter) SendTerminalInput(_ context.Context, _, _ string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.input = append(r.input, append([]byte(nil), data...))
	return nil
}

func (r *cliLoginRouter) sent() []cliLoginCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]cliLoginCommand(nil), r.commands...)
}

func (r *cliLoginRouter) typed() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.input...)
}

func (r *cliLoginRouter) sentOf(commandType string) []cliLoginCommand {
	var out []cliLoginCommand
	for _, c := range r.sent() {
		if c.commandType == commandType {
			out = append(out, c)
		}
	}
	return out
}

// tokenTable is an access-token introspector over a fixed table.
type tokenTable map[string]*fat.Principal

func (t tokenTable) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	if p, ok := t[token]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("introspect: %w", auth.ErrAccessTokenInactive)
}

// cliLoginMachines: cliLoginMachine is the test user's; "daemon-other" is
// someone else's.
type cliLoginMachines struct{}

func (cliLoginMachines) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	switch id {
	case cliLoginMachine:
		return &db.Daemon{ID: id, UserID: terminalTestUserID, Name: "cloud-box"}, nil
	case "daemon-other":
		return &db.Daemon{ID: id, UserID: "someone-else"}, nil
	}
	return nil, errors.New("daemon not found")
}

func mintToken(t *testing.T) string {
	t.Helper()
	m, err := fat.Mint()
	require.NoError(t, err)
	return m.Plaintext
}

type cliLoginServer struct {
	url    string
	router *cliLoginRouter
	// person is the CLI login (reliant:api + daemon:manage); narrow lacks
	// daemon:manage; machine is a daemon's own credential.
	person, narrow, machine string
}

func startCLILoginServer(t *testing.T, router *cliLoginRouter) cliLoginServer {
	t.Helper()
	s := cliLoginServer{router: router, person: mintToken(t), narrow: mintToken(t), machine: mintToken(t)}
	tokens := tokenTable{
		s.person:  {ActingUserID: terminalTestUserID, Scopes: fat.SetOf(fat.ScopeReliantAPI, fat.ScopeDaemonManage)},
		s.narrow:  {ActingUserID: terminalTestUserID, Scopes: fat.SetOf(fat.ScopeReliantAPI)},
		s.machine: {ActingUserID: terminalTestUserID, Scopes: fat.SetOf(fat.ScopeReliantAPI, fat.ScopeDaemonManage, fat.ScopeDaemonConnect)},
	}
	srv := httptest.NewServer(TerminalCLILoginWSHandler(router, staticTokenValidator{userID: terminalTestUserID}, tokens, cliLoginMachines{},
		withTerminalCreateTimeout(2*time.Second)))
	t.Cleanup(srv.Close)
	s.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return s
}

func (s cliLoginServer) dial(t *testing.T, token string, q url.Values) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(s.url+"/?"+q.Encode(), h)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, err
}

func loginQuery(daemonID, vendor string) url.Values {
	return url.Values{"daemonId": {daemonID}, "vendor": {vendor}, "cols": {"132"}, "rows": {"43"}}
}

func refusalBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	require.NotNil(t, resp)
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// The CLI login opens the session on the machine it names: the daemon is sent
// the new command with the vendor and the size, never a program to run, and
// the session's bytes flow both ways.
func TestCLILoginWS_PersonLoginOpensTheVendorSignInOnTheMachine(t *testing.T) {
	router := &cliLoginRouter{}
	s := startCLILoginServer(t, router)

	conn, _, err := s.dial(t, s.person, loginQuery(cliLoginMachine, "claude"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)
	require.Equal(t, "login-session-1", init.SessionID)
	require.Equal(t, cliLoginMachine, init.DaemonID)
	require.Equal(t, "cloud-box", init.MachineName)

	creates := router.sentOf(toolexec.TerminalCreateCLILoginCommand)
	require.Len(t, creates, 1)
	require.Equal(t, cliLoginMachine, creates[0].daemonID)
	require.Equal(t, map[string]any{"vendor": "claude", "cols": float64(132), "rows": float64(43)}, creates[0].payload,
		"the server names the vendor only: the daemon builds the program")
	require.Empty(t, router.sentOf("terminal.create"), "never a plain shell")

	var out wsMessage
	require.NoError(t, conn.ReadJSON(&out))
	require.Equal(t, "output", out.Type)
	require.Contains(t, out.Data, "ABCD-1234")

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("one-time-code\r")))
	require.Eventually(t, func() bool { return len(router.typed()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, "one-time-code\r", string(router.typed()[0]))

	_ = conn.Close()
	require.Eventually(t, func() bool { return len(router.sentOf("terminal.close")) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the sign-in session must be closed when the connection ends")
}

// A session sign-in (the app's JWT) is a person's too.
func TestCLILoginWS_SessionTokenIsAccepted(t *testing.T) {
	router := &cliLoginRouter{}
	s := startCLILoginServer(t, router)

	conn, _, err := s.dial(t, "a-session-jwt", loginQuery(cliLoginMachine, "codex"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var init wsMessage
	require.NoError(t, conn.ReadJSON(&init))
	require.Equal(t, "init", init.Type)
}

func TestCLILoginWS_RefusesBeforeReachingTheMachine(t *testing.T) {
	cases := []struct {
		name    string
		token   func(cliLoginServer) string
		query   url.Values
		status  int
		message string
	}{
		{"no token", func(cliLoginServer) string { return "" }, loginQuery(cliLoginMachine, "claude"), http.StatusUnauthorized, "missing bearer token"},
		{"dead token", func(cliLoginServer) string { return mintToken(t) }, loginQuery(cliLoginMachine, "claude"), http.StatusUnauthorized, "reliant auth login"},
		{"login without daemon:manage", func(s cliLoginServer) string { return s.narrow }, loginQuery(cliLoginMachine, "claude"), http.StatusForbidden, "daemon:manage"},
		{"a machine's own credential", func(s cliLoginServer) string { return s.machine }, loginQuery(cliLoginMachine, "claude"), http.StatusForbidden, "daemon:manage"},
		{"someone else's machine", func(s cliLoginServer) string { return s.person }, loginQuery("daemon-other", "claude"), http.StatusNotFound, "no such machine"},
		{"unknown machine", func(s cliLoginServer) string { return s.person }, loginQuery("daemon-nope", "claude"), http.StatusNotFound, "no such machine"},
		{"unknown vendor", func(s cliLoginServer) string { return s.person }, loginQuery(cliLoginMachine, "bash"), http.StatusBadRequest, "unknown vendor CLI"},
		{"no machine", func(s cliLoginServer) string { return s.person }, loginQuery("", "claude"), http.StatusBadRequest, "daemonId is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &cliLoginRouter{}
			s := startCLILoginServer(t, router)
			_, resp, err := s.dial(t, tc.token(s), tc.query)
			require.ErrorIs(t, err, websocket.ErrBadHandshake)
			require.Equal(t, tc.status, resp.StatusCode)
			require.Contains(t, refusalBody(t, resp), tc.message)
			require.Empty(t, router.sent(), "nothing may reach a machine before the caller is checked")
		})
	}
}

// A daemon that predates the command says "unknown command": the person is
// told to update Reliant on that machine, and no shell is opened instead.
func TestCLILoginWS_OldDaemonIsToldToUpdateNeverGivenAShell(t *testing.T) {
	for name, createErr := range map[string]error{
		"today's wording": errors.New(`daemon command terminal.create_cli_login failed: this machine is running an older version of Reliant (v1.20.0) that doesn't support "terminal.create_cli_login" — update it to continue`),
		"older wording":   errors.New(`daemon command failed: unknown daemon command type: "terminal.create_cli_login"`),
	} {
		t.Run(name, func(t *testing.T) {
			router := &cliLoginRouter{createErr: createErr}
			s := startCLILoginServer(t, router)

			conn, _, err := s.dial(t, s.person, loginQuery(cliLoginMachine, "gemini"))
			require.NoError(t, err)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var msg wsMessage
			require.NoError(t, conn.ReadJSON(&msg))
			require.Equal(t, "error", msg.Type)
			require.Equal(t, WSErrorDaemonOutdated, msg.Code)
			require.Contains(t, msg.Data, "cloud-box")
			require.Contains(t, msg.Data, "update Reliant")
			require.Empty(t, router.sentOf("terminal.create"))
		})
	}
}
