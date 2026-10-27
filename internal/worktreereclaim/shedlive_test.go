// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// age sets path's mtime to at.
func age(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// Every source/cache/tool/output/data write is activity, at any depth.
// Only git administration is excluded; it has separate semantic HEAD checks.
func TestLastActivity_IncludesRegenerablePaths(t *testing.T) {
	t.Parallel()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now().Add(-time.Minute)
	layout := func(t *testing.T) string {
		c := t.TempDir()
		for _, p := range []string{"main.go", "web/src/app.ts", "web/node_modules/pkg/index.js", "dist/app.js",
			".tools/bin/tool", ".git", "bin/run.sh", "data/rows.csv"} {
			write(t, filepath.Join(c, filepath.FromSlash(p)), "x\n")
		}
		// Oldest first, so each directory ends older than what is inside it.
		_ = filepath.WalkDir(c, func(p string, _ os.DirEntry, _ error) error { return os.Chtimes(p, old, old) })
		return c
	}
	cases := map[string]struct {
		touch    string // made fresh before reading
		wantIdle bool
	}{
		"nothing changed":                 {wantIdle: true},
		"a cache changed":                 {touch: "web/node_modules/pkg/index.js"},
		"build output changed":            {touch: "dist/app.js"},
		"a pinned tool changed":           {touch: ".tools/bin/tool"},
		"the git pointer changed":         {touch: ".git", wantIdle: true},
		"a source file changed":           {touch: "web/src/app.ts"},
		"a top-level file changed":        {touch: "main.go"},
		"ignored data changed":            {touch: "data/rows.csv"},
		"a bin/ that is not skip changed": {touch: "bin/run.sh"},
		"a directory changed":             {touch: "web/src"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := layout(t)
			if tc.touch != "" {
				age(t, filepath.Join(c, filepath.FromSlash(tc.touch)), fresh)
			}
			last, err := lastActivity(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if idle := time.Since(last) > 24*time.Hour; idle != tc.wantIdle {
				t.Fatalf("idle = %v (last activity %v ago), want %v", idle, time.Since(last).Round(time.Minute), tc.wantIdle)
			}
		})
	}
}

// A walk that cannot see the whole checkout says so rather than guess.
func TestLastActivity_UnreadableIsUnknown(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	c := t.TempDir()
	locked := filepath.Join(c, "private")
	write(t, filepath.Join(locked, "f"), "x\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := lastActivity(context.Background(), c); err == nil {
		t.Fatal("an unreadable directory was read as idle")
	}
}

// liveFixture is a live (active) worktree filled with regenerable bulk and
// real work, asked to shed after an hour, with the clock a day ahead: every
// file in it looks a day old unless a case freshens it.
func liveFixture(t *testing.T, id string) (*fixture, Worktree, []string) {
	t.Helper()
	f := stripFixture(t)
	w := f.worktree(id, "feat/"+id)
	bulk := fill(t, w.Path)
	write(t, filepath.Join(w.Path, "wip.txt"), "unsaved work\n")
	write(t, filepath.Join(w.Path, "data", "rows.csv"), "1,2\n")
	w.State, w.Fence, w.ShedIdleAfterSeconds = StateActive, "", 3600
	// As the server sends it: the checkout named, so locking does not walk
	// into the pre-commit clone fill planted under .tools/.
	w.Checkouts = []Checkout{{RepoPath: f.project, Rel: ""}}
	return f, w, bulk
}

// An idle live worktree loses its regenerable bulk, keeps every tracked,
// untracked and data file and its git state, and stays locked and live.
func TestReconcile_IdleLiveWorktreeShedsRegenerableFiles(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f, w, bulk := liveFixture(t, "live-shed")
	status := run(t, w.Path, "status", "--porcelain")
	head := run(t, w.Path, "rev-parse", "HEAD")
	before, err := os.Stat(w.Path)
	if err != nil {
		t.Fatal(err)
	}

	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeLocked {
		t.Fatalf("got %+v, want locked", got)
	}
	for _, p := range bulk {
		if exists(filepath.Join(w.Path, filepath.FromSlash(p))) {
			t.Errorf("%s survived; it is regenerable and the worktree is idle", p)
		}
		if !slices.Contains(got.Stripped, p) {
			t.Errorf("%s is not in the stripped paths %v", p, got.Stripped)
		}
	}
	if got.StrippedBytes <= 0 {
		t.Errorf("stripped bytes = %d, want > 0", got.StrippedBytes)
	}
	for _, p := range []string{"wip.txt", "a.txt", "data/rows.csv", ".gitignore"} {
		if !exists(filepath.Join(w.Path, filepath.FromSlash(p))) {
			t.Errorf("%s was deleted; only regenerable files may be", p)
		}
	}
	if s := run(t, w.Path, "status", "--porcelain"); s != status {
		t.Errorf("git status changed:\n%s\nwant\n%s", s, status)
	}
	if h := run(t, w.Path, "rev-parse", "HEAD"); h != head {
		t.Errorf("HEAD moved: %s, want %s", h, head)
	}
	after, err := os.Stat(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("shedding moved the checkout's mtime %v -> %v; a shed is not activity", before.ModTime(), after.ModTime())
	}
	info, err := inspectLock(context.Background(), w.Path)
	if err != nil || !info.Locked || info.Reason != LockReason(w.ID) {
		t.Fatalf("lock after shedding = %+v (%v), want the live lock %q", info, err, LockReason(w.ID))
	}
}

// Anything that says the worktree is in use, or that the request does not ask
// for a shed, keeps every file.
func TestReconcile_LiveShedOnlyWhenIdleAndAsked(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds a git fixture per case; skipped under -short")
	}
	ahead := func(f *fixture) time.Time { return f.r.now().Add(-time.Minute) }
	cases := map[string]func(f *fixture, w *Worktree){
		"not asked":       func(_ *fixture, w *Worktree) { w.ShedIdleAfterSeconds = 0 },
		"keep everything": func(_ *fixture, w *Worktree) { w.KeepFiles = true },
		"being restored":  func(_ *fixture, w *Worktree) { w.Retire = "100" },
		"in use": func(f *fixture, w *Worktree) {
			path := filepath.Join(w.Path, "web")
			f.r.Cwds = func(context.Context) ([]string, error) { return []string{path}, nil }
		},
		"processes unreadable": func(f *fixture, _ *Worktree) {
			f.r.Cwds = func(context.Context) ([]string, error) { return nil, os.ErrPermission }
		},
		"a tracked file was edited":  func(f *fixture, w *Worktree) { age(f.t, filepath.Join(w.Path, "a.txt"), ahead(f)) },
		"untracked work was written": func(f *fixture, w *Worktree) { age(f.t, filepath.Join(w.Path, "wip.txt"), ahead(f)) },
		"ignored data was written": func(f *fixture, w *Worktree) {
			age(f.t, filepath.Join(w.Path, "data", "rows.csv"), ahead(f))
		},
		"idle for less than asked": func(_ *fixture, w *Worktree) { w.ShedIdleAfterSeconds = int64(48 * time.Hour / time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, w, _ := liveFixture(t, "live-keep")
			mutate(f, &w)
			got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
			if got.Outcome != OutcomeLocked {
				t.Fatalf("got %+v, want locked: a kept worktree is still locked as live", got)
			}
			if len(got.Stripped) != 0 || got.StrippedBytes != 0 {
				t.Fatalf("got %+v, want nothing stripped", got)
			}
			if !exists(filepath.Join(w.Path, "web", "node_modules", "pkg", "index.js")) {
				t.Fatal("node_modules was deleted")
			}
		})
	}
}

// Fresh activity inside the target itself keeps the whole workspace: a build
// can use its cache without any process having its cwd inside the workspace.
func TestReconcile_LiveShedPreservesActivityInsideCaches(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f, w, _ := liveFixture(t, "live-cache-busy")
	age(t, filepath.Join(w.Path, "web", "node_modules", "pkg", "index.js"), f.r.now())
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if !exists(filepath.Join(w.Path, "web", "node_modules", "pkg", "index.js")) || got.StrippedBytes != 0 || len(got.Stripped) != 0 {
		t.Fatalf("fresh cache activity failed to preserve workspace: %+v", got)
	}
}
