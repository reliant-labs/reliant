package migrationgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeMig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	usersMig    = "20260101120000_add_users.up.sql"
	teamsMig    = "20260102000000_add_teams.up.sql"
	sessionsMig = "20260501000000_add_sessions.up.sql"
)

// FilesAt names the migrations at any revision, tells a directory that did
// not exist yet (an empty answer) from a revision it cannot read (unknown),
// and never lists subdirectory contents.
func TestFilesAtReadsAnyRevision(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig, teamsMig}, []string{sessionsMig})
	root := RepoRoot(repoRoot)

	got, ok := FilesAt(root, migDir, "main")
	if !ok || !slices.Equal(got, []string{usersMig, teamsMig}) {
		t.Errorf("FilesAt(main) = %v, %v; want the two merged migrations", got, ok)
	}
	got, ok = FilesAt(root, migDir, "HEAD")
	if !ok || !slices.Equal(got, []string{usersMig, teamsMig, sessionsMig}) {
		t.Errorf("FilesAt(HEAD) = %v, %v; want all three", got, ok)
	}

	if _, ok := FilesAt(root, migDir, "no-such-rev"); ok {
		t.Error("an unreadable revision must be unknown, not an empty list")
	}

	// A directory that does not exist at that revision is a real, empty answer.
	got, ok = FilesAt(root, filepath.Join(root, "other", "dir"), "main")
	if !ok || got == nil || len(got) != 0 {
		t.Errorf("FilesAt of an absent directory = %v (nil %v), %v; want an empty non-nil list", got, got == nil, ok)
	}

	// A directory outside the repository is not in any revision of it.
	got, ok = FilesAt(root, t.TempDir(), "main")
	if !ok || len(got) != 0 {
		t.Errorf("FilesAt of a directory outside the repository = %v, %v; want an empty list", got, ok)
	}
}

func TestContentAtReturnsTheFileAsItWasAtThatRevision(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig}, nil)
	root := RepoRoot(repoRoot)
	writeMig(t, migDir, usersMig, "SELECT 2;\n")

	got, ok := ContentAt(root, filepath.Join(migDir, usersMig), "HEAD")
	if !ok || string(got) != "SELECT 1;\n" {
		t.Errorf("ContentAt(HEAD) = %q, %v; want the committed content, not the working tree's", got, ok)
	}
	// A relative path is resolved against the working directory, as the CLI's
	// relative --dir is.
	t.Chdir(root)
	if got, ok := ContentAt(root, filepath.Join("db", "migrations", usersMig), "HEAD"); !ok || string(got) != "SELECT 1;\n" {
		t.Errorf("ContentAt(relative) = %q, %v", got, ok)
	}
	if _, ok := ContentAt(root, filepath.Join(migDir, "missing.up.sql"), "HEAD"); ok {
		t.Error("a path absent at the revision must be unknown")
	}
	if _, ok := ContentAt(root, filepath.Join(migDir, usersMig), "no-such-rev"); ok {
		t.Error("an unreadable revision must be unknown")
	}
}

func TestFilesMatchingAtIsACaseInsensitiveFixedStringSearch(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{"00000000000000_seed.up.sql"}, nil)
	writeMig(t, migDir, usersMig, "CREATE INDEX CONCURRENTLY i ON t (a);\n")
	writeMig(t, migDir, teamsMig, "ALTER TABLE t ADD COLUMN b text;\n")
	writeMig(t, migDir, sessionsMig, "DROP index concurrently j;\n")
	if err := os.MkdirAll(filepath.Join(migDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMig(t, filepath.Join(migDir, "nested"), "x.sql", "concurrently\n")
	runGitT(t, repoRoot, "add", ".")
	runGitT(t, repoRoot, "commit", "-q", "-m", "files")
	root := RepoRoot(repoRoot)

	got, ok := FilesMatchingAt(root, migDir, "HEAD", "CONCURRENTLY")
	slices.Sort(got)
	if !ok || !slices.Equal(got, []string{usersMig, sessionsMig}) {
		t.Errorf("match CONCURRENTLY = %v, %v; want the two index migrations and not the nested file", got, ok)
	}
	// Several needles are an OR; a regexp metacharacter is literal.
	got, ok = FilesMatchingAt(root, migDir, "HEAD", "add column", "no-such-text")
	if !ok || !slices.Equal(got, []string{teamsMig}) {
		t.Errorf("match add column = %v, %v", got, ok)
	}
	if got, ok := FilesMatchingAt(root, migDir, "HEAD", "t (a)"); !ok || !slices.Equal(got, []string{usersMig}) {
		t.Errorf("a needle with regexp characters must match literally: %v, %v", got, ok)
	}
	// No match is a real, empty answer.
	got, ok = FilesMatchingAt(root, migDir, "HEAD", "nothing like this")
	if !ok || got == nil || len(got) != 0 {
		t.Errorf("no match = %v (nil %v), %v; want an empty non-nil list", got, got == nil, ok)
	}
	if _, ok := FilesMatchingAt(root, migDir, "no-such-rev", "x"); ok {
		t.Error("an unreadable revision must be unknown")
	}
	if _, ok := FilesMatchingAt(root, t.TempDir(), "HEAD", "x"); ok {
		t.Error("a directory outside the repository must be unknown")
	}
}

// DefaultBranchFiles and MergeBaseFiles answer "which files" where
// DefaultBranchVersions answers "which versions": two branches can claim one
// version, and only the file name tells them apart.
func TestDefaultBranchAndMergeBaseFiles(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig}, []string{sessionsMig})
	root := RepoRoot(repoRoot)
	// A migration lands on main after the branch was cut.
	runGitT(t, repoRoot, "checkout", "-q", "main")
	writeMig(t, migDir, teamsMig, "SELECT 1;\n")
	runGitT(t, repoRoot, "add", ".")
	runGitT(t, repoRoot, "commit", "-q", "-m", "later on main")
	runGitT(t, repoRoot, "checkout", "-q", "feature")

	tip, ref, ok := DefaultBranchFiles(root, migDir)
	if !ok || ref != "main" || !tip[usersMig] || !tip[teamsMig] || tip[sessionsMig] || len(tip) != 2 {
		t.Errorf("DefaultBranchFiles = %v, %q, %v; want users+teams on main", tip, ref, ok)
	}
	base, ok := MergeBaseFiles(root, migDir)
	if !ok || !base[usersMig] || base[teamsMig] || base[sessionsMig] || len(base) != 1 {
		t.Errorf("MergeBaseFiles = %v, %v; want only what existed when the branch was cut", base, ok)
	}

}

func TestDefaultBranchAndMergeBaseFilesAreUnknownWithoutADefaultBranch(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig}, nil)
	runGitT(t, repoRoot, "branch", "-q", "-m", "main", "trunk")
	root := RepoRoot(repoRoot)

	if _, _, ok := DefaultBranchFiles(root, migDir); ok {
		t.Error("DefaultBranchFiles claims a default branch in a repository with none of the usual names")
	}
	if _, ok := MergeBaseFiles(root, migDir); ok {
		t.Error("MergeBaseFiles claims a merge-base without a default branch")
	}
	if _, _, ok := MergeBase(root); ok {
		t.Error("MergeBase claims a default branch")
	}
	if _, ok := DefaultBranchVersions(root, migDir); ok {
		t.Error("DefaultBranchVersions claims a default branch")
	}
}

// The remote's own declaration of its default branch beats the name guesses.
func TestDefaultBranchPrefersTheRemotesDeclaration(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig}, nil)
	root := RepoRoot(repoRoot)
	// origin/develop is the remote's default, and also differs from "main".
	runGitT(t, repoRoot, "update-ref", "refs/remotes/origin/develop", "main")
	runGitT(t, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")

	tip, ref, ok := DefaultBranchFiles(root, migDir)
	if !ok || ref != "refs/remotes/origin/develop" || !tip[usersMig] {
		t.Errorf("DefaultBranchFiles = %v, %q, %v; want the ref origin/HEAD names", tip, ref, ok)
	}
	if _, gotRef, ok := MergeBase(root); !ok || gotRef != "refs/remotes/origin/develop" {
		t.Errorf("MergeBase ref = %q, %v", gotRef, ok)
	}
}

func TestDefaultBranchFallsBackThroughTheUsualNames(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	for _, name := range []string{"master", "main"} {
		t.Run(name, func(t *testing.T) {
			repoRoot, migDir := repo(t, []string{usersMig}, nil)
			if name != "main" {
				runGitT(t, repoRoot, "branch", "-q", "-m", "main", name)
			}
			_, ref, ok := DefaultBranchFiles(RepoRoot(repoRoot), migDir)
			if !ok || ref != name {
				t.Errorf("default branch = %q, %v; want %q", ref, ok, name)
			}
		})
	}
}

func TestResolveCommit(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, _ := repo(t, []string{usersMig}, []string{sessionsMig})
	root := RepoRoot(repoRoot)
	head := runGitT(t, repoRoot, "rev-parse", "HEAD")
	parent := runGitT(t, repoRoot, "rev-parse", "HEAD~1")

	for rev, want := range map[string]string{"HEAD": head, "HEAD~1": parent, "feature": head, head: head} {
		if got, ok := ResolveCommit(root, rev); !ok || got != want {
			t.Errorf("ResolveCommit(%q) = %q, %v; want %q", rev, got, ok, want)
		}
	}
	if got, ok := ResolveCommit(root, "no-such-rev"); ok || got != "" {
		t.Errorf("ResolveCommit(unknown) = %q, %v; want unknown", got, ok)
	}
	// A path in the tree is not a commit.
	if _, ok := ResolveCommit(root, "HEAD:db"); ok {
		t.Error("a tree-ish that is not a commit must not resolve")
	}
}

func TestChangedSinceAndChangedPathsSinceAreUnknownForABadRevision(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig}, nil)
	root := RepoRoot(repoRoot)
	if _, ok := ChangedSince(root, migDir, "no-such-rev"); ok {
		t.Error("ChangedSince with a bad revision must be unknown")
	}
	if _, ok := ChangedPathsSince(root, "no-such-rev"); ok {
		t.Error("ChangedPathsSince with a bad revision must be unknown")
	}
	if _, ok := ChangedSince(root, t.TempDir(), "HEAD"); ok {
		t.Error("ChangedSince for a directory outside the repository must be unknown")
	}
	// Nothing changed: a real, empty answer.
	if got, ok := ChangedSince(root, migDir, "HEAD"); !ok || len(got) != 0 {
		t.Errorf("ChangedSince on a clean tree = %v, %v", got, ok)
	}
	if got, ok := ChangedPathsSince(root, "HEAD"); !ok || len(got) != 0 {
		t.Errorf("ChangedPathsSince on a clean tree = %v, %v", got, ok)
	}
}

// ChangedSince only reports files directly in the directory, and a deletion
// counts as a change.
func TestChangedSinceReportsDeletionsAndIgnoresNestedFiles(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	repoRoot, migDir := repo(t, []string{usersMig, teamsMig}, nil)
	root := RepoRoot(repoRoot)
	if err := os.Remove(filepath.Join(migDir, usersMig)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(migDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMig(t, filepath.Join(migDir, "nested"), "x.sql", "x\n")
	runGitT(t, repoRoot, "add", "-A")

	got, ok := ChangedSince(root, migDir, "HEAD")
	if !ok || len(got) != 1 || !got[usersMig] {
		t.Errorf("ChangedSince = %v, %v; want only the deleted migration", got, ok)
	}
	// Asked for the repo root, the prefix is empty and a top-level file counts.
	writeMig(t, root, "top.txt", "t\n")
	runGitT(t, repoRoot, "add", "top.txt")
	if got, ok := ChangedSince(root, root, "HEAD"); !ok || !got["top.txt"] {
		t.Errorf("ChangedSince at the repo root = %v, %v; want top.txt", got, ok)
	}
}

func TestRepoRelativeIsSlashFormAndRefusesPathsOutsideTheRepository(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "db", "migrations")
	// The path must exist: both sides are symlink-resolved before they are
	// compared (macOS /private, Windows 8.3 names), and a missing path stays
	// unresolved and so would not compare as inside the resolved root.
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := RepoRelative(root, inside); !ok || got != "db/migrations" {
		t.Errorf("RepoRelative(inside) = %q, %v; want db/migrations", got, ok)
	}
	if got, ok := RepoRelative(root, root); !ok || got != "." {
		t.Errorf("RepoRelative(root) = %q, %v; want .", got, ok)
	}
	for name, outside := range map[string]string{
		"sibling": filepath.Join(filepath.Dir(root), "elsewhere"),
		"parent":  filepath.Dir(root),
	} {
		if got, ok := RepoRelative(root, outside); ok {
			t.Errorf("RepoRelative(%s) = %q, true; want false", name, got)
		}
	}
}

// Next allocates a version above everything on disk, whether or not there is
// a repository.
func TestNextAllocatesAboveTheDirectoryAndReportsScanErrors(t *testing.T) {
	dir := t.TempDir()
	writeMig(t, dir, "99990101000000_future.up.sql", "SELECT 1;\n")
	v, err := Next(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v <= "99990101000000" || len(v) != 14 {
		t.Errorf("Next = %q, want a 14-digit version above the directory's newest", v)
	}
	if _, err := Next(filepath.Join(dir, "no", "such", "dir", "\x00")); err == nil {
		t.Error("Next on an unscannable directory must fail rather than allocate blindly")
	}
}

func TestScrubGitEnvDropsOnlyRepositoryPointingVariables(t *testing.T) {
	env := []string{"PATH=/bin", "GIT_DIR=/x/.git", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=/x/index", "HOME=/h", "GIT_CONFIG_COUNT=1"}
	got := scrubGitEnv(env)
	if slices.Contains(got, "GIT_DIR=/x/.git") || slices.Contains(got, "GIT_WORK_TREE=/x") || slices.Contains(got, "GIT_INDEX_FILE=/x/index") {
		t.Errorf("repository-pointing variables survived: %v", got)
	}
	for _, keep := range []string{"PATH=/bin", "HOME=/h", "GIT_CONFIG_COUNT=1"} {
		if !slices.Contains(got, keep) {
			t.Errorf("%s was dropped: %v", keep, got)
		}
	}
}
