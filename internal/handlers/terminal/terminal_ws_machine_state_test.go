// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// dialTerminalWSForProject opens the terminal websocket for a project and
// returns the first message the server sends.
func dialTerminalWSForProject(t *testing.T, router toolexec.DaemonRouter, projectID string) map[string]any {
	t.Helper()
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}))
	t.Cleanup(srv.Close)

	query := url.Values{"token": {"test"}, "workingDir": {projectWorkingDir}, "projectId": {projectID}, "worktreeId": {"wt-1"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query.Encode(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	require.NoError(t, conn.ReadJSON(&first))
	return first
}

// logRecords returns every JSON log record with the given message.
func logRecords(logs *logCapture, msg string) []map[string]any {
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(logs.String()))
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record["msg"] == msg {
			records = append(records, record)
		}
	}
	return records
}

// freshTerminalMachineStateLog gives one test its own throttle, so another
// test's machine-state line cannot hold back this one's.
func freshTerminalMachineStateLog(t *testing.T) {
	t.Helper()
	previous := terminalMachineStateLog
	terminalMachineStateLog = logging.NewThrottle(terminalMachineStateLogWindow, time.Now)
	t.Cleanup(func() { terminalMachineStateLog = previous })
}

// Prod, 2026-10-09: a crash-looping machine made the browser's terminal retry
// every 10–20 s, and every retry logged "[TerminalWS] Error" at ERROR with no
// user, project or machine id — 1,019 lines in five hours. A machine that is
// not up is the machine's state: the browser is still told (it shows the wait
// and retries), but the server says so once, at INFO, saying whose.
func TestTerminalWS_MachineNotReadyIsLoggedOnceBelowErrorWithContext(t *testing.T) {
	logs := captureLogs(t)
	freshTerminalMachineStateLog(t)
	router := &terminalCreateRouter{createErr: connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected for user"))}

	for i := 0; i < 3; i++ {
		first := dialTerminalWSForProject(t, router, "proj-1")
		require.Equal(t, "error", first["type"])
		require.Equal(t, wsErrorDaemonUnavailable, first["code"], "the browser keys its wait on this code")
	}

	require.Empty(t, logRecords(logs, "[TerminalWS] Error"), "a machine that is not up is not an ERROR")
	records := logRecords(logs, "[TerminalWS] Machine not ready; the browser shows it and retries")
	require.Len(t, records, 1, "the browser's retries are the same news")
	record := records[0]
	require.Equal(t, "INFO", record["level"])
	require.Equal(t, "not_connected", record["machine_state"])
	require.Equal(t, terminalTestUserID, record["user_id"])
	require.Equal(t, "proj-1", record["project_id"])
	require.Equal(t, "wt-1", record["worktree_id"])
	require.Equal(t, "test-daemon-id", record["daemon_id"])
}

// Prod, 2026-10-11 (ELECTRON-CZ): one machine whose daemon was connected but
// never answered terminal.create produced 24 "[TerminalWS] Error" events in
// five minutes — four terminals, each retrying every ~62s. A connected daemon
// that cannot open a shell is still an error, but it is the machine's state:
// said once per window, with whose machine it was, not once per retry.
func TestTerminalWS_MachineNotAnsweringIsOneErrorPerWindowWithContext(t *testing.T) {
	logs := captureLogs(t)
	freshTerminalMachineStateLog(t)
	// The NATS router's shape for a live subscription that did not reply.
	notAnswering := fmt.Errorf("daemon command terminal.create (subject daemon.command.u.d, timeout 30s) via NATS failed: %w: %w",
		toolexec.ErrDaemonTimeout, errors.New("nats: timeout"))
	router := &terminalCreateRouter{createErr: notAnswering}

	for i := 0; i < 4; i++ {
		first := dialTerminalWSForProject(t, router, "proj-1")
		require.Equal(t, "error", first["type"], "the browser is still told, and retries")
		require.Nil(t, first["code"], "not a wait: the browser shows the error")
	}

	require.Empty(t, logRecords(logs, "[TerminalWS] Error"), "a machine's state is not a per-retry error")
	require.Empty(t, logRecords(logs, "[TerminalWS] Machine not ready; the browser shows it and retries"),
		"a connected machine that does not answer is not a machine that is not up")
	records := logRecords(logs, "[TerminalWS] Machine is connected but did not answer; the browser retries")
	require.Len(t, records, 1, "the browser's retries are the same news")
	record := records[0]
	require.Equal(t, "ERROR", record["level"], "nothing the user did explains a connected daemon that cannot open a shell")
	require.Equal(t, "not_answering", record["machine_state"])
	require.Equal(t, terminalTestUserID, record["user_id"])
	require.Equal(t, "proj-1", record["project_id"])
	require.Equal(t, "test-daemon-id", record["daemon_id"])
}

// No machine at all is its own code: there is nothing to wait for, so the
// browser must not spin on it as it does on daemon_unavailable.
func TestTerminalWS_NoMachineHasItsOwnCode(t *testing.T) {
	freshTerminalMachineStateLog(t)
	router := &terminalCreateRouter{createErr: fmt.Errorf("resolving daemon for command: %w: no machine is connected to your account yet", toolexec.ErrNoDaemon)}

	first := dialTerminalWSForProject(t, router, "proj-1")
	require.Equal(t, "error", first["type"])
	require.Equal(t, wsErrorNoMachine, first["code"])
}

// A real failure stays at ERROR, and now says whose terminal it was.
func TestTerminalWS_RealFailureIsStillAnErrorWithContext(t *testing.T) {
	logs := captureLogs(t)
	freshTerminalMachineStateLog(t)
	router := &terminalCreateRouter{createErr: errors.New("terminal.create via NATS failed: nats: timeout")}

	first := dialTerminalWSForProject(t, router, "proj-1")
	require.Equal(t, "error", first["type"])

	records := logRecords(logs, "[TerminalWS] Error")
	require.Len(t, records, 1)
	require.Equal(t, "ERROR", records[0]["level"])
	require.Equal(t, terminalTestUserID, records[0]["user_id"])
	require.Equal(t, "proj-1", records[0]["project_id"])
}
