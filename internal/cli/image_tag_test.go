package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestResolveImageTag_DirtyTree exercises the canonical bug case: a
// working tree with a modified tracked file produces a `*-dirty` tag,
// which is exactly what `forge env build --push` will tag the image with.
// This is the contract that both build and deploy now consume.
//
// Note: `git describe --dirty` only considers MODIFIED tracked files
// (not untracked ones). The build-push lane has the same behaviour —
// an untracked file alone won't change the tag — so we test the case
// that actually flips the bit.
func TestResolveImageTag_DirtyTree(t *testing.T) {
	dir := newGitRepo(t)
	withDir(t, dir, func() {
		// Modify the tracked README to dirty the tree.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0o644); err != nil {
			t.Fatal(err)
		}
		tag, err := resolveImageTag(context.Background(), "")
		if err != nil {
			t.Fatalf("resolveImageTag: %v", err)
		}
		if !strings.HasSuffix(tag, "-dirty") {
			t.Errorf("expected tag to end with -dirty (modified file), got %q", tag)
		}
	})
}

// TestResolveImageTag_CleanTree confirms the dirty suffix is absent
// when the working tree matches HEAD. Complements the dirty case so we
// know the suffix isn't always-on.
func TestResolveImageTag_CleanTree(t *testing.T) {
	dir := newGitRepo(t)
	withDir(t, dir, func() {
		tag, err := resolveImageTag(context.Background(), "")
		if err != nil {
			t.Fatalf("resolveImageTag: %v", err)
		}
		if strings.HasSuffix(tag, "-dirty") {
			t.Errorf("expected no -dirty suffix on clean tree, got %q", tag)
		}
		if tag == "" {
			t.Error("expected non-empty tag on clean repo")
		}
	})
}

// TestResolveImageTag_NotAGitRepo confirms the helper surfaces a useful
// error rather than silently returning the empty string. Callers
// already wrap with "pass --tag to override" so this is the test that
// pins the contract the deploy command relies on.
func TestResolveImageTag_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	withDir(t, dir, func() {
		tag, err := resolveImageTag(context.Background(), "")
		if err == nil {
			t.Fatalf("expected error in non-git dir, got tag=%q", tag)
		}
	})
}

// newGitRepo creates a temp dir, initializes it as a git repo with
// `git init`, sets a deterministic user, makes one commit, and returns
// the directory path. Tests skip when git isn't on PATH so CI hosts
// without git don't fail on a missing dep.
func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		// Pin author so commit hashes are stable enough across test
		// runs to debug failures; not used for the assertions.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@e.x",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@e.x",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-q", "-m", "init")
	return dir
}

// withDir chdirs to dir for the duration of fn and restores the prior
// working directory afterwards. Used by tests that exercise git-shelling
// helpers which read CWD-relative state.
func withDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(prev)
	}()
	fn()
}

// TestBuildStateRoundTrip writes a BuildState and reads it back,
// asserting the on-disk JSON survives unchanged. Pins the file shape
// callers depend on (snake_case keys, RFC3339 timestamp).
func TestBuildStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := BuildState{
		Image:    "cp-forge",
		Tag:      "2d54e0c-dirty",
		PushedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := WriteBuildState(dir, "dev-host", want); err != nil {
		t.Fatalf("WriteBuildState: %v", err)
	}
	// File lands under .forge/state/build-<env>.json.
	path := filepath.Join(dir, ".forge", "state", "build-dev-host.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not written at %s: %v", path, err)
	}
	got, err := ReadBuildState(dir, "dev-host")
	if err != nil {
		t.Fatalf("ReadBuildState: %v", err)
	}
	if got == nil {
		t.Fatal("ReadBuildState returned nil for an existing file")
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round-trip mismatch:\n  want %+v\n  got  %+v", want, *got)
	}
}

// TestReadBuildState_Missing confirms the "no build was run" path:
// returns (nil, nil) so callers can fall through to resolveImageTag
// without distinguishing "file missing" from "file present but the user
// wants a recompute". An error here would force the deploy command to
// special-case ENOENT, which would invariably grow rotten over time.
func TestReadBuildState_Missing(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadBuildState(dir, "dev")
	if err != nil {
		t.Errorf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil BuildState for missing file, got %+v", got)
	}
}

// TestReadBuildState_Malformed confirms callers get a real error
// (not silent fallback) when the file exists but isn't parseable.
// Silent fallback would mask a genuine bug — the file was written by
// some prior version that drifted from the current schema, or by a
// concurrent process mid-write.
func TestReadBuildState_Malformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "state", "build-dev.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBuildState(dir, "dev")
	if err == nil {
		t.Errorf("expected error for malformed file, got nil (state=%+v)", got)
	}
}

// TestBuildStatePath_EmptyEnv confirms the empty-env fallback uses the
// literal "default" segment. This keeps single-environment projects
// (which never call `forge build ...`) reading and writing a
// stable path.
func TestBuildStatePath_EmptyEnv(t *testing.T) {
	got := buildStatePath("/proj", "")
	want := filepath.Join("/proj", ".forge", "state", "build-default.json")
	if got != want {
		t.Errorf("buildStatePath(empty env) = %q, want %q", got, want)
	}
}

// TestDeployTagFlagRegistered confirms the new `--tag` flag is wired
// onto the deploy command — the user-visible side of the fix.
func TestDeployTagFlagRegistered(t *testing.T) {
	cmd := newDeployCmd()
	f := cmd.Flags().Lookup("tag")
	if f == nil {
		t.Fatal("--tag flag not registered on deploy command")
	}
	if f.DefValue != "" {
		t.Errorf("--tag default = %q, want empty", f.DefValue)
	}
}

// TestBuildTagFlagRegistered confirms the new `--tag` flag is wired
// onto the build command and pairs with the deploy-side flag.
func TestBuildTagFlagRegistered(t *testing.T) {
	cmd := newBuildCmd()
	f := cmd.Flags().Lookup("tag")
	if f == nil {
		t.Fatal("--tag flag not registered on build command")
	}
	if f.DefValue != "" {
		t.Errorf("--tag default = %q, want empty", f.DefValue)
	}
}

// TestResolveDeployImageTag_FlagOverrideWins confirms the highest-
// priority resolution path: an explicit --tag wins over a present
// state file. CI pipelines that pin a release number must always
// land here even if a stale build-state file is lying around.
func TestResolveDeployImageTag_FlagOverrideWins(t *testing.T) {
	dir := t.TempDir()
	// Seed a state file so we know the flag really did jump the line.
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(WriteBuildState(dir, "dev", BuildState{
		Image: "cp-forge", Tag: "from-state", PushedAt: nowRFC3339(),
	}))
	tag, _, src, err := resolveDeployImageTag(context.Background(), dir, "dev", "from-flag", false)
	if err != nil {
		t.Fatalf("resolveDeployImageTag: %v", err)
	}
	if tag != "from-flag" {
		t.Errorf("flag-override tag = %q, want %q", tag, "from-flag")
	}
	if !strings.Contains(src, "--tag") {
		t.Errorf("source should mention --tag flag, got %q", src)
	}
}

// TestResolveDeployImageTag_StateFileWinsOverFallback confirms the
// state file takes precedence over the git-derived fallback. This is
// the load-bearing path for the bug fix: build records what it
// pushed, deploy reads it back unchanged.
func TestResolveDeployImageTag_StateFileWinsOverFallback(t *testing.T) {
	dir := newGitRepo(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(WriteBuildState(dir, "dev", BuildState{
		Image: "cp-forge", Tag: "from-state-2d54e0c-dirty", PushedAt: nowRFC3339(),
	}))
	tag, _, src, err := resolveDeployImageTag(context.Background(), dir, "dev", "", false)
	if err != nil {
		t.Fatalf("resolveDeployImageTag: %v", err)
	}
	if tag != "from-state-2d54e0c-dirty" {
		t.Errorf("state-file tag = %q, want %q", tag, "from-state-2d54e0c-dirty")
	}
	if !strings.Contains(src, "build-dev.json") {
		t.Errorf("source should mention state file, got %q", src)
	}
}

// TestResolveDeployImageTag_FallbackWhenStateMissing confirms the
// final path: no flag, no state file, fall through to the git-
// derived helper. This is the standalone-deploy path — `forge
// deploy` on a fresh clone with no preceding build still works.
func TestResolveDeployImageTag_FallbackWhenStateMissing(t *testing.T) {
	dir := newGitRepo(t)
	withDir(t, dir, func() {
		tag, _, src, err := resolveDeployImageTag(context.Background(), dir, "dev", "", false)
		if err != nil {
			t.Fatalf("resolveDeployImageTag: %v", err)
		}
		if tag == "" {
			t.Error("expected non-empty git-derived tag")
		}
		if !strings.Contains(src, "git describe") {
			t.Errorf("source should mention git describe, got %q", src)
		}
	})
}

// TestResolveDeployImageTag_MalformedStateIsHardError confirms a
// broken state file surfaces an error rather than silently falling
// back. Silent fallback would mask a real bug; the error tells the
// user how to recover.
func TestResolveDeployImageTag_MalformedStateIsHardError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "state", "build-dev.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "dev", "", false)
	if err == nil {
		t.Fatal("expected error for malformed state file, got nil")
	}
	if !strings.Contains(err.Error(), "build-dev.json") {
		t.Errorf("error should mention the file path, got %v", err)
	}
}

// A fresh `forge project new` has run `git init` and nothing else: HEAD is
// unborn, so `git describe` and `git rev-parse` both exit 128. That must not
// stop `forge env up`; it resolves to an untagged dev version, and the first
// commit replaces it with the usual git-derived tag.
func TestResolveImageTag_NoCommitsYet(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: shells out to git; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	withDir(t, dir, func() {
		tag, err := resolveImageTag(context.Background(), "")
		if err != nil {
			t.Fatalf("a repository with no commits must resolve to a dev tag, got error: %v", err)
		}
		if tag != untaggedDevTag {
			t.Errorf("tag = %q, want %q", tag, untaggedDevTag)
		}
	})
}
