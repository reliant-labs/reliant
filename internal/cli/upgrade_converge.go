package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// upgrade_converge.go — `forge project upgrade` as the one verb that takes a
// project from its old forge pin to the new one with every mechanical fix
// applied. The phases run in this order, and the order is the contract:
//
//	modules              go get/tidy the forge requirement of every module
//	codemods             the codemods keyed to releases the range crosses
//	templates            frozen-file drift (and the scaffold-once advisories)
//	pin                  forge.yaml forge_version
//	playbooks            each crossed playbook's declared `automation:` steps
//	vendored-contracts   re-vendor a declared contract that lags
//	generate             forge generate
//	locks                lockfiles that disagree with their package.json
//	lint                 forge lint --json: what is left for a human
//
// generate precedes locks because generate edits dependency ranges; lint is
// last because it judges everything above. A project that ends with no
// needs_human item and exit 0 is one a pin bot may merge.

// upgradeSchema names the --json document. Bump it on a breaking change.
const upgradeSchema = "forge-project-upgrade/v1"

// The phases a caller may skip with --skip. modules, templates and pin always
// run: they are the upgrade. codemods and playbooks are skippable so a big
// rewrite can be split into its own PR: codemods omits the registry codemods the
// range crosses; playbooks omits the codemods a playbook automates (connect-errors,
// composition-graph) along with the playbook reporting.
var skippablePhases = []string{"codemods", "playbooks", "vendored-contracts", "generate", "locks", "lint"}

// upgradeOptions is one converge run's input.
type upgradeOptions struct {
	check, force, showAll, allowDirty bool
	forcePaths                        []string
	to                                string
	skip                              map[string]bool
	// goRun, steps, metas and lookup are seams: nil is the real thing.
	goRun  goOffWorkspaceRunner
	steps  *upgradeSteps
	metas  []migrationMeta
	lookup func(string) (codemod.Codemod, bool)
}

// upgradeSteps are the follow-up phases that run other tools.
type upgradeSteps struct {
	// generate runs `forge generate` in the project.
	generate func(dir string) error
	// lint runs `forge lint --json` and returns its stdout, whatever its
	// exit status: a failing lint prints the same document.
	lint func(dir string) ([]byte, error)
	// command runs one declared re-vendor argv from dir with env added.
	command func(dir string, env, argv []string) error
}

// Seams for the command-level test, which cannot reach the real tools.
var (
	upgradeStepsOverride    *upgradeSteps
	upgradeGoRunnerOverride goOffWorkspaceRunner
	upgradeMetasOverride    []migrationMeta
)

// ── the report ───────────────────────────────────────────────────────────────

// upgradeReport is the --json document, and the state the run accumulates.
// Every list is non-nil so a consumer indexes it without a nil check.
type upgradeReport struct {
	jsonEnvelope
	Schema string `json:"schema"`
	// Mode is "apply", or "check" for a dry run that writes nothing.
	Mode string `json:"mode"`
	From string `json:"from"`
	To   string `json:"to"`
	// Converged is true exactly when the run applied everything and nothing
	// is left for a person: OK, with an empty NeedsHuman.
	Converged bool                 `json:"converged"`
	Phases    []upgradePhase       `json:"phases"`
	Codemods  []upgradeCodemodRun  `json:"codemods"`
	Locks     []upgradeLockRun     `json:"locks"`
	Playbooks []upgradePlaybookRun `json:"playbooks"`
	Vendored  []upgradeVendoredRun `json:"vendored_contracts"`
	// NeedsHuman is what is left after every mechanical fix: each item names
	// where, why, and the command or skill that clears it.
	NeedsHuman []upgradeHumanItem `json:"needs_human"`
	// Upcoming are lint findings of rules still in their warn-first grace:
	// not gating this pin, gating from GatesFrom.
	Upcoming []upgradeUpcoming `json:"upcoming"`
	// StartsGating are the rules whose warn-first grace this pin bump ends:
	// they warned under From and gate under To (forge#2230).
	StartsGating []upgradeStartsGating `json:"starts_gating"`
	// TemplateDrift are frozen files you modified that a newer template
	// changed; upgrade never overwrites them without --force.
	TemplateDrift []string `json:"template_drift"`
	// TemplateDriftRows are every file both template lanes report and do
	// not write — upgrade-managed ("managed") and scaffold-once — sized,
	// with whether the range changed its template (upgrade_drift_range.go).
	// TemplateDriftHidden is how many of them the text did not list, and
	// TemplateDriftRangeNote why changed_in_range is null for all of them,
	// when the pinned forge's templates could not be read.
	TemplateDriftRows      []upgradeDriftRow `json:"template_drift_rows"`
	TemplateDriftHidden    int               `json:"template_drift_hidden"`
	TemplateDriftRangeNote string            `json:"template_drift_range_note,omitempty"`
	// Offers are scaffold-once files this project never had whose
	// templates are not new across the range: upgrade did not add them,
	// and Command does.
	Offers []upgradeOffer `json:"offers"`
	// Crossed is what forge from..to brings: one entry per release tag the
	// bump crosses, then the untagged commits up to a pseudo-version
	// target, each with its changelog entries and the instructions its
	// PRs address to the agent running the upgrade (upgrade_crossed.go).
	Crossed []upgradeCrossed `json:"crossed"`
	// GeneratedChanges are the generated files generate changed, by
	// generator, with the range's notes on each (upgrade_generated.go).
	// Under --check, the range's notes alone, planned.
	GeneratedChanges []upgradeGeneratedChange `json:"generated_changes"`
	// GeneratedHint is said when generated output changes: tests that pin
	// it may need updating.
	GeneratedHint string `json:"generated_hint,omitempty"`
}

// upgradeOffer is one scaffold-once file upgrade offers instead of writing.
type upgradeOffer struct {
	Path string `json:"path"`
	// Since is the release whose templates first shipped it.
	Since   string `json:"since,omitempty"`
	Command string `json:"command"`
}

type upgradePhase struct {
	Name string `json:"name"`
	// Status is done, skipped, planned (--check) or failed.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// DurationMS is the phase's wall time. A phase --skip left out did not
	// run and records 0; one that ran and found nothing to do under the
	// mode (generate and lint under --check) records the time it took to
	// decide that.
	DurationMS int64 `json:"duration_ms"`
}

type upgradeCodemodRun struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Source is "upgrade" (runs on every crossing) or "playbook" (a
	// playbook's automation step).
	Source   string   `json:"source"`
	Rewrites []string `json:"rewrites"`
	Next     []string `json:"next"`
	Blocked  []string `json:"blocked"`
}

type upgradeLockRun struct {
	Path    string   `json:"path"`
	Runner  string   `json:"runner"`
	Details []string `json:"details"`
	// Pending: --check found it out of step and changed nothing.
	Pending   bool `json:"pending"`
	Refreshed bool `json:"refreshed"`
}

type upgradePlaybookRun struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Automation  []string `json:"automation"`
	// Resolved: the playbook's detection no longer matches once its
	// automation ran. A playbook with no automation is never resolved here.
	Resolved bool `json:"resolved"`
	// Applies is whether this project needs the playbook: its release is
	// in the range, it is not recorded as applied, and its detection
	// matched before any phase changed the tree. Every playbook of a
	// release after the old pin is listed — those that do not apply with
	// Reason — so "none exist" and "checked, none apply" read differently.
	Applies bool `json:"applies"`
	// Reason says why a playbook does not apply; empty when it does.
	Reason    string `json:"reason,omitempty"`
	Version   string `json:"version,omitempty"`
	SkillPath string `json:"skill_path,omitempty"`
	Detection string `json:"detection,omitempty"`
}

// playbookEval is one playbook judged against this run's range and project.
type playbookEval struct {
	pendingMigration
	applies bool
	reason  string
}

type upgradeVendoredRun struct {
	Name string `json:"name"`
	// Status is the report's: in-sync, lag; or no-report, unrecognized.
	Status     string `json:"status"`
	Revendored bool   `json:"revendored"`
}

type upgradeHumanItem struct {
	// Kind is lint-error, playbook, codemod-blocked, codemod-skipped,
	// lock-drift or vendored-contract.
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Rule    string `json:"rule,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	// Context is the tool output that belongs to this item: the source
	// snippet golangci-lint prints under an issue, the diff a formatter
	// would apply. Lines, in order; omitted when there is none.
	Context []string `json:"context,omitempty"`
}

type upgradeUpcoming struct {
	Rule         string `json:"rule"`
	Count        int    `json:"count"`
	IntroducedIn string `json:"introduced_in"`
	GatesFrom    string `json:"gates_from"`
	// GatesAfter is set when the pin jumped the rule's warn-first window:
	// it gates from the next pin after this one, not from GatesFrom.
	GatesAfter string   `json:"gates_after,omitempty"`
	Fix        string   `json:"fix,omitempty"`
	Examples   []string `json:"examples"`
}

func newUpgradeReport(opts upgradeOptions) *upgradeReport {
	mode := "apply"
	if opts.check {
		mode = "check"
	}
	return &upgradeReport{
		Schema: upgradeSchema, Mode: mode,
		Phases: []upgradePhase{}, Codemods: []upgradeCodemodRun{}, Locks: []upgradeLockRun{},
		Playbooks: []upgradePlaybookRun{}, Vendored: []upgradeVendoredRun{},
		NeedsHuman: []upgradeHumanItem{}, Upcoming: []upgradeUpcoming{}, StartsGating: []upgradeStartsGating{},
		TemplateDrift: []string{},
		Offers:        []upgradeOffer{},
		Crossed:       []upgradeCrossed{}, GeneratedChanges: []upgradeGeneratedChange{},
	}
}

// upgradeStartsGating is one rule that gates from this pin on.
type upgradeStartsGating struct {
	Rule         string `json:"rule"`
	IntroducedIn string `json:"introduced_in"`
	GatesFrom    string `json:"gates_from"`
	Fix          string `json:"fix,omitempty"`
	// Findings is how many of the rule's findings the lint phase reported
	// as errors under the new pin; absent when lint did not run.
	Findings *int `json:"findings,omitempty"`
}

// ── the run ──────────────────────────────────────────────────────────────────

type upgradeRun struct {
	opts         upgradeOptions
	rep          *upgradeReport
	out          io.Writer
	projectDir   string
	configPath   string
	cfg          *config.ProjectConfig
	from, target string
	check        bool
	startedClean bool
	steps        *upgradeSteps
	lookup       func(string) (codemod.Codemod, bool)
	// applicable are the playbooks the range crosses that this project still
	// exhibits, evaluated BEFORE any phase changes the tree.
	applicable []pendingMigration
	// evaluated is every playbook of a release after the old pin, applying
	// or not, oldest release first: applicable is its applying subset.
	evaluated []playbookEval
	// ran are the codemods already run, by name, so a playbook that names
	// one an Upgrade crossing ran does not run it twice.
	ran map[string]bool
}

// runUpgradeConverge is the whole `forge project upgrade`. It always returns a
// report; the error, when set, is what the process exits with.
func runUpgradeConverge(opts upgradeOptions) (*upgradeReport, error) {
	rep := newUpgradeReport(opts)
	rep.TemplateDriftRows = []upgradeDriftRow{}
	err := convergeUpgrade(opts, rep)
	rep.stamp(err)
	rep.Converged = err == nil && !opts.check && len(rep.NeedsHuman) == 0
	return rep, err
}

func convergeUpgrade(opts upgradeOptions, rep *upgradeReport) error {
	r, err := newUpgradeRun(opts, rep)
	if err != nil {
		return err
	}
	if c := crossedFor(r.from, r.target); c != nil {
		rep.Crossed = c
	}
	r.noteGenerated(nil)
	// The pin moves in "pin"; generate and lint then judge the project under
	// the forge it now pins.
	phases := []struct {
		name string
		run  func() error
	}{
		{"modules", r.phaseModules},
		{"codemods", r.phaseCodemods},
		{"templates", r.phaseTemplates},
		{"pin", r.phasePin},
		{"playbooks", r.phasePlaybooks},
		{"vendored-contracts", r.phaseVendored},
		{"generate", r.phaseGenerate},
		{"locks", r.phaseLocks},
		{"lint", r.phaseLint},
	}
	for _, p := range phases {
		if err := r.step(p.name, p.run); err != nil {
			return err
		}
	}
	if len(rep.NeedsHuman) > 0 && !opts.check {
		// Exit 9 is the contract's "needs a person", not a failure: it is
		// printed without "Error:" (exitcode.Line).
		return &exitCodeError{code: exitNeedsHuman, verdict: true, msg: needsPersonMessage(len(rep.NeedsHuman), false)}
	}
	return nil
}

func newUpgradeRun(opts upgradeOptions, rep *upgradeReport) (*upgradeRun, error) {
	configPath, err := findProjectConfigFile()
	if err != nil {
		return nil, err
	}
	store, err := loadProjectStoreFrom(configPath)
	if err != nil {
		return nil, err
	}
	r := &upgradeRun{
		opts: opts, rep: rep, out: os.Stdout, configPath: configPath,
		projectDir: filepath.Dir(configPath), cfg: store.Config(),
		check: opts.check, steps: opts.steps, lookup: opts.lookup, ran: map[string]bool{},
	}
	if r.steps == nil {
		r.steps = realUpgradeSteps(os.Stdout)
	}
	if r.lookup == nil {
		r.lookup = codemod.Lookup
	}
	r.target = strings.TrimSpace(opts.to)
	if r.target == "" {
		r.target = buildinfo.Version()
	}
	r.from = store.Meta().EffectiveForgeVersion()
	rep.From, rep.To = r.from, r.target
	for name := range opts.skip {
		if !containsString(skippablePhases, name) {
			return nil, cliutil.UserErr(Name()+" project upgrade --skip",
				fmt.Sprintf("%q is not a phase that can be skipped", name), "",
				"skippable phases: "+strings.Join(skippablePhases, ", "))
		}
	}
	if hop := minorHopDistance(r.from, r.target); hop > supportedUpgradeWindowMinors {
		stagedTo := minorPlus(r.from, supportedUpgradeWindowMinors)
		return nil, cliutil.UserErr(
			fmt.Sprintf("forge project upgrade --to %s", r.target),
			fmt.Sprintf("%s is %d minor releases behind %s — forge supports upgrading from at most %d minors back in one step",
				describeVersion(r.from), hop, r.target, supportedUpgradeWindowMinors),
			"",
			fmt.Sprintf("staged upgrade: run '%s project upgrade --to v%s' first, then re-run '%s project upgrade --to %s'",
				Name(), stagedTo, Name(), r.target),
		)
	}
	r.startedClean = opts.allowDirty || !upgradeTreeDirty(r.projectDir)
	// Detection runs once per playbook, here, before any phase changes the
	// tree; the banner and the playbooks phase both read the verdicts.
	r.evaluated = r.evaluatePlaybooks()
	for _, ev := range r.evaluated {
		if ev.applies {
			r.applicable = append(r.applicable, ev.pendingMigration)
		}
	}
	r.banner()
	return r, nil
}

func (r *upgradeRun) banner() {
	if r.check {
		fmt.Fprintf(r.out, "%s project upgrade --check (dry run): %s → %s\n", Name(), describeVersion(r.from), describeVersion(r.target))
	} else {
		fmt.Fprintf(r.out, "%s project upgrade: %s → %s\n", Name(), describeVersion(r.from), describeVersion(r.target))
	}
	fmt.Fprintln(r.out)
	printPlaybookVerdicts(r.out, r.evaluated)
	printMigrationWorklist(r.out, r.applicable)
}

// step runs one phase and records its status and wall time. A skipped phase
// is recorded and not run (duration 0); a failed one stops the run with its
// error.
func (r *upgradeRun) step(name string, run func() error) error {
	if r.opts.skip[name] {
		r.rep.Phases = append(r.rep.Phases, upgradePhase{Name: name, Status: "skipped", Detail: r.skipDetail(name)})
		return nil
	}
	start := time.Now()
	defer func() { r.setPhaseDuration(name, time.Since(start)) }()
	err := run()
	switch {
	case err != nil:
		r.setPhase(name, "failed", err.Error())
	case !r.hasPhase(name):
		status := "done"
		if r.check {
			status = "planned"
		}
		r.setPhase(name, status, "")
	}
	return err
}

// setPhaseDuration records how long a phase that ran took.
func (r *upgradeRun) setPhaseDuration(name string, d time.Duration) {
	for i := range r.rep.Phases {
		if r.rep.Phases[i].Name == name {
			r.rep.Phases[i].DurationMS = d.Milliseconds()
			return
		}
	}
}

func (r *upgradeRun) hasPhase(name string) bool {
	for _, p := range r.rep.Phases {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (r *upgradeRun) setPhase(name, status, detail string) {
	for i := range r.rep.Phases {
		if r.rep.Phases[i].Name == name {
			r.rep.Phases[i].Status, r.rep.Phases[i].Detail = status, detail
			return
		}
	}
	r.rep.Phases = append(r.rep.Phases, upgradePhase{Name: name, Status: status, Detail: detail})
}

// skipDetail says why a phase did not run. For the two phases that run codemods
// it also names each one left undone and how to run it: the pin moves in this
// run, so a codemod skipped here is not picked up by the next one, and this
// report is the only record that it was not applied.
func (r *upgradeRun) skipDetail(name string) string {
	detail := "--skip " + name
	var left []string
	switch name {
	case "codemods":
		for _, c := range upgradeCodemods(r.from, r.target) {
			left = appendNew(left, c.Name)
		}
	case "playbooks":
		for _, row := range r.applicable {
			for _, step := range row.Meta.Automation {
				if kind, codemodName, ok := strings.Cut(step, ":"); ok && kind == "fix" {
					left = appendNew(left, codemodName)
				}
			}
		}
	}
	if len(left) == 0 {
		return detail
	}
	for i, n := range left {
		left[i] = fmt.Sprintf("`%s fix %s`", Name(), n)
	}
	return detail + "; not run, run each yourself: " + strings.Join(left, ", ")
}

// notRun records a phase that has nothing to do under the current mode.
func (r *upgradeRun) notRun(name, why string) { r.setPhase(name, "skipped", why) }

func (r *upgradeRun) human(it upgradeHumanItem) { r.rep.NeedsHuman = append(r.rep.NeedsHuman, it) }

// ── modules, codemods, templates, pin ────────────────────────────────────────

func (r *upgradeRun) phaseModules() error {
	run := r.opts.goRun
	if run == nil {
		run = upgradeGoRunnerOverride
	}
	if run == nil {
		run = runGoOffWorkspace
	}
	return upgradeForgeModules(r.projectDir, r.target, r.check, run)
}

func (r *upgradeRun) phaseCodemods() error {
	if r.check && r.cfg.ForgeVersion != r.target {
		fmt.Fprintf(r.out, "  forge.yaml: %s → %s\n", describeVersion(r.cfg.ForgeVersion), describeVersion(r.target))
	}
	runs, err := runCodemodSteps(r.projectDir, r.from, r.target, !r.check)
	if err != nil {
		return cliutil.WrapUserErr(
			fmt.Sprintf("forge project upgrade --to %s", r.target), "codemod chain failed", "",
			"fix the offending file (the error names it), then re-run upgrade; each codemod is also `forge fix <name> --dry-run`", err)
	}
	var merged CodemodReport
	for _, cr := range runs {
		r.ran[cr.Name] = true
		r.rep.Codemods = append(r.rep.Codemods, cr)
		merged.Rewrites = append(merged.Rewrites, cr.Rewrites...)
		merged.Next = append(merged.Next, cr.Next...)
		if len(cr.Blocked) > 0 {
			r.humanBlocked(cr)
		}
	}
	r.printCodemods(merged)
	if !r.check && merged.Changed() {
		if err := writeUpgradeNotes(r.projectDir, r.from, r.target, merged); err != nil {
			return fmt.Errorf("write UPGRADE_NOTES.md: %w", err)
		}
		fmt.Fprintln(r.out, "📝 UPGRADE_NOTES.md written at the project root.")
		fmt.Fprintln(r.out, "    Review it for items needing LLM/manual attention, then delete the file once the upgrade lands.")
	}
	if !r.check {
		if err := migrateLegacyChecksums(r.projectDir); err != nil {
			return err
		}
	}
	return nil
}

func (r *upgradeRun) printCodemods(merged CodemodReport) {
	if !merged.Changed() {
		return
	}
	verb := "Applied"
	if r.check {
		verb = "Would apply"
	}
	fmt.Fprintf(r.out, "🔧 %s %d codemod rewrites:\n", verb, len(merged.Rewrites))
	for _, line := range merged.Rewrites {
		fmt.Fprintf(r.out, "    %s\n", line)
	}
	fmt.Fprintln(r.out)
}

// migrationSkillManualPath names release version's migration skill as the
// manual path, when forge still ships it: a codemod outlives its release's
// playbook once that release ages out of the upgrade window.
func migrationSkillManualPath(version string) string {
	if _, err := templates.ProjectTemplates().Get(path.Join("skills", "forge", migrationSkillsRoot, version, "SKILL.md")); err != nil {
		return ""
	}
	return fmt.Sprintf("; `%s skill load %s/%s` is the manual path", Name(), migrationSkillsRoot, version)
}

func (r *upgradeRun) humanBlocked(cr upgradeCodemodRun) {
	r.human(upgradeHumanItem{
		Kind: "codemod-blocked", ID: cr.Name,
		Message: fmt.Sprintf("forge fix %s wrote nothing: %s", cr.Name, strings.Join(cr.Blocked, "; ")),
		Fix: fmt.Sprintf("clear each item it lists (`%s fix %s --dry-run`), then re-run `%s project upgrade`", Name(), cr.Name, Name()) +
			migrationSkillManualPath(cr.Version),
	})
}

func (r *upgradeRun) phaseTemplates() error {
	selection, err := resolveForceSelection(r.projectDir, r.cfg, r.opts.force, r.opts.forcePaths)
	if err != nil {
		return err
	}
	results, err := generator.UpgradeWith(r.projectDir, r.cfg, generator.UpgradeOptions{
		Force: selection, CheckOnly: r.check, NewSince: r.newAcrossRange,
	})
	if err != nil {
		return err
	}
	rng := newTemplateRange(r.from, r.target)
	view := driftView{showAll: r.opts.showAll, rng: rng}
	counts := tallyAndPrintUpgradeResults(results, r.check, view)
	printSkippedPaths(counts, r.opts.showAll)
	printUserModifiedRemedies(counts, r.opts.showAll)
	r.rep.TemplateDrift = append(r.rep.TemplateDrift, counts.userModifiedPaths...)
	r.rep.Offers = append(r.rep.Offers, counts.offers...)
	// The scaffold-once rows render as the project will be pinned once this
	// run is done. The pin phase moves forge_version after this one, and a
	// template gated on the pin (build-images.yml runs `forge vuln scan` from
	// v1.10.0) would otherwise be compared to, and `--force` would adopt, the
	// form the OLD pin gets.
	atTarget := *r.cfg
	atTarget.ForgeVersion = r.target
	adv, err := runAdvisoryPassView(r.projectDir, &atTarget, selection, r.check, view)
	if err != nil {
		return err
	}
	appendDriftRows(r.rep, "managed", counts.modifiedRows)
	appendDriftRows(r.rep, "scaffold-once", adv.rows)
	r.rep.TemplateDriftHidden = counts.hiddenByRange + adv.hidden
	if rng.dir == "" && len(r.rep.TemplateDriftRows) > 0 {
		r.rep.TemplateDriftRangeNote = rng.why
	}
	r.setPhase("templates", map[bool]string{true: "planned", false: "done"}[r.check],
		fmt.Sprintf("%d updated, %d modified by you (kept), %d up to date", counts.updated, counts.userModified, counts.upToDate))
	return nil
}

// newAcrossRange reports whether a template first shipped in release v is new
// to this project: after its old pin, and reached by the target. An unknown
// baseline makes nothing new — "never had it" cannot be told from "deleted it".
func (r *upgradeRun) newAcrossRange(v string) bool {
	return !baselineIsUnknown(r.from) && baselinePrecedes(r.from, v) && targetReaches(r.target, v)
}

func (r *upgradeRun) phasePin() error {
	for _, g := range lint.RulesStartingToGate(r.cfg, r.from, r.target) {
		r.rep.StartsGating = append(r.rep.StartsGating, upgradeStartsGating{
			Rule: g.Rule, IntroducedIn: g.IntroducedIn, GatesFrom: g.GatesFrom, Fix: g.Fix,
		})
	}
	return bumpForgeVersion(r.cfg, r.configPath, r.target, r.check)
}

// ── playbooks ────────────────────────────────────────────────────────────────

// evaluatePlaybooks judges every playbook of a release after the old pin, in
// release order: the ones this range crosses that the project still exhibits
// and has not recorded as applied apply; the rest say why not. A playbook of a
// release at or before the old pin was crossed by an earlier upgrade and is not
// listed. The applying set is migrationApplies' decision (the one `upgrade
// list` makes) narrowed to the target, so the two cannot disagree.
func (r *upgradeRun) evaluatePlaybooks() []playbookEval {
	metas := r.opts.metas
	if metas == nil {
		var err error
		if metas, err = loadMigrationMetas(); err != nil {
			return nil
		}
	}
	state, err := readMigrationsState(r.projectDir)
	if err != nil {
		return nil
	}
	return evaluatePlaybookMetas(metas, r.from, r.target, state, func(script string) bool {
		return runDetection(r.projectDir, script)
	})
}

// evaluatePlaybookMetas is evaluatePlaybooks over explicit inputs: detect runs
// a detection script in the project, and is called at most once per playbook —
// only for one the range crosses, not recorded as applied, that declares one.
func evaluatePlaybookMetas(metas []migrationMeta, from, target string, state migrationsState, detect func(string) bool) []playbookEval {
	var out []playbookEval
	for _, m := range metas {
		if !baselineIsUnknown(from) && !baselinePrecedes(from, m.Version) {
			continue
		}
		ev := playbookEval{pendingMigration: pendingMigration{Meta: m}}
		switch at, done := state.Applied[m.ID]; {
		case !targetReaches(target, m.Version):
			ev.reason = fmt.Sprintf("its release %s is past the target %s", m.Version, target)
		case done:
			ev.Applied, ev.AppliedAt = true, at
			ev.reason = "recorded as applied at " + at + " (.forge/migrations.json)"
		case strings.TrimSpace(m.Detection) == "":
			ev.reason = "it declares no detection, so it cannot show it applies"
		case detect(m.Detection):
			ev.applies = true
		default:
			ev.reason = "detection did not match: this project does not have the old shape"
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		vi, vj := semverKey(out[i].Meta.Version), semverKey(out[j].Meta.Version)
		if vi != vj && vi != "" && vj != "" {
			return semver.Compare(vi, vj) < 0
		}
		return out[i].Meta.ID < out[j].Meta.ID
	})
	return out
}

// printPlaybookVerdicts prints one line per release whose playbooks the
// range reaches: how many, how many apply, and why each other one does not.
// Silence used to be the only answer when none applied, which an agent could
// not tell from "this range ships no playbooks".
func printPlaybookVerdicts(w io.Writer, evals []playbookEval) {
	if len(evals) == 0 {
		return
	}
	var releases []string
	byRelease := map[string][]playbookEval{}
	for _, ev := range evals {
		v := ev.Meta.Version
		if _, seen := byRelease[v]; !seen {
			releases = append(releases, v)
		}
		byRelease[v] = append(byRelease[v], ev)
	}
	fmt.Fprintln(w, "Migration playbooks of the releases this range crosses:")
	for _, v := range releases {
		rows := byRelease[v]
		apply := 0
		var why []string
		for _, ev := range rows {
			if ev.applies {
				apply++
				continue
			}
			why = append(why, ev.Meta.ID+": "+ev.reason)
		}
		line := fmt.Sprintf("  %s: %d %s, %d %s", v, len(rows), plural(len(rows), "playbook", "playbooks"), apply, plural(apply, "applies", "apply"))
		if len(why) > 0 {
			line += " (" + strings.Join(why, "; ") + ")"
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
}

func (r *upgradeRun) phasePlaybooks() error {
	for _, ev := range r.evaluated {
		if ev.applies {
			continue
		}
		r.rep.Playbooks = append(r.rep.Playbooks, upgradePlaybookRun{
			ID: ev.Meta.ID, Description: shorten(ev.Meta.Description, 200), Automation: nonNil(ev.Meta.Automation),
			Reason: ev.reason, Version: ev.Meta.Version, SkillPath: ev.Meta.SkillPath, Detection: ev.Meta.Detection,
		})
	}
	for _, row := range r.applicable {
		pr := upgradePlaybookRun{ID: row.Meta.ID, Description: shorten(row.Meta.Description, 200), Automation: nonNil(row.Meta.Automation),
			Applies: true, Version: row.Meta.Version, SkillPath: row.Meta.SkillPath, Detection: row.Meta.Detection}
		for _, step := range row.Meta.Automation {
			if err := r.runAutomation(row.Meta, step); err != nil {
				return err
			}
		}
		pr.Resolved = len(row.Meta.Automation) > 0 && !runDetection(r.projectDir, row.Meta.Detection)
		r.rep.Playbooks = append(r.rep.Playbooks, pr)
		if !pr.Resolved && !r.check {
			r.human(upgradeHumanItem{
				Kind: "playbook", ID: row.Meta.ID,
				Message: fmt.Sprintf("the %s migration playbook still applies: %s", row.Meta.ID, shorten(row.Meta.Description, 160)),
				Fix: fmt.Sprintf("%s skill load %s, follow the sections whose \"Use when\" matches, then %s project upgrade apply %s",
					Name(), row.Meta.SkillPath, Name(), row.Meta.ID),
			})
		}
	}
	return nil
}

// runAutomation runs one declared `fix:<codemod>` step of a playbook.
func (r *upgradeRun) runAutomation(meta migrationMeta, step string) error {
	kind, name, ok := strings.Cut(step, ":")
	if !ok || kind != "fix" {
		return fmt.Errorf("playbook %s: automation step %q is not fix:<codemod>", meta.ID, step)
	}
	if r.ran[name] {
		return nil
	}
	c, found := r.lookup(name)
	if !found {
		return fmt.Errorf("playbook %s: automation names codemod %q, which this forge does not register", meta.ID, name)
	}
	if c.CleanTree && !r.startedClean && !r.check {
		r.human(upgradeHumanItem{
			Kind: "codemod-skipped", ID: name,
			Message: fmt.Sprintf("forge fix %s replaces files you own and the tree had uncommitted changes, so upgrade left it alone", name),
			Fix:     fmt.Sprintf("commit the tree, then `%s fix %s` (or re-run `%s project upgrade --allow-dirty`)", Name(), name, Name()),
		})
		return nil
	}
	rep, err := c.Run(r.projectDir, codemod.Options{Write: !r.check})
	if err != nil {
		return fmt.Errorf("codemod %s (%s): %w", c.Name, c.Version, err)
	}
	r.ran[name] = true
	cr := upgradeCodemodRun{Name: c.Name, Version: c.Version, Source: "playbook",
		Rewrites: nonNil(rep.Rewrites), Next: nonNil(rep.Next), Blocked: nonNil(rep.Blocked)}
	r.rep.Codemods = append(r.rep.Codemods, cr)
	if len(rep.Blocked) > 0 {
		r.humanBlocked(cr)
	}
	return nil
}

// ── vendored contracts ───────────────────────────────────────────────────────

func (r *upgradeRun) phaseVendored() error {
	for _, vc := range r.cfg.Upgrade.VendoredContracts {
		run := upgradeVendoredRun{Name: vc.Name}
		status, env, problem := readVendoredReport(r.projectDir, vc)
		run.Status = status
		switch {
		case problem != "":
			run.Status = "unrecognized"
			r.human(upgradeHumanItem{Kind: "vendored-contract", ID: vc.Name, File: vc.Report, Message: problem,
				Fix: "run the project's own vendoring check, or fix the report, then re-run " + Name() + " project upgrade"})
		case status == "lag":
			if !r.check {
				if err := r.revendor(vc, env); err != nil {
					return err
				}
				run.Revendored = true
			}
		}
		r.rep.Vendored = append(r.rep.Vendored, run)
	}
	return nil
}

func (r *upgradeRun) revendor(vc config.VendoredContract, env []string) error {
	fmt.Fprintf(r.out, "📦 vendored contract %q lags its source — re-vendoring\n", vc.Name)
	for _, argv := range vc.Commands {
		if err := r.steps.command(r.projectDir, env, argv); err != nil {
			return fmt.Errorf("re-vendor %q: %s failed: %w", vc.Name, strings.Join(argv, " "), err)
		}
	}
	return nil
}

// readVendoredReport reads a contract's lag report. status is in-sync, lag or
// no-report; problem is set when the report cannot be trusted (another schema,
// a status the hook does not know, unreadable JSON).
func readVendoredReport(projectDir string, vc config.VendoredContract) (status string, env []string, problem string) {
	raw, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(vc.Report)))
	if errors.Is(err, os.ErrNotExist) {
		return "no-report", nil, ""
	}
	if err != nil {
		return "", nil, fmt.Sprintf("%s: %v", vc.Report, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Sprintf("%s is not JSON: %v", vc.Report, err)
	}
	if schema, _ := doc["schema"].(string); schema != vc.Schema {
		return "", nil, fmt.Sprintf("%s declares schema %q, not the %q this project's upgrade.vendored_contracts expects", vc.Report, schema, vc.Schema)
	}
	status, _ = doc["status"].(string)
	switch status {
	case "in-sync":
		return status, nil, ""
	case "lag":
		return status, reportEnv(doc, vc.EnvPath), ""
	}
	return "", nil, fmt.Sprintf("%s has status %q: only \"in-sync\" and \"lag\" are acted on", vc.Report, status)
}

// reportEnv reads the string values of the object at a dotted path in doc as
// KEY=VALUE pairs, sorted.
func reportEnv(doc map[string]any, path string) []string {
	if path == "" {
		return nil
	}
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	m, _ := cur.(map[string]any)
	var out []string
	for k, v := range m {
		if s, ok := v.(string); ok {
			out = append(out, k+"="+s)
		}
	}
	sort.Strings(out)
	return out
}

// ── generate, locks, lint ────────────────────────────────────────────────────

func (r *upgradeRun) phaseGenerate() error {
	if r.check {
		r.notRun("generate", "--check changes nothing")
		return nil
	}
	fmt.Fprintln(r.out, "🔁 regenerating")
	before := snapshotGenerated(r.projectDir)
	if err := r.steps.generate(r.projectDir); err != nil {
		return fmt.Errorf("forge generate failed after the upgrade's rewrites (the pin is already moved; fix the cause and run `%s generate`): %w", Name(), err)
	}
	r.noteGenerated(diffGenerated(before, snapshotGenerated(r.projectDir)))
	return nil
}

// lockTargets are the package.json directories whose lockfile forge keeps in
// step: every frontend it owns a toolchain for, and packages/hooks.
func (r *upgradeRun) lockTargets() []string {
	var dirs []string
	for _, fe := range r.cfg.ToolchainFrontends() {
		if rel, ok := fe.Dir(r.projectDir); ok {
			dirs = append(dirs, rel)
		}
	}
	if st, err := os.Stat(filepath.Join(r.projectDir, "packages", "hooks", "package.json")); err == nil && !st.IsDir() {
		dirs = append(dirs, "packages/hooks")
	}
	return dirs
}

func (r *upgradeRun) phaseLocks() error {
	seen := map[string]bool{}
	for _, rel := range r.lockTargets() {
		manifestDir := filepath.Join(r.projectDir, filepath.FromSlash(rel))
		lock, ok := frontenddeps.FindLock(manifestDir, r.projectDir)
		if !ok || seen[lock.Path()] {
			continue
		}
		seen[lock.Path()] = true
		drift, err := frontenddeps.LockDrift(lock, manifestDir)
		if err != nil {
			return &exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("compare %s/package.json with %s: %v", rel, lock.File, err)}
		}
		if drift == nil {
			continue
		}
		lr := upgradeLockRun{Runner: lock.Runner, Details: nonNil(drift.Details)}
		lr.Path, _ = filepath.Rel(r.projectDir, lock.Path())
		lr.Path = filepath.ToSlash(lr.Path)
		cmd := frontenddeps.LockRefreshCommand(lock, manifestDir)
		switch {
		case r.check:
			lr.Pending = true
		case os.Getenv("FORGE_SKIP_NPM_INSTALL") != "":
			lr.Pending = true
			r.human(upgradeHumanItem{Kind: "lock-drift", File: lr.Path,
				Message: "FORGE_SKIP_NPM_INSTALL is set, so the lockfile was not refreshed: " + strings.Join(drift.Details, "; "), Fix: "run " + cmd})
		default:
			fmt.Fprintf(r.out, "  ↻ %s: refreshing %s\n", rel, lock.File)
			if err := frontenddeps.RefreshLock(context.Background(), lock, manifestDir); err != nil {
				return &exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
					"%s could not be refreshed (a package manager or registry that cannot be reached is retryable): run %s: %v", lr.Path, cmd, err)}
			}
			lr.Refreshed = true
		}
		r.rep.Locks = append(r.rep.Locks, lr)
	}
	return nil
}

// lintDoc is the part of `forge lint --json` upgrade reads.
type lintDoc struct {
	Findings []lintDocFinding `json:"findings"`
}

type lintDocFinding struct {
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
	FixHint  string `json:"fix_hint"`
	Lane     string `json:"lane"`
	Grace    *struct {
		IntroducedIn string `json:"introduced_in"`
		GatesFrom    string `json:"gates_from"`
		GatesAfter   string `json:"gates_after"`
		Fix          string `json:"fix"`
	} `json:"grace"`
}

// golangciSummary matches the summary golangci-lint ends a run with ("1
// issues:", "* goimports: 1"). It counts the issues already listed, so it
// belongs to no one of them.
var golangciSummary = regexp.MustCompile(`^(\d+ issues?[.:]?|\* [A-Za-z0-9_-]+: \d+)$`)

// lintHumanItems turns lint's error findings into one needs_human item per
// issue.
//
// `forge lint --json` keeps every line a sub-tool prints: a line that is not
// `file:line: message` is a finding of rule "external" with no file. Those are
// not issues. They are the output under the issue before them in the same lane
// — golangci-lint's source snippet and caret, a formatter's diff — or the run
// summary, and reading each as an item split one goimports finding into seven
// (reliant#2327: the issue, its snippet, its caret, "1 issues:", "* goimports:
// 1", the format lane's finding for the same file, and its diff). So:
//
//   - a raw line joins the item before it in its lane as Context;
//   - golangci-lint's summary is dropped;
//   - a raw line with no item before it in its lane (a tool that failed
//     before reporting anything anchored) starts an item, and the lines
//     after it join that one;
//   - the format lane's finding for a file golangci-lint's gofmt or
//     goimports also flagged is the same defect: it folds into that item,
//     which keeps the file:line, and brings its diff and fix.
func lintHumanItems(fs []lintDocFinding) []upgradeHumanItem {
	var items []upgradeHumanItem
	open, openLane := -1, ""
	for _, f := range fs {
		if f.File == "" && f.Rule == "external" {
			msg := strings.TrimRight(f.Message, "\n")
			if golangciSummary.MatchString(strings.TrimSpace(msg)) {
				continue
			}
			if open >= 0 && openLane == f.Lane {
				items[open].Context = append(items[open].Context, strings.Split(msg, "\n")...)
				continue
			}
		}
		items = append(items, upgradeHumanItem{Kind: "lint-error", Rule: f.Rule, File: f.File, Line: f.Line,
			Message: strings.TrimRight(f.Message, "\n"), Fix: f.FixHint})
		open, openLane = len(items)-1, f.Lane
	}
	return foldFormatItems(items)
}

// foldFormatItems merges each format-lane item into the gofmt/goimports item
// golangci-lint reported for the same file (lintHumanItems).
func foldFormatItems(items []upgradeHumanItem) []upgradeHumanItem {
	formatter := map[string]int{}
	for i, it := range items {
		if (it.Rule == "gofmt" || it.Rule == "goimports") && it.File != "" {
			if _, seen := formatter[it.File]; !seen {
				formatter[it.File] = i
			}
		}
	}
	folded := map[int]bool{}
	for j, it := range items {
		if i, ok := formatter[it.File]; ok && it.Rule == "format" {
			items[i].Context = append(items[i].Context, it.Context...)
			if items[i].Fix == "" {
				items[i].Fix = it.Fix
			}
			folded[j] = true
		}
	}
	out := make([]upgradeHumanItem, 0, len(items)-len(folded))
	for j, it := range items {
		if !folded[j] {
			out = append(out, it)
		}
	}
	return out
}

func (r *upgradeRun) phaseLint() error {
	if r.check {
		r.notRun("lint", "--check changes nothing")
		return nil
	}
	raw, err := r.steps.lint(r.projectDir)
	if err != nil {
		return &exitCodeError{code: exitUndetermined, msg: "forge lint could not run: " + err.Error()}
	}
	var doc lintDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return &exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("forge lint --json printed no readable verdict (%v): %s", err, shorten(strings.TrimSpace(string(raw)), 200))}
	}
	upcoming := map[string]*upgradeUpcoming{}
	var order []string
	var errs []lintDocFinding
	for _, f := range doc.Findings {
		switch {
		case f.Severity == "error":
			errs = append(errs, f)
		case f.Grace != nil && !(f.Rule == "external" && f.File == ""):
			// A raw sub-tool line (rule "external", no file) is the output
			// under the finding before it — a formatter's diff — not a
			// finding: counted, it made a phantom rule "external" ×30 with
			// empty examples beside forge-proto-format ×30.
			u := upcoming[f.Rule]
			if u == nil {
				u = &upgradeUpcoming{Rule: f.Rule, IntroducedIn: f.Grace.IntroducedIn, GatesFrom: f.Grace.GatesFrom,
					GatesAfter: f.Grace.GatesAfter, Fix: f.Grace.Fix, Examples: []string{}}
				upcoming[f.Rule] = u
				order = append(order, f.Rule)
			}
			u.Count++
			if len(u.Examples) < 3 && f.File != "" {
				u.Examples = append(u.Examples, strings.TrimSuffix(fmt.Sprintf("%s:%d", f.File, f.Line), ":0"))
			}
		}
	}
	countStartsGating(r.rep.StartsGating, errs)
	for _, it := range lintHumanItems(errs) {
		r.human(it)
	}
	for _, rule := range order {
		r.rep.Upcoming = append(r.rep.Upcoming, *upcoming[rule])
	}
	return nil
}

// countStartsGating records, on each rule this pin starts gating, how many of
// lint's error findings are that rule's.
func countStartsGating(rows []upgradeStartsGating, errs []lintDocFinding) {
	for i := range rows {
		n := 0
		for _, f := range errs {
			if f.Rule == rows[i].Rule {
				n++
			}
		}
		rows[i].Findings = &n
	}
}

// ── printing ─────────────────────────────────────────────────────────────────

// noteGenerated records generated_changes: the files generate changed (nil
// before it runs) grouped by generator, with the crossed range's notes.
func (r *upgradeRun) noteGenerated(changed []string) {
	r.rep.GeneratedChanges = groupGenerated(changed, generatedNotes(r.rep.Crossed), r.check || changed == nil)
	r.rep.GeneratedHint = ""
	if len(r.rep.GeneratedChanges) > 0 {
		r.rep.GeneratedHint = generatedHint
	}
}

// printUpgradeSummary is the human tail: what the range brings, what is
// left, and what is coming.
func printUpgradeSummary(w io.Writer, rep *upgradeReport) {
	printCrossed(w, rep.From, rep.To, rep.Crossed)
	printGenerated(w, rep.GeneratedChanges, rep.GeneratedHint)
	if line := slowPhasesLine(rep.Phases); line != "" {
		fmt.Fprintf(w, "\n%s\n", line)
	}
	if len(rep.StartsGating) > 0 {
		fmt.Fprintf(w, "\nStarts gating at %s (warn-first rules whose grace this pin ends):\n", rep.To)
		for _, g := range rep.StartsGating {
			line := fmt.Sprintf("  - %s (new in %s; warned under %s, gates under %s)", g.Rule, g.IntroducedIn, rep.From, rep.To)
			if g.Findings != nil {
				line += fmt.Sprintf(": %d %s now %s", *g.Findings, plural(*g.Findings, "finding", "findings"), plural(*g.Findings, "gates", "gate"))
			}
			if g.Fix != "" {
				line += "; fix with `" + g.Fix + "`"
			}
			fmt.Fprintln(w, line)
		}
	}
	if len(rep.Upcoming) > 0 {
		fmt.Fprintln(w, "\nGating soon (warn-first rules this pin's lint reports; they gate from the release named):")
		for _, u := range rep.Upcoming {
			line := fmt.Sprintf("  - %s ×%d, %s", u.Rule, u.Count, lint.GatesWhen(u.GatesFrom, u.GatesAfter))
			if u.Fix != "" {
				line += "; fix with `" + u.Fix + "`"
			}
			fmt.Fprintln(w, line)
		}
	}
	if len(rep.NeedsHuman) > 0 {
		fmt.Fprintf(w, "\nStill needs a person (%d):\n", len(rep.NeedsHuman))
		for _, it := range rep.NeedsHuman {
			where := it.File
			if it.Line > 0 {
				where = fmt.Sprintf("%s:%d", it.File, it.Line)
			}
			head := it.Kind
			if it.Rule != "" {
				head += " " + it.Rule
			} else if it.ID != "" {
				head += " " + it.ID
			}
			if where != "" {
				head += " (" + where + ")"
			}
			fmt.Fprintf(w, "  - %s: %s\n", head, it.Message)
			printHumanContext(w, it.Context)
			if it.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", it.Fix)
			}
		}
	} else if rep.Converged {
		fmt.Fprintln(w, "\nConverged: every mechanical fix applied and nothing is left for a person.")
	}
}

// humanContextLines caps the context printed under one item; --json carries
// all of it.
const humanContextLines = 12

func printHumanContext(w io.Writer, lines []string) {
	for i, l := range lines {
		if i == humanContextLines {
			fmt.Fprintf(w, "      | … %d more line(s) (needs_human[].context in --json)\n", len(lines)-i)
			return
		}
		fmt.Fprintf(w, "      | %s\n", l)
	}
}

// ── real steps ───────────────────────────────────────────────────────────────

// realUpgradeSteps runs the tools as children of this forge, so each takes its
// own project lock and heavy slot and a failure cannot unwind the upgrade's
// process. Their stdout goes to out, which is stderr under --json.
func realUpgradeSteps(out io.Writer) *upgradeSteps {
	forge := func(dir string, args ...string) (*exec.Cmd, error) {
		self, err := forgeExecCommand()
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(self[0], append(self[1:], args...)...)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		cmd.Stderr = os.Stderr
		return cmd, nil
	}
	return &upgradeSteps{
		generate: func(dir string) error {
			cmd, err := forge(dir, "generate")
			if err != nil {
				return err
			}
			cmd.Stdout = out
			return cmd.Run()
		},
		lint: func(dir string) ([]byte, error) {
			cmd, err := forge(dir, "lint", "--json")
			if err != nil {
				return nil, err
			}
			var buf bytes.Buffer
			cmd.Stdout = &buf
			// A failing lint prints the same document and exits 1.
			var ee *exec.ExitError
			if err := cmd.Run(); err != nil && !(errors.As(err, &ee) && buf.Len() > 0) {
				return nil, err
			}
			return buf.Bytes(), nil
		},
		command: func(dir string, env, argv []string) error {
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), env...)
			cmd.Stdout, cmd.Stderr = out, os.Stderr
			return cmd.Run()
		},
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// upgradeTreeDirty reports whether git sees tracked changes other than the pin
// files a forge bump itself edits. A directory git does not track is clean.
func upgradeTreeDirty(root string) bool {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if _, to, ok := strings.Cut(path, " -> "); ok {
			path = to
		}
		switch filepath.Base(strings.Trim(path, `"`)) {
		case "go.mod", "go.sum", "go.work", "go.work.sum", "forge.yaml":
		default:
			return true
		}
	}
	return false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// needsPersonMessage is exit 9's one line: what is left and where it is
// listed — the JSON document on stdout under --json (it is not "above" when
// a bot captured stdout), the "Still needs a person" list otherwise.
func needsPersonMessage(n int, jsonOut bool) string {
	where := `listed above under "Still needs a person"`
	if jsonOut {
		where = "needs_human in the JSON document on stdout"
	}
	return fmt.Sprintf("upgrade applied every mechanical fix; %d item(s) need a person (%s)", n, where)
}
