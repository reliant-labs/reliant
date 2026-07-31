// Copyright (c) 2025 Reliant Labs
package daemonstate

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInitRecordsProcessAndBinaryIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))

	state, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, os.Getpid(), state.PID)
	require.NotEmpty(t, state.Executable)
	require.False(t, state.BinaryModTime.IsZero())
	require.Equal(t, "http://localhost:29190", state.GatewayURL)
	require.Equal(t, "h2c", state.TLSMode)

	// A daemon that has not yet been acked is not established, and must not
	// look like one that has connected before.
	require.Equal(t, StreamConnecting, state.Stream)
	require.False(t, state.Stream.Established())
	require.True(t, state.ConnectedAt.IsZero())
}

func TestInitInServerModeStartsListening(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "", "", true))

	state, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, StreamListening, state.Stream)
	require.False(t, state.Stream.Established())
}

// ConnectedAt is the discriminator between "flapping" and "never came up" —
// the question an operator staring at a silent daemon actually has.
func TestSetStreamTracksConnectedAtAndPreservesIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))

	require.NoError(t, SetStream(dir, StreamDisconnected, "write envelope: EOF"))
	down, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, StreamDisconnected, down.Stream)
	require.Equal(t, "write envelope: EOF", down.StreamDetail)
	require.True(t, down.ConnectedAt.IsZero())
	require.Equal(t, os.Getpid(), down.PID, "a stream transition must not lose the process record")
	require.Equal(t, "http://localhost:29190", down.GatewayURL)

	require.NoError(t, SetStream(dir, StreamConnected, ""))
	up, err := Read(dir)
	require.NoError(t, err)
	require.True(t, up.Stream.Established())
	require.False(t, up.ConnectedAt.IsZero())
	require.Empty(t, up.StreamDetail, "a live stream must not carry a stale error")

	require.NoError(t, SetStream(dir, StreamDisconnected, "gateway closed"))
	again, err := Read(dir)
	require.NoError(t, err)
	require.False(t, again.ConnectedAt.IsZero(), "a daemon that has connected before must keep saying so")
}

// A single instantaneous read must be able to answer "has this stream HELD?",
// not only "is it up right this moment". Six `daemon status` samples 12s apart
// all reported "connected" against a daemon that was reconnecting every 15-30s
// — each sample caught a different session, and the check was used as a
// pre-flight that pronounced a known-bad daemon safe for a 30-minute run.
func TestSetStreamRecordsFlapHistory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))

	require.NoError(t, SetStream(dir, StreamConnected, ""))
	first, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, 1, first.Sessions)
	require.True(t, first.LastDisconnectAt.IsZero())
	require.True(t, first.Stable(time.Now().UTC()), "a stream that has never dropped is stable")

	// One flap: connected -> disconnected -> connecting -> connected.
	require.NoError(t, SetStream(dir, StreamDisconnected, "daemon stream receive: unknown: EOF"))
	require.NoError(t, SetStream(dir, StreamConnecting, ""))
	require.NoError(t, SetStream(dir, StreamConnected, ""))

	flapped, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, 2, flapped.Sessions, "each new session must be counted")
	require.False(t, flapped.LastDisconnectAt.IsZero(), "the drop must be recorded")

	now := time.Now().UTC()
	require.True(t, flapped.Stream.Established(),
		"the stream IS up — which is exactly why an instantaneous check passes")
	require.False(t, flapped.Stable(now),
		"a stream that dropped moments ago is not safe to start long work against")
	require.True(t, flapped.Stable(now.Add(StabilityWindow+time.Second)),
		"once it has held for the window, it is stable again")
}

// Re-asserting the state the record already holds is not a new session.
func TestSetStreamDoesNotCountRepeatedConnectedAsANewSession(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))
	require.NoError(t, SetStream(dir, StreamConnected, ""))
	require.NoError(t, SetStream(dir, StreamConnected, ""))

	state, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, 1, state.Sessions)
	require.True(t, state.LastDisconnectAt.IsZero())
}

// A stream that is not established is never stable, however long ago it dropped.
func TestStableRequiresAnEstablishedStream(t *testing.T) {
	state := State{Stream: StreamDisconnected}
	require.False(t, state.Stable(time.Now().UTC()))
}

func TestClearRemovesRecord(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, "http://localhost:29190", "h2c", false))
	require.NoError(t, Clear(dir))

	_, err := Read(dir)
	require.True(t, os.IsNotExist(err))

	// Clearing an absent record is not an error — the daemon may have been
	// stopped twice.
	require.NoError(t, Clear(dir))
}

// An empty data dir means "nowhere to publish"; recording must be inert rather
// than failing a daemon boot.
func TestNoDataDirIsInert(t *testing.T) {
	require.NoError(t, Init("", "http://localhost:29190", "h2c", false))
	require.NoError(t, SetStream("", StreamConnected, ""))
	require.NoError(t, Clear(""))
}
