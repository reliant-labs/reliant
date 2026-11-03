// File: internal/database/rebase_test.go
//
// These tests pin the behaviour of `forge db migration rebase`, which exists
// because two separate messages told users to run it while no such command
// existed: pkg/migratekit's missing-migration refusal and forge lint's
// version rules both said "forge db migration rebase <file>", and a user
// following either one hit "unknown command".
//
// Each test here fails on the code that preceded RebaseMigrations — the
// function did not exist — and the two behavioural guarantees are the ones a
// user's data depends on: a rebase must produce a version that actually sorts
// after everything merged (or it fixes nothing), and it must refuse a
// migration that already merged (or it corrupts every database that recorded
// it).

package database

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/migrationver"
)

// gitRepo builds a throwaway repository with a main branch, a migrations
// directory holding the named files, and a feature branch checked out — the
// shape every rebase decision is made against.
//
// The migrations named in onMain are committed to main BEFORE the feature
// branch is cut, so they are what the merge-base high-water mark sees.
func gitRepo(t *testing.T, onMain []string, onBranch []string) (repoRoot, migDir string) {
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
		if err := os.WriteFile(filepath.Join(migDir, name), []byte("-- "+name+"\nSELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")

	for _, name := range onMain {
		write(name)
	}
	git("add", ".")
	git("commit", "-q", "-m", "migrations on main")

	git("checkout", "-q", "-b", "feature")
	for _, name := range onBranch {
		write(name)
	}
	if len(onBranch) > 0 {
		git("add", ".")
		git("commit", "-q", "-m", "migrations on feature")
	}
	return repoRoot, migDir
}

// versionOf is the numeric prefix of a filename, as a test assertion rather
// than a parse the test has to get right twice.
func versionOf(t *testing.T, name string) uint64 {
	t.Helper()
	v, ok := migrationver.ParseVersion(filepath.Base(name))
	if !ok {
		t.Fatalf("%q has no version prefix", name)
	}
	return v
}

// THE CORE GUARANTEE. A rebase exists to move a migration above the mark the
// schema has already passed, so the new version must exceed every version in
// the directory AND every version on the default branch. A rename that
// merely changes the number fixes nothing — the file would be just as
// invisible to golang-migrate as before.
//
// The stem must survive, because the stem is how a reviewer follows the file
// across the rename. A rebase that renamed the migration as well as its
// version would read as a new, unreviewed file.
func TestRebasePreservesTheStemAndOrdersAfterTheMax(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, migDir := gitRepo(t,
		[]string{"20260301000000_add_accounts.up.sql"},
		[]string{"20260101120000_add_users.up.sql"},
	)
	_ = repoRoot

	stale := filepath.Join(migDir, "20260101120000_add_users.up.sql")
	results, err := RebaseMigrations(migDir, []string{stale})
	if err != nil {
		t.Fatalf("RebaseMigrations: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}

	got := filepath.Base(results[0].NewPath)

	// The stem is preserved: only the version changed.
	if want := "add_users.up.sql"; !strings.HasSuffix(got, "_"+want) {
		t.Errorf("rebased file %q does not keep the stem %q — a reviewer cannot follow the file across the rename", got, want)
	}

	// The new version sorts after the highest version in the directory,
	// which is what makes the migration pending again.
	if newV, maxV := versionOf(t, got), uint64(20260301000000); newV <= maxV {
		t.Errorf("rebased version %d is not above the directory max %d — the migration is still invisible to golang-migrate", newV, maxV)
	}
	if results[0].NewVersion <= results[0].OldVersion {
		t.Errorf("NewVersion %d is not above OldVersion %d", results[0].NewVersion, results[0].OldVersion)
	}

	// The rename actually happened on disk, under the new name only.
	if _, err := os.Stat(results[0].NewPath); err != nil {
		t.Errorf("new path does not exist: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("old path %s still exists; both versions would now be applied", stale)
	}

	// The version is a real timestamp, not a hand-shaped number.
	versionText, _, _ := strings.Cut(got, "_")
	if !migrationver.IsTimestamp(versionText) {
		t.Errorf("rebased version %q is not a 14-digit UTC calendar timestamp", versionText)
	}
}

// THE DEFAULT BRANCH IS CONSULTED, NOT JUST THE DIRECTORY.
//
// Scanning the migrations directory is not sufficient, and the gap is not
// hypothetical: `forge db squash` collapses many migrations into one
// baseline, so after a squash the directory no longer holds files that the
// default branch — and every deployed database's schema_migrations — still
// does. The high-water mark a rebase has to clear is the DATABASE's, and the
// default branch is the only remaining record of it.
//
// Here the merged migration is future-dated and absent from the working
// directory, so an allocator reading only the directory picks "now" and lands
// BELOW the mark: a rebase that renames the file and fixes nothing, which is
// the worst outcome because it looks like it worked.
func TestRebaseOrdersAfterAMergedVersionMissingFromTheDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	const squashedAway = "21000101000000_add_accounts.up.sql"

	repoRoot, migDir := gitRepo(t,
		[]string{squashedAway},
		[]string{"20260101120000_add_users.up.sql"},
	)

	// Squash: the merged migration leaves the working tree but stays on the
	// default branch.
	rm := exec.Command("git", "rm", "-q", filepath.Join("db", "migrations", squashedAway))
	rm.Dir = repoRoot
	if out, err := rm.CombinedOutput(); err != nil {
		t.Fatalf("git rm: %v\n%s", err, out)
	}
	commit := exec.Command("git", "commit", "-q", "-m", "squash baseline")
	commit.Dir = repoRoot
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	results, err := RebaseMigrations(migDir, []string{
		filepath.Join(migDir, "20260101120000_add_users.up.sql"),
	})
	if err != nil {
		t.Fatalf("RebaseMigrations: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	mark := versionOf(t, squashedAway)
	if results[0].NewVersion <= mark {
		t.Errorf("rebased to version %d, which is not above the merged version %d that is still on the default branch — "+
			"the migration remains below the schema's high-water mark, so the rename fixed nothing",
			results[0].NewVersion, mark)
	}
}

// THE REFUSAL. A migration already on the default branch has been recorded as
// applied, under its current filename, by every database that ran it.
// Renaming it leaves those databases with a recorded version whose file does
// not exist and a new file that has never been applied — strictly worse than
// the problem, and unrecoverable without hand-editing schema_migrations.
//
// So this must refuse, and refuse before touching anything.
func TestRebaseRefusesAnAlreadyMergedMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	_, migDir := gitRepo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	merged := filepath.Join(migDir, "20260101120000_add_users.up.sql")
	results, err := RebaseMigrations(migDir, []string{merged})
	if err == nil {
		t.Fatalf("RebaseMigrations renamed an already-merged migration (%+v); it must refuse", results)
	}

	var alreadyMerged *AlreadyMergedError
	if !errors.As(err, &alreadyMerged) {
		t.Fatalf("error is %T (%v), want *AlreadyMergedError so a caller can tell this refusal from a failure", err, err)
	}
	if alreadyMerged.Version != 20260101120000 {
		t.Errorf("refusal names version %d, want 20260101120000", alreadyMerged.Version)
	}

	// The file is untouched — a refusal must not half-apply.
	if _, statErr := os.Stat(merged); statErr != nil {
		t.Errorf("refused file was moved anyway: %v", statErr)
	}

	// The message has to tell the user what to do instead, because "no" on
	// its own sends them looking for a flag to override it.
	if msg := strings.ToLower(alreadyMerged.Error()); !strings.Contains(msg, "new forward migration") {
		t.Errorf("refusal does not name the alternative (a new forward migration); got: %s", msg)
	}
}

// SAME VERSION, DIFFERENT FILE IS A COLLISION TO FIX, NOT A MERGE TO REFUSE.
//
// The refusal exists for ONE file: the one whose version a database has
// already recorded under that exact name. A different migration that happens
// to claim the same number is the opposite case — it has never been applied
// anywhere, it is precisely what `duplicate-migration-version` tells the
// author to rebase, and it is the single most likely way two branches
// allocating in the same second collide.
//
// Matching on the version alone conflated the two. Rebase saw the number on
// the default branch, called the file already-merged, and told the author to
// "write a NEW forward migration instead" — advice that cannot fix a
// duplicate version, for a file that was safe to rename all along. The lint
// named this command and this command refused.
func TestRebaseFixesASameVersionDifferentFileCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	const contested = "20260101120000"

	_, migDir := gitRepo(t,
		[]string{contested + "_add_users.up.sql"},
		nil,
	)

	// The branch's own migration, claiming the same version under a
	// different name. This file exists only here; nothing has applied it.
	ours := filepath.Join(migDir, contested+"_add_sessions.up.sql")
	if err := os.WriteFile(ours, []byte("CREATE TABLE sessions (id INT);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := RebaseMigrations(migDir, []string{ours})
	if err != nil {
		t.Fatalf("RebaseMigrations refused a same-version DIFFERENT file: %v\n"+
			"This file has never been applied anywhere — it is the duplicate-version "+
			"collision the lint tells the author to fix with this very command.", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}

	// It must clear the contested version, or the duplicate persists.
	if results[0].NewVersion <= versionOf(t, contested+"_add_users.up.sql") {
		t.Errorf("rebased to %d, which does not clear the contested version %s — still a duplicate",
			results[0].NewVersion, contested)
	}
	if got := filepath.Base(results[0].NewPath); !strings.HasSuffix(got, "_add_sessions.up.sql") {
		t.Errorf("rebased file %q lost its stem", got)
	}

	// The merged file is untouched: it is the one that must keep its
	// version, and only the branch's file moves.
	merged := filepath.Join(migDir, contested+"_add_users.up.sql")
	if _, statErr := os.Stat(merged); statErr != nil {
		t.Errorf("the already-merged file was moved: %v — that is the database-corrupting rename the refusal exists to prevent", statErr)
	}
}

// THE REFUSAL STILL HOLDS FOR THE SAME FILE. Narrowing the check to compare
// filenames must not weaken it: the file that is on the default branch under
// this name is the one every database recorded, and it must still be refused.
//
// This is the guard on the fix above — a version-only check was too broad, a
// check that stopped refusing would be catastrophic.
func TestRebaseStillRefusesTheSameFileOnTheDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	_, migDir := gitRepo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	merged := filepath.Join(migDir, "20260101120000_add_users.up.sql")
	if _, err := RebaseMigrations(migDir, []string{merged}); err == nil {
		t.Fatal("rebase renamed a migration that is on the default branch under this exact name")
	}
	if _, statErr := os.Stat(merged); statErr != nil {
		t.Errorf("refused file was moved anyway: %v", statErr)
	}
}

// THE REFUSAL MUST HOLD WITH THE PATHS THE CLI ACTUALLY PASSES.
//
// `--dir` defaults to the relative "db/migrations" and nothing makes it
// absolute. Every other test here builds absolute paths from t.TempDir(), and
// with absolute paths the code was correct — so the suite was green while the
// shipped binary renamed an already-merged migration on the first try. Caught
// by smoke-testing the real `forge db migration rebase`, not by these tests.
//
// A relative dir made each git lookup report "cannot know", and "cannot know"
// disables the refusal rather than failing. That is the worst shape a bug can
// have here: the guarantee silently stops holding, and the command reports
// success.
func TestRefusalHoldsWithARelativeMigrationsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repoRoot, _ := gitRepo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)
	t.Chdir(repoRoot)

	const relDir = "db/migrations"
	merged := filepath.Join(relDir, "20260101120000_add_users.up.sql")

	results, err := RebaseMigrations(relDir, []string{merged})
	if err == nil {
		t.Fatalf("relative dir: renamed an already-merged migration (%+v) — the refusal does not hold on the paths the CLI passes", results)
	}
	var alreadyMerged *AlreadyMergedError
	if !errors.As(err, &alreadyMerged) {
		t.Fatalf("error is %T (%v), want *AlreadyMergedError", err, err)
	}
	if _, statErr := os.Stat(merged); statErr != nil {
		t.Errorf("refused file was moved anyway: %v", statErr)
	}
}

// A batch keeps its relative order. Migrations frequently depend on being
// applied in sequence — a table then its index, a column then its backfill —
// so a rebase that reordered them would produce a schema that fails to apply.
func TestRebasePreservesRelativeOrderOfABatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	_, migDir := gitRepo(t,
		[]string{"20260301000000_add_accounts.up.sql"},
		[]string{
			"20260101120000_create_widgets.up.sql",
			"20260101120001_index_widgets.up.sql",
		},
	)

	// Deliberately passed in the WRONG order: the caller's argument order
	// must not decide the applied order.
	results, err := RebaseMigrations(migDir, []string{
		filepath.Join(migDir, "20260101120001_index_widgets.up.sql"),
		filepath.Join(migDir, "20260101120000_create_widgets.up.sql"),
	})
	if err != nil {
		t.Fatalf("RebaseMigrations: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	var createV, indexV uint64
	for _, r := range results {
		switch base := filepath.Base(r.NewPath); {
		case strings.HasSuffix(base, "_create_widgets.up.sql"):
			createV = r.NewVersion
		case strings.HasSuffix(base, "_index_widgets.up.sql"):
			indexV = r.NewVersion
		default:
			t.Fatalf("unexpected rebased file %q", base)
		}
	}
	if createV == 0 || indexV == 0 {
		t.Fatalf("both migrations must be rebased; got create=%d index=%d", createV, indexV)
	}
	if createV >= indexV {
		t.Errorf("create_widgets rebased to %d and index_widgets to %d — the index would apply before its table", createV, indexV)
	}
}

// --all-pending means "every migration added on this branch", which is the
// set the merge-base defines. Merged history must never be in it: that is the
// set the refusal above protects, and offering to rename it would be the
// same mistake with a flag in front of it.
func TestPendingMigrationsExcludesMergedHistory(t *testing.T) {
	_, migDir := gitRepo(t,
		[]string{"20260101120000_add_users.up.sql"},
		[]string{"20260501000000_add_sessions.up.sql"},
	)

	pending, err := PendingMigrations(migDir)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1 (only the file added on this branch): %v", len(pending), pending)
	}
	if got := filepath.Base(pending[0]); got != "20260501000000_add_sessions.up.sql" {
		t.Errorf("pending = %q, want the branch-added migration", got)
	}
}

// Without git, "new on this branch" is unknowable. Returning every migration
// in the directory would hand --all-pending the whole of merged history,
// which is precisely what must never be renamed — so this errors instead of
// guessing.
func TestPendingMigrationsRefusesWithoutGitHistory(t *testing.T) {
	migDir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "20260101120000_add_users.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	pending, err := PendingMigrations(migDir)
	if err == nil {
		t.Fatalf("PendingMigrations returned %v with no git history; it must refuse rather than offer to rename everything", pending)
	}
	if !strings.Contains(err.Error(), "explicitly") {
		t.Errorf("error should tell the user to name files explicitly; got: %v", err)
	}
}
