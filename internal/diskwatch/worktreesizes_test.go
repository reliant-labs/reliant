// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wtRoot builds <root>/<project>/<worktree> trees plus a loose file, and
// returns the root and each worktree's allocated bytes.
func wtRoot(t *testing.T) (string, map[string]uint64, uint64) {
	t.Helper()
	root := t.TempDir()
	sizes := map[string]uint64{}
	mk := func(rel string, n int) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("proj/a/node_modules/x/index.js", 64<<10)
	mk("proj/a/src/main.go", 9000)
	mk("proj/b/README.md", 128<<10)
	mk(".reclaim/old/reliant/file", 32<<10)
	mk("proj/NOTES.md", 4096)
	for _, u := range []string{"proj/a", "proj/b", ".reclaim/old"} {
		sizes[filepath.Join(root, filepath.FromSlash(u))] = allocatedUnder(t, filepath.Join(root, filepath.FromSlash(u)))
	}
	info, err := os.Lstat(filepath.Join(root, "proj", "NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	return root, sizes, allocatedBytes(info)
}

func allocatedUnder(t *testing.T, dir string) uint64 {
	t.Helper()
	var n uint64
	if err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			n += allocatedBytes(info)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorktreeSizes_MeasuresEachWorktreeAndAddsLooseFiles(t *testing.T) {
	t.Parallel()
	root, sizes, loose := wtRoot(t)
	var s worktreeSizes
	got := s.measure(context.Background(), root, time.Now())
	want := loose
	for _, n := range sizes {
		want += n
	}
	if got.bytes != want || got.approx {
		t.Fatalf("got %d (approx=%v), want exactly %d", got.bytes, got.approx, want)
	}
}

// A scan whose budget is gone before it reaches some worktrees still reports
// them, at the mean of those it knows, marked approximate: the root is never
// counted as the few GB one cut-off walk saw, with the rest left in "Other".
func TestWorktreeSizes_UnmeasuredWorktreesAreEstimatedNotDropped(t *testing.T) {
	t.Parallel()
	root, sizes, loose := wtRoot(t)
	a := filepath.Join(root, "proj", "a")
	now := time.Now()
	s := worktreeSizes{units: map[string]unitSize{a: {bytes: 10 << 30, at: now, complete: true}}}

	spent, cancel := context.WithCancel(context.Background())
	cancel()
	got := s.measure(spent, root, now)
	if want := loose + 3*(10<<30); got.bytes != want || !got.approx {
		t.Fatalf("got %d (approx=%v), want %d approximate: two unmeasured worktrees at the mean of the one known", got.bytes, got.approx, want)
	}

	// The next scan measures them; the fresh one is not walked again.
	got = s.measure(context.Background(), root, now.Add(time.Minute))
	want := loose + 10<<30 + sizes[filepath.Join(root, "proj", "b")] + sizes[filepath.Join(root, ".reclaim", "old")]
	if got.bytes != want || got.approx {
		t.Fatalf("got %d (approx=%v), want exactly %d", got.bytes, got.approx, want)
	}

	// Once its size is old, it is measured again.
	got = s.measure(context.Background(), root, now.Add(worktreeUnitRefresh+time.Minute))
	want = loose + sizes[a] + sizes[filepath.Join(root, "proj", "b")] + sizes[filepath.Join(root, ".reclaim", "old")]
	if got.bytes != want {
		t.Fatalf("got %d, want %d after the refresh", got.bytes, want)
	}
}

func TestWorktreeSizes_RemovedWorktreesAreForgotten(t *testing.T) {
	t.Parallel()
	root, sizes, loose := wtRoot(t)
	var s worktreeSizes
	s.measure(context.Background(), root, time.Now())
	b := filepath.Join(root, "proj", "b")
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	got := s.measure(context.Background(), root, time.Now())
	if want := loose + sizes[filepath.Join(root, "proj", "a")] + sizes[filepath.Join(root, ".reclaim", "old")]; got.bytes != want {
		t.Fatalf("got %d, want %d: a removed worktree's size is freed", got.bytes, want)
	}
	if _, ok := s.units[b]; ok {
		t.Fatal("the removed worktree is still remembered")
	}
}

// measureConsumers reports the worktree root as Workspaces from the per-
// worktree cache, so "Other" holds only what no consumer accounts for.
func TestMeasureConsumers_WorktreeRootIsNotSwallowedByOther(t *testing.T) {
	t.Parallel()
	root, _, _ := wtRoot(t)
	now := time.Now()
	w := &Watcher{now: func() time.Time { return now }}
	// Earlier scans measured a huge worktree; this scan has no time left.
	w.wtSizes.units = map[string]unitSize{filepath.Join(root, "proj", "a"): {bytes: 200 << 30, at: now, complete: true}}
	v := &volume{members: []member{{Target: Target{Path: root, Role: RoleWorktrees, Kind: KindWorktrees, Label: "Workspaces"}, resolved: root}}}
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	var ws uint64
	for _, c := range w.measureConsumers(spent, v) {
		if c.Kind == KindWorktrees {
			ws = c.Bytes
			if !c.Approximate {
				t.Error("an estimate must be marked approximate")
			}
		}
	}
	if ws < 3*(200<<30) {
		t.Fatalf("Workspaces = %d bytes, want the remembered and estimated worktrees (≥ %d)", ws, uint64(3*(200<<30)))
	}
}
