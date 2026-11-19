// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
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

// A prune never runs while an add holds the lock: without waiting it says so
// and runs nothing; waiting, it gives up when ctx does.
func TestPruneWorktrees_DoesNotRunBesideAnAdd(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	release, err := LockWorktreeAdd(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := PruneWorktrees(ctx, repo, false); !errors.Is(err, ErrWorktreeAddInProgress) {
		t.Fatalf("prune beside an add: got %v, want ErrWorktreeAddInProgress", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := PruneWorktrees(waitCtx, repo, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting prune beside an add: got %v, want a deadline", err)
	}
}

// obs feeb22c1: git's add makes .git/worktrees/<name> and only then writes
// its "initializing" lock there, so a `git worktree prune` in between deleted
// the registration and the add died writing its gitdir file. The sweep's
// prune of a gone row's repositories, and a waiting prune, leave such a
// registration alone while the add that makes it holds the add lock.
func TestPruneWorktrees_LeavesARegistrationAnAddIsMaking(t *testing.T) {
	testkit.Slow(t, "builds a real git repository and runs git against it")
	t.Parallel()
	repo := t.TempDir()
	if out, err := gitOK(repo, "init", "-q"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// What a running add has made so far: the admin dir, no lock, no gitdir.
	adm := filepath.Join(repo, ".git", "worktrees", "control-plane66")
	if err := os.MkdirAll(adm, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	release, err := LockWorktreeAdd(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	pruneRegistrations(ctx, Worktree{Checkouts: []Checkout{{RepoPath: repo}}})
	if _, err := os.Stat(adm); err != nil {
		t.Fatalf("the sweep pruned a registration an add was making: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- PruneWorktrees(ctx, repo, true) }()
	select {
	case err := <-done:
		t.Fatalf("a waiting prune ran beside an add: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(adm); err != nil {
		t.Fatalf("a waiting prune removed a registration an add was making: %v", err)
	}

	// The add is over (here: it died, leaving the torn dir): the prune runs.
	release()
	if err := <-done; err != nil {
		t.Fatalf("prune after the add: %v", err)
	}
	if _, err := os.Stat(adm); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the torn registration survived a prune with no add running: %v", err)
	}
}
