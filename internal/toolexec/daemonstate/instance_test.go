// Copyright (c) 2025 Reliant Labs
package daemonstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The stamped key must survive every write path, because a reader that finds it
// empty has to fall back to trusting the record — which is the behaviour this
// field exists to remove.
func TestInstanceKeyRoundTripsThroughInitReadAndSetStream(t *testing.T) {
	root := t.TempDir()
	withInstancesRoot(t, root)
	dir := instanceDir(t, root, "http-localhost-8090-1d4f2c90", "_default", "reliant-9a3b1f22")
	const key = "http-localhost-8090-1d4f2c90/_default/reliant-9a3b1f22"

	require.NoError(t, Init(dir, "http://localhost:8090", "h2c", false))
	initial, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, key, initial.Instance)

	require.NoError(t, SetStream(dir, StreamConnected, ""))
	connected, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, key, connected.Instance, "a stream transition must not drop the instance key")

	// Every load-bearing field must be untouched by the addition.
	require.Equal(t, os.Getpid(), connected.PID)
	require.Equal(t, initial.StartedAt, connected.StartedAt)
	require.Equal(t, initial.Executable, connected.Executable)
	require.Equal(t, initial.BinaryModTime, connected.BinaryModTime)
	require.Equal(t, initial.Revision, connected.Revision)
	require.Equal(t, initial.Dirty, connected.Dirty)
	require.Equal(t, "http://localhost:8090", connected.GatewayURL)
	require.Equal(t, "h2c", connected.TLSMode)
	require.Equal(t, StreamConnected, connected.Stream)
	require.Equal(t, 1, connected.Sessions)
	require.False(t, connected.ConnectedAt.IsZero())
	require.True(t, connected.LastDisconnectAt.IsZero())

	require.NoError(t, SetStream(dir, StreamDisconnected, "write envelope: EOF"))
	dropped, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, key, dropped.Instance)
	require.False(t, dropped.LastDisconnectAt.IsZero())
	require.Equal(t, 1, dropped.Sessions)
}

// Electron reads this field from JavaScript by name. If the two sides disagree
// the record always looks foreign, which fails open — exactly the confusion the
// field was added to remove — so the wire name is pinned here, not just the Go
// field.
func TestInstanceKeyIsSerializedAsInstanceAndOmittedWhenAbsent(t *testing.T) {
	root := t.TempDir()
	withInstancesRoot(t, root)
	dir := instanceDir(t, root, "origin-aaaaaaaa", "_default", "ws-bbbbbbbb")

	require.NoError(t, Init(dir, "http://localhost:8090", "h2c", false))
	raw, err := os.ReadFile(Path(dir))
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.Equal(t, "origin-aaaaaaaa/_default/ws-bbbbbbbb", fields["instance"])

	// A data dir that is not an instance directory claims no identity. Empty
	// means "unknown" and must be absent from the JSON, never an empty string
	// a reader could compare against.
	plain := t.TempDir()
	require.NoError(t, Init(plain, "http://localhost:8090", "h2c", false))
	raw, err = os.ReadFile(Path(plain))
	require.NoError(t, err)
	fields = map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.NotContains(t, fields, "instance")

	state, err := Read(plain)
	require.NoError(t, err)
	require.Empty(t, state.Instance)
	require.Equal(t, os.Getpid(), state.PID, "a non-instance data dir still gets a full record")
}

func TestInstanceForRejectsPathsOutsideTheInstancesRoot(t *testing.T) {
	root := t.TempDir()
	withInstancesRoot(t, root)

	require.Empty(t, InstanceFor(""))
	require.Empty(t, InstanceFor(t.TempDir()), "an unrelated directory names no instance")
	require.Empty(t, InstanceFor(root), "the root itself is not an instance")
	require.Empty(t, InstanceFor(filepath.Join(root, "origin-a", "_default")), "two levels is not an instance")
	require.Empty(t, InstanceFor(filepath.Join(root, "origin-a", "_default", "ws-b", "logs")), "four levels is not an instance")
	require.Equal(t, "origin-a/_default/ws-b", InstanceFor(filepath.Join(root, "origin-a", "_default", "ws-b")))
}
