// Copyright (c) 2025 Reliant Labs
package daemonstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// withInstancesRoot points enumeration and instance-key derivation at a temp
// directory. The developer's real ~/.reliant/instances holds live daemons; a
// test that probed it would take locks against processes it does not own.
func withInstancesRoot(t *testing.T, root string) {
	t.Helper()
	previous := instancesRoot
	instancesRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { instancesRoot = previous })
}

// instanceDir creates a three-segment instance directory under root and returns
// it, mirroring daemoninstance's projection.
func instanceDir(t *testing.T, root, origin, sub, workspace string) string {
	t.Helper()
	dir := filepath.Join(root, origin, sub, workspace)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

func TestProbeReportsHeldLockAsRunningAndUnheldAsNot(t *testing.T) {
	dir := t.TempDir()

	// No lock file at all: nothing has ever claimed this directory.
	locked, err := ProbeLocked(dir)
	require.NoError(t, err)
	require.False(t, locked)

	held, err := Acquire(dir)
	require.NoError(t, err)

	locked, err = ProbeLocked(dir)
	require.NoError(t, err)
	require.True(t, locked, "a directory held by a live daemon must report as running")

	require.NoError(t, held.Release())

	locked, err = ProbeLocked(dir)
	require.NoError(t, err)
	require.False(t, locked, "once the holder releases, the directory is free")
}

// The probe must leave nothing behind. A leaked probe lock would make every
// subsequent daemon start fail with ErrLocked against a daemon that does not
// exist — a read-only command causing an outage.
func TestProbeReleasesSoADaemonCanStillStart(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, LockFileName), nil, 0o644))

	for range 3 {
		locked, err := ProbeLocked(dir)
		require.NoError(t, err)
		require.False(t, locked)
	}

	lock, err := Acquire(dir)
	require.NoError(t, err, "the probe must not leave the directory claimed")
	t.Cleanup(func() { _ = lock.Release() })

	// And a probe taken while a real daemon holds it still does not disturb
	// that daemon: the holder keeps its claim afterwards.
	locked, err := ProbeLocked(dir)
	require.NoError(t, err)
	require.True(t, locked)

	second, err := Acquire(dir)
	require.ErrorIs(t, err, ErrLocked, "the original holder must still own the directory")
	require.Nil(t, second)
}

// Probing must never create or destroy the lock file. Unlinking it in
// particular would hand the next daemon a fresh inode and defeat the
// one-daemon-per-directory guarantee entirely.
func TestProbeDoesNotCreateOrRemoveTheLockFile(t *testing.T) {
	dir := t.TempDir()

	_, err := ProbeLocked(dir)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, LockFileName))
	require.True(t, os.IsNotExist(err), "probing an unclaimed directory must not create a lock file")

	lock, err := Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())

	before, err := os.Stat(filepath.Join(dir, LockFileName))
	require.NoError(t, err)

	_, err = ProbeLocked(dir)
	require.NoError(t, err)

	after, err := os.Stat(filepath.Join(dir, LockFileName))
	require.NoError(t, err, "probing must never unlink daemon.lock")
	require.Equal(t, before.Size(), after.Size(), "probing must not truncate the lock file")
}

func TestListReportsEachInstanceUnderItsOwnKey(t *testing.T) {
	root := t.TempDir()
	withInstancesRoot(t, root)

	alpha := instanceDir(t, root, "http-localhost-8090-aaaaaaaa", "_default", "reliant-11111111")
	beta := instanceDir(t, root, "http-localhost-8090-aaaaaaaa", "sub-bbbbbbbb", "reliant-22222222")
	gamma := instanceDir(t, root, "https-staging-cccccccc", "_default", "reliant-33333333")

	// alpha has a live daemon and a connected record.
	alphaLock, err := Acquire(alpha)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alphaLock.Release() })
	require.NoError(t, Init(alpha, "http://localhost:8090", "h2c", false))
	require.NoError(t, SetStream(alpha, StreamConnected, ""))

	// beta has a record but no holder — a corpse.
	require.NoError(t, Init(beta, "http://localhost:8090", "h2c", false))

	// gamma has neither.
	_ = gamma

	instances, err := List(root)
	require.NoError(t, err)
	require.Len(t, instances, 3)

	byKey := map[string]Instance{}
	for _, instance := range instances {
		require.NotContains(t, byKey, instance.Slug, "each instance must appear exactly once")
		byKey[instance.Slug] = instance
	}

	alphaKey := "http-localhost-8090-aaaaaaaa/_default/reliant-11111111"
	betaKey := "http-localhost-8090-aaaaaaaa/sub-bbbbbbbb/reliant-22222222"
	gammaKey := "https-staging-cccccccc/_default/reliant-33333333"
	require.Contains(t, byKey, alphaKey)
	require.Contains(t, byKey, betaKey)
	require.Contains(t, byKey, gammaKey)

	require.True(t, byKey[alphaKey].Locked)
	require.True(t, byKey[alphaKey].HasRecord)
	require.Equal(t, StreamConnected, byKey[alphaKey].Record.Stream)
	require.Equal(t, os.Getpid(), byKey[alphaKey].Record.PID)

	require.False(t, byKey[betaKey].Locked, "a record without a holder is not a running daemon")
	require.True(t, byKey[betaKey].HasRecord)
	require.Equal(t, StreamConnecting, byKey[betaKey].Record.Stream)

	require.False(t, byKey[gammaKey].Locked)
	require.False(t, byKey[gammaKey].HasRecord)

	// The two records must never be conflated: each carries its OWN instance
	// key, which is the whole point of stamping it.
	require.Equal(t, alphaKey, byKey[alphaKey].Record.Instance)
	require.Equal(t, betaKey, byKey[betaKey].Record.Instance)
	require.NotEqual(t, byKey[alphaKey].Record.Instance, byKey[betaKey].Record.Instance)
}

// Sibling directories at the wrong depth are not instances. Enumerating them
// would invent keys that no daemon ever claims.
func TestListIgnoresPathsThatAreNotThreeLevelsDeep(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "origin-aaaaaaaa", "_default"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "origin-aaaaaaaa", "_default", "ws-bbbbbbbb", "logs"), 0o700))

	instances, err := List(root)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "origin-aaaaaaaa/_default/ws-bbbbbbbb", instances[0].Slug)
}

// A machine where no daemon has ever run is a legitimate state, not a fault.
func TestListOnMissingRootReportsNoInstances(t *testing.T) {
	instances, err := List(filepath.Join(t.TempDir(), "never-created"))
	require.NoError(t, err)
	require.Empty(t, instances)
}
