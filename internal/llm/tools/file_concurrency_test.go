// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustReturnWithin fails the test if f has not returned by d. A deadlock in the
// path lock would otherwise hang the whole package until the go test timeout,
// which reports as a stall rather than as this test failing.
func mustReturnWithin(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s while the path lock was held — deadlock", what, d)
	}
}

// TestPathLockNotHeldAcrossPermissionGate proves the lock cannot deadlock
// against a permission gate, because no gate runs inside it.
//
// A per-path lock held across anything that waits on a human would turn a lost
// write into a hang, which is worse. Two things make that impossible here, and
// this test pins both:
//
//   - The gate runs BEFORE the lock. Tool authorization is a static tier
//     comparison — MinimumPermissionForTool + PermissionAtLeast — evaluated in
//     the ExecuteTools worker at execute_tools.go:318 before executeSingleTool is
//     called, so before ToolWrapper.Run, so before Execute, so before the lock.
//     It compares two strings and never blocks on anything.
//   - The Tool interface's RequiresPermission is not an execution-time gate at
//     all: outside the per-tool implementations, the only reference in the tree
//     is ToolWrapper.RequiresPermission passing through to it, and nothing calls
//     that during execution.
//
// So the locked region is exactly the read-modify-write. This test holds the
// lock and drives every gate an edit passes through on its way to Execute; each
// must complete while the lock is held.
func TestPathLockNotHeldAcrossPermissionGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gated.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), 0o644))

	// Hold the lock for exactly the path the gates are about to be asked about.
	lock := pathLockFor(path)
	require.NoError(t, lock.acquire(context.Background()))
	defer lock.release()

	mustReturnWithin(t, 5*time.Second, "permission tier gate", func() {
		required := MinimumPermissionForTool(EditToolName)
		assert.Equal(t, PermissionMutating, required, "edit is a mutating tool")
		assert.True(t, PermissionAtLeast(PermissionMutating, required),
			"a mutating agent must clear edit's gate")
		assert.False(t, PermissionAtLeast(PermissionReadOnly, required),
			"a readonly agent must not clear edit's gate")
	})

	wrapped := NewEditTool()

	mustReturnWithin(t, 5*time.Second, "ToolWrapper.RequiresPermission", func() {
		needs, err := wrapped.RequiresPermission(
			newTestToolContext(t, dir, "gate-chat", "0"),
			ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"` + path + `","old_string":"hello","new_string":"HELLO"}`},
		)
		assert.NoError(t, err)
		assert.True(t, needs, "edit declares that it needs permission")
	})

	mustReturnWithin(t, 5*time.Second, "ToolWrapper.ParamSchema", func() {
		assert.NotNil(t, wrapped.ParamSchema())
	})

	// And the whole wrapper path, for a DIFFERENT file, must run to completion
	// while this file's lock is held — proving the lock scope is per path and
	// does not gate unrelated work.
	other := filepath.Join(dir, "other.txt")
	require.NoError(t, os.WriteFile(other, []byte("world\n"), 0o644))
	mustReturnWithin(t, 5*time.Second, "ToolWrapper.Run on an unrelated file", func() {
		resp, err := wrapped.Run(
			newTestToolContext(t, dir, "gate-chat", "0"),
			ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"` + other + `","old_string":"world","new_string":"WORLD"}`},
		)
		assert.NoError(t, err)
		assert.False(t, resp.IsError, "unrelated edit should succeed: %s", resp.Content)
	})
}

// TestPathLockIsContextCancellable is the by-construction backstop. Even if some
// future caller did hold the lock across something slow, a waiter cannot hang:
// the lock is waited on with the tool call's own context, so it fails with a
// visible error at the deadline instead of blocking forever.
//
// The failure is an ERROR, never a success — the whole point of the fix is that
// an edit which could not be applied must say so.
func TestPathLockIsContextCancellable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contended.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), 0o644))

	// Somebody else holds the lock for the whole test.
	lock := pathLockFor(path)
	require.NoError(t, lock.acquire(context.Background()))
	defer lock.release()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	toolCtx := rctx.NewToolContext(ctx, "cancel-chat", "0", nil, &rctx.WorktreeInfo{ID: "test", Path: dir}).
		WithMessageID("test-message").
		WithDaemon(daemon.NewLocalClient())

	var resp ToolResponse
	var err error
	start := time.Now()
	mustReturnWithin(t, 5*time.Second, "edit blocked on a held path lock", func() {
		resp, err = (&editTool{}).Execute(toolCtx, EditParams{
			FilePath:  path,
			OldString: "hello",
			NewString: "HELLO",
		})
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, resp.IsError, "a blocked edit must report an error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "timed out waiting for exclusive access")
	assert.NotContains(t, resp.Content, "Content replaced in file",
		"a blocked edit must never report success")
	assert.Less(t, elapsed, 3*time.Second, "should fail at its own deadline, not hang")

	// Nothing was written.
	assert.Equal(t, "hello\n", readRaw(t, path), "a refused edit must not touch the file")
}

// TestPathLockMultiPathOrdering pins the property that keeps move_code from
// deadlocking against itself: two calls that lock the same pair of files in
// opposite argument order still acquire in the same global order, so neither can
// hold one while waiting for the other.
func TestPathLockMultiPathOrdering(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")

	ctxA := newTestToolContext(t, dir, "order-chat", "0")
	ctxB := newTestToolContext(t, dir, "order-chat", "0")

	inA := make(chan struct{})
	release := make(chan struct{})

	// Holder locks (a, b) and stays inside.
	go func() {
		_, _ = withPathLocks(ctxA, []string{a, b}, func() (ToolResponse, error) {
			close(inA)
			<-release
			return NewTextResponse("held"), nil
		})
	}()
	<-inA

	// Contender asks for the same pair in the OPPOSITE order. If locks were taken
	// in argument order this is the classic ABBA deadlock; ordered acquisition
	// makes it a plain wait that clears as soon as the holder leaves.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = withPathLocks(ctxB, []string{b, a}, func() (ToolResponse, error) {
			return NewTextResponse("acquired"), nil
		})
	}()

	select {
	case <-done:
		t.Fatal("contender acquired the pair while the holder still held it")
	case <-time.After(100 * time.Millisecond):
		// Correct: it is waiting, not deadlocked and not racing through.
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("contender never acquired the pair after the holder released — deadlock")
	}
}

// TestWriteFileGuardedDetectsClobber exercises the write-time check directly:
// when the bytes on disk are not the bytes the edit was computed from, the write
// must be reported as a conflict and the other writer's content put back.
func TestWriteFileGuardedDetectsClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cas.txt")
	require.NoError(t, os.WriteFile(path, []byte("theirs\n"), 0o644))

	ctx := newTestToolContext(t, dir, "cas-chat", "0")

	// We think the file says "ours-basis" but it actually says "theirs".
	err := writeFileGuarded(ctx, path, "ours-basis\n", "our-edit\n")
	require.Error(t, err)

	var conflict *ConcurrentModificationError
	require.ErrorAs(t, err, &conflict)
	assert.True(t, conflict.Restored, "their content should have been restored")
	assert.Equal(t, "theirs\n", readRaw(t, path), "the other writer's content must survive")

	// The matching case is an ordinary write.
	require.NoError(t, writeFileGuarded(ctx, path, "theirs\n", "ours\n"))
	assert.Equal(t, "ours\n", readRaw(t, path))
}
