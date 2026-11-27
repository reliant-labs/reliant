// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// makeGoCache makes dir a Go build cache: the toolchain's README is what
// forge and the daemon take as proof (gocache.IsCache).
func makeGoCache(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := "This directory holds cached build artifacts from the Go build system.\n"
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func roots(s goCachesSeen) []string {
	var out []string
	for _, c := range s.Caches {
		out = append(out, c.Root)
	}
	return out
}

// TestMergeGoCacheSightings: a cache a build was seen using is remembered,
// restamped at most hourly (the record is not rewritten every poll), and
// forgotten a week after the last sighting, or at once when it is no longer a
// Go build cache. A path that is not one is never recorded.
func TestMergeGoCacheSightings(t *testing.T) {
	a, b := makeGoCache(t, filepath.Join(t.TempDir(), "a")), makeGoCache(t, filepath.Join(t.TempDir(), "b"))
	notCache := t.TempDir()
	now := time.Now()

	s, changed := mergeGoCacheSightings(goCachesSeen{}, []string{a, a + "/", notCache, "relative/go-build"}, now)
	if !changed || !reflect.DeepEqual(roots(s), []string{a}) {
		t.Fatalf("first sighting: changed=%v roots=%v, want [%s]", changed, roots(s), a)
	}
	if _, changed := mergeGoCacheSightings(s, []string{a}, now.Add(10*time.Minute)); changed {
		t.Fatal("a sighting minutes after the last rewrote the record")
	}
	s, changed = mergeGoCacheSightings(s, []string{a, b}, now.Add(2*time.Hour))
	if !changed || !reflect.DeepEqual(roots(s), []string{a, b}) {
		t.Fatalf("second cache: changed=%v roots=%v", changed, roots(s))
	}
	// a was last seen at +2h: a week after that it goes, while b is seen again.
	s, _ = mergeGoCacheSightings(s, []string{b}, now.Add(2*time.Hour+goCacheForgetAfter+time.Minute))
	if !reflect.DeepEqual(roots(s), []string{b}) {
		t.Fatalf("a cache unseen for a week must be forgotten, the other kept: %v", roots(s))
	}
	if err := os.Remove(filepath.Join(b, "README")); err != nil {
		t.Fatal(err)
	}
	if s, changed = mergeGoCacheSightings(s, nil, now.Add(goCacheForgetAfter+2*time.Hour)); !changed || len(s.Caches) != 0 {
		t.Fatalf("a directory that is no longer a Go cache must be forgotten: %v", roots(s))
	}
}

func TestMergeGoCacheSightingsIsBounded(t *testing.T) {
	var seen []string
	dir := t.TempDir()
	for i := 0; i < maxGoCachesSeen+5; i++ {
		seen = append(seen, makeGoCache(t, filepath.Join(dir, "c", string(rune('a'+i/26)), string(rune('a'+i%26)))))
	}
	s, _ := mergeGoCacheSightings(goCachesSeen{}, seen, time.Now())
	if len(s.Caches) != maxGoCachesSeen {
		t.Fatalf("%d caches recorded, want at most %d", len(s.Caches), maxGoCachesSeen)
	}
}

func TestGoCacheRootFromEnv(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"GOCACHE": "/w/.gocache", "HOME": "/h"}, "/w/.gocache"},
		{map[string]string{"GOCACHE": "off", "HOME": "/h"}, ""},
		{map[string]string{"XDG_CACHE_HOME": "/x", "HOME": "/h"}, "/x/go-build"},
		{map[string]string{"HOME": "/h"}, "/h/.cache/go-build"},
		{map[string]string{"XDG_CACHE_HOME": "rel", "HOME": "/h"}, "/h/.cache/go-build"},
		{map[string]string{}, ""},
	} {
		if got := goCacheRootFromEnv(tc.env); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.env, got, tc.want)
		}
	}
}

// TestReaperTickRemembersBuildCachesAndHandsThemToForge: each poll records
// the caches running builds use beside the policy, and both forge passes are
// handed every remembered cache with --go-cache, so forge trims it like the
// shared one.
func TestReaperTickRemembersBuildCachesAndHandsThemToForge(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	cache := makeGoCache(t, filepath.Join(t.TempDir(), "worktree", ".gocache"))
	h.w.reaper.scanGoCaches = func() []string { return []string{cache} }
	h.w.reaperTick(context.Background())

	want := []string{"--go-cache", cache}
	if got := goCacheFlags(h.policy); !reflect.DeepEqual(got, want) {
		t.Fatalf("flags = %q, want %q", got, want)
	}
	args := goCacheCommandArgs(h.policy, goCacheFlags(h.policy)...)
	if tail := args[len(args)-2:]; !reflect.DeepEqual(tail, want) {
		t.Fatalf("the Go-cache pass's argv %q does not hand forge the cache", args)
	}
	args = reaperCommandArgs(h.policy, goCacheFlags(h.policy)...)
	if tail := args[len(args)-2:]; !reflect.DeepEqual(tail, want) {
		t.Fatalf("the full pass's argv %q does not hand forge the cache", args)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.policy), goCachesSeenName)); err != nil {
		t.Fatalf("the record is not beside the policy: %v", err)
	}
}
