// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScratchRevalidatesAfterAdmission(t *testing.T) {
	for _, change := range []string{"keep", "source", "activity", "protected"} {
		t.Run(change, func(t *testing.T) {
			h := newHarness(t)
			dir := h.chat("c1")
			write(t, dir, "app/package.json", "{}")
			write(t, dir, "app/dist/output.js", "generated")
			h.age(dir, 8*day)
			acquire := h.r.Acquire
			h.r.Acquire = func(ctx context.Context, paths []string) (Lease, error) {
				switch change {
				case "keep":
					write(t, dir, KeepFile, "")
				case "source":
					write(t, dir, "new.go", "source")
				case "activity":
					h.scratchUse[dir] = h.now
				case "protected":
					write(t, dir, "app/dist/.env", "secret")
				}
				return acquire(ctx, paths)
			}
			h.pass()
			require.True(t, exists(filepath.Join(dir, "app/dist/output.js")))
		})
	}
}

// The final process check runs once the directory is parked in the trash,
// and again at deletion boundaries: a shell that enters it after that check
// stops the deletion at the next boundary, and what is left goes back to its
// path.
func TestScratchStopsWhenAProcessEntersMidDelete(t *testing.T) {
	requireSafeRemoval(t)
	h := newHarness(t)
	dir := h.chat("c1")
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		write(t, dir, "notes/"+n, n)
	}
	h.age(dir, 8*day)
	trashRoot := filepath.Join(h.r.ScratchRoot, TrashDir)
	processUse := h.r.ProcessUse
	parkedChecks := 0
	h.r.ProcessUse = func(ctx context.Context, paths []string) error {
		for _, p := range paths {
			if filepath.Dir(filepath.Dir(p)) != trashRoot {
				continue
			}
			_, err := os.Lstat(dir)
			require.ErrorIs(t, err, os.ErrNotExist, "the directory is parked before any check that covers its parked path")
			parkedChecks++
			// 1: the final check, before anything is deleted; 2: the boundary
			// before notes/a.md; 3: before notes/b.md, where a shell has entered.
			if parkedChecks >= 3 {
				return errors.New("process 42 has its cwd in " + p)
			}
		}
		return processUse(ctx, paths)
	}

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionHeld, e.Action)
	require.Contains(t, e.Reason, "deletion stopped")
	require.Equal(t, 3, parkedChecks, "no boundary is crossed after the use is seen")
	require.False(t, exists(filepath.Join(dir, "notes/a.md")))
	for _, rel := range []string{"notes/b.md", "notes/c.md", "briefing.md"} {
		require.True(t, exists(filepath.Join(dir, rel)), "%s is restored to its path", rel)
	}
	require.False(t, exists(trashRoot), "nothing stays parked")
}

// A deletion that stops after its path was taken again cannot put the rest
// back. What is left stays parked and is never deleted: not by the recovery
// at the end of the pass, not by later passes. It goes back once its path is
// free.
func TestScratchFailedRestoreIsKeptNotDeleted(t *testing.T) {
	requireSafeRemoval(t)
	h := newHarness(t)
	dir := h.chat("c1")
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		write(t, dir, "notes/"+n, n)
	}
	h.age(dir, 8*day)
	trashRoot := filepath.Join(h.r.ScratchRoot, TrashDir)
	processUse := h.r.ProcessUse
	parkedChecks, parked := 0, ""
	h.r.ProcessUse = func(ctx context.Context, paths []string) error {
		for _, p := range paths {
			if filepath.Dir(filepath.Dir(p)) != trashRoot {
				continue
			}
			parkedChecks++
			if parkedChecks == 3 {
				// Mid-delete: an agent recreates the directory at its old
				// path, and a process writes new work into the parked tree.
				parked = p
				write(t, dir, "new.md", "written at the old path")
				write(t, p, "notes/new-work.md", "unique work")
				return errors.New("process 42 has its cwd in " + p)
			}
		}
		return processUse(ctx, paths)
	}

	rep := h.pass()
	require.NotEmpty(t, parked)
	require.Contains(t, h.entry(rep, dir).Reason, "stays parked")
	kept := h.entry(rep, parked)
	require.Equal(t, ActionHeld, kept.Action)
	require.Contains(t, kept.Reason, "belongs at "+dir)
	requireParked := func() {
		t.Helper()
		for _, rel := range []string{"notes/b.md", "notes/c.md", "notes/new-work.md", "briefing.md"} {
			require.True(t, exists(filepath.Join(parked, rel)), "%s is kept parked", rel)
		}
		require.True(t, exists(filepath.Join(dir, "new.md")), "the new directory at the old path is untouched")
	}
	requireParked()

	h.r.ProcessUse = processUse
	h.pass()
	requireParked()

	// Once the path is free, the next pass puts the tree back.
	require.NoError(t, os.RemoveAll(dir))
	h.pass()
	require.True(t, exists(filepath.Join(dir, "notes/new-work.md")))
	require.True(t, exists(filepath.Join(dir, "notes/c.md")))
	require.False(t, exists(trashRoot))
}

func TestScratchPreservesForceAddedOutput(t *testing.T) {
	for _, output := range []string{"dist", "node_modules"} {
		t.Run(output, func(t *testing.T) {
			h := newHarness(t)
			dir := h.chat("c1")
			gitRun(t, dir, "init")
			write(t, dir, "package.json", "{}")
			write(t, dir, ".gitignore", output+"/\n")
			write(t, dir, output+"/manual.js", "original")
			gitRun(t, dir, "add", "-f", output+"/manual.js")
			write(t, dir, output+"/manual.js", "edited source")
			h.age(dir, 3*day)
			h.pass()
			b, err := os.ReadFile(filepath.Join(dir, output, "manual.js"))
			require.NoError(t, err)
			require.Equal(t, "edited source", string(b))
		})
	}
}

func TestScratchFindsRepositoryInsideNodeModules(t *testing.T) {
	h := newHarness(t)
	dir := h.chat("c1")
	nested := filepath.Join(dir, "node_modules", "local")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	gitRun(t, nested, "init")
	write(t, nested, "work.txt", "unique work")
	h.age(dir, 8*day)
	h.pass()
	require.True(t, exists(filepath.Join(nested, "work.txt")))
}
