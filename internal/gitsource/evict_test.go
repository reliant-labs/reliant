package gitsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// seedEntry materializes a cache entry that LOOKS like one Resolve wrote:
// a tree with content, plus the completion marker whose mtime is the
// recorded last-use time.
func seedEntry(t *testing.T, root, name string, lastUse time.Time) string {
	t.Helper()
	entry := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(entry, "web"), 0o755); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(entry, "web", "bundle.js"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := writeMetadata(entry, Metadata{Repo: "github.com/acme/app", Ref: "v1"}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := os.Chtimes(filepath.Join(entry, MetadataFile), lastUse, lastUse); err != nil {
		t.Fatalf("seed mtime %s: %v", name, err)
	}
	return entry
}

// neverInUse is the stub every table case installs so no test shells out
// to lsof. The in-use path has its own case below.
func neverInUse(string) bool { return false }

func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestEvictRetainsNewestAndRecentAndUnreadable is the whole policy in one
// table: what gets reclaimed, and the four distinct reasons something is
// retained.
func TestEvictRetainsNewestAndRecentAndUnreadable(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)

	tests := []struct {
		name string
		// seed is name -> last use. A nil time means "write a directory
		// with no readable metadata" (interrupted-fetch debris).
		seed       map[string]*time.Time
		policy     EvictPolicy
		wantEvict  []string
		wantKeptAt int
	}{
		{
			name: "old and not in the newest K is evicted",
			seed: map[string]*time.Time{
				"app-aaaaaaaaaaaa": &now,
				"app-bbbbbbbbbbbb": ptr(now.Add(-time.Hour)),
				"app-cccccccccccc": &old,
			},
			policy:     EvictPolicy{KeepPerSlug: 2, InUse: neverInUse},
			wantEvict:  []string{"app-cccccccccccc"},
			wantKeptAt: 2,
		},
		{
			name: "the newest K survive even when every entry is ancient",
			seed: map[string]*time.Time{
				"app-aaaaaaaaaaaa": &old,
				"app-bbbbbbbbbbbb": ptr(old.Add(-time.Hour)),
			},
			policy:     EvictPolicy{KeepPerSlug: 2, InUse: neverInUse},
			wantEvict:  nil,
			wantKeptAt: 2,
		},
		{
			name: "recent entries survive past the keep floor",
			seed: map[string]*time.Time{
				"app-aaaaaaaaaaaa": &now,
				"app-bbbbbbbbbbbb": ptr(now.Add(-time.Hour)),
				"app-cccccccccccc": &recent,
			},
			policy:     EvictPolicy{KeepPerSlug: 2, InUse: neverInUse},
			wantEvict:  nil,
			wantKeptAt: 3,
		},
		{
			name: "unparseable metadata fails closed",
			seed: map[string]*time.Time{
				"app-aaaaaaaaaaaa": &now,
				"app-bbbbbbbbbbbb": ptr(now.Add(-time.Hour)),
				"app-cccccccccccc": &old,
				"app-dddddddddddd": nil,
			},
			policy:     EvictPolicy{KeepPerSlug: 2, InUse: neverInUse},
			wantEvict:  []string{"app-cccccccccccc"},
			wantKeptAt: 3,
		},
		{
			name: "the keep floor is per repository, not per cache",
			seed: map[string]*time.Time{
				"app-aaaaaaaaaaaa":   &old,
				"other-bbbbbbbbbbbb": &old,
			},
			policy:     EvictPolicy{KeepPerSlug: 1, InUse: neverInUse},
			wantEvict:  nil,
			wantKeptAt: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sources")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, at := range tc.seed {
				if at == nil {
					if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				seedEntry(t, root, name, *at)
			}

			var log strings.Builder
			got, err := Evict(root, now, tc.policy, true, &log)
			if err != nil {
				t.Fatalf("Evict: %v", err)
			}
			if want, have := sortedNames(tc.wantEvict), sortedNames(got.Removed); strings.Join(want, ",") != strings.Join(have, ",") {
				t.Fatalf("evicted %v, want %v\n%s", have, want, log.String())
			}
			if got.Kept != tc.wantKeptAt {
				t.Fatalf("kept %d, want %d\n%s", got.Kept, tc.wantKeptAt, log.String())
			}
			// What the plan said must match what is on disk.
			for name := range tc.seed {
				_, statErr := os.Stat(filepath.Join(root, name))
				evicted := contains(tc.wantEvict, name)
				if evicted && statErr == nil {
					t.Errorf("%s was reported evicted but still exists", name)
				}
				if !evicted && statErr != nil {
					t.Errorf("%s was retained but is gone: %v", name, statErr)
				}
			}
		})
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func ptr(t time.Time) *time.Time { return &t }

// TestEvictDryRunRemovesNothing is the guard on the preview contract: the
// plan a human reviews must not have already happened.
func TestEvictDryRunRemovesNothing(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	seedEntry(t, root, "app-aaaaaaaaaaaa", now)
	doomed := seedEntry(t, root, "app-bbbbbbbbbbbb", now.Add(-90*24*time.Hour))

	var log strings.Builder
	got, err := Evict(root, now, EvictPolicy{KeepPerSlug: 1, InUse: neverInUse}, false, &log)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if len(got.Removed) != 1 || got.Removed[0] != "app-bbbbbbbbbbbb" {
		t.Fatalf("preview plan %v, want [app-bbbbbbbbbbbb]", got.Removed)
	}
	if got.Bytes <= 0 {
		t.Errorf("preview reported %d bytes; a plan with no size tells a reviewer nothing", got.Bytes)
	}
	if _, err := os.Stat(doomed); err != nil {
		t.Fatalf("dry run removed %s: %v", doomed, err)
	}
	if !strings.Contains(log.String(), "evict source cache") {
		t.Errorf("preview printed no plan:\n%s", log.String())
	}
}

// TestEvictNeverRemovesAnEntryHeldByARunningProcess pins the
// never-evict-in-use rule, and makes the process check load-bearing: the
// held entry is the OLDEST of its slug, so the keep floor cannot be what
// spares it. A `forge env up` builds from the resolved directory, so an
// entry something is reading must survive however idle it looks.
func TestEvictNeverRemovesAnEntryHeldByARunningProcess(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Both names must share a slug, or the second becomes its own group
	// and the keep floor spares it for the wrong reason.
	seedEntry(t, root, "app-aaaaaaaaaaaa", now)
	held := seedEntry(t, root, "app-ffffffffffff", now.Add(-200*24*time.Hour))

	got, err := Evict(root, now, EvictPolicy{
		KeepPerSlug: 1,
		InUse:       func(entry string) bool { return entry == held },
	}, true, nil)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the oldest entry was in use and must survive: %v", err)
	}
	if len(got.Held) != 1 || got.Held[0] != "app-ffffffffffff" {
		t.Errorf("Held = %v, want [app-ffffffffffff] so a caller can retry later", got.Held)
	}
	if len(got.Removed) != 0 {
		t.Errorf("Removed = %v, want nothing", got.Removed)
	}
}

// TestEvictRemovesReadOnlyTrees reproduces the node_modules shape: an
// installed tree leaves mode-0444 files under mode-0555 directories, and
// a plain RemoveAll fails partway through on EACCES.
func TestEvictRemovesReadOnlyTrees(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	seedEntry(t, root, "app-aaaaaaaaaaaa", now)
	entry := seedEntry(t, root, "app-bbbbbbbbbbbb", now.Add(-90*24*time.Hour))

	locked := filepath.Join(entry, "web", "node_modules", "pkg")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "index.js"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, err := Evict(root, now, EvictPolicy{KeepPerSlug: 1, InUse: neverInUse}, true, nil); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if _, err := os.Stat(entry); err == nil {
		t.Fatal("a read-only tree survived eviction")
	}
}

// TestEvictStoppedPartwayLeavesNoHalfEntryAndIsFinished: the caller's
// RemoveTree may stop partway (machine maintenance caps and paces its
// unlinks). The entry is renamed out of its cache key first, so Resolve never
// finds a half-deleted clone, and the next Evict finishes the leftover.
func TestEvictStoppedPartwayLeavesNoHalfEntryAndIsFinished(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	seedEntry(t, root, "app-aaaaaaaaaaaa", now)
	doomed := seedEntry(t, root, "app-bbbbbbbbbbbb", now.Add(-90*24*time.Hour))

	stopped := errors.New("delete cap reached")
	policy := EvictPolicy{KeepPerSlug: 1, InUse: neverInUse, RemoveTree: func(string) error { return stopped }}
	if _, err := Evict(root, now, policy, true, nil); !errors.Is(err, stopped) {
		t.Fatalf("the stop must reach the caller, got %v", err)
	}
	if _, err := os.Stat(doomed); err == nil {
		t.Fatal("a half-evicted entry is still under its cache key")
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, evictingPrefix+"*"))
	if len(leftovers) != 1 {
		t.Fatalf("want one leftover, got %v", leftovers)
	}

	got, err := Evict(root, now, EvictPolicy{KeepPerSlug: 1, InUse: neverInUse}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftovers[0]); err == nil {
		t.Fatal("the next Evict did not finish the interrupted eviction")
	}
	if got.Kept != 1 {
		t.Fatalf("the leftover was counted as a kept entry: %+v", got)
	}
}

// TestEvictMissingRootIsNotAnError — a machine that never fetched a
// source has nothing to reclaim, and maintenance must not fail on it.
func TestEvictMissingRootIsNotAnError(t *testing.T) {
	got, err := Evict(filepath.Join(t.TempDir(), "absent"), time.Now(), EvictPolicy{InUse: neverInUse}, true, nil)
	if err != nil {
		t.Fatalf("Evict on a missing root: %v", err)
	}
	if len(got.Removed) != 0 || got.Kept != 0 {
		t.Fatalf("unexpected plan %+v", got)
	}
}

// TestResolveTouchesLastUseOnCacheHit is the half of the mechanism that
// makes eviction correct: without it every entry's recorded last use is
// its FETCH time, so a pin resolved daily for a month looks a month idle
// and gets reclaimed out from under the project still using it.
func TestResolveTouchesLastUseOnCacheHit(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	fake := &fakeFetcher{}
	r, err := NewResolver(t.TempDir(), WithFetcher(fake),
		WithCacheRoot(cacheRoot), WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := Source{Repo: "github.com/acme/app", Ref: "v1.0.0"}
	if _, err := r.Resolve(context.Background(), src); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	marker := filepath.Join(cacheRoot, src.CacheKey(), MetadataFile)
	stale := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}

	res, err := r.Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !res.Cached {
		t.Fatal("second Resolve was not a cache hit; the touch path never ran")
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Hour {
		t.Fatalf("cache hit left last-use at %s; Resolve must record the hit", info.ModTime())
	}
	// And the marker must still be a valid completion marker: the touch
	// may not rewrite its contents.
	if _, ok := readMetadata(filepath.Join(cacheRoot, src.CacheKey())); !ok {
		t.Fatal("the touch corrupted the completion marker")
	}
}

// TestEntrySlugGroupsOnlyRealCacheKeys — an unrecognized directory must
// be its own group, so it can never consume another repository's keep
// floor or be counted against it.
func TestEntrySlugGroupsOnlyRealCacheKeys(t *testing.T) {
	for name, want := range map[string]string{
		"github.com-acme-app-0123456789ab": "github.com-acme-app",
		"github.com-acme-app-0123456789aZ": "github.com-acme-app-0123456789aZ",
		"github.com-acme-app-abc":          "github.com-acme-app-abc",
		"noseparator":                      "noseparator",
		"-0123456789ab":                    "-0123456789ab",
	} {
		if got := entrySlug(name); got != want {
			t.Errorf("entrySlug(%q) = %q, want %q", name, got, want)
		}
	}
}
