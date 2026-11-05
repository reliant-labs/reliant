package cli

// THE MIGRATION-ORDER GUARD on a release (pkg/release migrations.go has the
// why). Two refusals, both before anything ships:
//
//  1. PASSED MIGRATIONS — a release carries a migration the target env's
//     schema has already gone past without applying: a file no release the
//     env applied carried, at a version at or below the highest one any of
//     them did. The env's migrator would refuse it at boot, after the build,
//     with the new pods crash-looping. Judged at `forge env build <env>
//     --release` (before the build, and again at the cut over what the build
//     recorded) and at `forge env deploy <env> <version>`.
//     --allow-passed-migrations waives it, for the one honest case: the SQL
//     was applied to the env's database by hand and recorded in
//     schema_migrations_applied.
//
//  2. UNMERGED MIGRATIONS — a release, for an env on a control plane, cut from
//     a commit carrying one of the PROJECT'S OWN migrations that is not on the
//     default branch. Once such a
//     release reaches an env, that env's schema is ahead of main, and main's
//     next migration can land below it while passing every merge-time check
//     (those compare against main). Requiring release migrations to be merged
//     first keeps main's head at or above every env's, which is what makes
//     the merge-time lint sufficient. --allow-unmerged-migrations waives it
//     (a hotfix that genuinely cannot land on main first); the passed-
//     migration guard still protects the next release.
//
// Sibling checkouts are judged only by (1). Their commits are already held to
// the project's pins (source_pin.go), and the pin is the sibling's business.
//
// EVERYTHING THAT CANNOT BE KNOWN IS A NOTE, NEVER A REFUSAL. A ledger that
// cannot list history, an applied release cut before manifests existed, a
// checkout with no default branch: each is said once, and the env's own
// migrator remains the last line of defence. Refusing on missing evidence
// would block every deploy of every project that predates this check.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/pkg/release"
)

// Waiver flag names, named by every refusal that can be waived.
const (
	allowPassedMigrationsFlag   = "allow-passed-migrations"
	allowUnmergedMigrationsFlag = "allow-unmerged-migrations"
)

// migrationHistoryCap bounds how many promotions the guard reads back. An
// env's schema head is the highest version ANY applied release carried, so
// the guard reads the whole history it can; the cap only keeps a pathological
// ledger from making a deploy wait.
const migrationHistoryCap = 5000

// migrationSourceID is a migrations directory's identity across machines:
// the canonical repository plus the directory's path inside it. A checkout
// with no remote falls back to the project name, which is stable on the one
// machine such a ledger can be read from.
func migrationSourceID(repo, fallback, relDir string) string {
	base := repo
	if base == "" {
		base = "project:" + fallback
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(relDir, "/")
}

// projectMigrationsDir is the project's own migrations directory, absolute,
// and whether it exists. forge.yaml cannot move it (config.DefaultMigrationsDir
// is derived), so neither does this.
func projectMigrationsDir(projectDir string) (string, bool) {
	abs, err := filepath.Abs(filepath.Join(projectDir, config.DefaultMigrationsDir))
	if err != nil {
		return "", false
	}
	info, err := os.Stat(abs)
	return abs, err == nil && info.IsDir()
}

// projectMigrationSet is the project's directory as the build embeds it:
// read from DISK, because go:embed embeds the working tree — a dirty cut's
// bytes include files no commit holds.
func projectMigrationSet(projectDir string, prov release.Provenance) (*release.MigrationSet, error) {
	dir, ok := projectMigrationsDir(projectDir)
	if !ok {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	root := migrationgit.RepoRoot(projectDir)
	rel, ok := migrationgit.RepoRelative(root, dir)
	if !ok {
		rel = config.DefaultMigrationsDir
	}
	commit := prov.Commit
	if prov.Dirty || !release.ValidObjectID(commit) {
		commit = ""
	}
	s := release.NewMigrationSet(migrationSourceID(prov.Repo, hostedProjectName(), rel), commit, names)
	return &s, nil
}

// siblingMigrationSet is one external checkout's directory, read from git at
// the commit the build recorded — the checkout on disk may have moved since.
// Only a forge project has one: a sibling with no forge.yaml embeds no
// forge migrations.
func siblingMigrationSet(src *buildtarget.Source) (*release.MigrationSet, string) {
	if src == nil || src.Project || src.Dir == "" {
		return nil, ""
	}
	if _, err := os.Stat(filepath.Join(src.Dir, "forge.yaml")); err != nil {
		return nil, ""
	}
	dir := filepath.Join(src.Dir, config.DefaultMigrationsDir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, ""
	}
	if !release.ValidObjectID(src.Commit) {
		return nil, fmt.Sprintf("%s records no commit, so its migrations were not recorded", src.Dir)
	}
	names, ok := migrationgit.FilesAt(src.Dir, dir, src.Commit)
	if !ok {
		return nil, fmt.Sprintf("%s: commit %s could not be read, so its migrations were not recorded", src.Dir, shortSHA(src.Commit))
	}
	rel, ok := migrationgit.RepoRelative(src.Dir, dir)
	if !ok {
		rel = config.DefaultMigrationsDir
	}
	s := release.NewMigrationSet(migrationSourceID(src.Repo, filepath.Base(src.Dir), rel), src.Commit, names)
	return &s, ""
}

// captureReleaseMigrations is the manifest a release records: the project's
// own directory plus every sibling checkout's the build ran in. Nil when the
// release embeds none. A var so a test states the manifest instead of
// staging repositories.
var captureReleaseMigrations = func(projectDir string, prov release.Provenance, uses []sourceUse) (*release.MigrationManifest, []string, error) {
	var (
		m     release.MigrationManifest
		notes []string
		seen  = map[string]bool{}
	)
	own, err := projectMigrationSet(projectDir, prov)
	if err != nil {
		return nil, nil, err
	}
	if own != nil {
		m.Sets = append(m.Sets, *own)
		seen[own.Source] = true
	}
	for _, use := range uses {
		s, note := siblingMigrationSet(use.src)
		if note != "" {
			notes = append(notes, note)
		}
		if s == nil || seen[s.Source] {
			continue
		}
		seen[s.Source] = true
		m.Sets = append(m.Sets, *s)
	}
	if len(m.Sets) == 0 {
		return nil, notes, nil
	}
	m.Normalize()
	if err := m.Validate(); err != nil {
		return nil, nil, err
	}
	return &m, notes, nil
}

// appliedMigrationHistory is the input the passed-migration guard judges
// against: every release env's promotion history names whose deploy did not
// FAIL, newest first, with the manifest it recorded.
//
// A failed promotion is left out on purpose. A rollout the migrator refused
// applied nothing, and counting its files as applied would wave through the
// very file that refused it. A promotion with no recorded outcome counts as
// applied — the common case for a deploy that never waited — so the guard can
// under-refuse there, never over-refuse; the migrator backstops it.
func appliedMigrationHistory(ctx context.Context, projectDir, env string, ledger envLedger) (applied []release.AppliedManifest, unrecorded []string, ok bool, err error) {
	reader, isReader := ledger.Bindings.(bindingHistoryReader)
	if !isReader || ledger.Releases == nil {
		return nil, nil, false, nil
	}
	seen := map[string]bool{}
	q := historyQuery{Limit: maxHistoryLimit}
	read := 0
	for read < migrationHistoryCap {
		page, err := reader.HistoryPage(ctx, env, q)
		if err != nil {
			return nil, nil, false, fmt.Errorf("read the promotion history of %s (%s): %w", env, ledger.Bindings.Location(), err)
		}
		for _, p := range page.Promotions {
			read++
			if seen[p.Release] || applyOf(projectDir, env, p).Failed {
				continue
			}
			seen[p.Release] = true
			rel, err := ledger.Releases.Get(ctx, p.Release)
			if err != nil {
				return nil, nil, false, fmt.Errorf("read release %s from %s: %w", p.Release, ledger.Releases.Location(), err)
			}
			if rel == nil || rel.Migrations == nil {
				unrecorded = append(unrecorded, p.Release)
				continue
			}
			applied = append(applied, release.AppliedManifest{Release: p.Release, Manifest: *rel.Migrations})
		}
		if page.Next == "" {
			break
		}
		q.Before = page.Next
	}
	return applied, unrecorded, true, nil
}

// releaseRef names one release of one project against one env: what the
// migration guards judge together.
type releaseRef struct {
	projectDir string
	env        string
	version    string
}

// checkPassedMigrations refuses target (the release `version` carries) when
// env's schema has already passed one of its migrations, unless allow.
// prefix is the caller's log prefix ("[build]", "[deploy]").
func checkPassedMigrations(ctx context.Context, out io.Writer, prefix string, rel releaseRef, target *release.MigrationManifest, ledger envLedger, allow bool) error {
	if target == nil {
		return nil
	}
	projectDir, env, version := rel.projectDir, rel.env, rel.version
	applied, unrecorded, ok, err := appliedMigrationHistory(ctx, projectDir, env, ledger)
	if err != nil {
		// Could not look, which is not a finding: the cut and the deploy
		// own their ledger failures, and env's migrator backstops this.
		fmt.Fprintf(out, "%s note: release %s's migrations were not checked against what %s has applied: %v\n", prefix, version, env, err)
		return nil
	}
	if !ok {
		fmt.Fprintf(out, "%s note: the ledger at %s cannot list %s's history, so release %s's migrations were not checked against what %s has applied\n",
			prefix, ledgerLocation(ledger), env, version, env)
		return nil
	}
	if len(unrecorded) > 0 {
		sort.Strings(unrecorded)
		fmt.Fprintf(out, "%s note: %d release(s) %s applied record no migrations (cut before forge recorded them: %s), so the check knows only what the others carried — "+
			"record them with `forge env releases backfill-migrations %s`\n",
			prefix, len(unrecorded), env, strings.Join(unrecorded, ", "), env)
	}
	passed := release.PassedMigrations(*target, applied)
	if len(passed) == 0 {
		return nil
	}
	msg := passedMigrationsMessage(env, version, passed)
	if allow {
		fmt.Fprintf(out, "%s WARNING (--%s): %s\n", prefix, allowPassedMigrationsFlag, msg)
		return nil
	}
	return exitCodeError{code: exitRefused, msg: msg + passedMigrationsRemedy(env)}
}

func passedMigrationsMessage(env, version string, passed []release.PassedMigration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "release %s carries %d migration(s) %s's schema has already passed, so %s's migrator would refuse them at boot and apply nothing:",
		version, len(passed), env, env)
	for _, p := range passed {
		fmt.Fprintf(&b, "\n  %s  (%s) — at or below %d (%s), which %s applied with %s",
			p.File, p.Source, p.Head, p.HeadFile, env, p.HeadRelease)
	}
	return b.String()
}

func passedMigrationsRemedy(env string) string {
	return "\n  The fix gives each file a version newer than the head, and how depends on whether it has merged:" +
		"\n    not yet merged: `forge db migration rebase <file>` in its repository" +
		"\n    already merged (databases created since have run it): delete the file and add the same SQL, re-runnably (IF NOT EXISTS), with `forge db migration new <name>`" +
		fmt.Sprintf("\n  Only if the SQL was already applied to %s's database by hand and recorded in schema_migrations_applied, pass --%s.", env, allowPassedMigrationsFlag)
}

// checkUnmergedMigrations refuses a cut whose own migrations include a file
// the default branch does not hold, unless allow. Unknowable without a
// default branch, and then a note.
func checkUnmergedMigrations(out io.Writer, prefix, projectDir, version string, allow bool) error {
	dir, ok := projectMigrationsDir(projectDir)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	files := release.NewMigrationSet("", "", names).Files
	if len(files) == 0 {
		return nil
	}
	root := migrationgit.RepoRoot(projectDir)
	onMain, ref, ok := migrationgit.DefaultBranchFiles(root, dir)
	if !ok {
		fmt.Fprintf(out, "%s note: no default branch to compare with, so release %s's migrations were not checked for being merged\n", prefix, version)
		return nil
	}
	var unmerged []string
	for _, f := range files {
		if !onMain[f] {
			unmerged = append(unmerged, f)
		}
	}
	if len(unmerged) == 0 {
		return nil
	}
	msg := fmt.Sprintf("release %s carries %d migration(s) that are not on %s: %s — "+
		"an env that applies them gets a schema ahead of the default branch, and the next migration main merges can land below it while passing every merge-time check",
		version, len(unmerged), ref, strings.Join(unmerged, ", "))
	if allow {
		fmt.Fprintf(out, "%s WARNING (--%s): %s\n", prefix, allowUnmergedMigrationsFlag, msg)
		return nil
	}
	return exitCodeError{code: exitRefused, msg: msg +
		fmt.Sprintf("\n  Merge them first and cut from a commit that carries them (a hotfix branch can cherry-pick the merged file), "+
			"or pass --%s for a hotfix that cannot wait — the passed-migration check still guards the next release.", allowUnmergedMigrationsFlag)}
}

// releaseMigrationGuard is a release build's two migration checks, run
// against the sources given: the checkouts on disk before the build, the ones
// the build state recorded at the cut. It returns the manifest the release
// records.
//
// THE UNMERGED CHECK APPLIES ONLY TO AN ENV ON A CONTROL PLANE. An env on
// this machine's ledger is this machine's: a feature branch deployed to it,
// new migration and all, is the dev loop, and no one else's main has to
// respect its schema. An env on a control plane is shared — prod, staging —
// and its schema is what every later merge to main must stay above.
func releaseMigrationGuard(ctx context.Context, out io.Writer, rel releaseRef, prov release.Provenance, uses []sourceUse, ledger envLedger, opts buildOptions) (*release.MigrationManifest, error) {
	projectDir, version := rel.projectDir, rel.version
	m, notes, err := captureReleaseMigrations(projectDir, prov, uses)
	if err != nil {
		return nil, fmt.Errorf("--release %s: record its migrations: %w", version, err)
	}
	for _, n := range notes {
		fmt.Fprintf(out, "[build]   note: %s\n", n)
	}
	if ledger.Hosted {
		if err := checkUnmergedMigrations(out, "[build]  ", projectDir, version, opts.allowUnmergedMigrations); err != nil {
			return nil, err
		}
	}
	if err := checkPassedMigrations(ctx, out, "[build]  ", rel, m, ledger, opts.allowPassedMigrations); err != nil {
		return nil, err
	}
	return m, nil
}

// preflightReleaseMigrations is releaseMigrationGuard before a release-bound
// build, over the checkouts as they are, so a refused release builds and
// pushes nothing. A ledger that cannot be resolved here is left to the cut,
// which needs it anyway and fails loudly.
func preflightReleaseMigrations(ctx context.Context, projectDir string, entities *KCLEntities, opts buildOptions) error {
	if opts.release == "" || opts.env == "" {
		return nil
	}
	ledger, err := ledgerForRendered(ctx, projectDir, opts.env, entities)
	if err != nil {
		return nil //nolint:nilerr // the cut resolves the ledger again and owns that failure
	}
	var uses []sourceUse
	if entities != nil {
		if uses, err = liveShellSources(ctx, projectDir, entities); err != nil {
			return fmt.Errorf("--release %s: %w", opts.release, err)
		}
	}
	_, err = releaseMigrationGuard(ctx, os.Stdout, releaseRef{projectDir, opts.env, opts.release}, captureReleaseProvenance(ctx, projectDir), uses, ledger, opts)
	return err
}

// checkDeployMigrations is the guard at `forge env deploy <env> <version>`:
// the release as its cut recorded it, against env's history.
func checkDeployMigrations(ctx context.Context, out io.Writer, projectDir, env, version string, ledger envLedger, allow bool) error {
	if ledger.Releases == nil {
		return nil
	}
	rel, err := ledger.Releases.Get(ctx, version)
	if err != nil || rel == nil {
		// The promote reads the release itself and owns both failures.
		return nil //nolint:nilerr // see above
	}
	if rel.Migrations == nil {
		fmt.Fprintf(out, "[deploy] note: release %s records no migrations (cut before forge recorded them), so its migrations were not checked against what %s has applied\n", version, env)
		return nil
	}
	return checkPassedMigrations(ctx, out, "[deploy]", releaseRef{projectDir, env, version}, rel.Migrations, ledger, allow)
}

func ledgerLocation(l envLedger) string {
	if l.Bindings != nil {
		return l.Bindings.Location()
	}
	return "this env's ledger"
}
