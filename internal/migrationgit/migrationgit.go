// Package migrationgit answers, from git, the one question both the version
// lint and `forge db migration rebase` have to agree on: which migrations
// were already on the default branch when this branch was cut.
//
// Both callers act on that answer, in opposite directions. The lint flags a
// migration as NEW — and therefore still fixable — when its version is above
// the merge-base high-water mark. Rebase REFUSES to re-version a migration
// that is at or below it and present on the default branch, because renaming
// one that has already merged breaks every database that recorded it.
//
// Two implementations of that boundary would eventually disagree, and the
// disagreement would be silent: the lint would tell someone to rebase a file
// the rebase command would happily rename, or refuse one the lint had just
// flagged. One definition, in one place.
//
// EVERY FAILURE HERE IS "UNKNOWN", NOT AN ERROR. This code runs in checkouts
// it does not control: a shallow CI clone, an exported tarball, a repository
// with no default branch, a migrations directory that is not tracked at all.
// A missing git binary is deliberately indistinguishable from a failed
// command, because both mean the same thing to a caller.
package migrationgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/migrationver"
)

// RepoRoot returns the git repository root containing dir, or dir itself when
// there is none. Every git call is scoped to it, so a lookup never reads a
// parent repository that happens to sit above an untracked project.
func RepoRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	out, ok := runGit(abs, "rev-parse", "--show-toplevel")
	if !ok {
		return abs
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return abs
	}
	return root
}

// MergeBaseMax returns the highest migration version present in
// migrationsDir as of the merge-base between HEAD and the default branch —
// the state this branch started from — and whether that could be determined
// at all.
func MergeBaseMax(repoRoot, migrationsDir string) (uint64, bool) {
	versions, ok := versionsAt(repoRoot, migrationsDir, mergeBaseRev)
	if !ok {
		return 0, false
	}
	var max uint64
	for v := range versions {
		if v > max {
			max = v
		}
	}
	return max, true
}

// DefaultBranchVersions returns the migration versions present in
// migrationsDir on the default branch's tip, mapped to the filename that
// claims each one.
//
// The TIP, not the merge-base, because this answers "has this version already
// merged". A migration that landed on main after this branch was cut is just
// as merged as one that predates it, and renaming it is just as wrong.
func DefaultBranchVersions(repoRoot, migrationsDir string) (map[uint64]string, bool) {
	return versionsAt(repoRoot, migrationsDir, defaultBranchRev)
}

// IsTracked reports whether path is tracked by git, which decides whether a
// rename goes through `git mv` or a plain filesystem rename.
func IsTracked(repoRoot, path string) bool {
	rel, err := repoRelative(repoRoot, path)
	if err != nil {
		return false
	}
	out, ok := runGit(repoRoot, "ls-files", "--error-unmatch", filepath.ToSlash(rel))
	return ok && strings.TrimSpace(out) != ""
}

// Move renames from to to, using `git mv` when the source is tracked so the
// rename is staged as a rename rather than appearing as a delete plus an
// untracked add.
func Move(repoRoot, from, to string) error {
	fromRel, fromErr := repoRelative(repoRoot, from)
	toRel, toErr := repoRelativeDest(repoRoot, to)
	if fromErr == nil && toErr == nil && IsTracked(repoRoot, from) {
		cmd := exec.Command("git", "mv", filepath.ToSlash(fromRel), filepath.ToSlash(toRel))
		cmd.Dir = repoRoot
		// Same reason as runGit: an inherited GIT_DIR / GIT_INDEX_FILE would
		// stage this rename into the enclosing hook's repository and index
		// rather than repoRoot's.
		cmd.Env = scrubGitEnv(os.Environ())
		if err := cmd.Run(); err == nil {
			return nil
		}
		// Fall through to a plain rename. `git mv` refuses in states where
		// the rename is still legitimate (an unmerged index, a path already
		// staged for deletion). The rename is the user's intent; how it
		// reaches the index is not.
	}
	return os.Rename(from, to)
}

// revSpec names one of the two git revisions this package resolves. It is a
// function rather than a string because "the merge-base with the default
// branch" takes two git calls to name.
type revSpec func(repoRoot string) (string, bool)

// mergeBaseRev resolves the merge-base between HEAD and the default branch.
func mergeBaseRev(repoRoot string) (string, bool) {
	defaultBranch, ok := defaultBranchRef(repoRoot)
	if !ok {
		return "", false
	}
	base, ok := runGit(repoRoot, "merge-base", "HEAD", defaultBranch)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(base), true
}

// defaultBranchRev resolves the default branch's tip.
func defaultBranchRev(repoRoot string) (string, bool) {
	ref, ok := defaultBranchRef(repoRoot)
	if !ok {
		return "", false
	}
	return ref, true
}

// versionsAt lists the migration versions present in migrationsDir at one
// revision, mapped to the filename claiming each.
func versionsAt(repoRoot, migrationsDir string, rev revSpec) (map[uint64]string, bool) {
	revision, ok := rev(repoRoot)
	if !ok {
		return nil, false
	}
	rel, err := repoRelative(repoRoot, migrationsDir)
	if err != nil {
		return nil, false
	}
	out, ok := runGit(repoRoot, "ls-tree", "--name-only", revision+":"+filepath.ToSlash(rel))
	if !ok {
		// The directory did not exist at that revision: every migration in
		// it is new, which is a real and useful answer (an empty set).
		return map[uint64]string{}, true
	}
	versions := map[uint64]string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if v, ok := migrationver.ParseVersion(name); ok {
			versions[v] = name
		}
	}
	return versions, true
}

// defaultBranchRef resolves the default branch to compare against, preferring
// the remote's own declaration over a guess.
func defaultBranchRef(repoRoot string) (string, bool) {
	// What the remote says its default branch is — the authoritative answer
	// when the repository has a remote.
	if out, ok := runGit(repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD"); ok {
		ref := strings.TrimSpace(out)
		if ref != "" {
			return ref, true
		}
	}
	// A local-only repository, or one whose origin/HEAD was never set.
	for _, candidate := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, ok := runGit(repoRoot, "rev-parse", "--verify", candidate); ok {
			return candidate, true
		}
	}
	return "", false
}

// repoRelative makes an EXISTING path relative to repoRoot, for handing to a
// git command that names a path inside the repository.
//
// MAKE IT ABSOLUTE BEFORE RESOLVING IT. This is the step that was missing and
// it broke every lookup in the common case. The CLI passes a relative
// migrations dir ("db/migrations"), while `git rev-parse --show-toplevel`
// always reports a fully resolved absolute path — and on macOS every /tmp and
// /var path really lives under /private. EvalSymlinks on a relative path
// returns it unchanged, so filepath.Rel was comparing "/private/tmp/x"
// against "db/migrations" and producing garbage like
// ../../private/tmp/x/db/migrations, which ls-tree cannot resolve.
//
// Every caller then took its "cannot know" branch: the lint silently fell
// back to its no-git rule, and rebase silently stopped refusing already-merged
// migrations — the one thing it must never do. Nothing errored, which is why
// a test using absolute paths could not see it.
func repoRelative(repoRoot, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Rel(resolvePath(repoRoot), resolvePath(abs))
}

// repoRelativeDest makes a path that does NOT EXIST YET relative to repoRoot,
// for `git mv`'s destination.
//
// It resolves the parent directory rather than the path itself, because
// EvalSymlinks fails on a missing path and hands back the input unchanged —
// which left an unresolved destination compared against a resolved root, so
// `git mv` rejected the path and Move fell through to os.Rename. Git then
// recorded a delete plus an untracked add instead of a rename, and the
// version change stopped being reviewable as one file.
func repoRelativeDest(repoRoot, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent := resolvePath(filepath.Dir(abs))
	return filepath.Rel(resolvePath(repoRoot), filepath.Join(parent, filepath.Base(abs)))
}

// resolvePath returns path with every symlink resolved, falling back to the
// input when it cannot be resolved. See versionsAt for why this matters.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// runGit runs one git command in dir, returning its stdout and whether it
// succeeded. A missing git binary is indistinguishable from a failed command
// on purpose: both mean "cannot know", which is the only thing callers act on.
//
// THE INHERITED GIT ENVIRONMENT IS SCRUBBED, and that is not defensive
// tidying — without it this package answers about the wrong repository. git
// exports GIT_DIR (and often GIT_WORK_TREE, GIT_INDEX_FILE) to every hook it
// runs, and those variables take PRECEDENCE over a child's working directory.
// So inside a pre-push hook, `cmd.Dir = dir` was silently overridden: the
// merge-base and ls-tree lookups resolved against the hook's repository,
// failed, and MergeBaseMax reported "cannot know". The version lint then fell
// back to its no-git rule and flagged a project's long-merged
// `00001_*.up.sql` as "new but not a UTC timestamp".
//
// That produced the worst possible shape of failure: `forge lint` passed from
// the shell and the identical check failed on `git push`, naming a file the
// author had never touched. Clearing these is what makes dir authoritative.
func runGit(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = scrubGitEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// gitEnvOverrides are the inherited variables that would redirect a git
// invocation away from cmd.Dir. Each one names a repository, an index or a
// work tree explicitly, which is precisely what this package is trying to
// determine FROM the directory it was given.
var gitEnvOverrides = []string{
	"GIT_DIR=",
	"GIT_WORK_TREE=",
	"GIT_INDEX_FILE=",
	"GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
}

// scrubGitEnv returns env without the repository-pointing variables. Only
// those are dropped: the rest of the environment (PATH, HOME, proxy and
// credential settings, GIT_CONFIG_*) is what lets git run at all, and
// clearing it wholesale would break the lookups in a different way.
func scrubGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, prefix := range gitEnvOverrides {
			if strings.HasPrefix(kv, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
