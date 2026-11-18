package gitsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The tests in this file drive the REAL GitFetcher, so they spawn git
// subprocesses. They are skipped under -short per forge's testing tiers.
//
// They never touch the network: each builds a throwaway local repository
// in a temp dir and fetches from it by path. That still exercises the
// whole fetch path — init, remote add, the shallow fetch and its
// unshallowed fallback, the detached checkout, and rev-parse — which the
// injected-fetcher unit tests deliberately do not.

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// buildSourceRepo creates a local repository with a web/ subdir, one
// commit, and a v1.0.0 tag. It returns the repo path and the commit sha.
func buildSourceRepo(t *testing.T) (repoDir, commit string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "forge-test@example.com")
	run("config", "user.name", "forge test")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "package.json"), []byte(`{"name":"web"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	run("tag", "v1.0.0")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return dir, string(out[:len(out)-1])
}

// TestGitFetcher_FetchesTagIntoCache drives the real fetcher end to end
// through the resolver and confirms the subdir is materialized.
func TestGitFetcher_FetchesTagIntoCache(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns git subprocesses")
	}
	gitAvailable(t)
	repo, wantCommit := buildSourceRepo(t)

	r, err := NewResolver(t.TempDir(),
		WithCacheRoot(filepath.Join(t.TempDir(), "cache")),
		WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	// A local path is a valid git remote; CloneURL passes URL-ish repos
	// through, and an absolute path is handled by git directly.
	src := Source{Repo: "file://" + repo, Ref: "v1.0.0", Subdir: "web"}

	res, err := r.Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Commit != wantCommit {
		t.Errorf("Commit = %q, want %q", res.Commit, wantCommit)
	}
	data, err := os.ReadFile(filepath.Join(res.Dir, "package.json"))
	if err != nil {
		t.Fatalf("fetched subdir is missing its contents: %v", err)
	}
	if string(data) != `{"name":"web"}`+"\n" {
		t.Errorf("fetched content = %q", data)
	}

	// The completion marker is resolver metadata, not source dirt. ShellBuild
	// release provenance must see an unchanged fetched checkout as clean.
	status, err := exec.Command("git", "-C", filepath.Dir(res.Dir), "status", "--porcelain").Output()
	if err != nil || len(status) != 0 {
		t.Fatalf("fetched cache checkout is dirty: %s (%v)", status, err)
	}

	// Second resolve is a cache hit — no second clone.
	again, err := r.Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !again.Cached {
		t.Error("second Resolve was not a cache hit")
	}
}

// TestGitFetcher_FetchesBareCommitSha covers the fallback that matters
// most: a sha is the MOST reproducible pin a user can write, and most
// hosts refuse to serve one shallow. It must not be the spelling that
// fails.
func TestGitFetcher_FetchesBareCommitSha(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns git subprocesses")
	}
	gitAvailable(t)
	repo, commit := buildSourceRepo(t)

	r, err := NewResolver(t.TempDir(),
		WithCacheRoot(filepath.Join(t.TempDir(), "cache")),
		WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	res, err := r.Resolve(context.Background(), Source{
		Repo: "file://" + repo, Ref: commit, Subdir: "web",
	})
	if err != nil {
		t.Fatalf("Resolve by sha: %v", err)
	}
	if res.Commit != commit {
		t.Errorf("Commit = %q, want %q", res.Commit, commit)
	}
}

// TestGitFetcher_UnknownRefFails — a typo'd ref must fail loudly at
// fetch time rather than resolving to something arbitrary.
func TestGitFetcher_UnknownRefFails(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns git subprocesses")
	}
	gitAvailable(t)
	repo, _ := buildSourceRepo(t)

	cacheRoot := filepath.Join(t.TempDir(), "cache")
	r, err := NewResolver(t.TempDir(),
		WithCacheRoot(cacheRoot), WithOverrides(map[string]string{}))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := Source{Repo: "file://" + repo, Ref: "v9.9.9"}
	if _, err := r.Resolve(context.Background(), src); err == nil {
		t.Fatal("Resolve of a nonexistent ref = nil error")
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, src.CacheKey())); !os.IsNotExist(err) {
		t.Error("a failed fetch left a cache entry behind")
	}
}
