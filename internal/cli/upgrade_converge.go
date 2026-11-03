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
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/generator"
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
	// TemplateDrift are frozen files you modified that a newer template
	// changed; upgrade never overwrites them without --force.
	TemplateDrift []string `json:"template_drift"`
}

type upgradePhase struct {
	Name string `json:"name"`
	// Status is done, skipped, planned (--check) or failed.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
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
}

type upgradeUpcoming struct {
	Rule         string   `json:"rule"`
	Count        int      `json:"count"`
	IntroducedIn string   `json:"introduced_in"`
	GatesFrom    string   `json:"gates_from"`
	Fix          string   `json:"fix,omitempty"`
	Examples     []string `json:"examples"`
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
		NeedsHuman: []upgradeHumanItem{}, Upcoming: []upgradeUpcoming{}, TemplateDrift: []string{},
	}
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
	// ran are the codemods already run, by name, so a playbook that names
	// one an Upgrade crossing ran does not run it twice.
	ran map[string]bool
}

// runUpgradeConverge is the whole `forge project upgrade`. It always returns a
// report; the error, when set, is what the process exits with.
func runUpgradeConverge(opts upgradeOptions) (*upgradeReport, error) {
	rep := newUpgradeReport(opts)
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
		return &exitCodeError{code: exitNeedsHuman, msg: fmt.Sprintf(
			"upgrade applied every mechanical fix; %d item(s) still need a person (see needs_human in --json, or above)", len(rep.NeedsHuman))}
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
	r.banner()
	r.applicable = r.pendingPlaybooks()
	return r, nil
}

func (r *upgradeRun) banner() {
	if r.check {
		fmt.Fprintf(r.out, "%s project upgrade --check (dry run): %s → %s\n", Name(), describeVersion(r.from), describeVersion(r.target))
	} else {
		fmt.Fprintf(r.out, "%s project upgrade: %s → %s\n", Name(), describeVersion(r.from), describeVersion(r.target))
	}
	if isPreV01Baseline(r.from) {
		fmt.Fprintln(r.out, "  Baseline is pre-v0.1: forge has not declared backwards compatibility yet, so expect template churn.")
	}
	fmt.Fprintln(r.out)
	printApplicableMigrations(r.projectDir, r.from)
}

// step runs one phase and records its status. A skipped phase is recorded and
// not run; a failed one stops the run with its error.
func (r *upgradeRun) step(name string, run func() error) error {
	if r.opts.skip[name] {
		r.rep.Phases = append(r.rep.Phases, upgradePhase{Name: name, Status: "skipped", Detail: r.skipDetail(name)})
		return nil
	}
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

func (r *upgradeRun) humanBlocked(cr upgradeCodemodRun) {
	r.human(upgradeHumanItem{
		Kind: "codemod-blocked", ID: cr.Name,
		Message: fmt.Sprintf("forge fix %s wrote nothing: %s", cr.Name, strings.Join(cr.Blocked, "; ")),
		Fix: fmt.Sprintf("clear each item it lists (`%s fix %s --dry-run`), then re-run `%s project upgrade`; `%s skill load migrations/%s` is the manual path",
			Name(), cr.Name, Name(), Name(), cr.Version),
	})
}

func (r *upgradeRun) phaseTemplates() error {
	selection, err := resolveForceSelection(r.projectDir, r.cfg, r.opts.force, r.opts.forcePaths)
	if err != nil {
		return err
	}
	results, err := generator.UpgradeSelection(r.projectDir, r.cfg, selection, r.check)
	if err != nil {
		return err
	}
	counts := tallyAndPrintUpgradeResults(results, r.check, r.opts.showAll)
	printSkippedPaths(counts, r.opts.showAll)
	printUserModifiedRemedies(counts, r.opts.showAll)
	r.rep.TemplateDrift = append(r.rep.TemplateDrift, counts.userModifiedPaths...)
	if _, err := runAdvisoryPass(r.projectDir, r.cfg, selection, r.check, r.opts.showAll); err != nil {
		return err
	}
	r.setPhase("templates", map[bool]string{true: "planned", false: "done"}[r.check],
		fmt.Sprintf("%d updated, %d modified by you (kept), %d up to date", counts.updated, counts.userModified, counts.upToDate))
	return nil
}

func (r *upgradeRun) phasePin() error {
	return bumpForgeVersion(r.cfg, r.configPath, r.target, r.check)
}

// ── playbooks ────────────────────────────────────────────────────────────────

// pendingPlaybooks are the playbooks this range crosses that the project still
// exhibits and has not recorded as applied, oldest release first.
func (r *upgradeRun) pendingPlaybooks() []pendingMigration {
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
	var out []pendingMigration
	for _, row := range applicableMigrations(metas, r.from, r.projectDir) {
		if _, done := state.Applied[row.Meta.ID]; done || !targetReaches(r.target, row.Meta.Version) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (r *upgradeRun) phasePlaybooks() error {
	for _, row := range r.applicable {
		pr := upgradePlaybookRun{ID: row.Meta.ID, Description: shorten(row.Meta.Description, 200), Automation: nonNil(row.Meta.Automation)}
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
	if err := r.steps.generate(r.projectDir); err != nil {
		return fmt.Errorf("forge generate failed after the upgrade's rewrites (the pin is already moved; fix the cause and run `%s generate`): %w", Name(), err)
	}
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
	Findings []struct {
		Severity string `json:"severity"`
		Rule     string `json:"rule"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Message  string `json:"message"`
		FixHint  string `json:"fix_hint"`
		Grace    *struct {
			IntroducedIn string `json:"introduced_in"`
			GatesFrom    string `json:"gates_from"`
			Fix          string `json:"fix"`
		} `json:"grace"`
	} `json:"findings"`
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
	for _, f := range doc.Findings {
		switch {
		case f.Severity == "error":
			r.human(upgradeHumanItem{Kind: "lint-error", Rule: f.Rule, File: f.File, Line: f.Line, Message: f.Message, Fix: f.FixHint})
		case f.Grace != nil:
			u := upcoming[f.Rule]
			if u == nil {
				u = &upgradeUpcoming{Rule: f.Rule, IntroducedIn: f.Grace.IntroducedIn, GatesFrom: f.Grace.GatesFrom, Fix: f.Grace.Fix, Examples: []string{}}
				upcoming[f.Rule] = u
				order = append(order, f.Rule)
			}
			u.Count++
			if len(u.Examples) < 3 {
				u.Examples = append(u.Examples, strings.TrimSuffix(fmt.Sprintf("%s:%d", f.File, f.Line), ":0"))
			}
		}
	}
	for _, rule := range order {
		r.rep.Upcoming = append(r.rep.Upcoming, *upcoming[rule])
	}
	return nil
}

// ── printing ─────────────────────────────────────────────────────────────────

// printUpgradeSummary is the human tail: what is left, and what is coming.
func printUpgradeSummary(w io.Writer, rep *upgradeReport) {
	if len(rep.Upcoming) > 0 {
		fmt.Fprintln(w, "\nGating soon (warn-first rules this pin's lint reports; they gate from the release named):")
		for _, u := range rep.Upcoming {
			line := fmt.Sprintf("  - %s ×%d, gates from %s", u.Rule, u.Count, u.GatesFrom)
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
			if it.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", it.Fix)
			}
		}
	} else if rep.Converged {
		fmt.Fprintln(w, "\nConverged: every mechanical fix applied and nothing is left for a person.")
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
