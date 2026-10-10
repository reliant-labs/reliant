package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// These tests pin what the server does with the working directory a terminal
// asks for. In prod a project's terminal opened in $HOME because the daemon
// was asked for a directory that did not exist yet (its clone was still
// running) and silently substituted $HOME. The server logged "Session
// created" with neither directory, so nothing in prod could show it.

const projectWorkingDir = "/home/workspace/projects/forge"

// missingWorkingDirTransportError is terminal.create's failure for a missing
// directory as the api-server receives it: the daemon's error message,
// flattened to a string by the NATS router (daemon_router_nats.go).
//
// Spelled out rather than built from terminal.ErrWorkingDirUnavailable on
// purpose. Daemons in the field send this text; if the sentinel's wording
// changes, the server stops recognising them, and this test is what says so.
var missingWorkingDirTransportError = errors.New(`daemon command "terminal.create" failed: create session: ` +
	`terminal working directory unavailable: ` + projectWorkingDir + ` does not exist`)

// terminalCreateRouter answers terminal.create with a fixed response or
// error, and records the create payloads it was sent.
type terminalCreateRouter struct {
	fakeDaemonRouter

	createResponse []byte
	createErr      error

	mu             sync.Mutex
	createRequests []map[string]any
}

var _ toolexec.DaemonRouter = (*terminalCreateRouter)(nil)

func (r *terminalCreateRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType != "terminal.create" {
		return json.Marshal(map[string]any{"success": true})
	}
	var req map[string]any
	_ = json.Unmarshal(payload, &req)
	r.mu.Lock()
	r.createRequests = append(r.createRequests, req)
	r.mu.Unlock()
	return r.createResponse, r.createErr
}

func (r *terminalCreateRouter) SubscribeTerminalOutput(context.Context, string, string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	return make(chan *toolexec.TerminalOutputEvent), func() {}, nil
}

// dialTerminalWSAt opens the terminal websocket asking for workingDir and
// returns the first message the server sends.
func dialTerminalWSAt(t *testing.T, router toolexec.DaemonRouter, workingDir string) map[string]any {
	t.Helper()
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	t.Cleanup(srv.Close)

	query := url.Values{"token": {"test"}, "workingDir": {workingDir}}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?" + query.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	require.NoError(t, conn.ReadJSON(&first))
	return first
}

// logCapture collects JSON log lines. The handler under test keeps logging
// from its own goroutine after the test has read what it needs ("Session
// ended"), so every access goes through the lock.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// captureLogs routes the process logger into a logCapture for one test. Not
// safe alongside parallel tests, so callers must not call t.Parallel.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	logs := &logCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// logRecord returns the first JSON log record with the given message.
func logRecord(t *testing.T, logs *logCapture, msg string) map[string]any {
	t.Helper()
	captured := logs.String()
	scanner := bufio.NewScanner(strings.NewReader(captured))
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record["msg"] == msg {
			return record
		}
	}
	t.Fatalf("no %q log record in:\n%s", msg, captured)
	return nil
}

func TestTerminalWS_SessionCreatedLogsRequestedAndEffectiveWorkingDir(t *testing.T) {
	logs := captureLogs(t)
	router := &terminalCreateRouter{}
	router.createResponse, _ = json.Marshal(map[string]any{
		"session_id":  "daemon-session-1",
		"pid":         4242,
		"working_dir": projectWorkingDir,
	})

	first := dialTerminalWSAt(t, router, projectWorkingDir)
	require.Equal(t, "init", first["type"])

	record := logRecord(t, logs, "[TerminalWS] Session created")
	require.Equal(t, projectWorkingDir, record["requested_working_dir"])
	require.Equal(t, projectWorkingDir, record["working_dir"])
}

func TestTerminalWS_MissingWorkingDirIsReportedAsWorkingDirUnavailable(t *testing.T) {
	logs := captureLogs(t)
	router := &terminalCreateRouter{createErr: missingWorkingDirTransportError}

	first := dialTerminalWSAt(t, router, projectWorkingDir)

	// The browser needs a machine-readable reason to tell "this directory is
	// not there (yet)" apart from a broken session, so it can wait for the
	// directory instead of burning its reconnect budget.
	require.Equal(t, "error", first["type"])
	require.Equal(t, "working_dir_unavailable", first["code"])
	require.Contains(t, first["data"], projectWorkingDir)

	record := logRecord(t, logs, "[TerminalWS] Working directory unavailable")
	require.Equal(t, projectWorkingDir, record["requested_working_dir"])
}

// openStreamTerminalAt serves TerminalProxyService over h2c and opens a
// stream asking for workingDir, returning the stream's first receive.
func openStreamTerminalAt(t *testing.T, router toolexec.DaemonRouter, workingDir string) (*reliantv1.TerminalStreamOutput, error) {
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
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})
	require.NoError(t, stream.Send(&reliantv1.TerminalStreamInput{
		Input: &reliantv1.TerminalStreamInput_Create{Create: &reliantv1.TerminalCreateRequest{WorkingDir: workingDir}},
	}))
	return stream.Receive()
}

func TestStreamTerminal_MissingWorkingDirIsFailedPrecondition(t *testing.T) {
	router := &terminalCreateRouter{createErr: missingWorkingDirTransportError}

	_, err := openStreamTerminalAt(t, router, projectWorkingDir)

	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a directory that does not exist is the caller's precondition, not a server fault")
	require.Contains(t, err.Error(), projectWorkingDir)
}

func TestStreamTerminal_SessionCreatedLogsRequestedAndEffectiveWorkingDir(t *testing.T) {
	logs := captureLogs(t)
	router := &terminalCreateRouter{}
	router.createResponse, _ = json.Marshal(map[string]any{
		"session_id":  "daemon-session-1",
		"pid":         4242,
		"working_dir": projectWorkingDir,
	})

	first, err := openStreamTerminalAt(t, router, projectWorkingDir)
	require.NoError(t, err)
	require.Equal(t, "daemon-session-1", first.GetCreated().GetSessionId())

	record := logRecord(t, logs, "[Terminal] Stream session created")
	require.Equal(t, projectWorkingDir, record["requested_working_dir"])
	require.Equal(t, projectWorkingDir, record["working_dir"])
}

// fakeTerminalCheckouts serves the workspace records TerminalWSHandler reads
// to decide whether a missing working directory may still appear.
type fakeTerminalCheckouts struct {
	worktrees      map[string]*db.Worktree
	projectDaemons map[string][]*db.ProjectDaemon
}

func (f fakeTerminalCheckouts) GetWorktree(_ context.Context, id string) (*db.Worktree, error) {
	if wt, ok := f.worktrees[id]; ok {
		return wt, nil
	}
	return nil, fmt.Errorf("%w: %s", core.ErrWorktreeNotFound, id)
}

func (f fakeTerminalCheckouts) ListProjectDaemonsForProject(_ context.Context, projectID string) ([]*db.ProjectDaemon, error) {
	return f.projectDaemons[projectID], nil
}

// dialTerminalWSFor opens the terminal websocket for a worktree's directory
// and returns the first message the server sends.
func dialTerminalWSFor(t *testing.T, router toolexec.DaemonRouter, checkouts terminalCheckouts, workingDir, worktreeID string) map[string]any {
	t.Helper()
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}, WithTerminalCheckouts(checkouts)))
	t.Cleanup(srv.Close)

	query := url.Values{"token": {"test"}, "workingDir": {workingDir}, "worktreeId": {worktreeID}}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?" + query.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	require.NoError(t, conn.ReadJSON(&first))
	return first
}

// requireNoErrorOrWarnLogs fails if anything was logged at WARN or above:
// the condition under test is the user's state, and an ERROR line is what
// the Sentry log handler reports.
func requireNoErrorOrWarnLogs(t *testing.T, logs *logCapture) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(logs.String()))
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record["level"] == "ERROR" || record["level"] == "WARN" {
			t.Fatalf("logged at %v for a user-state condition: %s", record["level"], scanner.Text())
		}
	}
}

const removedWorktreeDir = "/home/workspace/.reliant/worktrees/reliant-labs/365-95b57228"

// The prod case behind 905 refusals in five hours: a persisted terminal for a
// worktree whose directory was removed after it had been set up, retried every
// ~12s because every refusal said "wait for it". Nothing recreates that
// directory, so the browser must be told to stop.
func TestTerminalWS_RemovedWorktreeDirIsMissingNotUnavailable(t *testing.T) {
	logs := captureLogs(t)
	router := &terminalCreateRouter{createErr: errors.New(`daemon command "terminal.create" failed: create session: ` +
		`terminal working directory unavailable: ` + removedWorktreeDir + ` does not exist`)}
	checkouts := fakeTerminalCheckouts{worktrees: map[string]*db.Worktree{
		"wt-active": {ID: "wt-active", ProjectID: "p1", Path: removedWorktreeDir,
			Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)},
	}}

	first := dialTerminalWSFor(t, router, checkouts, removedWorktreeDir, "wt-active")

	require.Equal(t, "error", first["type"])
	require.Equal(t, "working_dir_missing", first["code"], "a removed worktree directory will not come back; retrying cannot succeed")
	require.Contains(t, first["data"], removedWorktreeDir)
	requireNoErrorOrWarnLogs(t, logs)
}

func TestTerminalWS_MissingDirClassification(t *testing.T) {
	archivedAt := time.Now()
	checkouts := fakeTerminalCheckouts{
		worktrees: map[string]*db.Worktree{
			"wt-creating": {ID: "wt-creating", ProjectID: "p1", Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING)},
			"wt-failed":   {ID: "wt-failed", ProjectID: "p1", Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)},
			"wt-archived": {ID: "wt-archived", ProjectID: "p1", Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), DeletedAt: &archivedAt},
			"main-cloning": {ID: "main-cloning", ProjectID: "p-cloning", IsMain: true,
				Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)},
			"main-cloned": {ID: "main-cloned", ProjectID: "p-cloned", IsMain: true,
				Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)},
		},
		projectDaemons: map[string][]*db.ProjectDaemon{
			// fakeDaemonRouter resolves every user to "test-daemon-id".
			"p-cloning": {{ProjectID: "p-cloning", DaemonID: "test-daemon-id", InstallState: core.ProjectInstallInstalling}},
			"p-cloned":  {{ProjectID: "p-cloned", DaemonID: "test-daemon-id", InstallState: core.ProjectInstallInstalled}},
		},
	}
	cases := []struct {
		worktreeID string
		wantCode   string
	}{
		{"wt-creating", "working_dir_unavailable"},  // its directory is being made
		{"main-cloning", "working_dir_unavailable"}, // its clone is still landing
		{"wt-failed", "working_dir_missing"},
		{"wt-archived", "working_dir_missing"},
		{"wt-gone", "working_dir_missing"},     // no row at all
		{"main-cloned", "working_dir_missing"}, // cloned, then removed
		{"", "working_dir_unavailable"},        // nothing to look up: keep waiting
	}
	for _, tc := range cases {
		t.Run(tc.worktreeID, func(t *testing.T) {
			router := &terminalCreateRouter{createErr: missingWorkingDirTransportError}
			first := dialTerminalWSFor(t, router, checkouts, projectWorkingDir, tc.worktreeID)
			require.Equal(t, "error", first["type"])
			require.Equal(t, tc.wantCode, first["code"])
		})
	}
}
