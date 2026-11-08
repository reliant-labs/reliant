// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Adds share the add lock; a prune excludes them and never waits for one.
func TestAddLock_AddsShareItAndPruneExcludesThem(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	common := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(common, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first, err := LockWorktreeAdd(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LockWorktreeAdd(ctx, repo)
	if err != nil {
		t.Fatalf("a second add waited on the first: %v", err)
	}
	if _, ok, err := tryLockPrune(common); err != nil || ok {
		t.Fatalf("prune took the lock while adds held it: ok=%v err=%v", ok, err)
	}
	first()
	if _, ok, _ := tryLockPrune(common); ok {
		t.Fatal("prune took the lock while one add still held it")
	}
	second()

	release, ok, err := tryLockPrune(common)
	if err != nil || !ok {
		t.Fatalf("prune could not take a free lock: ok=%v err=%v", ok, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := LockWorktreeAdd(waitCtx, repo); err == nil {
		t.Fatal("an add took the lock while a prune held it")
	}
	release()
	again, err := LockWorktreeAdd(ctx, repo)
	if err != nil {
		t.Fatalf("an add could not take the lock after the prune released it: %v", err)
	}
	again()
}
