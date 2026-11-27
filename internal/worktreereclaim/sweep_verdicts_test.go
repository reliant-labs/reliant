// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestVerdictStampInvalidatesOnGitClassificationInputs(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "creates a checkout and changes refs and config (~40 git processes)")
	f := newFixture(t)
	w := f.worktree("stamp", "feat/stamp")
	head := commit(t, w.Path, "work.txt", "unpushed\n")
	e := &SweepEntry{Key: "stamp", Path: w.Path, Head: head}
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	c := loadVerdicts(t.TempDir())
	stamp := func() string {
		t.Helper()
		s := verdictStamp(ctx, e, now)
		if s == "" {
			t.Fatal("could not stamp checkout")
		}
		return s
	}
	previous := stamp()
	if got := stamp(); got != previous {
		t.Fatal("unchanged checkout changed its stamp")
	}
	// None of these mutations changes the worktree's HEAD, index, reflog,
	// or newest file time. Each must nevertheless invalidate a cached hold.
	changes := []struct {
		name  string
		apply func()
	}{
		{"push", func() { run(t, w.Path, "push", "-q", "origin", "feat/stamp") }},
		{"pack refs", func() { run(t, w.Path, "pack-refs", "--all") }},
		{"delete packed remote ref", func() { run(t, w.Path, "update-ref", "-d", "refs/remotes/origin/feat/stamp") }},
		{"remote config", func() { run(t, w.Path, "config", "remote.origin.url", "https://example.invalid/repo.git") }},
		{"upstream config", func() { run(t, w.Path, "config", "branch.feat/stamp.merge", "refs/heads/other") }},
		{"local base", func() { run(t, w.Path, "update-ref", "refs/heads/main", head) }},
		{"per-worktree ref", func() { run(t, w.Path, "update-ref", "refs/worktree/held", head) }},
		{"pseudo ref", func() { write(t, filepath.Join(adminDir(w.Path), "ORIG_HEAD"), head+"\n") }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			c.old[verdictKey(e)] = cachedVerdict{Stamp: previous, At: now, Reason: ReasonUnpushed}
			change.apply()
			next := stamp()
			if change.name == "pack refs" {
				if next != previous {
					t.Fatal("packing unchanged refs changed their semantic stamp")
				}
			} else {
				if _, ok := c.get(e, next, now); ok {
					t.Fatal("git classification input changed but cached hold was reused")
				}
			}
			previous = next
		})
	}
}
