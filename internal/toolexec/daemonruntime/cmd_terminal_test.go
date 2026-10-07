package daemonruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/terminal"
)

// useTerminalManager installs a fresh terminal manager for the duration of a
// test and restores the previous one afterwards.
func useTerminalManager(t *testing.T) *terminal.Manager {
	t.Helper()
	previous := terminalManager()
	m := terminal.NewManager()
	SetTerminalManager(m)
	t.Cleanup(func() {
		m.Cleanup()
		SetTerminalManager(previous)
	})
	return m
}

func terminalCreatePayload(t *testing.T, workingDir string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"working_dir": workingDir})
	require.NoError(t, err)
	return payload
}

// The api-server has no filesystem, so it can only learn where the shell
// actually started from the daemon. Without this in the response, a session
// in the wrong directory is indistinguishable in the server logs from a
// correct one.
func TestHandleTerminalCreate_ReportsTheDirectoryTheShellStartedIn(t *testing.T) {
	useTerminalManager(t)
	dir := t.TempDir()

	out, err := handleTerminalCreate(context.Background(), terminalCreatePayload(t, dir))
	require.NoError(t, err)

	var resp struct {
		SessionID  string `json:"session_id"`
		WorkingDir string `json:"working_dir"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.NotEmpty(t, resp.SessionID)
	require.Equal(t, dir, resp.WorkingDir)
}

// The prod failure: a project opened while its clone was still running.
func TestHandleTerminalCreate_MissingWorkingDirIsAnError(t *testing.T) {
	m := useTerminalManager(t)
	missing := filepath.Join(t.TempDir(), "projects", "forge")

	_, err := handleTerminalCreate(context.Background(), terminalCreatePayload(t, missing))

	require.Error(t, err, "the daemon must refuse, not start the shell in $HOME")
	require.Contains(t, err.Error(), missing, "the error must name the directory it could not use")
	require.Empty(t, m.ListSessions())
}
