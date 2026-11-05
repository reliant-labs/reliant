// File: internal/cli/release_forge_script_test.go
//
// Exercises scripts/release-forge.sh — the one-command release gate that
// tags github.com/reliant-labs/forge (vX.Y.Z) once, at a commit already on
// origin/main, and lands the bookkeeping (version files, CHANGELOG.md) as a
// release/vX.Y.Z branch for a pull request.
//
// The tests that matter most here are not the refusals but the invariants:
//
//   - one tag, on a commit origin/main already has, and NOTHING pushed to
//     main. main merges only through the merge queue; a release that pushed
//     to it needed a ruleset bypass actor.
//   - the caller's checkout untouched. The release works in a private
//     worktree, so a shared checkout's uncommitted work can never be swept
//     into a release, and a dry run leaves no trace.
//
// These tests shell out to bash + git + go; they skip when bash is
// unavailable (never the case on the supported dev platforms). The script
// builds the fixture module and the push tests drive a real git remote,
// which is why they are skipped in -short mode.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	write("NEXT_VERSION", "v0.2.0\n")
	write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- first\n\n[Unreleased]: https://github.com/reliant-labs/forge/compare/v0.1.0...HEAD\n")
	write("internal/buildinfo/VERSION", "v0.1.0\n")
	// The release builds archives (LICENSE and NOTICE ride in each) and syncs
	// the npm shim's version.
	write("LICENSE", "license\n")
	write("NOTICE", "notice\n")
	write("npm/package.json", "{\n  \"name\": \"@reliantlabs/forge\",\n  \"version\": \"0.1.0\",\n  \"engines\": {\n    \"node\": \">=18\"\n  }\n}\n")
	write("npm/server.json", "{\n  \"name\": \"io.github.reliant-labs/forge\",\n  \"version\": \"0.1.0\",\n  \"packages\": [\n    {\n      \"identifier\": \"@reliantlabs/forge\",\n      \"version\": \"0.1.0\",\n      \"transport\": {\n        \"type\": \"stdio\"\n      }\n    }\n  ]\n}\n")

	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "test")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "fixture")
	gitIn(t, root, "tag", "-a", "v0.1.0", "-m", "forge v0.1.0")
	// A bare origin holding main: the release names a commit already on
	// origin/main, so even a dry run reads it.
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "clone", "--bare", "--quiet", root, origin)
	gitIn(t, root, "remote", "add", "origin", origin)
	return root
}

// fixtureOrigin is the bare origin newForgeFixtureRepo made for repo.
func fixtureOrigin(t *testing.T, repo string) string {
	t.Helper()
	return gitOut(t, repo, "config", "remote.origin.url")
}

// bookkeepingFile reads rel from the release/<version> branch the script
// pushed to origin, byte for byte.
func bookkeepingFile(t *testing.T, origin, version, rel string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "show", "release/"+version+":"+rel)
	cmd.Dir = origin
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show release/%s:%s in %s: %v", version, rel, origin, err)
	}
	return string(out)
}

// assertNoReleaseWorktree: the script's private worktree is gone after a run.
func assertNoReleaseWorktree(t *testing.T, repo string) {
	t.Helper()
	wts := gitOut(t, repo, "worktree", "list", "--porcelain")
	n := 0
	for _, l := range strings.Split(wts, "\n") {
		if strings.HasPrefix(l, "worktree ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the release left a worktree behind:\n%s", wts)
	}
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
	return runForgeScriptBaseEnv(t, repo, os.Environ(), extraEnv, args...)
}

// runForgeScriptBaseEnv is runForgeScriptEnv over baseEnv instead of the
// test's own environment, for a test that must REMOVE inherited variables.
func runForgeScriptBaseEnv(t *testing.T, repo string, baseEnv, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	script := releaseForgeScriptPath(t)
	full := append([]string{script, "--repo", repo}, args...)
	cmd := exec.CommandContext(context.Background(), "bash", full...)
	registry := ingestingRegistry(t, repo)
	cmd.Env = append(append([]string(nil), baseEnv...),
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
		// One archive, for the host: the fixture module is tiny but a
		// six-target cross-build is still six links per release test.
		"FORGE_RELEASE_TARGETS="+runtime.GOOS+"/"+runtime.GOARCH,
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
	assertNoReleaseWorktree(t, repo)
	if branches := gitOut(t, fixtureOrigin(t, repo), "branch", "--list", "release/*"); branches != "" {
		t.Errorf("dry run pushed a bookkeeping branch: %q", branches)
	}
}

// The release to cut is declared in NEXT_VERSION, not computed from the last
// tag (v0.1.46 was followed by v1.0.0): no version argument cuts it, and any
// version but it (or a pre-release of it) is refused.
func TestReleaseForgeScript_CutsTheReleaseNextVersionNames(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	repo := newForgeFixtureRepo(t)
	out, err := runForgeScript(t, repo, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run with no version failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "releasing v0.2.0 (from NEXT_VERSION)") || !strings.Contains(out, "NEXT_VERSION: v0.2.0 → v0.2.1") {
		t.Errorf("dry run did not cut NEXT_VERSION's release and name the one after it:\n%s", out)
	}
	if out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0-rc.1"); err != nil {
		t.Errorf("a pre-release of NEXT_VERSION was refused: %v\n%s", err, out)
	} else if strings.Contains(out, "NEXT_VERSION: v0.2.0 →") {
		t.Errorf("a pre-release moved NEXT_VERSION:\n%s", out)
	}
	for _, v := range []string{"v0.1.1", "v0.3.0"} {
		out, err := runForgeScript(t, repo, "--dry-run", v)
		if err == nil || !strings.Contains(out, "is not the next release: NEXT_VERSION names v0.2.0") {
			t.Errorf("version %s: want a refusal naming NEXT_VERSION, got err=%v:\n%s", v, err, out)
		}
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("dry runs left the tree dirty:\n%s", status)
	}
}

// TestReleaseForgeScript_TagsOriginsCommitAndNeverPushesMain: one module, one
// tag, on the commit origin/main already has; the bookkeeping that syncs the
// version files is pushed to release/v0.2.0, a child of the tagged commit, for
// a pull request to land. main itself is never pushed: it merges only through
// the merge queue, and a release that pushed to it needed a bypass actor.
func TestReleaseForgeScript_TagsOriginsCommitAndNeverPushesMain(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	origin := fixtureOrigin(t, repo)
	mainBefore := gitOut(t, origin, "rev-parse", "refs/heads/main")
	headBefore := gitOut(t, repo, "rev-parse", "HEAD")

	out, err := runForgeScript(t, repo, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}

	if got := gitOut(t, origin, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Errorf("the release pushed to main: %s -> %s", mainBefore, got)
	}
	if tag := gitOut(t, origin, "rev-parse", "v0.2.0^{commit}"); tag != mainBefore {
		t.Errorf("origin tag v0.2.0 -> %s, want origin/main's commit %s", tag, mainBefore)
	}
	if tag := gitOut(t, repo, "rev-parse", "v0.2.0^{commit}"); tag != mainBefore {
		t.Errorf("local tag v0.2.0 -> %s, want %s", tag, mainBefore)
	}
	// The caller's checkout is not where the release happened.
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head != headBefore {
		t.Errorf("the release committed in the caller's checkout: HEAD %s -> %s", headBefore, head)
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("the release left the caller's tree dirty:\n%s", status)
	}
	assertNoReleaseWorktree(t, repo)

	// The bookkeeping: one commit on top of the tagged one.
	if parent := gitOut(t, origin, "rev-parse", "release/v0.2.0^"); parent != mainBefore {
		t.Errorf("release/v0.2.0's parent is %s, want the tagged commit %s", parent, mainBefore)
	}
	// Only the version files move: there is no require to bump and no go.sum
	// to populate.
	files := gitOut(t, origin, "show", "--pretty=format:", "--name-only", "release/v0.2.0")
	for _, want := range []string{"VERSION", "internal/buildinfo/VERSION", "NEXT_VERSION", "CHANGELOG.md"} {
		if !strings.Contains(files, want) {
			t.Errorf("the bookkeeping commit does not touch %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "go.mod") {
		t.Errorf("the bookkeeping commit touches go.mod — nothing in a single-module release should:\n%s", files)
	}
	rootV := bookkeepingFile(t, origin, "v0.2.0", "VERSION")
	if strings.TrimSpace(rootV) != "v0.2.0" {
		t.Errorf("VERSION = %q, want v0.2.0", strings.TrimSpace(rootV))
	}
	// The two VERSION files must stay byte-identical — buildinfo embeds a
	// copy and TestEmbeddedVersionFileMatchesSource enforces the match.
	if embeddedV := bookkeepingFile(t, origin, "v0.2.0", "internal/buildinfo/VERSION"); rootV != embeddedV {
		t.Errorf("VERSION (%q) and internal/buildinfo/VERSION (%q) diverged", rootV, embeddedV)
	}
	// The bookkeeping names the release after it.
	if next := bookkeepingFile(t, origin, "v0.2.0", "NEXT_VERSION"); strings.TrimSpace(next) != "v0.2.1" {
		t.Errorf("NEXT_VERSION = %q after releasing v0.2.0, want v0.2.1", strings.TrimSpace(next))
	}
	// A local origin gets no pull request: the script says to merge by hand.
	if !strings.Contains(out, "merge release/v0.2.0 into main by hand") {
		t.Errorf("the script did not say how the bookkeeping lands:\n%s", out)
	}
	// The release lock is dropped on the way out.
	if locks := gitOut(t, origin, "for-each-ref", "refs/release-lock/"); locks != "" {
		t.Errorf("the release left its lock on origin:\n%s", locks)
	}
}

// TestReleaseForgeScript_RejectsAConcurrentRelease: while another release of this
// repository (any version) holds a live refs/release-lock/release on origin, the script
// refuses before it commits, tags or pushes anything, and leaves that holder's lock
// alone. This is what keeps a hand cut and the release workflow from racing.
func TestReleaseForgeScript_RejectsAConcurrentRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	origin := fixtureOrigin(t, repo)
	tree := gitOut(t, repo, "mktree")
	lock := gitOut(t, repo, "-c", "user.name=F", "-c", "user.email=f@x.invalid", "commit-tree", tree, "-m", "release lock: v0.1.9 by other@box pid 1 release-forge.sh")
	gitIn(t, repo, "push", "-q", "origin", lock+":refs/release-lock/release")
	before := gitOut(t, repo, "rev-parse", "HEAD")

	out, err := runForgeScript(t, repo, "v0.2.0")
	if err == nil {
		t.Fatalf("expected a refusal while another release holds the lock, got success:\n%s", out)
	}
	if !strings.Contains(out, "already in progress: release lock: v0.1.9 by other@box") {
		t.Errorf("refusal does not say a release is in progress:\n%s", out)
	}
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head != before {
		t.Errorf("the refused release committed: HEAD %s -> %s", before, head)
	}
	if tags := gitOut(t, origin, "tag", "-l"); strings.Contains(tags, "v0.2.0") {
		t.Errorf("the refused release pushed its tag")
	}
	if got := gitOut(t, origin, "rev-parse", "refs/release-lock/release"); got != lock {
		t.Errorf("the refused release touched the holder's lock: %s, want %s", got, lock)
	}
	// Refused before it edited anything: the lock is taken ahead of the backups.
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("the refused release left the tree dirty:\n%s", status)
	}
}

// TestReleaseForgeScript_OneRunEditsACheckoutAtATime: two runs in one shared
// checkout both pass the clean-tree gate before either edits a file, so the
// release files are guarded by a checkout lock taken BEFORE they are backed up.
// While another run holds it, a dry run and a real release are both refused
// without touching a file (a refused run used to restore its backups over the
// holder's edits), and neither takes the release lock on origin. Without
// flock(1) at all the script refuses to edit.
func TestReleaseForgeScript_OneRunEditsACheckoutAtATime(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not on PATH")
	}
	repo := newForgeFixtureRepo(t)
	origin := fixtureOrigin(t, repo)

	// Another run of the script, mid-release: it holds the checkout lock.
	lockPath := filepath.Join(gitOut(t, repo, "rev-parse", "--absolute-git-dir"), "release-forge.lock")
	holder := exec.CommandContext(context.Background(), "bash", "-c", `exec 9>>"$1"; flock 9; echo held; exec sleep 300`, "_", lockPath)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stdout, buf); err != nil || string(buf) != "held\n" {
		t.Fatalf("the holder did not take the lock: %q %v", buf, err)
	}

	before := gitOut(t, repo, "rev-parse", "HEAD")
	version, _ := os.ReadFile(filepath.Join(repo, "VERSION"))
	for _, args := range [][]string{{"--dry-run", "v0.2.0"}, {"v0.2.0"}} {
		out, err := runForgeScript(t, repo, args...)
		if err == nil {
			t.Fatalf("%v: expected a refusal while another run edits this checkout, got success:\n%s", args, out)
		}
		if !strings.Contains(out, "another release-forge.sh run is editing this checkout") {
			t.Errorf("%v: the refusal does not say why:\n%s", args, out)
		}
		if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
			t.Errorf("%v: the refused run touched the tree:\n%s", args, status)
		}
		if got, _ := os.ReadFile(filepath.Join(repo, "VERSION")); string(got) != string(version) {
			t.Errorf("%v: the refused run rewrote VERSION: %q", args, got)
		}
		if head := gitOut(t, repo, "rev-parse", "HEAD"); head != before {
			t.Errorf("%v: the refused run committed: HEAD %s -> %s", args, before, head)
		}
		if locks := gitOut(t, origin, "for-each-ref", "refs/release-lock/"); locks != "" {
			t.Errorf("%v: the refused run took the release lock:\n%s", args, locks)
		}
	}

	// Without flock(1) there is no checkout lock, and the script refuses rather
	// than edit unguarded: a pid-file fallback lets two runs that judge the same
	// dead holder both take the lock.
	noFlock := t.TempDir()
	seen := map[string]bool{"flock": true}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			_ = os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(noFlock, e.Name()))
		}
	}
	// An empty PATH copy would refuse for want of bash or git, not flock.
	if !seen["git"] {
		t.Fatalf("no git found on PATH to copy into the flock-less PATH (%d tools seen)", len(seen))
	}
	out, err := runForgeScriptEnv(t, repo, []string{"PATH=" + noFlock}, "--dry-run", "v0.2.0")
	if err == nil || !strings.Contains(out, "flock(1) is not installed") {
		t.Errorf("without flock the script must refuse to edit the checkout; err=%v:\n%s", err, out)
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("the run without flock touched the tree:\n%s", status)
	}

	// The holder finishes: the next run proceeds.
	_ = holder.Process.Kill()
	_ = holder.Wait()
	if out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0"); err != nil {
		t.Fatalf("a dry run after the holder finished failed: %v\n%s", err, out)
	}
}

// TestReleaseForgeScript_ReleasesOriginsMainNotTheLocalCheckout: what is
// released is decided by origin, never by the checkout the script runs in.
// Here local `main` lags origin (merged work it has not fetched) and the
// caller sits on a feature branch with an unpushed commit: the tag still lands
// on origin/main's commit, the unpushed commit is in neither the tag nor the
// bookkeeping, and the caller's branch is left alone.
func TestReleaseForgeScript_ReleasesOriginsMainNotTheLocalCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a git push")
	}
	repo := newForgeFixtureRepo(t)
	origin := fixtureOrigin(t, repo)

	// origin/main moves ahead of the local main ref.
	gitIn(t, repo, "checkout", "-q", "-b", "merged")
	commitEmpty(t, repo, "feat: merged work (#9)")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	merged := gitOut(t, repo, "rev-parse", "HEAD")
	// The caller then works on a branch with a commit origin does not have.
	gitIn(t, repo, "checkout", "-q", "-b", "feature")
	commitEmpty(t, repo, "feat: unpushed work (#10)")
	unpushed := gitOut(t, repo, "rev-parse", "HEAD")

	out, err := runForgeScript(t, repo, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	if remoteMain := gitOut(t, origin, "rev-parse", "refs/heads/main"); remoteMain != merged {
		t.Errorf("origin main = %s, want it untouched at %s", remoteMain, merged)
	}
	if remoteTag := gitOut(t, origin, "rev-parse", "v0.2.0^{commit}"); remoteTag != merged {
		t.Errorf("origin tag v0.2.0 -> %s, want origin/main's commit %s", remoteTag, merged)
	}
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head != unpushed {
		t.Errorf("the caller's branch moved: HEAD %s, want %s", head, unpushed)
	}
	changelog := bookkeepingFile(t, origin, "v0.2.0", "CHANGELOG.md")
	if !strings.Contains(changelog, "merged work") || strings.Contains(changelog, "unpushed work") {
		t.Errorf("the changelog must cover origin/main, not the local branch:\n%s", changelog)
	}
}

// TestReleaseForgeScript_CommitMustBeOnOrigin: --commit names a commit the
// merge queue already landed, so a commit origin/main does not have is refused
// before anything is edited.
func TestReleaseForgeScript_CommitMustBeOnOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script under bash against a git fixture; runs in task test")
	}
	repo := newForgeFixtureRepo(t)
	commitEmpty(t, repo, "feat: unpushed (#11)")
	out, err := runForgeScript(t, repo, "--dry-run", "--commit", "HEAD", "v0.2.0")
	if err == nil || !strings.Contains(out, "is not on origin/main") {
		t.Fatalf("want a refusal for a commit origin/main lacks; err=%v:\n%s", err, out)
	}
	assertNoReleaseWorktree(t, repo)
}

func TestReleaseForgeScript_RejectsBadVersions(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script under bash against a git fixture; runs in task test")
	}
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

// TestReleaseForgeScript_IgnoresADirtyCallerCheckout: the release is cut in a
// private worktree at origin's commit, so uncommitted work in the (shared)
// checkout it is run from can neither block it nor be swept into it, and is
// left exactly as it was.
func TestReleaseForgeScript_IgnoresADirtyCallerCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script under bash against a git fixture; runs in task test")
	}
	repo := newForgeFixtureRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "unrelated.go"), []byte("package forge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "VERSION"), []byte("someone's edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runForgeScript(t, repo, "--dry-run", "v0.2.0"); err != nil {
		t.Fatalf("a dirty caller checkout blocked the release: %v\n%s", err, out)
	}
	if got := readRepoFile(t, repo, "VERSION"); got != "someone's edit\n" {
		t.Errorf("the release touched the caller's VERSION: %q", got)
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "M VERSION\n?? unrelated.go" {
		t.Errorf("the caller's checkout changed:\n%s", status)
	}
}

// TestReleaseForgeScript_RejectsEitherExistingTag covers both tags
// independently: a half-finished earlier release leaves exactly one of them
// behind, and reusing it would publish immutable bytes under a used version.
// TestReleaseForgeScript_RejectsExistingTag: versions are immutable, so a
// tag that already exists locally is a hard stop.
func TestReleaseForgeScript_RejectsExistingTag(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script under bash against a git fixture; runs in task test")
	}
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

// TestReleaseLockScript runs scripts/test-release-lock.sh, the hermetic self-test of the
// lock every release script sources (the same file in forge, reliant and control-plane):
// refusal naming the holder, heartbeat, a nested holder, stale replacement, and the
// holder's re-check of main with a fake gh.
func TestReleaseLockScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git pushes against throwaway remotes; runs in task test")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test-release-lock.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release-lock.sh: %v\n%s", err, out)
	}
}

// TestReviewMarkerScript runs scripts/test-ci-review-marker.sh, the hermetic
// self-test of the `Reliant review` required check: an exact-head pass marker
// passes a branch of this repository, and nothing passes a fork's head
// (control-plane finding CPI-1).
func TestReviewMarkerScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in task test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test-ci-review-marker.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-ci-review-marker.sh: %v\n%s", err, out)
	}
}

// TestReviewRerunScript runs scripts/test-ci-review-rerun.sh, the hermetic
// self-test of the healer behind the `Reliant review` check: a passed head's
// red and cancelled runs are re-run one at a time, a head that has not passed
// and merge-group runs are never touched.
func TestReviewRerunScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in task test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test-ci-review-rerun.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-ci-review-rerun.sh: %v\n%s", err, out)
	}
}
