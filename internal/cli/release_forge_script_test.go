// File: internal/cli/release_forge_script_test.go
//
// Exercises scripts/release-forge.sh — the one-command release gate that
// tags github.com/reliant-labs/forge (vX.Y.Z) once, at the commit that syncs
// the version files.
//
// The tests that matter most here are not the refusals but the invariants:
//
//   - one tag, on the release commit. It used to be two — the module and its
//     pkg/ submodule, which had to land on the same commit — until pkg/ was
//     folded into the root module.
//   - a dry run leaving NO trace. The script edits the version files in
//     place, so a failed or dry run must restore the tree exactly — this
//     checkout is routinely shared with other agents, and the obvious cleanup
//     verb (`git checkout -- <path>`) is the destructive one we must never use.
//
// These tests shell out to bash + git + go; they skip when bash is
// unavailable (never the case on the supported dev platforms). The script
// builds the fixture module and the push tests drive a real git remote,
// which is why they are skipped in -short mode.
package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func releaseForgeScriptPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(cwd, "..", "..", "scripts", "release-forge.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("release-forge.sh not found at %s: %v", p, err)
	}
	return p
}

// gitIn runs a git command in dir and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// gitOut runs a git command in dir and returns trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// newForgeFixtureRepo builds a minimal repo shaped like the forge repo from
// the script's point of view: one module with pkg/ as a plain directory, a
// cmd/forge main package, and the version files.
func newForgeFixtureRepo(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// ONE module, with pkg/ as a plain directory inside it — the shape a
	// release now has to handle. No pkg/go.mod, no go.work, and no
	// self-require, which is why the script no longer needs to resolve an
	// unpushed version's hashes into go.sum.
	write("go.mod", "module github.com/reliant-labs/forge\n\ngo 1.24\n")
	write("pkg/svcerr/svcerr.go", "package svcerr\n\n// OK is a placeholder.\nconst OK = true\n")
	write("cmd/forge/main.go", "package main\n\nfunc main() {}\n")
	write("VERSION", "v0.1.0\n")
	write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- first\n\n[Unreleased]: https://github.com/reliant-labs/forge/compare/v0.1.0...HEAD\n")
	write("internal/buildinfo/VERSION", "v0.1.0\n")

	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "test")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "fixture")
	gitIn(t, root, "tag", "-a", "v0.1.0", "-m", "forge v0.1.0")
	return root
}

// runForgeScript runs the release script HERMETICALLY: both of its network
// reads — the pre-push immutable-version check and the post-push ingest wait —
// are pointed at ingestingRegistry, never at proxy.golang.org / sum.golang.org.
//
// It used to inherit the real network. A push test then waited out the full
// 600s ingest window polling the public proxy for a fixture version that will
// never be published there, which is how `go test ./internal/cli` came to hang
// for ten minutes on every full run.
func runForgeScript(t *testing.T, repo string, args ...string) (string, error) {
	t.Helper()
	return runForgeScriptEnv(t, repo, nil, args...)
}

// runForgeScriptEnv is runForgeScript plus extra environment. The CHANGELOG
// formatter defaults to `true` (a no-op) so no test reaches npm; the tests that
// care about prettier set FORGE_RELEASE_PRETTIER themselves.
func runForgeScriptEnv(t *testing.T, repo string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	script := releaseForgeScriptPath(t)
	full := append([]string{script, "--repo", repo}, args...)
	cmd := exec.CommandContext(context.Background(), "bash", full...)
	registry := ingestingRegistry(t, repo)
	cmd.Env = append(os.Environ(),
		// ONE override for the proxy: release-forge.sh forwards it to the
		// wait (GOPROXY_WAIT_BASE), so this also proves the forwarding.
		"FORGE_RELEASE_PROXY_BASE="+registry,
		"GOSUMDB_WAIT_BASE="+registry,
		// Belt and braces: a regression that stopped forwarding must fail
		// in seconds, not hang for the default 600.
		"GOPROXY_WAIT_MAX_SECONDS=20",
		"GOPROXY_WAIT_FIRST_DELAY=1",
		"GOPROXY_WAIT_MAX_DELAY=1",
		"FORGE_RELEASE_PRETTIER=true",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ingestingRegistry is a fixture Go module proxy AND checksum database that
// behaves like the real pair: a version is unknown (404) until its tag has
// been pushed to the repo's `origin` remote, then the proxy serves its .info
// and the sumdb its lookup. That keeps the pre-push gate passing ("not yet
// published") and lets the post-push wait observe ingestion, with no network.
// A repo with no origin (dry runs) simply never ingests anything.
func ingestingRegistry(t *testing.T, repo string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := ""
		switch {
		case strings.Contains(r.URL.Path, "/@v/") && strings.HasSuffix(r.URL.Path, ".info"):
			version = strings.TrimSuffix(r.URL.Path[strings.LastIndex(r.URL.Path, "/@v/")+len("/@v/"):], ".info")
		case strings.HasPrefix(r.URL.Path, "/lookup/") && strings.Contains(r.URL.Path, "@"):
			version = r.URL.Path[strings.LastIndex(r.URL.Path, "@")+1:]
		}
		if version == "" || !originHasTag(repo, version) {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"Version":%q}`, version)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// originHasTag reports whether the repo's origin remote holds tag. Errors
// (no origin, a dry-run fixture) read as "not pushed".
func originHasTag(repo, tag string) bool {
	cmd := exec.CommandContext(context.Background(), "git", "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	cmd.Dir = repo
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// TestReleaseForgeScript_DryRunLeavesNoTrace: a dry run validates and edits,
// then restores every file it touched — the checkout is shared with other
// agents, so a dry run must be invisible.
func TestReleaseForgeScript_DryRunLeavesNoTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	repo := newForgeFixtureRepo(t)
	if out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0"); err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("dry run left the tree dirty:\n%s", status)
	}
	// go.sum did not exist before the run; restoring means removing it.
	if _, err := os.Stat(filepath.Join(repo, "go.sum")); !os.IsNotExist(err) {
		t.Errorf("dry run left a stray go.sum behind")
	}
	if tags := gitOut(t, repo, "tag", "-l", "v0.2.0", "pkg/v0.2.0"); tags != "" {
		t.Errorf("dry run created tags: %q", tags)
	}
}

// TestReleaseForgeScript_TagsOnceAtTheReleaseCommit: one module, one tag, on
// the commit that syncs the version files. This replaced a test asserting
// that pkg/vX.Y.Z and vX.Y.Z landed on the SAME commit — an invariant that
// only had to be asserted because there were two tags to get wrong.
func TestReleaseForgeScript_TagsOnceAtTheReleaseCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	// A bare remote so the script's atomic push has a destination.
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, repo, "clone", "--bare", "--quiet", repo, origin)
	gitIn(t, repo, "remote", "add", "origin", origin)

	out, err := runForgeScript(t, repo, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}

	head := gitOut(t, repo, "rev-parse", "HEAD")
	tag := gitOut(t, repo, "rev-parse", "v0.2.0^{commit}")
	if tag != head {
		t.Errorf("tag does not point at the release commit: tag=%s HEAD=%s", tag, head)
	}

	// Only the version files move now: there is no require to bump and no
	// go.sum to populate.
	files := gitOut(t, repo, "show", "--pretty=format:", "--name-only", "HEAD")
	for _, want := range []string{"VERSION", "internal/buildinfo/VERSION"} {
		if !strings.Contains(files, want) {
			t.Errorf("release commit does not touch %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "go.mod") {
		t.Errorf("release commit touches go.mod — nothing in a single-module release should:\n%s", files)
	}

	for _, vf := range []string{"VERSION", "internal/buildinfo/VERSION"} {
		got, err := os.ReadFile(filepath.Join(repo, vf))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(got)) != "v0.2.0" {
			t.Errorf("%s = %q, want v0.2.0", vf, strings.TrimSpace(string(got)))
		}
	}
	// The two VERSION files must stay byte-identical — buildinfo embeds a
	// copy and TestEmbeddedVersionFileMatchesSource enforces the match.
	rootV, _ := os.ReadFile(filepath.Join(repo, "VERSION"))
	embeddedV, _ := os.ReadFile(filepath.Join(repo, "internal/buildinfo/VERSION"))
	if string(rootV) != string(embeddedV) {
		t.Errorf("VERSION (%q) and internal/buildinfo/VERSION (%q) diverged", rootV, embeddedV)
	}
	// The push is atomic, so the remote must have the branch AND the tag.
	if remoteTags := gitOut(t, origin, "tag", "-l"); !strings.Contains(remoteTags, "v0.2.0") {
		t.Errorf("tag v0.2.0 did not reach the remote; got:\n%s", remoteTags)
	}
}

// TestReleaseForgeScript_ReleasesFromABranchThatIsNotMain pins the push
// refspec. The script used to push `origin main v0.2.0`, which sends the LOCAL
// `main` ref — the release commit only when the script runs on a checked-out,
// up-to-date main. Released from a worktree on another branch (how v0.1.18 was
// cut), local main lagged origin, the remote rejected the stale tip as
// non-fast-forward, and the atomic push dropped the tag with it.
func TestReleaseForgeScript_ReleasesFromABranchThatIsNotMain(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, repo, "clone", "--bare", "--quiet", repo, origin)
	gitIn(t, repo, "remote", "add", "origin", origin)

	// origin/main moves ahead; the release is cut from a branch based on it
	// while the local `main` ref stays behind.
	gitIn(t, repo, "checkout", "-q", "-b", "release/v0.2.0")
	if err := os.WriteFile(filepath.Join(repo, "CHANGES"), []byte("merged work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "CHANGES")
	gitIn(t, repo, "commit", "-q", "-m", "merged work")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")

	if out, err := runForgeScript(t, repo, "v0.2.0"); err != nil {
		t.Fatalf("release from a non-main branch failed: %v\n%s", err, out)
	}

	release := gitOut(t, repo, "rev-parse", "HEAD")
	if remoteMain := gitOut(t, origin, "rev-parse", "refs/heads/main"); remoteMain != release {
		t.Errorf("origin main = %s, want the release commit %s", remoteMain, release)
	}
	if remoteTag := gitOut(t, origin, "rev-parse", "v0.2.0^{commit}"); remoteTag != release {
		t.Errorf("origin tag v0.2.0 -> %s, want the release commit %s", remoteTag, release)
	}
}

func TestReleaseForgeScript_RejectsBadVersions(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	for _, bad := range []string{"0.2.0", "pkg/v0.2.0", "v1.2", "latest", "v1.2.3+meta"} {
		out, err := runForgeScript(t, repo, "--dry-run", bad)
		if err == nil {
			t.Errorf("version %q: expected rejection, got success:\n%s", bad, out)
			continue
		}
		if !strings.Contains(out, "version must look like vX.Y.Z") {
			t.Errorf("version %q: unexpected error output:\n%s", bad, out)
		}
	}
}

func TestReleaseForgeScript_RejectsDirtyTree(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	// Any dirty file blocks a release: the script commits root-module files,
	// so unrelated work would otherwise be swept into the release commit.
	if err := os.WriteFile(filepath.Join(repo, "unrelated.go"), []byte("package forge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0")
	if err == nil {
		t.Fatalf("expected dirty-tree rejection, got success:\n%s", out)
	}
	if !strings.Contains(out, "working tree is not clean") {
		t.Errorf("unexpected error output:\n%s", out)
	}
}

// TestReleaseForgeScript_RejectsEitherExistingTag covers both tags
// independently: a half-finished earlier release leaves exactly one of them
// behind, and reusing it would publish immutable bytes under a used version.
// TestReleaseForgeScript_RejectsExistingTag: versions are immutable, so a
// tag that already exists locally is a hard stop.
func TestReleaseForgeScript_RejectsExistingTag(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	gitIn(t, repo, "tag", "v0.2.0")
	out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0")
	if err == nil {
		t.Fatalf("expected rejection for an existing tag, got success:\n%s", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("unexpected error output:\n%s", out)
	}
}
