package database

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/internal/migrationver"
)

// REBASING A MIGRATION: WHAT THIS FIXES AND WHY RENAMING IS THE FIX.
//
// A migration whose version is BELOW the schema's current version can never
// run. golang-migrate tracks one high-water number and applies only what is
// above it, so a file at a passed version is not pending — it is invisible.
// That happens exactly one way with timestamp versions: branches A and B are
// cut, A allocates the earlier timestamp, and B merges and deploys first.
// Main now holds A's SQL at a version the database has already gone past, and
// every later `up` agrees the schema is current.
//
// The file has not run ANYWHERE, so the fix is to re-version it — move it
// above the high-water mark and it becomes pending again. Both the migrator's
// refusal (pkg/migratekit.MissingMigrationError) and the version lint tell
// users to do this, and both name this command.
//
// WHY THE NEW VERSION IS ALLOCATED, NOT TYPED. A hand-typed replacement
// reintroduces the collision the timestamp scheme exists to prevent: the
// obvious choice is "one more than what I can see", which is what every
// parallel branch also picks. RebaseMigrations allocates through
// migrationver, the same allocator `forge db migration new` uses, and seeds
// it above BOTH the directory's max and the default branch's merge-base max —
// so the result orders after everything this branch can see and after
// everything the branch was cut from.
//
// THE REFUSAL IS THE LOAD-BEARING PART. A version that already merged is
// recorded in every database that ran it, under a filename this rebase would
// change. Renaming it makes those databases permanently inconsistent with the
// repository: the recorded version has no file, and the new file has never
// been applied. That is strictly worse than the problem being fixed, and it
// is unrecoverable without hand-editing schema_migrations. So a file at or
// below the merge-base max that is also present on the default branch is
// refused, always, with no flag to override it.
//
// THE REFUSAL IS ABOUT A FILE, NOT A NUMBER. What a database recorded is one
// specific migration, identified by the name it ran under — so the question
// is whether THIS FILE is on the default branch, not whether its version is.
// Those come apart in the most common collision there is: two branches
// allocate in the same second, one merges, and the other now holds a
// different migration under a version that is also on main. That file has
// never been applied anywhere. It is exactly what `duplicate-migration-
// version` tells the author to rebase, and renaming it is completely safe.
//
// Comparing versions alone conflated the two and refused it, with a message
// explaining that a database had recorded it and advising a new forward
// migration — advice that cannot resolve a duplicate version, about a file
// nothing had applied. The lint named this command and this command said no.
// So the match is on the filename; a same-version-different-file case is a
// collision to fix.

// RebaseResult is one file's rename: what it was called and what it is called
// now. Returned rather than only printed so the CLI owns the output format
// and a test can assert on the decision instead of parsing stdout.
type RebaseResult struct {
	OldPath string
	NewPath string
	// OldVersion and NewVersion are the numeric version prefixes, so a
	// caller can assert ordering without re-parsing the filenames.
	OldVersion uint64
	NewVersion uint64
}

// AlreadyMergedError reports a refusal to re-version a migration that has
// already merged to the default branch.
//
// It is a type, not a formatted string, so the CLI can report every refused
// file in one pass and a test can assert on the reason rather than on prose.
type AlreadyMergedError struct {
	// File is the base name of the migration that was refused.
	File string
	// Version is the version it claims.
	Version uint64
	// MergeBaseMax is the high-water mark it is at or below.
	MergeBaseMax uint64
}

func (e *AlreadyMergedError) Error() string {
	return fmt.Sprintf("refusing to re-version %s: version %d is already on the default branch "+
		"(at or below the merge-base high-water mark %d), so a database somewhere has already recorded it as applied — "+
		"renaming it would leave that database with a recorded version whose file no longer exists. "+
		"A migration that has already merged must keep its version; write a NEW forward migration instead",
		e.File, e.Version, e.MergeBaseMax)
}

// RebaseMigrations re-versions each file in paths to a fresh UTC timestamp
// that sorts after every version in dir and after the default branch's
// merge-base high-water mark.
//
// The name stem is preserved: the version is the only thing that changes, so
// a reviewer following the file across the rename sees the same migration.
// Files are processed in ascending version order, which keeps their relative
// order intact — a batch that depends on being applied in sequence still is.
func RebaseMigrations(dir string, paths []string) ([]RebaseResult, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	repoRoot := migrationgit.RepoRoot(dir)
	mergeBaseMax, haveMergeBase := migrationgit.MergeBaseMax(repoRoot, dir)
	var defaultBranch map[uint64]string
	if haveMergeBase {
		defaultBranch, _ = migrationgit.DefaultBranchVersions(repoRoot, dir)
	}

	type candidate struct {
		path    string
		base    string
		version uint64
		stem    string
	}
	candidates := make([]candidate, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		version, ok := migrationver.ParseVersion(base)
		if !ok {
			return nil, fmt.Errorf("%s has no numeric version prefix, so there is nothing to re-version — a migration is named <version>_<name>.up.sql", base)
		}
		stem, ok := versionStem(base)
		if !ok {
			return nil, fmt.Errorf("%s is not a migration filename (expected <version>_<name>.up.sql)", base)
		}
		candidates = append(candidates, candidate{path: path, base: base, version: version, stem: stem})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].version < candidates[j].version })

	// Refuse the whole batch before renaming anything. A partial rebase
	// leaves the directory in a state neither the user nor a retry can
	// reason about.
	for _, c := range candidates {
		if !haveMergeBase || c.version > mergeBaseMax {
			continue
		}
		// Compare the FILE, not the version. The default branch holding
		// this version under a DIFFERENT name means two migrations collided
		// on one number — this one was never applied, and rebasing it is
		// the fix. Only the same file is immutable.
		mergedName, merged := defaultBranch[c.version]
		if !merged || filepath.Base(mergedName) != c.base {
			continue
		}
		return nil, &AlreadyMergedError{File: c.base, Version: c.version, MergeBaseMax: mergeBaseMax}
	}

	// Seed the allocator above the merge-base mark as well as the
	// directory's own contents. The directory alone is not enough: the file
	// being rebased may BE the directory max, and a version equal to one
	// already merged and deployed elsewhere would be invisible all over
	// again.
	taken, err := migrationver.Scan(dir)
	if err != nil {
		return nil, err
	}
	if haveMergeBase {
		taken[mergeBaseMax] = true
	}
	for v := range defaultBranch {
		taken[v] = true
	}

	versions := migrationver.AllocateAfter(taken, len(candidates))

	results := make([]RebaseResult, 0, len(candidates))
	for i, c := range candidates {
		newBase := versions[i] + "_" + c.stem
		newPath := filepath.Join(filepath.Dir(c.path), newBase)
		if newPath == c.path {
			continue
		}
		if err := migrationgit.Move(repoRoot, c.path, newPath); err != nil {
			return results, fmt.Errorf("rename %s to %s: %w", c.base, newBase, err)
		}
		newVersion, _ := migrationver.ParseVersion(newBase)
		results = append(results, RebaseResult{
			OldPath:    c.path,
			NewPath:    newPath,
			OldVersion: c.version,
			NewVersion: newVersion,
		})
	}
	return results, nil
}

// PendingMigrations returns the *.up.sql files in dir that are new on this
// branch — above the default branch's merge-base high-water mark — which is
// what `--all-pending` means.
//
// Without git, newness is unknowable and this returns nothing rather than
// guessing. Offering to rename every migration in a directory, including
// merged history, is exactly the outcome the refusal exists to prevent, and
// an empty answer with an explanation is the honest version of "cannot tell".
func PendingMigrations(dir string) ([]string, error) {
	repoRoot := migrationgit.RepoRoot(dir)
	mergeBaseMax, haveMergeBase := migrationgit.MergeBaseMax(repoRoot, dir)
	if !haveMergeBase {
		return nil, fmt.Errorf("cannot determine which migrations are new without git history "+
			"(no default branch, or %s is not in a git repository) — name the files to rebase explicitly", dir)
	}
	entries, err := upSQLFiles(dir)
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, path := range entries {
		v, ok := migrationver.ParseVersion(filepath.Base(path))
		if !ok || v <= mergeBaseMax {
			continue
		}
		pending = append(pending, path)
	}
	sort.Strings(pending)
	return pending, nil
}

// upSQLFiles lists the *.up.sql files of a migrations directory, as paths.
func upSQLFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files, nil
}

// versionStem returns everything after the version prefix of a migration
// filename ("20260101120000_add_users.up.sql" -> "add_users.up.sql").
func versionStem(base string) (string, bool) {
	_, stem, ok := strings.Cut(base, "_")
	if !ok || stem == "" {
		return "", false
	}
	return stem, true
}
