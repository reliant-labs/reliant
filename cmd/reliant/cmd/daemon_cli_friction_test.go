// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// TL-10: `daemon logs -n N` read only the last 1MB, so a large N on a log of
// long lines silently printed fewer lines than asked for.
func TestReadLastLines_HonoursNBeyondOneMegabyte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	long := strings.Repeat("x", 4096)
	var b strings.Builder
	for i := range 600 { // ~2.4MB
		fmt.Fprintf(&b, "%04d %s\n", i, long)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	lines, err := readLastLines(f, 500)
	require.NoError(t, err)
	require.Len(t, lines, 500)
	require.True(t, strings.HasPrefix(lines[0], "0100 "), "the first of the last 500 lines is whole: %.10q", lines[0])
	require.True(t, strings.HasPrefix(lines[499], "0599 "))

	pos, err := f.Seek(0, 1)
	require.NoError(t, err)
	require.Equal(t, int64(b.Len()), pos, "--follow continues from the end")
}

func TestReadLastLines_ShortFilesAndEdges(t *testing.T) {
	dir := t.TempDir()
	read := func(content string, n int) []string {
		path := filepath.Join(dir, fmt.Sprintf("f%d", n))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()
		lines, err := readLastLines(f, n)
		require.NoError(t, err)
		return lines
	}
	require.Equal(t, []string{"a", "b", "c"}, read("a\nb\nc\n", 10))
	require.Equal(t, []string{"b", "c"}, read("a\nb\nc", 2))
	require.Empty(t, read("", 3))
	require.Empty(t, read("a\n", 0))
}

// TL-32: a token the gateway refuses because it is not a machine token said
// only "(unauthenticated)", hiding the reason the gateway gave.
func TestTokenRejectedError_NamesTheMissingScope(t *testing.T) {
	conn := &connection{ServerURL: "https://api.example"}
	err := tokenRejectedError(conn, connect.NewError(connect.CodeUnauthenticated, errors.New("token does not grant daemon:connect")))
	require.Contains(t, err.Error(), "lacks the daemon:connect scope")
	require.Contains(t, err.Error(), "without --token")

	err = tokenRejectedError(conn, connect.NewError(connect.CodeUnauthenticated, errors.New("token revoked")))
	require.Contains(t, err.Error(), "unauthenticated: token revoked")
	require.Contains(t, err.Error(), "verify the PAT")
}

// TL-33: while an interactive `daemon start` waits for its pairing code to be
// approved, `daemon status` said "No daemon running".
func TestPublishAwaitingPairing_RecordsAndClearsOnFailure(t *testing.T) {
	dataDir := t.TempDir()
	ctx := withPairingRecord(context.Background(), dataDir)

	publishAwaitingPairing(ctx)
	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	require.Equal(t, daemonstate.StreamAwaitingCredentials, state.Stream)
	require.Equal(t, os.Getpid(), state.PID)
	require.Contains(t, state.StreamDetail, "paired")

	publishPairingDone(ctx, false)
	_, err = daemonstate.Read(dataDir)
	require.True(t, os.IsNotExist(err), "a failed pairing leaves no record behind: %v", err)

	publishAwaitingPairing(ctx)
	publishPairingDone(ctx, true)
	_, err = daemonstate.Read(dataDir)
	require.NoError(t, err, "a pairing that succeeded leaves the record to the runtime")

	// `daemon register` pairs with no data directory and publishes nothing.
	publishAwaitingPairing(context.Background())
}
