// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// quarantineEpoch is the fixed instant the quarantine tests date their
// output at; no test reads the wall clock.
var quarantineEpoch = time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

// quarantineOutput makes base/dist with the named files, all dated
// quarantineEpoch, and returns it with the trash beside it.
func quarantineOutput(t *testing.T, names ...string) (dir, trash string) {
	t.Helper()
	skipWithoutSafeInspection(t)
	base := t.TempDir()
	dir, trash = filepath.Join(base, "dist"), filepath.Join(base, ".trash", "pass")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, n := range names {
		p := filepath.Join(dir, n)
		require.NoError(t, os.WriteFile(p, []byte("generated "+n), 0o600))
		require.NoError(t, os.Chtimes(p, quarantineEpoch, quarantineEpoch))
	}
	require.NoError(t, os.Chtimes(dir, quarantineEpoch, quarantineEpoch))
	return dir, trash
}

func requireTrashEmpty(t *testing.T, trash string) {
	t.Helper()
	entries, err := os.ReadDir(trash)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	require.NoError(t, err)
	require.Empty(t, entries, "nothing may stay parked in the trash")
}

func TestOutputInventorySurvivesAuthorization(t *testing.T) {
	dir, trash := quarantineOutput(t, "output.js")
	err := RemoveInventoriedOutput(context.Background(), dir, 0, quarantineEpoch.Add(time.Hour), Quarantine{
		Trash: trash,
		Authorize: func(context.Context) error {
			return os.WriteFile(filepath.Join(dir, ".env"), []byte("unique secret"), 0o600)
		},
		InUse: func(context.Context, []string) error { return nil },
	})
	require.Error(t, err)
	require.FileExists(t, filepath.Join(dir, "output.js"))
	require.FileExists(t, filepath.Join(dir, ".env"))
	requireTrashEmpty(t, trash)
}

// The final use check runs with the directory already parked: nothing can
// reach it by its old name any more, and a process inside it shows at its
// parked path. A use found there puts it back whole.
func TestQuarantineParksBeforeFinalUseCheck(t *testing.T) {
	dir, trash := quarantineOutput(t, "a.js", "b.js")
	var seen []string
	err := RemoveInventoriedOutput(context.Background(), dir, time.Hour, quarantineEpoch.Add(2*time.Hour), Quarantine{
		Trash: trash,
		InUse: func(_ context.Context, paths []string) error {
			seen = paths
			_, statErr := os.Lstat(dir)
			require.ErrorIs(t, statErr, os.ErrNotExist, "the original path must be gone before the final check")
			require.Len(t, paths, 2)
			require.Equal(t, dir, paths[0])
			require.Equal(t, trash, filepath.Dir(paths[1]))
			require.FileExists(t, filepath.Join(paths[1], "a.js"))
			return errors.New("process 42 has its cwd in " + paths[1])
		},
	})
	require.ErrorContains(t, err, "process 42")
	require.NotEmpty(t, seen)
	require.FileExists(t, filepath.Join(dir, "a.js"))
	require.FileExists(t, filepath.Join(dir, "b.js"))
	requireTrashEmpty(t, trash)
}

// A process that starts using the tree after the final check stops the
// deletion at the next boundary, and what is left goes back to its path.
func TestQuarantineStopsWhenUseStartsMidDelete(t *testing.T) {
	dir, trash := quarantineOutput(t, "a.js", "b.js", "c.js", "d.js", "e.js")
	calls := 0
	err := RemoveInventoriedOutput(context.Background(), dir, 0, quarantineEpoch, Quarantine{
		Trash: trash,
		InUse: func(context.Context, []string) error {
			calls++
			// 1: the final authorization; 2, 3: the boundaries before a.js
			// and b.js; 4: before c.js, where a shell has entered.
			if calls >= 4 {
				return errors.New("process 42 has its cwd in it")
			}
			return nil
		},
	})
	require.ErrorContains(t, err, "deletion stopped")
	require.Equal(t, 4, calls, "no boundary may be crossed after use is seen")
	require.NoFileExists(t, filepath.Join(dir, "a.js"))
	require.NoFileExists(t, filepath.Join(dir, "b.js"))
	for _, n := range []string{"c.js", "d.js", "e.js"} {
		require.FileExists(t, filepath.Join(dir, n), "the rest is restored to its path")
	}
	requireTrashEmpty(t, trash)
}

func TestQuarantineRemovesAnIdleTree(t *testing.T) {
	dir, trash := quarantineOutput(t, "a.js", "b.js")
	calls := 0
	err := RemoveInventoriedOutput(context.Background(), dir, 0, quarantineEpoch, Quarantine{
		Trash: trash,
		InUse: func(context.Context, []string) error { calls++; return nil },
	})
	require.NoError(t, err)
	require.NoDirExists(t, dir)
	require.Equal(t, 4, calls, "the final check, one per entry, and one before the directory itself")
	requireTrashEmpty(t, trash)
}

func TestQuarantineIsRequired(t *testing.T) {
	dir, _ := quarantineOutput(t, "a.js")
	require.Error(t, RemoveInventoriedOutput(context.Background(), dir, 0, quarantineEpoch, Quarantine{}))
	require.FileExists(t, filepath.Join(dir, "a.js"))
}
