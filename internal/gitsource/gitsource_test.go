package gitsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeFetcher stands in for git. It records how many times it was called
// — the assertion that matters for the cache — and materializes a
// recognizable tree so the resolver's subdir handling can be checked.
type fakeFetcher struct {
	calls   atomic.Int32
	subdirs []string
	err     error
}

func (f *fakeFetcher) Fetch(_ context.Context, _ Source, dst string) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dst, "README.md"), []byte("repo root\n"), 0o644); err != nil {
		return "", err
	}
	for _, sub := range f.subdirs {
		dir := filepath.Join(dst, filepath.FromSlash(sub))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
			return "", err
		}
	}
	return "deadbeefcafe", nil
}

func testResolver(t *testing.T, projectDir string, f Fetcher, overrides map[string]string) *Resolver {
	t.Helper()
	if overrides == nil {
		overrides = map[string]string{}
	}
	r, err := NewResolver(projectDir,
		WithFetcher(f),
		WithCacheRoot(filepath.Join(t.TempDir(), "cache")),
		WithOverrides(overrides))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

// TestValidate_RejectsMalformed pins the shapes a source must not take.
// The ref rule is the load-bearing one: forge refuses to default to a
// branch, because an unpinned cross-repo dependency is the exact problem
// GitSource exists to remove.
func TestValidate_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		want string
	}{
		{"missing repo", Source{Ref: "v1.0.0"}, "source.repo is required"},
		{"missing ref", Source{Repo: "github.com/org/app"}, "source.ref is required"},
		{"empty", Source{}, "both required"},
		{"bad repo", Source{Repo: "not a repo!", Ref: "v1"}, "not a recognized repository"},
		{"bad ref", Source{Repo: "github.com/org/app", Ref: "--upload-pack=evil"}, "not a valid git ref"},
		{"escaping subdir", Source{Repo: "github.com/org/app", Ref: "v1", Subdir: "../etc"}, "must be a relative path"},
		{"absolute subdir", Source{Repo: "github.com/org/app", Ref: "v1", Subdir: "/etc"}, "must be a relative path"},
		// The same verdict on every host: a Windows spelling is not a loophole on
		// POSIX, and a POSIX one is not a loophole on Windows.
		{"drive-letter subdir", Source{Repo: "github.com/org/app", Ref: "v1", Subdir: `C:\etc`}, "must be a relative path"},
		{"backslash-rooted subdir", Source{Repo: "github.com/org/app", Ref: "v1", Subdir: `\etc`}, "must be a relative path"},
		{"backslash-escaping subdir", Source{Repo: "github.com/org/app", Ref: "v1", Subdir: `..\etc`}, "must be a relative path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.src.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestValidate_AcceptsWellFormed covers the spellings a user may
// reasonably write, including the explicit-URL forms a private host needs.
func TestValidate_AcceptsWellFormed(t *testing.T) {
	for _, src := range []Source{
		{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web"},
		{Repo: "github.com/reliant-labs/reliant", Ref: "main"},
		{Repo: "github.com/org/app", Ref: "3f1a2b9c4d5e6f70819a2b3c4d5e6f7081920a1b"},
		{Repo: "https://github.com/org/app.git", Ref: "v1"},
		{Repo: "ssh://git@git.internal/team/app.git", Ref: "v1"},
		{Repo: "git@github.com:org/app.git", Ref: "release/2024-06"},
	} {
		if err := src.Validate(); err != nil {
			t.Errorf("Validate(%s) = %v, want nil", src, err)
		}
	}
}

// TestResolve_FetchesThenReusesCache is the core cache guarantee: a
// second resolution of the same pin does NO fetch. Without it every build
// would re-clone, which is the reason a naive implementation of this
// feature gets reverted.
func TestResolve_FetchesThenReusesCache(t *testing.T) {
	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r := testResolver(t, t.TempDir(), fetcher, nil)
	src := Source{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web"}

	first, err := r.Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("after first Resolve, fetches = %d, want 1", got)
	}
	if first.Cached {
		t.Error("first Resolve reported Cached=true; nothing was in the cache yet")
	}
	if first.Commit != "deadbeefcafe" {
		t.Errorf("Commit = %q, want the fetcher's resolved sha", first.Commit)
	}

	second, err := r.Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Errorf("after second Resolve, fetches = %d, want 1 — the cache was not reused", got)
	}
	if !second.Cached {
		t.Error("second Resolve reported Cached=false; it should have been a cache hit")
	}
	if second.Dir != first.Dir {
		t.Errorf("second Resolve dir = %q, want the cached %q", second.Dir, first.Dir)
	}

	// A fresh resolver over the SAME cache root must also hit — the cache
	// is on disk, not in the process.
	r2, err := NewResolver(t.TempDir(), WithFetcher(fetcher),
		WithCacheRoot(filepath.Dir(filepath.Dir(first.Dir))), WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if _, err := r2.Resolve(context.Background(), src); err != nil {
		t.Fatalf("cross-process Resolve: %v", err)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Errorf("fresh resolver over the same cache root fetched again (calls = %d)", got)
	}
}

// TestResolve_SubdirPointsInsideTheFetchedTree confirms the resolved dir
// is the subdir, not the repo root — the frontend's build runs there.
func TestResolve_SubdirPointsInsideTheFetchedTree(t *testing.T) {
	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r := testResolver(t, t.TempDir(), fetcher, nil)

	res, err := r.Resolve(context.Background(), Source{
		Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(res.Dir) != "web" {
		t.Errorf("resolved dir = %q, want it to end in the subdir", res.Dir)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "package.json")); err != nil {
		t.Errorf("resolved dir does not contain the fetched subdir contents: %v", err)
	}
}

// TestResolve_MissingSubdirIsAClearError — a typo'd subdir must name
// itself rather than surfacing later as a confusing npm failure.
func TestResolve_MissingSubdirIsAClearError(t *testing.T) {
	r := testResolver(t, t.TempDir(), &fakeFetcher{subdirs: []string{"web"}}, nil)
	_, err := r.Resolve(context.Background(), Source{
		Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "frontend",
	})
	if err == nil {
		t.Fatal("Resolve with a nonexistent subdir = nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "frontend") {
		t.Errorf("error %q should name the missing subdir", err)
	}
}

// TestResolve_DistinctRefsGetDistinctCacheEntries — two pins of one repo
// must not fight over one directory, or bumping a ref in one env would
// silently change another.
func TestResolve_DistinctRefsGetDistinctCacheEntries(t *testing.T) {
	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r := testResolver(t, t.TempDir(), fetcher, nil)

	a, err := r.Resolve(context.Background(), Source{Repo: "github.com/org/app", Ref: "v1.0.0", Subdir: "web"})
	if err != nil {
		t.Fatalf("Resolve v1.0.0: %v", err)
	}
	b, err := r.Resolve(context.Background(), Source{Repo: "github.com/org/app", Ref: "v2.0.0", Subdir: "web"})
	if err != nil {
		t.Fatalf("Resolve v2.0.0: %v", err)
	}
	if a.Dir == b.Dir {
		t.Errorf("two refs of one repo resolved to the same dir %q", a.Dir)
	}
	if got := fetcher.calls.Load(); got != 2 {
		t.Errorf("fetches = %d, want 2 (one per distinct ref)", got)
	}
}

// TestResolve_SameRefDifferentSubdirsFetchOnce — the cache key covers
// repo+ref but not subdir, so a project consuming two directories of one
// pin pays for one fetch.
func TestResolve_SameRefDifferentSubdirsFetchOnce(t *testing.T) {
	fetcher := &fakeFetcher{subdirs: []string{"web", "admin"}}
	r := testResolver(t, t.TempDir(), fetcher, nil)
	base := Source{Repo: "github.com/org/app", Ref: "v1.0.0"}

	web := base
	web.Subdir = "web"
	admin := base
	admin.Subdir = "admin"

	wres, err := r.Resolve(context.Background(), web)
	if err != nil {
		t.Fatalf("Resolve web: %v", err)
	}
	ares, err := r.Resolve(context.Background(), admin)
	if err != nil {
		t.Fatalf("Resolve admin: %v", err)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1 — two subdirs of one pin share a checkout", got)
	}
	if filepath.Dir(wres.Dir) != filepath.Dir(ares.Dir) {
		t.Errorf("subdirs resolved into different checkouts: %q vs %q", wres.Dir, ares.Dir)
	}
}

// TestResolve_IncompleteCacheEntryIsRefetched — a fetch interrupted
// partway leaves a directory with no completion marker. Trusting it would
// build from a half-materialized tree, which fails in ways that look like
// a bug in the dependency rather than a bad cache.
func TestResolve_IncompleteCacheEntryIsRefetched(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r, err := NewResolver(t.TempDir(), WithFetcher(fetcher),
		WithCacheRoot(cacheRoot), WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := Source{Repo: "github.com/org/app", Ref: "v1.0.0", Subdir: "web"}

	// Plant debris: the entry directory with content but no marker.
	entry := filepath.Join(cacheRoot, src.CacheKey())
	if err := os.MkdirAll(filepath.Join(entry, "web"), 0o755); err != nil {
		t.Fatalf("plant debris: %v", err)
	}
	if err := os.WriteFile(filepath.Join(entry, "web", "stale.txt"), []byte("half\n"), 0o644); err != nil {
		t.Fatalf("plant debris: %v", err)
	}

	if _, err := r.Resolve(context.Background(), src); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1 — the incomplete entry should have been refetched", got)
	}
	if _, err := os.Stat(filepath.Join(entry, "web", "stale.txt")); !os.IsNotExist(err) {
		t.Error("debris from the interrupted fetch survived; the entry was not cleared")
	}
}

// TestResolve_FailedFetchLeavesNoCacheEntry — a failure must not leave
// something a later run treats as a hit.
func TestResolve_FailedFetchLeavesNoCacheEntry(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	fetcher := &fakeFetcher{err: errors.New("ref not found")}
	r, err := NewResolver(t.TempDir(), WithFetcher(fetcher),
		WithCacheRoot(cacheRoot), WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := Source{Repo: "github.com/org/app", Ref: "v9.9.9"}

	if _, err := r.Resolve(context.Background(), src); err == nil {
		t.Fatal("Resolve with a failing fetcher = nil error")
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, src.CacheKey())); !os.IsNotExist(err) {
		t.Error("a failed fetch left a cache entry behind")
	}
}

// TestResolve_LocalOverrideWinsOverThePin is the "20% are never
// disempowered" clause: an override resolves to the working copy, and no
// fetch happens at all.
func TestResolve_LocalOverrideWinsOverThePin(t *testing.T) {
	projectDir := t.TempDir()
	sibling := filepath.Join(projectDir, "..", "reliant")
	if err := os.MkdirAll(filepath.Join(sibling, "web"), 0o755); err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r := testResolver(t, projectDir, fetcher, map[string]string{
		"github.com/reliant-labs/reliant": "../reliant",
	})

	res, err := r.Resolve(context.Background(), Source{
		Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Overridden {
		t.Error("Resolution.Overridden = false, want true")
	}
	if got := fetcher.calls.Load(); got != 0 {
		t.Errorf("fetches = %d, want 0 — an override must not fetch the pin", got)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(sibling, "web"))
	gotDir, _ := filepath.EvalSymlinks(res.Dir)
	if gotDir != wantDir {
		t.Errorf("resolved dir = %q, want the override %q", gotDir, wantDir)
	}
}

// TestResolve_OverrideMatchesAcrossRepoSpellings — an override written
// as host/owner/name must match a source declared as a full https URL, or
// the override silently does nothing and the developer debugs the wrong
// thing.
func TestResolve_OverrideMatchesAcrossRepoSpellings(t *testing.T) {
	projectDir := t.TempDir()
	local := filepath.Join(projectDir, "local-reliant")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("create local: %v", err)
	}
	fetcher := &fakeFetcher{}
	r := testResolver(t, projectDir, fetcher, map[string]string{
		"github.com/reliant-labs/reliant": "local-reliant",
	})

	for _, spelling := range []string{
		"github.com/reliant-labs/reliant",
		"https://github.com/reliant-labs/reliant.git",
		"git@github.com:reliant-labs/reliant.git",
	} {
		res, err := r.Resolve(context.Background(), Source{Repo: spelling, Ref: "v1.6.3"})
		if err != nil {
			t.Fatalf("Resolve(%s): %v", spelling, err)
		}
		if !res.Overridden {
			t.Errorf("Resolve(%s) did not match the override", spelling)
		}
	}
	if got := fetcher.calls.Load(); got != 0 {
		t.Errorf("fetches = %d, want 0", got)
	}
}

// TestResolve_OverrideToMissingDirIsAnError — a stale override must say
// so, not silently fall back to the pin. Falling back would build
// something other than what the developer believes they are building.
func TestResolve_OverrideToMissingDirIsAnError(t *testing.T) {
	projectDir := t.TempDir()
	r := testResolver(t, projectDir, &fakeFetcher{}, map[string]string{
		"github.com/org/app": "does-not-exist",
	})
	_, err := r.Resolve(context.Background(), Source{Repo: "github.com/org/app", Ref: "v1"})
	if err == nil {
		t.Fatal("Resolve with an override to a missing dir = nil error")
	}
	if !strings.Contains(err.Error(), OverridesFileName) {
		t.Errorf("error %q should point at %s", err, OverridesFileName)
	}
}

// TestRefresh_DropsTheCacheEntry — the supported way to move a branch
// pin. Without it a branch ref would be cached forever with no recourse
// short of deleting the cache by hand.
func TestRefresh_DropsTheCacheEntry(t *testing.T) {
	fetcher := &fakeFetcher{subdirs: []string{"web"}}
	r := testResolver(t, t.TempDir(), fetcher, nil)
	src := Source{Repo: "github.com/org/app", Ref: "main", Subdir: "web"}

	if _, err := r.Resolve(context.Background(), src); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := r.Refresh(src); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := r.Resolve(context.Background(), src); err != nil {
		t.Fatalf("Resolve after Refresh: %v", err)
	}
	if got := fetcher.calls.Load(); got != 2 {
		t.Errorf("fetches = %d, want 2 — Refresh should force a re-fetch", got)
	}
}

// TestCloneURL covers the shorthand expansion and the pass-through that a
// private host or an ssh remote depends on.
func TestCloneURL(t *testing.T) {
	cases := map[string]string{
		"github.com/org/app":            "https://github.com/org/app.git",
		"github.com/org/app.git":        "https://github.com/org/app.git",
		"https://github.com/org/app":    "https://github.com/org/app",
		"ssh://git@host/team/app.git":   "ssh://git@host/team/app.git",
		"git@github.com:org/app.git":    "git@github.com:org/app.git",
		"git.internal.example/team/app": "https://git.internal.example/team/app.git",
	}
	for repo, want := range cases {
		if got := (Source{Repo: repo, Ref: "v1"}).CloneURL(); got != want {
			t.Errorf("CloneURL(%q) = %q, want %q", repo, got, want)
		}
	}
}

// TestCacheKey_StableAndDistinguishing — the key must be deterministic
// (so a rebuild hits) and must separate refs (so pins don't collide).
func TestCacheKey_StableAndDistinguishing(t *testing.T) {
	a := Source{Repo: "github.com/org/app", Ref: "v1.0.0"}
	// Determinism across separately-constructed values: a rebuild must
	// land on the same cache entry, or the cache never hits.
	sameAgain := Source{Repo: "github.com/org/app", Ref: "v1.0.0"}
	if a.CacheKey() != sameAgain.CacheKey() {
		t.Error("CacheKey is not deterministic across equal sources")
	}
	b := Source{Repo: "github.com/org/app", Ref: "v2.0.0"}
	if a.CacheKey() == b.CacheKey() {
		t.Error("different refs produced the same cache key")
	}
	c := Source{Repo: "github.com/org/other", Ref: "v1.0.0"}
	if a.CacheKey() == c.CacheKey() {
		t.Error("different repos produced the same cache key")
	}
	// Subdir must NOT affect the key — two subdirs share one checkout.
	d := a
	d.Subdir = "web"
	if a.CacheKey() != d.CacheKey() {
		t.Error("subdir changed the cache key; two subdirs of one pin would fetch twice")
	}
	if !strings.HasPrefix(a.CacheKey(), "github.com-org-app-") {
		t.Errorf("CacheKey %q should start with a readable repo slug", a.CacheKey())
	}
}

// TestLoadOverrides_MissingFileIsNotAnError — having no overrides is the
// normal state, and the pinned path must work without any local file.
func TestLoadOverrides_MissingFileIsNotAnError(t *testing.T) {
	got, err := LoadOverrides(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOverrides on a project with no override file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LoadOverrides = %v, want empty", got)
	}
}

// TestLoadOverrides_RoundTrip covers the file shape through the writer
// that a future `forge source override` subcommand would use.
func TestLoadOverrides_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOverrides(dir, map[string]string{
		"github.com/reliant-labs/reliant": "../reliant",
	}); err != nil {
		t.Fatalf("WriteOverrides: %v", err)
	}
	got, err := LoadOverrides(dir)
	if err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}
	if got["github.com/reliant-labs/reliant"] != "../reliant" {
		t.Errorf("round-trip = %v, want the written override", got)
	}
}

// TestLoadOverrides_MalformedIsAnError — a typo'd override silently
// ignored is the worst failure available here: the build quietly uses the
// pin while the developer believes they are testing their working copy.
func TestLoadOverrides_MalformedIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, OverridesDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, OverridesDirName, OverridesFileName)
	if err := os.WriteFile(path, []byte("sources: [this is not a map\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOverrides(dir); err == nil {
		t.Fatal("LoadOverrides on a malformed file = nil error")
	}
}

// TestResolve_ValidatesBeforeTouchingTheNetwork — a malformed pin must
// fail on its own terms, not as a confusing git error.
func TestResolve_ValidatesBeforeTouchingTheNetwork(t *testing.T) {
	fetcher := &fakeFetcher{}
	r := testResolver(t, t.TempDir(), fetcher, nil)
	if _, err := r.Resolve(context.Background(), Source{Repo: "github.com/org/app"}); err == nil {
		t.Fatal("Resolve with no ref = nil error")
	}
	if got := fetcher.calls.Load(); got != 0 {
		t.Errorf("fetches = %d, want 0 — validation must precede the fetch", got)
	}
}
