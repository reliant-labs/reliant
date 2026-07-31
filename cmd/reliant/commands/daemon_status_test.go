// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// runDaemonStatus executes `reliant daemon status --data-dir <dir>` and returns
// its combined output plus the command error.
func runDaemonStatus(t *testing.T, dataDir string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"daemon", "status", "--data-dir", dataDir})
	err := root.Execute()
	return out.String(), err
}

// writeRecord stamps a runtime record for the current (live) process, so the
// process-existence check passes and the assertion is purely about how the
// command reports the gateway stream.
func writeRecord(t *testing.T, dataDir string, mutate func(*daemonstate.State)) {
	t.Helper()
	require.NoError(t, daemonstate.Init(dataDir, "http://localhost:29190", "h2c", false))
	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	require.Equal(t, os.Getpid(), state.PID)
	if mutate != nil {
		mutate(&state)
	}
	require.NoError(t, daemonstate.SetStream(dataDir, state.Stream, state.StreamDetail))
}

// A daemon that never established its gateway stream serves nothing: tool
// execution happens in the daemon, over that stream. Reporting "Daemon is
// running" because a PID exists is what let a run be measured against a daemon
// that had never connected.
func TestDaemonStatusReportsGatewayStreamNotEstablished(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) {
		s.Stream = daemonstate.StreamDisconnected
		s.StreamDetail = "daemon stream receive: unknown: write envelope: EOF"
	})

	out, err := runDaemonStatus(t, dataDir)

	require.Error(t, err, "status must exit non-zero when the gateway stream is not established")
	require.Contains(t, err.Error(), "not established")
	require.Contains(t, out, "NOT established")
	require.Contains(t, out, "write envelope: EOF", "the error that killed the stream must be surfaced")
	require.Contains(t, out, "never connected since this daemon started")
	require.NotContains(t, out, "Daemon is running and its gateway stream is established")
}

func TestDaemonStatusReportsEstablishedStream(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamConnected })

	out, err := runDaemonStatus(t, dataDir)

	require.NoError(t, err)
	require.Contains(t, out, "Daemon is running and its gateway stream is established")
	require.Contains(t, out, "http://localhost:29190 (h2c)")
}

// "Process exists but stream state unknown" must be said, not rounded up to
// "running".
func TestDaemonStatusReportsUnknownStreamRatherThanRunning(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamUnknown })

	out, err := runDaemonStatus(t, dataDir)

	require.Error(t, err)
	require.Contains(t, out, "gateway stream state is unknown")
}

// The record identifies the binary the daemon is running, so a stale daemon
// left over from an earlier session is visible rather than indistinguishable.
func TestDaemonStatusReportsRunningBinary(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamConnected })

	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	require.NotEmpty(t, state.Executable)
	require.False(t, state.BinaryModTime.IsZero())

	out, statusErr := runDaemonStatus(t, dataDir)
	require.NoError(t, statusErr)
	require.Contains(t, out, state.Executable)
	require.Contains(t, out, "ago)")
}

// `daemon status` samples the record once. Against a stream that flaps every
// 15-30s, a single sample says "connected" nearly every time it is taken — six
// consecutive samples did exactly that on 2026-07-27 and were used as the
// pre-flight that cleared a 30-minute run. Established is not usable: a stream
// that dropped moments ago will drop again mid-run and take every in-flight
// tool call with it, so the pre-flight must fail.
func TestDaemonStatusFailsOnAFlappingStream(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamConnected })

	// The stream is UP right now, and dropped six seconds ago — the exact
	// condition the six passing samples were taken against.
	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	state.Sessions = 47
	state.LastDisconnectAt = time.Now().UTC().Add(-6 * time.Second)
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(daemonstate.Path(dataDir), data, 0o644))

	out, statusErr := runDaemonStatus(t, dataDir)

	require.Error(t, statusErr, "a flapping stream must not pass a pre-flight check")
	require.Contains(t, statusErr.Error(), "flapping")
	require.Contains(t, out, "FLAPPING")
	require.Contains(t, out, "UNSTABLE")
	require.Contains(t, out, "47")
	require.NotContains(t, out, "Daemon is running and its gateway stream is established")
}

// A stream that has held keeps passing, and says so.
func TestDaemonStatusReportsAStableStreamAsStable(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamConnected })

	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	state.Sessions = 3
	state.LastDisconnectAt = time.Now().UTC().Add(-30 * time.Minute)
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(daemonstate.Path(dataDir), data, 0o644))

	out, statusErr := runDaemonStatus(t, dataDir)

	require.NoError(t, statusErr)
	require.Contains(t, out, "Daemon is running and its gateway stream is established")
	require.Contains(t, out, "stable")
	require.NotContains(t, out, "UNSTABLE")
}

func TestDaemonStatusReportsNoDaemonWithoutRecord(t *testing.T) {
	out, err := runDaemonStatus(t, t.TempDir())
	require.Error(t, err)
	require.Contains(t, out, "No daemon running")
}

func TestDaemonStatusReportsStaleRecordForDeadProcess(t *testing.T) {
	dataDir := t.TempDir()
	writeRecord(t, dataDir, func(s *daemonstate.State) { s.Stream = daemonstate.StreamConnected })

	state, err := daemonstate.Read(dataDir)
	require.NoError(t, err)
	// A PID above the kernel maximum can never name a live process, so the
	// record is unambiguously stale.
	state.PID = 4194303
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(daemonstate.Path(dataDir), data, 0o644))

	out, statusErr := runDaemonStatus(t, dataDir)
	require.Error(t, statusErr)
	require.Contains(t, out, "stale runtime record")
}
