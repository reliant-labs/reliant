package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setTestGitEnv isolates git from the host's config and provides the
// identity needed for `git commit`. init.defaultBranch is deliberately set
// to something that is NOT "main" so tests can prove the requested branch
// (not the default) ends up on HEAD.
func setTestGitEnv(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	content := "[user]\n\tname = Test\n\temail = test@example.com\n[init]\n\tdefaultBranch = testdefault\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v, output: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInitGitRepository_TrimsTrailingSpaceInBranch(t *testing.T) {
	setTestGitEnv(t)
	dir := t.TempDir()

	// The exact reported bug: the UI sent "main " (trailing space) and git
	// rejected it AFTER creating .git. Trimming must make this just work.
	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:          dir,
		InitialBranch: "main ",
	})
	if err != nil {
		t.Fatalf("InitGitRepository failed: %v", err)
	}

	if head := gitOut(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", head)
	}
}

func TestInitGitRepository_EmptyBranchDefaultsToMain(t *testing.T) {
	setTestGitEnv(t)
	dir := t.TempDir()

	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:          dir,
		InitialBranch: "   ",
	})
	if err != nil {
		t.Fatalf("InitGitRepository failed: %v", err)
	}

	if head := gitOut(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", head)
	}
}

func TestInitGitRepository_InvalidBranchRejectedBeforeAnyState(t *testing.T) {
	setTestGitEnv(t)
	dir := t.TempDir()

	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:          dir,
		InitialBranch: "bad branch", // interior space survives trimming
	})
	if err == nil {
		t.Fatal("expected error for invalid branch name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid branch name") {
		t.Fatalf("error should identify the invalid branch name, got: %v", err)
	}
	// The failure must not leave a half-initialized .git behind.
	if IsGitRepository(dir) {
		t.Fatal("invalid branch name must fail before .git is created")
	}
}

func TestInitGitRepository_AdoptsExistingEmptyRepo(t *testing.T) {
	setTestGitEnv(t)
	dir := t.TempDir()

	// Simulate the stuck state: .git exists (created by a failed init
	// attempt) but the repo is unborn — no commits, HEAD on some other
	// branch.
	gitOut(t, dir, "init")
	if head := gitOut(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/testdefault" {
		t.Fatalf("precondition: HEAD = %q, want refs/heads/testdefault", head)
	}

	// Retry with the (untrimmed) requested branch must adopt and self-heal.
	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:              dir,
		InitialBranch:     "main ",
		GitignorePatterns: DefaultGitignorePatterns(),
		InitialCommit:     true,
	})
	if err != nil {
		t.Fatalf("InitGitRepository failed to adopt existing repo: %v", err)
	}

	if branch := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatalf(".gitignore should have been created: %v", err)
	}
	// The initial commit should exist and be on the requested branch.
	gitOut(t, dir, "rev-parse", "--verify", "HEAD")
}

func TestInitGitRepository_AdoptionPreservesRepoWithCommits(t *testing.T) {
	setTestGitEnv(t)
	dir := t.TempDir()

	gitOut(t, dir, "init", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("custom-pattern\n"), 0o644); err != nil {
		t.Fatalf("failed to seed .gitignore: %v", err)
	}
	gitOut(t, dir, "add", ".")
	gitOut(t, dir, "commit", "-m", "existing work")

	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:              dir,
		InitialBranch:     "main",
		GitignorePatterns: DefaultGitignorePatterns(),
		InitialCommit:     true,
	})
	if err != nil {
		t.Fatalf("InitGitRepository failed to adopt repo with commits: %v", err)
	}

	// An established repo is adopted as-is: no branch move, no fabricated
	// commit, no .gitignore clobber.
	if branch := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "trunk" {
		t.Fatalf("branch = %q, want trunk (must not move HEAD)", branch)
	}
	if count := gitOut(t, dir, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count = %s, want 1 (must not fabricate a commit)", count)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	if string(data) != "custom-pattern\n" {
		t.Fatalf(".gitignore was clobbered: %q", data)
	}
}

// Branch-name validation/normalization tests live with the pure implementation
// in internal/gitref (gitutil re-exports those functions).

func TestEnsureGitignoreContains_CreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureGitignoreContains(dir, ".reliant.local/"); err != nil {
		t.Fatalf("EnsureGitignoreContains failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	got := string(data)
	want := ".reliant.local/\n"
	if got != want {
		t.Fatalf("unexpected .gitignore content\nwant: %q\ngot:  %q", want, got)
	}
}

func TestEnsureGitignoreContains_IsIdempotent_WithOrWithoutSlash(t *testing.T) {
	dir := t.TempDir()
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("node_modules/\n.reliant.local\n"), 0o644); err != nil {
		t.Fatalf("failed to seed .gitignore: %v", err)
	}

	if err := EnsureGitignoreContains(dir, ".reliant.local/"); err != nil {
		t.Fatalf("EnsureGitignoreContains failed: %v", err)
	}

	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	got := string(data)
	want := "node_modules/\n.reliant.local\n"
	if got != want {
		t.Fatalf("unexpected .gitignore content\nwant: %q\ngot:  %q", want, got)
	}
}

// setIdentitylessGitEnv isolates git from host config AND provides no user
// identity, mimicking a fresh cloud workspace pod. EnsureInitialCommit must
// still succeed via its own fallback identity.
func setIdentitylessGitEnv(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func TestEnsureInitialCommit_SeedsUnbornRepoWithoutIdentity(t *testing.T) {
	setIdentitylessGitEnv(t)
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-b", "trunk", ".").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v, output: %s", err, out)
	}
	// A working-tree file that must NOT be swept into the seeded commit.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if HasCommits(context.Background(), dir) {
		t.Fatal("precondition: repo should be commitless")
	}
	if err := EnsureInitialCommit(context.Background(), dir); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	if !HasCommits(context.Background(), dir) {
		t.Fatal("repo should have a commit after EnsureInitialCommit")
	}
	// Commit landed on the unborn branch, not a hardcoded "main".
	if head := gitOut(t, dir, "symbolic-ref", "--short", "HEAD"); head != "trunk" {
		t.Errorf("HEAD = %q, want trunk", head)
	}
	// The seeded commit is empty: main.go remains untracked.
	if status := gitOut(t, dir, "status", "--porcelain"); !strings.Contains(status, "?? main.go") {
		t.Errorf("main.go should still be untracked (empty seed commit), status: %q", status)
	}
}

// TestInitGitRepository_InitialCommitWithoutIdentity reproduces the cloud-pod
// / never-configured-git failure: the manual "Initialize Git" flow asks for
// InitialCommit and used to fail with exit 128 "Author identity unknown".
func TestInitGitRepository_InitialCommitWithoutIdentity(t *testing.T) {
	setIdentitylessGitEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := InitGitRepository(context.Background(), InitGitRepositoryOptions{
		Path:          dir,
		InitialBranch: "main",
		InitialCommit: true,
	})
	if err != nil {
		t.Fatalf("InitGitRepository with InitialCommit and no identity: %v", err)
	}
	if !HasCommits(context.Background(), dir) {
		t.Fatal("repo should have a commit after init")
	}
	// The fallback identity is ephemeral: it must NOT be written into repo config.
	if got := gitConfigValue(context.Background(), dir, "user.email"); got != "" {
		t.Errorf("fallback identity leaked into repo config: user.email = %q", got)
	}
}

func TestEnsureInitialCommit_NoopWhenCommitsExist(t *testing.T) {
	setTestGitEnv(t) // this env has an identity, so we can make a real commit
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-b", "main", ".").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v, output: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", ".")
	gitOut(t, dir, "commit", "-m", "real")
	want := gitOut(t, dir, "rev-parse", "HEAD")

	if err := EnsureInitialCommit(context.Background(), dir); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	// HEAD must be unchanged — no fabricated commit on an already-init'd repo.
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD moved: got %q, want %q", got, want)
	}
}
