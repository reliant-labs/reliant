package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func newUpgradeCmd() *cobra.Command {
	var (
		check     bool
		force     bool
		toVersion string
		dryRun    bool
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "upgrade [--force <path>...]",
		Short: "Update frozen project files from latest Forge templates",
		Long: `Detect template drift on frozen files (files written at 'forge project new' time but
not updated by 'forge generate') and apply updates from newer Forge templates.

Files that haven't been modified by the user are updated automatically.
User-modified files show a diff and are skipped unless --force is used.

--force is per-path. Naming paths after --force restricts the overwrite to
exactly those files, so you can adopt one new template without stomping every
other file you have customized. Bare --force keeps its whole-project meaning.

A file you have deliberately taken over is better claimed than defended:
'forge project disown <path> --reason ...' records the ownership transfer, and
upgrade then skips the file permanently — even under --force.

Scaffold-once files (each frontend's shared mechanism modules under src/lib and
src/hooks, plus the .github starters) get their own section and are NEVER written
implicitly. They are yours from birth; upgrade only reports what their templates
gained, how far behind you are in lines, and how many lines in your copy no
template accounts for. Adopt one by naming it:
'forge project upgrade --force <path>'. Bare --force does not reach them.

Upgrade synchronizes every project module that requires Forge using
GOWORK=off go get and go mod tidy, preserving module replacements and go.work.
If either command fails, its module-file changes are rolled back and upgrade
stops before changing templates or forge.yaml, with exact recovery commands.
After a successful upgrade the project's forge_version field in forge.yaml
is bumped to the current binary version (or to --to when provided). Migrations
whose declared version range AND detection script both match this project are
surfaced first, so the LLM running upgrade can follow them step-by-step.

--check SUMMARIZES; --check <path> DETAILS. The report names each file that
differs and sizes the difference in lines — it does NOT print diffs inline,
because on a real project that is thousands of lines nobody reads. Files are
grouped and ranked by what adopting them costs: the ones with no local edits
come first, since for those a single --force is the whole job. Name a path
after --check to see that one file's full diff, or pass --all to list every
file the groups summarized.

Examples:
  forge project upgrade                        # Upgrade to latest, run all needed migrations
  forge project upgrade --to 1.5.0             # Upgrade to a specific target version
  forge project upgrade --dry-run              # Show what would change (alias for --check)
  forge project upgrade --check                # Dry-run summary: what differs, and by how much
  forge project upgrade --check buf.yaml       # That one file's full diff
  forge project upgrade --check --all          # Every reported file (still no inline diffs)
  forge project upgrade --force                # Apply all updates, even for user-modified files
  forge project upgrade --force buf.yaml       # Overwrite ONLY buf.yaml; leave other edits alone`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --dry-run is an alias for --check; either toggles dry-run.
			return runUpgradeWithView(check || dryRun, force, all, args, toVersion)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "Dry-run: only show what would change, don't write files")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Alias for --check")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite user-modified files. Name paths after it to overwrite only those")
	cmd.Flags().BoolVar(&all, "all", false, "List every reported file instead of the first few per group")
	cmd.Flags().StringVar(&toVersion, "to", "", "Target forge version (defaults to the current binary version)")

	// `forge project upgrade list` and `forge project upgrade apply <id>` cover the
	// migration-skill surface (see upgrade_migrations.go). They share
	// the upgrade noun with the template-drift upgrader above but are
	// independent: list/apply work even on projects whose forge_version
	// is unpinned, and don't touch any user-owned files.
	attachMigrationSubcommands(cmd)

	return cmd
}

func runUpgrade(check, force bool, forcePaths []string, toVersion string) error {
	return runUpgradeWithView(check, force, false, forcePaths, toVersion)
}

// runUpgradeWithView is runUpgrade plus the two presentation controls.
//
// showAll uncaps every truncated list. Naming paths under --check (rather
// than under --force) selects the DETAIL view for exactly those files: the
// report summarizes, and a named path is how you ask for one file's diff.
// That is the whole shape of the fix — `--check` summarizes, `--check
// <path>` details — and it is why paths mean something different in the
// two modes without ever being ambiguous: --force writes, --check cannot.
func runUpgradeWithView(check, force, showAll bool, forcePaths []string, toVersion string) error {
	return runUpgradeWithGo(check, force, showAll, forcePaths, toVersion, runGoOffWorkspace)
}

func runUpgradeWithGo(check, force, showAll bool, forcePaths []string, toVersion string, run goOffWorkspaceRunner) error {
	configPath, err := findProjectConfigFile()
	if err != nil {
		return err
	}

	store, err := loadProjectStoreFrom(configPath)
	if err != nil {
		return err
	}
	cfg := store.Config()

	projectDir := filepath.Dir(configPath)

	// `--check <path>...` is the detail view: one file's full diff. It
	// short-circuits the whole report, because a reader who named a file
	// is not looking for the summary they just read.
	if check && !force && len(forcePaths) > 0 {
		return runUpgradeDetail(projectDir, cfg, forcePaths)
	}

	selection, err := resolveForceSelection(projectDir, cfg, force, forcePaths)
	if err != nil {
		return err
	}

	// Determine target version. Default to the running binary's version,
	// honour --to when provided. We don't constrain --to to "newer than
	// current" — downgrade is a legitimate (if rare) operation.
	target := strings.TrimSpace(toVersion)
	if target == "" {
		target = buildinfo.Version()
	}

	// The baseline is reported and compared EXACTLY as the project pinned
	// it. A dev build's pseudo-version is real, orderable SemVer (a
	// pre-release of the next patch), so nothing has to be substituted for
	// it to take part in version-gated migration selection — and
	// substituting a different version, as this once did, is what fed
	// projects the migrations of a release they were never on.
	from := store.Meta().EffectiveForgeVersion()

	// Supported-window enforcement (the Istio model). forge supports
	// upgrading from at most supportedUpgradeWindowMinors minor releases
	// back in a single run; anything older is a STAGED upgrade through an
	// intermediate release.
	//
	// The reason is the same one that bounds the migration registry: each
	// migration is written against the shapes of the releases inside the
	// window, so a project further back than that would have its
	// migrations applied to a tree none of their authors ever saw. Making
	// the user stop at an intermediate release means every migration runs
	// against a baseline it was actually written for.
	//
	// This check only applies when both from/to parse cleanly as
	// vMaj.Min(.patch) and span the same major — cross-major upgrades
	// aren't part of the staged chain at all.
	if hop := minorHopDistance(from, target); hop > supportedUpgradeWindowMinors {
		stagedTo := minorPlus(from, supportedUpgradeWindowMinors)
		return cliutil.UserErr(
			fmt.Sprintf("forge project upgrade --to %s", target),
			fmt.Sprintf("%s is %d minor releases behind %s — forge supports upgrading from at most %d minors back in one step",
				describeVersion(from), hop, target, supportedUpgradeWindowMinors),
			"",
			fmt.Sprintf("staged upgrade: run '%s project upgrade --to v%s' first, then re-run '%s project upgrade --to %s'",
				Name(), stagedTo, Name(), target),
		)
	}

	if check {
		fmt.Printf("%s project upgrade --check (dry run): %s → %s\n", Name(), describeVersion(from), describeVersion(target))
	} else {
		fmt.Printf("%s project upgrade: %s → %s\n", Name(), describeVersion(from), describeVersion(target))
	}
	if isPreV01Baseline(from) {
		fmt.Println("  Baseline is pre-v0.1: forge has not declared backwards compatibility yet, so expect template churn.")
	}
	fmt.Println()

	// Surface the migrations this project actually needs before doing
	// destructive work, so the user (or LLM) can decide whether to halt
	// and load them first.
	printApplicableMigrations(projectDir, from)

	// Resolve all module pins before changing templates or config. This phase
	// rolls back its Go-tool writes on failure; --check only reports drift.
	if err := upgradeForgeModules(projectDir, target, check, run); err != nil {
		return err
	}
	if check && cfg.ForgeVersion != target {
		fmt.Printf("  forge.yaml: %s → %s\n", describeVersion(cfg.ForgeVersion), describeVersion(target))
	}

	// Run the release codemods (deterministic AST rewrites) BEFORE the
	// template-update upgrade pass. Codemods rewrite user-owned files
	// (internal/app/lifecycle.go, serve.go) which the template upgrade
	// leaves alone; doing them first keeps the two concerns separate in
	// the logs and means a codemod failure aborts before template files
	// get touched. Under --check they only report.
	codemodReport, err := runUpgradeCodemods(projectDir, from, target, !check)
	if err != nil {
		return err
	}

	// One-time legacy .forge/checksums.json migration (same conversion
	// `forge generate` performs; see generate_legacy_migrate.go). Unlike
	// the pipeline, upgrade has no emitters to side-render against, so
	// unverifiable entries get the unverified-legacy sentinel directly —
	// the next generate's guard names them with the standard remedies.
	// Skipped under --check/--dry-run (read-only contract).
	if !check {
		if err := migrateLegacyChecksums(projectDir); err != nil {
			return err
		}
	}

	results, err := generator.UpgradeSelection(projectDir, cfg, selection, check)
	if err != nil {
		return err
	}

	counts := tallyAndPrintUpgradeResults(results, check, showAll)
	printSkippedPaths(counts, showAll)
	printUserModifiedRemedies(counts, showAll)

	// The scaffold-once tier. Separate pass, separate section, and
	// advisory by contract: these files are the user's from birth, so the
	// run reports what their templates gained and writes nothing unless a
	// path was named after --force. Runs in both modes — a --check that
	// stayed silent about them is how a retried-4xx query client shipped.
	if _, err := runAdvisoryPass(projectDir, cfg, selection, check, showAll); err != nil {
		return err
	}

	// Bump the project's forge_version after a successful, non-dry-run
	// upgrade. We do this last so a partial failure above leaves the
	// existing pin in place rather than silently advancing it.
	//
	// Hard error: silently failing to bump leaves the project pinned to
	// the old version, so the next `forge generate` runs the wrong
	// template set against an already-migrated tree.
	if err := bumpForgeVersion(cfg, configPath, target, check); err != nil {
		return err
	}

	// Write the UPGRADE_NOTES.md at the project root so the user (or an
	// LLM running the upgrade) has a single canonical worklist of
	// auto-applied changes + items needing manual attention. Skip on
	// dry-run — the report would be misleading without the actual
	// rewrites having happened.
	//
	// Hard error: this report is the canonical record of what the codemod
	// rewrote. Silently dropping it strands the user with no account of
	// what changed under them.
	if !check && codemodReport.Changed() {
		if err := writeUpgradeNotes(projectDir, from, target, codemodReport); err != nil {
			return fmt.Errorf("write UPGRADE_NOTES.md: %w", err)
		}
		fmt.Println()
		fmt.Println("📝 UPGRADE_NOTES.md written at the project root.")
		fmt.Println("    Review it for items needing LLM/manual attention, then delete the file once the upgrade lands.")
	}

	return nil
}

// printApplicableMigrations surfaces the migrations this project actually
// needs BEFORE the destructive work, so the user (or LLM) can decide whether
// to halt and load them first.
//
// "Actually needs" is migrationApplies (upgrade_migrations.go) — the same
// decision `forge project upgrade list` makes: version range AND detection
// script, minus anything already recorded in .forge/migrations.json. Silence
// means nothing applies, which is a real answer; a project that exhibits none
// of the old shapes should be told so rather than handed the catalogue.
func printApplicableMigrations(projectRoot, baseline string) {
	metas, err := loadMigrationMetas()
	if err != nil {
		return
	}
	state, err := readMigrationsState(projectRoot)
	if err != nil {
		return
	}
	var rows []pendingMigration
	for _, row := range applicableMigrations(metas, baseline, projectRoot) {
		if _, done := state.Applied[row.Meta.ID]; done {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return
	}
	fmt.Println("📚 Migrations this project still needs:")
	for _, row := range rows {
		fmt.Printf("    - %s\n      %s\n      Load with: %s skill load %s\n",
			row.Meta.SkillPath, row.Meta.Description, Name(), row.Meta.SkillPath)
	}
	fmt.Println()
	fmt.Println("    The deterministic steps (regen, build) run automatically below.")
	fmt.Println("    Load each skill above IN THE ORDER LISTED (oldest release first) and")
	fmt.Println("    follow its 'Migration (manual part)' section — each one assumes the")
	fmt.Println("    release above it has already been migrated.")
	fmt.Println()
}

// describeVersion renders a forge version for human output, labelling
// anything that is not a published tag. A project scaffolded by a local
// checkout pins a Go pseudo-version; that string is honest identity but
// reads like a release, so say which it is at the point it's shown.
func describeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "(unpinned)"
	}
	if buildinfo.IsDevVersion(v) {
		return v + " (dev build)"
	}
	return v
}

// resolveForceSelection turns the --force flag plus any paths named after it
// into the generator's per-file overwrite selection.
//
//	(no --force)                → overwrite nothing the user has touched
//	--force                     → overwrite every user-modified managed file
//	--force <path>...           → overwrite exactly those, leave the rest alone
//
// Paths without --force are refused rather than guessed at: a path list is
// only ever a narrowing of a destructive act, so arming it must be explicit.
// Unknown paths are refused too — silently ignoring a typo would report
// "nothing to force" while the user believes the file was adopted.
//
// The valid set is the Tier-2 managed files PLUS the scaffold-once advisory
// rows (upgrade_advisory.go). Bare --force still means only the Tier-2 set:
// the advisory tier is adoptable one named path at a time and never in bulk,
// because those files are the user's from birth.
func resolveForceSelection(projectDir string, cfg *config.ProjectConfig, force bool, paths []string) (generator.ForceSelection, error) {
	if len(paths) == 0 {
		if force {
			return generator.ForceAll(), nil
		}
		return generator.ForceNone(), nil
	}
	if !force {
		return generator.ForceNone(), cliutil.UserErr(
			fmt.Sprintf("%s project upgrade %s", Name(), strings.Join(paths, " ")),
			"paths select which user-modified files to overwrite, but nothing armed the overwrite",
			"",
			fmt.Sprintf("run '%s project upgrade --force %s' to adopt those files, or drop the paths to see what would change",
				Name(), strings.Join(paths, " ")),
		)
	}

	managed := generator.ManagedPathsFor(cfg)
	advisory, ciNote, err := upgradeAdvisoryFiles(projectDir, cfg)
	if err != nil {
		return generator.ForceNone(), err
	}
	managed = append(managed, generator.AdvisoryPaths(advisory)...)
	known := map[string]bool{}
	for _, p := range managed {
		known[p] = true
	}
	selected := make([]string, 0, len(paths))
	var unknown []string
	for _, raw := range paths {
		rel := filepath.Clean(strings.TrimSpace(raw))
		if !known[rel] {
			unknown = append(unknown, raw)
			continue
		}
		selected = append(selected, rel)
	}
	if len(unknown) > 0 {
		sort.Strings(managed)
		fix := fmt.Sprintf("upgrade only owns these paths in this project:\n    %s", strings.Join(managed, "\n    "))
		if ciNote != "" {
			fix += "\n" + ciNote
		}
		return generator.ForceNone(), cliutil.UserErr(
			fmt.Sprintf("%s project upgrade --force %s", Name(), strings.Join(paths, " ")),
			fmt.Sprintf("not upgrade-managed: %s", strings.Join(unknown, ", ")),
			"",
			fix,
		)
	}
	return generator.ForcePaths(selected...), nil
}

// runUpgradeCodemods runs the release codemods the upgrade crosses
// (deterministic AST rewrites of user-owned Go; upgrade_codemod.go) and
// reports what they rewrote. With write false (--check) it rewrites nothing
// and lists what it would. A codemod failure is wrapped as an actionable user
// error. Run BEFORE the template-update pass so a codemod failure aborts
// before template files get touched.
func runUpgradeCodemods(projectDir, from, target string, write bool) (CodemodReport, error) {
	report, err := runCodemodChain(projectDir, from, target, write)
	if err != nil {
		return CodemodReport{}, cliutil.WrapUserErr(
			fmt.Sprintf("forge project upgrade --to %s", target),
			"codemod chain failed",
			"",
			"fix the offending file (the error names it), then re-run upgrade; each codemod is also `forge fix <name> --dry-run`",
			err)
	}
	if !report.Changed() {
		return report, nil
	}
	if write {
		fmt.Printf("🔧 Applied %d codemod rewrites:\n", len(report.Rewrites))
	} else {
		fmt.Printf("🔧 Would apply %d codemod rewrites:\n", len(report.Rewrites))
	}
	for _, r := range report.Rewrites {
		fmt.Printf("    %s\n", r)
	}
	if write {
		fmt.Println("    Detail in UPGRADE_NOTES.md (written at the end).")
	}
	fmt.Println()
	return report, nil
}

// migrateLegacyChecksums performs the one-time legacy .forge/checksums.json
// migration (the same conversion `forge generate` performs). Unlike the
// pipeline, upgrade has no emitters to side-render against, so unverifiable
// entries get the unverified-legacy sentinel directly — the next generate's
// guard names them with the standard remedies. A missing/unreadable manifest is
// a no-op (nothing to migrate).
func migrateLegacyChecksums(projectDir string) error {
	mcs, lerr := generator.LoadChecksums(projectDir)
	if lerr != nil {
		return nil
	}
	outcome, merr := checksums.MigrateLegacyManifest(projectDir, mcs, legacyMigrationStampable)
	if merr != nil {
		return fmt.Errorf("legacy checksums migration: %w", merr)
	}
	if outcome == nil {
		return nil
	}
	fmt.Printf("\U0001F4DC Migrated off the legacy .forge/checksums.json (%d entries) — generated files are self-certifying now.\n", outcome.Total())
	for _, p := range outcome.Unverified {
		checksums.StampUnverified(projectDir, p)
		fmt.Fprintf(os.Stderr, "   ? %s — matches nothing the legacy manifest recorded; the next `forge generate` will name it (resolve with --force or `forge project disown`)\n", p)
	}
	if serr := generator.SaveChecksums(projectDir, mcs); serr != nil {
		return fmt.Errorf("save .forge ownership state: %w", serr)
	}
	return nil
}

// upgradeCounts tallies the per-file outcomes of a template-upgrade pass.
type upgradeCounts struct {
	updated      int
	userModified int
	upToDate     int
	skipped      int
	// userModifiedPaths are the paths reported as user-modified, in
	// output order — the exact argument list the remedy hints quote back.
	userModifiedPaths []string
	// skippedPaths are the paths upgrade declined to manage this run
	// (disowned, or a legacy layout). Kept so `--all` can name them:
	// "2 skipped" with no way to learn WHICH two is a count the reader
	// can do nothing with.
	skippedPaths []string
	// upToDatePaths are the files that match their template. The
	// summary carries them as a count — they need no action and listing
	// them is what a report does when it has nothing to say — but
	// `--all` names them, so no path the old report printed became
	// unreachable in the new one.
	upToDatePaths []string
	// modifiedRows are the user-modified rows, sized, for the remedy
	// block to rank the same way the listing above it did.
	modifiedRows []driftRow
}

// tallyAndPrintUpgradeResults reports the managed (Tier-2) lane and returns
// the aggregate counts.
//
// What it no longer does is print each file's diff inline. That is the
// single change that took this report from 14,244 lines to something a
// person reads: a drift REPORT's job is to say that a file differs and by
// how much, and the diff itself is what you ask for on ONE file. The
// per-file diff is one `--check <path>` away and the section says so.
//
// Files that are up to date are counted, not listed. "up to date" is the
// answer to a question nobody asked while looking for what changed; the
// headline count carries it.
func tallyAndPrintUpgradeResults(results []generator.UpgradeResult, check, showAll bool) upgradeCounts {
	var c upgradeCounts
	var updatedRows, modifiedRows []driftRow
	for _, r := range results {
		row := driftRow{Path: r.Path, Missing: r.Missing, Local: r.Local, Absent: r.Absent}
		switch r.Status {
		case generator.UpgradeUpToDate:
			c.upToDate++
			c.upToDatePaths = append(c.upToDatePaths, r.Path)
		case generator.UpgradeUpdated:
			c.updated++
			if r.Forced {
				row.Note = forcedNote(check)
			}
			updatedRows = append(updatedRows, row)
		case generator.UpgradeUserModified:
			c.userModified++
			c.userModifiedPaths = append(c.userModifiedPaths, r.Path)
			modifiedRows = append(modifiedRows, row)
		case generator.UpgradeSkipped:
			c.skipped++
			c.skippedPaths = append(c.skippedPaths, r.Path)
		}
	}
	c.modifiedRows = modifiedRows

	printCountsLine(
		countPart(c.updated, updatedCountLabel(check)),
		countPart(c.userModified, "with local edits (skipped)"),
		countPart(c.upToDate, "up to date"),
		countPart(c.skipped, "not upgrade-managed here"),
	)

	if len(updatedRows) > 0 {
		// Files being ADDED are separated from files being refreshed:
		// one creates something that was not there, the other changes
		// something that was, and a reader skimming for "what is about
		// to happen to my tree" wants those apart.
		var adds, refreshes []driftRow
		for _, row := range updatedRows {
			if row.Absent {
				adds = append(adds, row)
				continue
			}
			refreshes = append(refreshes, row)
		}
		sortDriftRows(adds)
		sortDriftRows(refreshes)
		printDriftGroup(addedTitle(check), adds, showAll)
		printDriftGroup(updatedTitle(check), refreshes, showAll)
	}
	if len(modifiedRows) > 0 {
		cheap, merge, absent := groupDriftRows(modifiedRows)
		// Cheap first: these are the rows where `--force <path>` is a
		// complete answer, and burying them under the merges is the
		// defect this report exists to fix.
		//
		// "Cheap" here means the line comparison found nothing in the
		// file the template cannot account for. These files still
		// reach this lane as user-modified — an unverifiable marker,
		// or provenance predating markers entirely — so the heading
		// says the file is unproven rather than claiming it is
		// unedited. Adopting one is still the cheapest move available.
		printDriftGroup("Cheap adopts (nothing here the template lacks):", cheap, showAll)
		printDriftGroup("Your lines and the template's (merge by hand):", merge, showAll)
		printDriftGroup("Shipped by the template, absent here:", absent, showAll)
	}
	return c
}

// updatedCountLabel / updatedTitle keep the dry-run conditional in one
// place: the same rows are either a report of what WOULD happen or a
// record of what did.
func updatedCountLabel(check bool) string {
	if check {
		return "would update"
	}
	return "updated"
}

func updatedTitle(check bool) string {
	if check {
		return "Would update (no local edits):"
	}
	return "Updated:"
}

// addedTitle names the pure-add group: a managed file the template ships
// that this project does not have yet.
func addedTitle(check bool) string {
	if check {
		return "Would add (not in this project yet):"
	}
	return "Added:"
}

// forcedNote marks the rows whose adoption discarded user edits, so that
// fact never rides only on a flag the reader has to remember passing.
func forcedNote(check bool) string {
	if check {
		return "--force: would discard your edits"
	}
	return "--force: your edits discarded"
}

// printUserModifiedRemedies names both ways out of a user-modified file, at
// the moment the user is looking at the diff.
//
// The two are not interchangeable. --force <path> adopts the new template and
// discards your edits, which is right when the edit was incidental or the
// file's provenance was simply unknown. disown is the durable answer when you
// meant to take the file over: it records the ownership transfer, and upgrade
// then skips the file forever — including under a later bare --force. Until
// someone runs it, "I edited a frozen file" is an implicit state that only
// survives as long as nobody forces.
//
// Both are printed and neither is applied: a user-modified verdict is not
// evidence of intent (it also fires on files whose provenance predates the
// self-certifying marker), and disown is a one-way door.
//
// The adopt command is emitted ONE PATH PER LINE. It used to be a single
// --force naming every user-modified path in sequence, which on a real
// project meant 34 paths and several hundred characters wrapped across the
// terminal — copy-pasteable in the sense that a loaded gun is portable.
// Per-line, each command is short, readable, and adopts exactly one file,
// which is the granularity the rest of this command already works at.
//
// The cheap rows lead, because those are the ones where running the
// printed command is the whole job.
func printUserModifiedRemedies(c upgradeCounts, showAll bool) {
	paths := c.userModifiedPaths
	if len(paths) == 0 {
		return
	}
	cheap, merge, absent := groupDriftRows(c.modifiedRows)

	fmt.Println()
	fmt.Printf("  Two ways to resolve the %d file(s) with local edits:\n", len(paths))
	if len(cheap) > 0 || len(absent) > 0 {
		fmt.Println("  Adopt the current template for one file (its contents are replaced):")
		printAdoptCommands(append(append([]driftRow{}, cheap...), absent...), showAll)
	}
	if len(merge) > 0 {
		fmt.Printf("  %d of them %s lines of your own — adopting discards those:\n",
			len(merge), plural(len(merge), "carries", "carry"))
		printAdoptCommands(merge, showAll)
	}
	fmt.Println("  Or claim a file as yours — upgrade never touches it again, even with --force:")
	fmt.Printf("    %s project disown %s --reason \"<what the template can't express>\"\n", Name(), paths[0])
	printDetailPointer()
}

// printSkippedPaths names the files upgrade declined to manage, under
// --all only.
//
// They are a real part of the report — a disowned file being skipped is
// the disown working — but they need no action, so the summary carries
// them as a count and --all names them. Nothing is silently dropped.
func printSkippedPaths(c upgradeCounts, showAll bool) {
	if !showAll {
		return
	}
	printNamedPathList("Up to date with the current template:", c.upToDatePaths)
	printNamedPathList("Not upgrade-managed in this project (disowned, or a legacy layout):", c.skippedPaths)
}

// printNamedPathList prints a titled, indented list of paths, or nothing.
func printNamedPathList(title string, paths []string) {
	if len(paths) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s\n", title)
	for _, p := range paths {
		fmt.Printf("    %s\n", p)
	}
}

// bumpForgeVersion pins the project's forge_version after a successful,
// non-dry-run upgrade. Done last so a partial failure upstream leaves the
// existing pin in place rather than silently advancing it. Skipped under
// --check and for the dev/(devel)/empty sentinel targets. A write failure is a
// hard error — a silent no-bump strands the project pinned to the old version,
// so the next `forge generate` runs the wrong template set.
//
// The write is SURGICAL — one scalar, in place. It used to marshal the whole
// config struct back over forge.yaml, which changed the one field and
// silently destroyed everything a Go struct cannot hold: on forge's own
// manifest that meant 84 lines and 40 comment lines down to 21 lines and
// none, with the ci: and lint: blocks gone and features: reduced to a single
// flag. Semantics survived, so nothing failed and nobody was told. forge.yaml
// is a file the user writes in; a one-field update has to read like one.
func bumpForgeVersion(cfg *config.ProjectConfig, configPath, target string, check bool) error {
	if check || target == "" || target == "dev" || target == "(devel)" {
		return nil
	}
	if cfg.ForgeVersion == target {
		return nil
	}
	cfg.ForgeVersion = target
	if err := generator.SetProjectConfigScalar(configPath, "forge_version", target); err != nil {
		return fmt.Errorf("bump forge_version in forge.yaml: %w", err)
	}
	fmt.Printf("\nforge_version → %s (forge.yaml updated)\n", target)
	return nil
}

// isPreV01Baseline reports whether v sits below v0.1.0 — the line forge has
// not crossed yet, and therefore the line before which no backwards-
// compatibility promise exists. Purely an ordering question, answered by
// SemVer precedence, so every shape a real pin takes lands correctly:
//
//	""                                    unpinned          → pre-v0.1
//	"0.0.0"                               unpinned sentinel → pre-v0.1
//	"v0.0.3"                              released patch    → pre-v0.1
//	"v0.0.4-0.20260724212501-dfb…+dirty"  dev build         → pre-v0.1
//	"dev"                                 unorderable       → pre-v0.1
//	"v0.1.0"                              first compat tag  → NOT pre-v0.1
//
// A dev build's pseudo-version is a PRE-RELEASE of the next patch, so it
// orders below that patch and far below v0.1.0 without any special casing:
// nothing here has to know a version came from a local checkout.
func isPreV01Baseline(v string) bool {
	key := semverKey(v)
	if key == "" {
		// Unorderable (the "dev" sentinel, a malformed pin). Forge has
		// never released v0.1.0, so anything we cannot place is below it.
		return true
	}
	return semver.Compare(key, "v0.1.0") < 0
}

// minorPlus returns the version n minor releases after v (e.g.
// minorPlus("0.1", 2) → "0.3", minorPlus("v1.4.3", 2) → "1.6"). When v
// can't be parsed cleanly we fall back to the input string — the
// caller's error message is still informative even with the fallback.
//
// Used by the supported-window guard to name the intermediate release a
// too-old project should stage through: landing exactly at the far edge
// of the window is the largest step that is still supported, so it is
// the fewest stops to current.
func minorPlus(v string, n int) string {
	maj, minor, ok := splitMinor(v)
	if !ok {
		return v
	}
	return fmt.Sprintf("%d.%d", maj, minor+n)
}

// splitLines splits a string into lines, handling both \n and \r\n.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
