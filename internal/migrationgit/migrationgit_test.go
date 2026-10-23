// File: internal/migrationgit/migrationgit_test.go
//
// This package is the single definition of "already on the default branch",
// which the version lint and `forge db migration rebase` both act on in
// opposite directions — the lint flags what is new, rebase refuses what is
// merged. The tests here pin the two behaviours that keep that honest: the
// merge-base answer itself, and the "cannot know" contract, which is what
// every caller branches on when git is unavailable.

package migrationgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T, onMain, onBranch []string) (repoRoot, migDir string) {
	t.Helper()

	repoRoot = t.TempDir()
	migDir = filepath.Join(repoRoot, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(migDir, name), []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	for _, n := range onMain {
		write(n)
	}
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", "feature")
	for _, n := range onBranch {
		write(n)
	}
	if len(onBranch) > 0 {
		git("add", ".")
		git("commit", "-q", "-m", "feature")
	}
	return repoRoot, migDir
}

// The mark is the state the branch was cut FROM. A migration added on the
// branch must not raise it, or the lint would consider the branch's own files
// pre-existing and never flag them.
func TestMergeBaseMaxIsTheStateTheBranchWasCutFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, migDir := repo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	max, ok := MergeBaseMax(RepoRoot(repoRoot), migDir)
	if !ok {
		t.Fatal("merge-base unknown in a repository that has a main branch")
	}
	if max != 20260101120000 {
		t.Errorf("MergeBaseMax = %d, want 20260101120000 — the branch's own migration must not raise the mark", max)
	}
}

// The default branch's TIP, not the merge-base: a migration that landed on
// main after this branch was cut is just as merged, and renaming it is just
// as wrong.
func TestDefaultBranchVersionsSeesCommitsMadeAfterTheBranchWasCut(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, migDir := repo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	// Land another migration on main after the feature branch exists.
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(migDir, "20260401000000_add_teams.up.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "later on main")
	git("checkout", "-q", "feature")

	versions, ok := DefaultBranchVersions(RepoRoot(repoRoot), migDir)
	if !ok {
		t.Fatal("default branch versions unknown")
	}
	if _, found := versions[20260401000000]; !found {
		t.Errorf("version landed on main after the branch was cut is not reported as merged; got %v", versions)
	}
	if _, found := versions[20260501000000]; found {
		t.Error("the feature branch's own migration must not be reported as merged")
	}
}

// THE "CANNOT KNOW" CONTRACT. Outside a repository every lookup reports
// false, and callers depend on that specific signal: the lint falls back to a
// directory-local rule and rebase refuses --all-pending rather than offering
// to rename merged history. An error or a zero-that-looks-real here would
// turn both into silent wrong answers.
func TestEveryLookupReportsUnknownOutsideAGitRepository(t *testing.T) {
	dir := t.TempDir()

	if _, ok := MergeBaseMax(RepoRoot(dir), dir); ok {
		t.Error("MergeBaseMax claims to know the merge-base outside a git repository")
	}
	if _, ok := DefaultBranchVersions(RepoRoot(dir), dir); ok {
		t.Error("DefaultBranchVersions claims to know the default branch outside a git repository")
	}
	if IsTracked(RepoRoot(dir), filepath.Join(dir, "whatever.up.sql")) {
		t.Error("IsTracked reports a file in no repository as tracked")
	}
}

// A RELATIVE migrations dir must work, because that is what the CLI passes:
// `--dir` defaults to "db/migrations" and nothing makes it absolute.
//
// This is the gap that shipped a broken refusal. Every other test here hands
// in an absolute t.TempDir() path, and with an absolute path the code was
// correct — so the whole suite passed while `forge db migration rebase`
// happily renamed an already-merged migration in a real repository.
//
// The mechanism: `git rev-parse --show-toplevel` always reports a resolved
// ABSOLUTE path, EvalSymlinks on a relative path returns it unchanged, and
// filepath.Rel between the two produces a path git cannot resolve. Every
// lookup then returned "cannot know", which each caller treats as a reason to
// stop checking rather than an error. Nothing failed loudly; the guarantee
// just quietly stopped holding.
func TestLookupsWorkWithARelativeMigrationsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, _ := repo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)
	t.Chdir(repoRoot)

	const relDir = "db/migrations"
	root := RepoRoot(relDir)

	max, ok := MergeBaseMax(root, relDir)
	if !ok {
		t.Fatal("MergeBaseMax reports \"cannot know\" for a relative dir inside a real repository — " +
			"every caller then stops checking, and rebase stops refusing merged migrations")
	}
	if max != 20260101120000 {
		t.Errorf("MergeBaseMax = %d, want 20260101120000", max)
	}

	versions, ok := DefaultBranchVersions(root, relDir)
	if !ok {
		t.Fatal("DefaultBranchVersions reports \"cannot know\" for a relative dir inside a real repository")
	}
	if _, found := versions[20260101120000]; !found {
		t.Errorf("merged version missing from the default-branch set; got %v", versions)
	}

	if !IsTracked(root, filepath.Join(relDir, "20260101120000_add_users.up.sql")) {
		t.Error("IsTracked says a committed file is untracked, so renames stop going through `git mv`")
	}
}

// A tracked rename goes through `git mv` so it is staged as a rename rather
// than a delete plus an untracked add — which is what makes the version
// change reviewable as one file.
func TestMoveStagesATrackedRename(t *testing.T) {
	repoRoot, migDir := repo(t, []string{"20260101120000_add_users.up.sql"}, nil)

	from := filepath.Join(migDir, "20260101120000_add_users.up.sql")
	to := filepath.Join(migDir, "20260601000000_add_users.up.sql")
	if err := Move(RepoRoot(repoRoot), from, to); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if _, err := os.Stat(to); err != nil {
		t.Fatalf("destination missing after Move: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("source still exists after Move")
	}

	status := exec.Command("git", "status", "--porcelain")
	status.Dir = repoRoot
	out, err := status.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "R ") {
		t.Errorf("git does not see a staged rename, so the diff will read as an unrelated new file; status:\n%s", out)
	}
}

// An untracked file still moves. A migration created but not yet added is the
// common case for a rebase — the author noticed before committing.
func TestMoveRenamesAnUntrackedFile(t *testing.T) {
	repoRoot, migDir := repo(t, []string{"20260101120000_add_users.up.sql"}, nil)

	from := filepath.Join(migDir, "20260201000000_untracked.up.sql")
	if err := os.WriteFile(from, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(migDir, "20260601000000_untracked.up.sql")
	if err := Move(RepoRoot(repoRoot), from, to); err != nil {
		t.Fatalf("Move on an untracked file: %v", err)
	}
	if _, err := os.Stat(to); err != nil {
		t.Errorf("untracked file was not moved: %v", err)
	}
}

// A GIT HOOK's environment must not change the answer, and this is the test
// that would have caught a real false positive.
//
// git exports GIT_DIR (and often GIT_WORK_TREE) to every hook it runs. Those
// variables take precedence over a child process's working directory, so a
// `git` invocation that only sets cmd.Dir resolves against the HOOK's
// repository instead of the directory it was handed. Inside a pre-push hook
// that made `merge-base`/`ls-tree` fail, MergeBaseMax report "cannot know",
// and the version lint fall back to its no-git rule — which flagged a
// project's long-merged `00001_*.up.sql` as "new but not a UTC timestamp".
//
// The failure mode is the worst shape available: `forge lint` passed from the
// shell and failed on `git push`, with a finding about a file the author had
// not touched. So the lookups scrub the inherited git env.
func TestLookupsIgnoreAnInheritedGitEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, migDir := repo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	// A SECOND, unrelated repository, standing in for the one whose hook is
	// running. Pointing GIT_DIR at it is exactly what git does to a hook.
	other := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = other
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init the hook's repository: %v\n%s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	if root := RepoRoot(repoRoot); resolvePath(root) != resolvePath(repoRoot) {
		t.Errorf("RepoRoot = %q under an inherited GIT_DIR, want %q — "+
			"the inherited env must not redirect the lookup to the hook's repository", root, repoRoot)
	}
	max, ok := MergeBaseMax(RepoRoot(repoRoot), migDir)
	if !ok {
		t.Fatal("MergeBaseMax reported `cannot know` under an inherited GIT_DIR; " +
			"the version lint then falls back to its no-git rule and flags merged history")
	}
	if max != 20260101120000 {
		t.Errorf("MergeBaseMax = %d, want 20260101120000", max)
	}
}
