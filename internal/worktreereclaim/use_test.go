// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Path use: work the daemon runs that names a path in a workspace is use of
// it, and an active answer reports the last one (Result.LastUse).

func TestUseLog_RecordsEveryWorkspaceAToolInputNames(t *testing.T) {
	t.Parallel()
	root := "/home/me/.reliant/worktrees"
	now := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	u := NewUseLog(root, "", func() time.Time { return now }, "~/.reliant/worktrees")

	// What 2026-11-10's agent sent: its chat bound elsewhere, the paths absolute.
	u.Note(`{"file_path":"/home/me/.reliant/worktrees/proj/fix-a1b2/reliant/internal/x.go","old_string":"a"}`)
	u.Note(`{"command":"cd /home/me/.reliant/worktrees/proj/build-c3d4 && go test ./...","timeout":600000}`)
	u.Note(`{"command":"ls ~/.reliant/worktrees/proj/tilde-e5f6/reliant"}`)
	u.Note(`{"path":"/home/me/.reliant/worktrees/legacy-0001"}`)
	u.Note(`{"path":"/home/me/.reliant/worktrees/proj/../escape/x"}`)
	u.Note(`{"path":"/home/me/elsewhere/proj/never"}`)

	for _, used := range []string{
		root + "/proj/fix-a1b2", root + "/proj/build-c3d4", root + "/proj/tilde-e5f6", root + "/legacy-0001",
		root + "/proj/fix-a1b2/reliant", // a checkout of a workspace
		root + "/proj/",                 // Clean'd like a row's path
	} {
		if got := u.LastUse(used); !got.Equal(now) {
			t.Errorf("LastUse(%s) = %v, want %v", used, got, now)
		}
	}
	for _, unused := range []string{
		root + "/proj/other-9999", root + "/escape", root + "/proj/escape", "/home/me/elsewhere/proj/never", root,
	} {
		if got := u.LastUse(unused); !got.IsZero() {
			t.Errorf("LastUse(%s) = %v, want none", unused, got)
		}
	}

	// Later use moves it on; it never moves back.
	later := now.Add(time.Hour)
	u.now = func() time.Time { return later }
	u.Note("/home/me/.reliant/worktrees/proj/build-c3d4")
	if got := u.LastUse(root + "/proj/build-c3d4"); !got.Equal(later) {
		t.Fatalf("after later use: %v, want %v", got, later)
	}
	u.now = func() time.Time { return now }
	u.Note("/home/me/.reliant/worktrees/proj/build-c3d4")
	if got := u.LastUse(root + "/proj/build-c3d4"); !got.Equal(later) {
		t.Fatalf("an earlier clock lowered it: %v", got)
	}
}

func TestUseLog_PersistsHashesAcrossRestarts(t *testing.T) {
	t.Parallel()
	root := "/home/me/.reliant/worktrees"
	state := filepath.Join(t.TempDir(), UseFile)
	now := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	u := NewUseLog(root, state, clock)
	u.Note(root + "/proj/first")
	// The first use is written at once; the next within useFlushEvery waits.
	now = now.Add(time.Second)
	u.Note(root + "/proj/second")
	if got := NewUseLog(root, state, clock).LastUse(root + "/proj/second"); !got.IsZero() {
		t.Fatalf("written before useFlushEvery: %v", got)
	}
	if got := NewUseLog(root, state, clock).LastUse(root + "/proj/first"); got.IsZero() {
		t.Fatal("the first use was not written")
	}
	u.Flush()

	restarted := NewUseLog(root, state, clock)
	if got := restarted.LastUse(root + "/proj/second"); !got.Equal(now) {
		t.Fatalf("after a restart: %v, want %v", got, now)
	}
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "proj") || strings.Contains(string(b), "second") {
		t.Fatalf("the log stores paths: %s", b)
	}
}

func TestUseLog_NilIsEmpty(t *testing.T) {
	t.Parallel()
	var u *UseLog
	u.Note(`/home/me/.reliant/worktrees/proj/x`)
	u.Flush()
	if got := u.LastUse("/home/me/.reliant/worktrees/proj/x"); !got.IsZero() {
		t.Fatal(got)
	}
}

func TestActiveAnswerReportsLastUse(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a linked worktree and an active reconcile (~10 git processes)")
	f := newFixture(t)
	w := f.worktree("used", "feat/used")
	w.State, w.Fence = StateActive, ""

	// Nothing named it: its own stamps (directory, HEAD, reflog) only.
	res := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if res.Outcome != OutcomeLocked || res.LastUse == nil {
		t.Fatalf("got %+v, want locked with its own stamps as last use", res)
	}
	if time.Since(*res.LastUse) > time.Hour {
		t.Fatalf("its stamps say %v; it was just created", *res.LastUse)
	}

	// A tool call named a file in it, later than anything on disk.
	used := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	f.r.Use = NewUseLog(f.root, "", func() time.Time { return used })
	f.r.Use.Note(`{"file_path":"` + filepath.Join(w.Path, "README.md") + `"}`)
	res = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if res.LastUse == nil || !res.LastUse.Equal(used) {
		t.Fatalf("LastUse = %v, want the tool call's %v", res.LastUse, used)
	}
}
