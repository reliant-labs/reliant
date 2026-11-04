package cli

// `forge env releases backfill-migrations <env>`: record the migration
// manifest (release_migrations.go) of releases cut before the cut recorded
// one, reconstructed from git.
//
// WHY IT EXISTS. The passed-migration guard judges a release against the
// manifests of the releases an env applied. Releases cut before the guard
// existed have none, so on the day it ships the guard knows nothing about what
// prod already runs — exactly the history the guard exists to consult. The
// backfill gives those releases the manifest their cut would have recorded.
//
// WHAT IT READS. A release records the commit it was cut from
// (Provenance.Commit), so the project's own migrations are `git ls-tree` at
// that commit. A sibling checkout the release built from (a ShellBuild cwd —
// ../reliant) is held to the project's go.mod pin at that same commit, so the
// sibling's migrations are `git ls-tree` at the commit go.mod pinned THEN.
// The sibling set is today's (this checkout's KCL): close enough for the
// recent releases a backfill is for, and named in the output.
//
// WHAT IT NEVER DOES. Rewrite a manifest. A release that already records one
// is skipped, and the ledger refuses a different one (release.ErrReleaseConflict):
// a manifest, once recorded, is what every later deploy judged.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/pkg/release"
)

// releaseMigrationRecorder is the one ledger capability the backfill needs.
// Declared here, at its consumer; both ledgers implement it.
type releaseMigrationRecorder interface {
	RecordMigrations(ctx context.Context, version string, m release.MigrationManifest) (recorded bool, err error)
}

func newEnvReleasesBackfillMigrationsCmd() *cobra.Command {
	var (
		since  string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "backfill-migrations <env>",
		Short: "Record, from git, the migrations of releases cut before forge recorded them",
		Args:  cobra.ExactArgs(1),
		Long: `Give each release in <env>'s ledger that records no migration manifest the one
its cut would have recorded, reconstructed from git:

  the project's migrations   git ls-tree at the release's own commit
  a sibling checkout's       git ls-tree at the commit the project's go.mod
                             pinned it to at the release's commit

` + "`forge env build --release`" + ` and ` + "`forge env deploy`" + ` refuse a release carrying a migration
the env's schema has already passed, judged against the manifests of the
releases the env applied; a release with none is history the check cannot see.

Releases are read newest first; --since <version> stops after that release.
A release that already records a manifest is skipped, never rewritten. The
commits must be in this checkout and its siblings (git fetch first).

Exit 0 done (or nothing to do), 1 a release could not be reconstructed or
recorded (the others still are), 2 the ledger could not be read.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackfillMigrations(cmd.Context(), cmd.OutOrStdout(), projectDirForKCL(), args[0], since, dryRun)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Oldest release to backfill (a version); default every release in the ledger")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print each manifest that would be recorded; record nothing")
	return cmd
}

func runBackfillMigrations(ctx context.Context, out io.Writer, projectDir, env, since string, dryRun bool) error {
	ledger, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		return undeterminedf("choose the ledger of %s: %v", env, err)
	}
	recorder, ok := ledger.Releases.(releaseMigrationRecorder)
	if !ok && !dryRun {
		return undeterminedf("the ledger at %s cannot record migration manifests", ledger.Releases.Location())
	}
	releases, err := ledger.Releases.List(ctx)
	if err != nil {
		return undeterminedf("list releases from %s: %v", ledger.Releases.Location(), err)
	}
	if since != "" {
		cut := -1
		for i, r := range releases {
			if r.Version == since {
				cut = i
				break
			}
		}
		if cut < 0 {
			return exitCodeError{code: exitWrong, msg: fmt.Sprintf("--since %s: no such release in %s", since, ledger.Releases.Location())}
		}
		releases = releases[:cut+1]
	}

	var siblings []*buildtarget.Source
	if entities, err := RenderKCL(ctx, projectDir, env); err == nil && entities != nil {
		uses, err := liveShellSources(ctx, projectDir, entities)
		if err != nil {
			return err
		}
		for _, u := range uses {
			if u.src != nil && !u.src.Project {
				siblings = append(siblings, u.src)
			}
		}
	} else if err != nil {
		fmt.Fprintf(out, "[backfill] note: deploy/kcl/%s did not render (%v), so only the project's own migrations are reconstructed\n", env, err)
	}

	var failed []string
	done := 0
	for _, r := range releases {
		if r.Migrations != nil {
			continue
		}
		m, notes, err := reconstructReleaseMigrations(ctx, projectDir, r, siblings)
		for _, n := range notes {
			fmt.Fprintf(out, "[backfill] %s: note: %s\n", r.Version, n)
		}
		if err != nil {
			fmt.Fprintf(out, "[backfill] %s: NOT RECORDED: %v\n", r.Version, err)
			failed = append(failed, r.Version)
			continue
		}
		fmt.Fprintf(out, "[backfill] %s:\n", r.Version)
		for _, s := range m.Sets {
			head := "(empty)"
			if n := len(s.Files); n > 0 {
				head = s.Files[n-1]
			}
			fmt.Fprintf(out, "[backfill]   %s @ %s: %d file(s), head %s\n", s.Source, shortSHA(s.Commit), len(s.Files), head)
		}
		if dryRun {
			done++
			continue
		}
		recorded, err := recorder.RecordMigrations(ctx, r.Version, *m)
		switch {
		case err != nil:
			fmt.Fprintf(out, "[backfill] %s: NOT RECORDED: %v\n", r.Version, err)
			failed = append(failed, r.Version)
		case recorded:
			done++
		}
	}
	verb := "recorded"
	if dryRun {
		verb = "would record (dry run)"
	}
	fmt.Fprintf(out, "[backfill] %s %d manifest(s) in %s\n", verb, done, ledger.Releases.Location())
	if len(failed) > 0 {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("%d release(s) were not recorded: %s", len(failed), strings.Join(failed, ", "))}
	}
	return nil
}

// reconstructReleaseMigrations is the manifest r's cut would have recorded,
// from git. An error means the release cannot be reconstructed truthfully and
// nothing should be recorded for it.
func reconstructReleaseMigrations(ctx context.Context, projectDir string, r release.Release, siblings []*buildtarget.Source) (*release.MigrationManifest, []string, error) {
	commit := r.Git.Commit
	repo := ""
	if r.Provenance != nil {
		commit, repo = r.Provenance.Commit, r.Provenance.Repo
	}
	if !release.ValidObjectID(commit) {
		return nil, nil, errors.New("it records no source commit")
	}
	var notes []string
	if r.Git.Dirty || (r.Provenance != nil && r.Provenance.Dirty) {
		notes = append(notes, "it was cut from a DIRTY checkout; its migrations are read from the commit it was built on")
	}
	if repo == "" {
		repo = captureReleaseProvenance(ctx, projectDir).Repo
	}
	root := migrationgit.RepoRoot(projectDir)
	if _, ok := migrationgit.FilesAt(root, root, commit); !ok {
		return nil, notes, fmt.Errorf("commit %s is not in this checkout — git fetch origin, then re-run", shortSHA(commit))
	}

	var m release.MigrationManifest
	dir, _ := projectMigrationsDir(projectDir)
	if dir == "" {
		dir = filepath.Join(projectDir, config.DefaultMigrationsDir)
	}
	if names, ok := migrationgit.FilesAt(root, dir, commit); ok && len(names) > 0 {
		rel, ok := migrationgit.RepoRelative(root, dir)
		if !ok {
			rel = config.DefaultMigrationsDir
		}
		m.Sets = append(m.Sets, release.NewMigrationSet(migrationSourceID(repo, hostedProjectName(), rel), commit, names))
	}

	goMod, goModErr := gitShow(ctx, root, commit+":"+projectGoModPath(root, projectDir))
	for _, src := range siblings {
		sdir := filepath.Join(src.Dir, config.DefaultMigrationsDir)
		if !isForgeCheckoutWithMigrations(src.Dir) {
			continue
		}
		if goModErr != nil {
			return nil, notes, fmt.Errorf("read go.mod at %s to find the %s commit it pinned: %v", shortSHA(commit), src.Dir, goModErr)
		}
		pin, ok := goModPinFrom(ctx, goMod, src)
		if !ok || pin.commit == "" {
			why := "go.mod does not pin it"
			if pin.unresolved != "" {
				why = pin.unresolved
			}
			return nil, notes, fmt.Errorf("cannot tell which %s commit it built (%s at %s)", src.Dir, why, shortSHA(commit))
		}
		full, err := src.ResolveCommit(ctx, pin.commit)
		if err != nil || full == "" {
			return nil, notes, fmt.Errorf("%s does not have commit %s (%s) — git -C %s fetch origin, then re-run", src.Dir, pin.commit, pin.declared, src.Dir)
		}
		names, ok := migrationgit.FilesAt(src.Dir, sdir, full)
		if !ok {
			return nil, notes, fmt.Errorf("read %s at %s", sdir, shortSHA(full))
		}
		rel, ok := migrationgit.RepoRelative(src.Dir, sdir)
		if !ok {
			rel = config.DefaultMigrationsDir
		}
		m.Sets = append(m.Sets, release.NewMigrationSet(migrationSourceID(src.Repo, filepath.Base(src.Dir), rel), full, names))
	}
	if len(m.Sets) == 0 {
		return nil, notes, errors.New("it carries no migrations directory at its commit")
	}
	m.Normalize()
	if err := m.Validate(); err != nil {
		return nil, notes, err
	}
	return &m, notes, nil
}

func isForgeCheckoutWithMigrations(dir string) bool {
	if dir == "" {
		return false
	}
	_, ok := projectMigrationsDir(dir)
	return ok && fileExists(filepath.Join(dir, "forge.yaml"))
}

// projectGoModPath is the project's go.mod relative to its repository root,
// in the slash form `git show <rev>:<path>` takes.
func projectGoModPath(root, projectDir string) string {
	abs, err := filepath.Abs(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return "go.mod"
	}
	rel, err := filepath.Rel(resolvedPath(root), resolvedPath(filepath.Dir(abs)))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "go.mod"
	}
	return filepath.ToSlash(filepath.Join(rel, "go.mod"))
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
