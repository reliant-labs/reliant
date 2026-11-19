// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wideProject is a project on disk with a checkout at aaa/ and an empty
// directory wide/, and an opener that lists wide/ as a synthetic directory of
// size entries (wideFS). The checkout sorts first, so a scan wide/ stops has
// still found it.
func wideProject(t *testing.T, size int) (string, *wideFS, dirOpener) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "aaa", ".git"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "wide"), 0o755))
	w := &wideFS{t: t, wide: filepath.Join(root, "wide"), size: size}
	return root, w, func(name string) (fs.File, error) {
		if name == w.wide {
			return &wideDir{fsys: w}, nil
		}
		return os.Open(name)
	}
}

// One directory of a billion entries: the entry budget stops Discover within
// one entry of the budget, never after listing (or sorting) the directory
// whole, and it keeps what it found before it, flagged partial.
func TestDiscover_WideDirectoryStopsWithinBudget(t *testing.T) {
	t.Parallel()
	const budget = 1_000
	root, wide, open := wideProject(t, 1_000_000_000)
	d, err := discoverWith(context.Background(), root, Options{MaxEntries: budget}, open)
	require.NoError(t, err)
	assert.True(t, d.Truncated, "a scan the budget stopped is partial")
	assert.Equal(t, fmt.Sprintf("it looked at %d entries", budget), d.TruncatedReason)
	assert.Equal(t, map[string]bool{"aaa": true}, names(d.Repos))
	assert.LessOrEqual(t, wide.served, budget+1, "the wide directory is read no further than the budget")

	// The default budget bounds it the same way.
	root, wide, open = wideProject(t, 1_000_000_000)
	d, err = discoverWith(context.Background(), root, Options{}, open)
	require.NoError(t, err)
	assert.True(t, d.Truncated)
	assert.Equal(t, map[string]bool{"aaa": true}, names(d.Repos))
	assert.LessOrEqual(t, wide.served, DefaultMaxEntries+1)
}

// The deadline and the request are checked between the batches of one
// directory's listing, not only between the entries the walk visits.
func TestDiscover_WideDirectoryStopsBetweenBatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, wide *wideFS) (context.Context, Options)
	}{
		{"the request ends", "the request ended", func(t *testing.T, wide *wideFS) (context.Context, Options) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			wide.onBatch = cancel
			return ctx, Options{}
		}},
		{"the deadline passes", "it ran out of time", func(t *testing.T, wide *wideFS) (context.Context, Options) {
			deadline := time.Now().Add(5 * time.Millisecond)
			// The first batch outlasts the deadline.
			wide.onBatch = func() {
				for time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			}
			return context.Background(), Options{Deadline: deadline}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, wide, open := wideProject(t, 1_000_000_000)
			ctx, opts := tc.setup(t, wide)
			opts.MaxEntries = 1 << 30
			d, err := discoverWith(ctx, root, opts, open)
			require.NoError(t, err)
			assert.True(t, d.Truncated)
			assert.Equal(t, tc.reason, d.TruncatedReason)
			assert.Equal(t, map[string]bool{"aaa": true}, names(d.Repos))
			assert.Equal(t, 1, wide.batches, "the walk stops before the next batch")
		})
	}
}

// shuffledOpener lists every directory in an order seeded by seed, a few
// entries at a time (shuffledDir), as a filesystem with its own listing order
// would.
func shuffledOpener(seed int64) dirOpener {
	return func(name string) (fs.File, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		list, err := f.ReadDir(-1)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		rand.New(rand.NewSource(seed)).Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		return &shuffledDir{ReadDirFile: f, list: list}, nil
	}
}

// The same tree gives the same Discovery whatever order the filesystem lists
// a directory in, even when the budget stops it part-way: which repositories
// a partial scan saw, and their order, must not flap between two scans of an
// unchanged project.
func TestDiscover_PartialScanIsDeterministic(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for g := range 20 {
		for r := range 5 {
			require.NoError(t, os.MkdirAll(filepath.Join(root, fmt.Sprintf("g%02d", g), fmt.Sprintf("r%d", r), ".git"), 0o755))
		}
	}
	// The root is one entry, each group and its five checkouts six more:
	// the budget stops the walk part-way through the groups.
	opts := Options{MaxEntries: 50}

	want, err := Discover(context.Background(), root, opts)
	require.NoError(t, err)
	require.True(t, want.Truncated)
	require.NotEmpty(t, want.Repos)
	require.Less(t, len(want.Repos), 100)
	assert.Equal(t, filepath.Join("g00", "r0"), want.Repos[0].RelativePath, "a partial scan walks in name order")

	for seed := range int64(5) {
		got, err := discoverWith(context.Background(), root, opts, shuffledOpener(seed))
		require.NoError(t, err)
		assert.Equal(t, want, got, "seed %d", seed)
	}
}

// What a directory that cannot be listed means is unchanged by the batched
// walk: a permission error skips it, not even marking the scan partial, and
// any other error fails the scan.
func TestDiscover_ListingErrorsKeepTheirMeaning(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, p := range []string{"api/.git", "locked/inner/.git", "web/.git"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, p), 0o755))
	}
	failing := func(errno error) dirOpener {
		return func(name string) (fs.File, error) {
			if name == filepath.Join(root, "locked") {
				return nil, &fs.PathError{Op: "open", Path: name, Err: errno}
			}
			return os.Open(name)
		}
	}

	d, err := discoverWith(context.Background(), root, Options{}, failing(fs.ErrPermission))
	require.NoError(t, err)
	assert.False(t, d.Truncated, d.TruncatedReason)
	assert.Equal(t, map[string]bool{"api": true, "web": true}, names(d.Repos))

	_, err = discoverWith(context.Background(), root, Options{}, failing(errors.New("input/output error")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan project")
}
