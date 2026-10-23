// File: internal/cli/lint/lint_structured.go
//
// `forge lint --quiet` and `forge lint --scope <path>` — the two output
// modes rendered from the STRUCTURED pipeline (the same per-step collectors
// `forge lint --json` renders) rather than from each lane's bespoke text.
//
// ── Why --quiet exists ────────────────────────────────────────────────────
//
// A full `forge lint` prints every lane's progress, every warning and every
// "clean" line: ~75 lines on a fresh one-entity project, hundreds on a real
// one. Agents gate phases on it and, to keep their context small, pipe it
// through `head` — which cuts off the one line that matters, the verdict at
// the bottom. A dogfood run did exactly that and could not tell whether lint
// had passed. --quiet prints only what FAILED: each failing lane's one-line
// summary, its error findings, and the verdict as the last line. A clean run
// is one line.
//
// ── Why --scope exists ────────────────────────────────────────────────────
//
// Several agents commonly share one checkout. An agent finishing
// internal/handlers/jobs wants to know whether ITS slice is clean, and a
// full lint answers a different question — one another agent's half-written
// file can fail. --scope restricts the report to files under one or more
// paths. How each lane honours it is declared per step (linterStep.scope):
//
//   - scopeByPackages — the lane takes Go package arguments (golangci-lint,
//     the typed-config guardrail, the contract linter), so it RUNS on the
//     scope's packages only. This is also what makes a scoped run fast:
//     golangci-lint is the slow lane.
//   - scopeByFile — the lane runs project-wide and reports only findings
//     whose file is under the scope. A finding with no file cannot be
//     attributed to any slice, so it is kept: hiding an error the scope
//     cannot place would be a false green.
//   - scopeWholeProject — the lane's verdict is about the project as a
//     whole and cannot be attributed to files: frontend lint (the
//     frontend's own `npm run lint` over the whole frontend) and
//     component-drift (code vs deploy/kcl/workloads.k), plus the targeted
//     --config-reach. These are SKIPPED under --scope and named in the
//     verdict, never silently dropped — a scoped run proves only its slice,
//     and the last line says so.
//
// Auto-fix honours the scope too: the Go format pre-pass rewrites only
// files under it, golangci-lint --fix runs only on its packages, and the
// frontend fixers do not run (their lane is whole-project). A scoped lint
// must not reformat files another agent is in the middle of editing.
//
// ── Why the structured pipeline, not filtered text ────────────────────────
//
// Scoping needs a file per finding, and only the structured collectors have
// one; filtering human text would mean re-parsing thirty bespoke formats.
// The collectors are already kept in lockstep with the text lanes by the
// shared lintPipeline table, so the verdict --quiet prints is the verdict
// `forge lint` prints — including auto-fix, which --quiet applies (silently)
// exactly as the default run does.

package lint

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// scopeMode declares how a pipeline step honours --scope. See the file
// header for the three modes.
type scopeMode int

const (
	// scopeUnset is the zero value and is never valid on a step: every
	// lane must consciously pick a mode (TestEveryLintStepDeclaresAScopeMode).
	scopeUnset scopeMode = iota
	scopeByFile
	scopeByPackages
	scopeWholeProject
)

// targetedWholeProjectLanes are the targeted-only lanes (not in the
// pipeline table) that cannot be scoped. config-reach reports config FIELDS
// no binary loads — a fact about the project, carrying no file.
var targetedWholeProjectLanes = map[string]bool{"config-reach": true, "graph": true}

// targetedPackageLanes are the targeted lanes that take Go package
// arguments, mirroring scopeByPackages.
var targetedPackageLanes = map[string]bool{"contract": true, "exported-vars": true}

// lintScope restricts a run to files under one or more project-relative
// paths. A nil *lintScope means "the whole project", and every method is
// nil-safe so callers never branch on it.
type lintScope struct {
	root  string
	paths []string // project-relative, slash-separated, cleaned, sorted
}

// parseLintScope validates the --scope values. Each must exist and lie
// inside the project: a typo would otherwise scope the run to nothing and
// report it clean, which is the silent green this flag must never produce.
func parseLintScope(root string, raw []string) (*lintScope, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	s := &lintScope{root: root}
	seen := map[string]bool{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs := r
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, cliutil.UserErr("forge lint --scope",
				fmt.Sprintf("%q is outside the project root %s", r, root), "",
				"pass a path inside the project, relative to its root")
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, cliutil.UserErr("forge lint --scope",
				fmt.Sprintf("%q does not exist", r), "",
				"check the spelling — a scope that matches nothing would report a clean run over no files")
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil, cliutil.UserErr("forge lint --scope",
				"--scope . is the whole project", "",
				"drop --scope to lint the whole project")
		}
		if !seen[rel] {
			seen[rel] = true
			s.paths = append(s.paths, rel)
		}
	}
	if len(s.paths) == 0 {
		return nil, cliutil.UserErr("forge lint --scope", "--scope was given no path", "",
			"pass a project-relative path, e.g. --scope internal/handlers/jobs")
	}
	sort.Strings(s.paths)
	return s, nil
}

// String renders the scope for the verdict line.
func (s *lintScope) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(s.paths, ", ")
}

// contains reports whether file — project-relative or absolute — lies
// under the scope.
func (s *lintScope) contains(file string) bool {
	if s == nil {
		return true
	}
	f := file
	if filepath.IsAbs(f) {
		rel, err := filepath.Rel(s.root, f)
		if err != nil {
			return false
		}
		f = rel
	}
	f = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(f)), "./")
	for _, p := range s.paths {
		if f == p || strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// filter keeps the findings a scoped run reports: those under the scope,
// plus those with no file at all (unattributable, so never hidden).
func (s *lintScope) filter(fs []lintJSONFinding) []lintJSONFinding {
	if s == nil {
		return fs
	}
	out := make([]lintJSONFinding, 0, len(fs))
	for _, f := range fs {
		if f.File == "" || s.contains(f.File) {
			out = append(out, f)
		}
	}
	return out
}

// goPackages translates the scope into Go package patterns for the lanes
// that take them: `./<dir>/...` for a directory, the containing package for
// a .go file. Non-Go paths (proto/, db/migrations/) contribute nothing, and
// an empty result means no Go package is in scope.
func (s *lintScope) goPackages() []string {
	if s == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range s.paths {
		fi, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		var pattern string
		switch {
		case fi.IsDir():
			// A directory with no Go source under it (proto/, db/) is not a
			// package pattern: golangci-lint refuses it with "no go files to
			// analyze" rather than reporting nothing.
			if !dirHasGoFiles(filepath.Join(s.root, filepath.FromSlash(p))) {
				continue
			}
			pattern = "./" + p + "/..."
		case strings.HasSuffix(p, ".go"):
			pattern = "./" + path.Dir(p)
			if path.Dir(p) == "." {
				pattern = "."
			}
		default:
			continue
		}
		if !seen[pattern] {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}
	return out
}

// namesGoFile reports whether any scope path is a single .go file rather
// than a directory.
func (s *lintScope) namesGoFile() bool {
	if s == nil {
		return false
	}
	for _, p := range s.paths {
		if strings.HasSuffix(p, ".go") {
			return true
		}
	}
	return false
}

// dirHasGoFiles reports whether any .go file lies under dir, skipping the
// trees `go list ./...` itself ignores.
func dirHasGoFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is simply not counted
		}
		if d.IsDir() {
			name := d.Name()
			if p != dir && (name == "node_modules" || name == "vendor" || name == "testdata" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// lintStepOutcome is what one lane proved, in the shape every structured
// renderer consumes. Exactly one of ran / unscoped / (neither — skipped) holds.
type lintStepOutcome struct {
	name  string
	gates bool
	ran   bool
	// skipMsg is the reason a lane that did not run was skipped; "" for a
	// lane that silently does not apply to this project shape.
	skipMsg string
	// unscoped marks a whole-project lane skipped because of --scope.
	unscoped bool
	findings []lintJSONFinding
	// gated reports that this lane fails the build.
	gated bool
	// gap is set when the lane (or part of it) was supposed to execute
	// and could not — the `-unavailable` rule family lint_json.go
	// documents, graded by applyGapPolicy. Such a lane is neither "ran"
	// nor "skipped", exactly as in the text driver.
	gap *laneGap
}

// failedBeyondGap reports whether the lane failed for a reason other than
// not running: an error finding that is not its gap headline.
func (o lintStepOutcome) failedBeyondGap() bool {
	if !o.gated {
		return false
	}
	if o.gap == nil || o.gap.verdict != gapFails {
		return true
	}
	for _, f := range o.findings {
		if f.Severity == lintSevError && !isGapRule(f.Rule) {
			return true
		}
	}
	return false
}

// collectLintOutcomes runs the pipeline's collectors and returns one outcome
// per step, applying --scope. It is the engine behind --json, --quiet and
// --scope; collectAllLintersJSON flattens it.
//
// The lint.rules check leads: a dial the run cannot honour is reported with
// every verdict it would have changed.
func collectLintOutcomes(rc *lintRunCtx) []lintStepOutcome {
	return append(lintRulesOutcomes(rc.cfg), collectLintOutcomesFrom(rc, lintPipeline())...)
}

// lintRulesOutcomes is the lint.rules check as zero or one outcome: none when
// every entry is honoured, so a clean dial adds nothing to the report.
func lintRulesOutcomes(cfg *config.ProjectConfig) []lintStepOutcome {
	if o := lintRulesOutcome(cfg); o.ran {
		return []lintStepOutcome{o}
	}
	return nil
}

// collectLintOutcomesFrom is collectLintOutcomes over an explicit step list,
// so the scope semantics can be tested against steps whose behaviour the
// test controls.
func collectLintOutcomesFrom(rc *lintRunCtx, pipeline []linterStep) []lintStepOutcome {
	outcomes := make([]lintStepOutcome, 0, len(pipeline))
	for _, step := range pipeline {
		o := lintStepOutcome{name: step.name, gates: step.gates}
		run, skipMsg := step.shouldRun(rc)
		if !run {
			o.skipMsg = skipMsg
			outcomes = append(outcomes, o)
			continue
		}
		if rc.progress != nil && !(rc.scope != nil && step.scope == scopeWholeProject) {
			fmt.Fprintf(rc.progress, "▸ %s\n", step.name)
		}
		stepRC := rc
		if rc.scope != nil {
			switch step.scope {
			case scopeWholeProject:
				o.unscoped = true
				outcomes = append(outcomes, o)
				continue
			case scopeByPackages:
				pkgs := rc.scope.goPackages()
				if len(pkgs) == 0 {
					o.skipMsg = "no Go package under --scope — skipping " + step.name
					outcomes = append(outcomes, o)
					continue
				}
				scoped := *rc
				scoped.paths = pkgs
				// A scope naming a single .go file still hands the linter
				// that file's whole package, and a lane's fixer would
				// rewrite siblings outside the scope. Detect-only then.
				scoped.fix = rc.fix && !rc.scope.namesGoFile()
				stepRC = &scoped
			}
		}
		fs, gated, err := step.collect(stepRC)
		if err != nil {
			// A hard collection failure degrades to a finding rather than
			// aborting the sweep — severity/gating governed by step.gates.
			sev := lintSevWarning
			if step.gates {
				sev = lintSevError
			}
			fs = []lintJSONFinding{{
				Severity: sev,
				Rule:     "external",
				Message:  fmt.Sprintf("%s failed: %v", step.name, err),
			}}
			gated = step.gates
		}
		// lint.rules grades every lane here, once, before the scope narrows
		// it (lint_rules.go).
		fs, gated = gradeLane(effectiveRules(rc.cfg), fs, gated)
		o.ran = true
		o.findings, o.gated = scopeOutcome(rc.scope, fs, gated)
		applyGapPolicy(&o, laneGates(step, rc), rc)
		outcomes = append(outcomes, o)
	}
	return outcomes
}

// scopeOutcome filters a lane's findings to the scope and re-derives its
// verdict from what survived. The verdict can only NARROW: a lane gates
// under --scope only if it gated unscoped AND an error is left in scope.
func scopeOutcome(scope *lintScope, fs []lintJSONFinding, gated bool) ([]lintJSONFinding, bool) {
	if scope == nil {
		return fs, gated
	}
	kept := scope.filter(fs)
	return kept, gated && anyErrorFinding(kept)
}

// flattenLintOutcomes renders outcomes onto the flat --json findings list,
// byte-for-byte what collectAllLintersJSON produced before outcomes existed:
// a skip message becomes an info "skipped" finding, a silent skip
// contributes nothing.
func flattenLintOutcomes(outcomes []lintStepOutcome) ([]lintJSONFinding, bool) {
	var findings []lintJSONFinding
	gated := false
	for _, o := range outcomes {
		switch {
		case o.unscoped:
			findings = append(findings, skippedFinding(o.name+
				": whole-project lane — not run under --scope (run an unscoped `forge lint` for it)"))
		case !o.ran:
			if o.skipMsg != "" {
				findings = append(findings, skippedFinding(o.skipMsg))
			}
		default:
			findings = append(findings, o.findings...)
			gated = gated || o.gated
		}
	}
	return findings, gated
}

// collectTargetedOutcomes is collectLintOutcomes for the `--<linter>` flags:
// one outcome per lane they name, in dispatch order, so the verdict rendered
// from them covers every named lane. targeted=false means no such flag was
// set.
func collectTargetedOutcomes(
	ctx context.Context,
	flags lintFlags,
	paths []string,
	cwd string,
	store *projectstore.Store,
	cfg *config.ProjectConfig,
	scope *lintScope,
	progress io.Writer,
) ([]lintStepOutcome, bool, error) {
	lanes := selectedLanes(flags)
	if len(lanes) == 0 {
		return nil, false, nil
	}
	outcomes := lintRulesOutcomes(cfg)
	for _, lane := range lanes {
		if progress != nil {
			fmt.Fprintf(progress, "▸ --%s\n", lane)
		}
		o, err := collectLaneOutcome(ctx, onlyLane(flags, lane), lane, paths, cwd, store, cfg, scope)
		if err != nil {
			return nil, true, err
		}
		outcomes = append(outcomes, o)
	}
	return outcomes, true, nil
}

// collectLaneOutcome runs one targeted lane, honouring --scope, as an
// outcome. flags selects that lane alone (onlyLane).
func collectLaneOutcome(
	ctx context.Context,
	flags lintFlags,
	lane string,
	paths []string,
	cwd string,
	store *projectstore.Store,
	cfg *config.ProjectConfig,
	scope *lintScope,
) (lintStepOutcome, error) {
	o := lintStepOutcome{name: lane, gates: true}
	if scope != nil {
		switch {
		case targetedWholeProjectLanes[lane]:
			o.unscoped = true
			return o, nil
		case targetedPackageLanes[lane]:
			paths = scope.goPackages()
			if len(paths) == 0 {
				o.skipMsg = "no Go package under --scope — skipping " + lane
				return o, nil
			}
		}
	}
	report, handled, err := collectSingleLinterJSON(ctx, flags, paths, cwd, store, cfg)
	if err != nil {
		return o, err
	}
	if !handled {
		// lintLaneFlags and the structured dispatch table disagree — a lane
		// added to one and not the other. Refuse rather than silently
		// running the whole suite under one lane's name.
		return o, fmt.Errorf("--%s has no structured collector; run it without --quiet/--scope/--json", lane)
	}
	fs, gated := gradeLane(effectiveRules(cfg), report.Findings, !report.OK)
	o.ran = true
	o.findings, o.gated = scopeOutcome(scope, fs, gated)
	return o, nil
}

// runLintStructured is the --quiet / --scope entry point.
//
// Stray helper prints are routed to stderr during collection, exactly as
// --json does, so the only thing on stdout is the report itself.
func runLintStructured(ctx context.Context, flags lintFlags, paths []string) error {
	startedAt := time.Now()
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	store, cfg, err := loadLintConfig()
	if err != nil {
		return err
	}
	scope, err := parseLintScope(cwd, flags.scope)
	if err != nil {
		return err
	}
	allow, err := lintAllowances(flags, cfg)
	if err != nil {
		return err
	}

	// A human run says where it is as each lane starts; --quiet promises
	// one line for a clean run, so it does not.
	var progress io.Writer
	if !flags.quiet {
		progress = os.Stderr
	}
	realStdout := os.Stdout
	os.Stdout = os.Stderr
	outcomes, targeted, err := collectTargetedOutcomes(ctx, flags, paths, cwd, store, cfg, scope, progress)
	fixed := 0
	switch {
	case targeted:
		// A targeted lane never auto-fixes, in text mode or here.
	default:
		fix := resolveFixMode(flags)
		if fix {
			fixed = structuredFixPrePass(ctx, cwd, cfg, scope, flags.skipFrontends)
		}
		outcomes = collectLintOutcomes(&lintRunCtx{
			ctx:           ctx,
			fix:           fix,
			strict:        flags.strict,
			skipFrontends: flags.skipFrontends,
			paths:         paths,
			cfg:           cfg,
			cwd:           cwd,
			scope:         scope,
			allowSkip:     allow,
			progress:      progress,
			golangciMemo:  newGolangciMemo(productionGolangciToolchain()),
		})
		if !fix {
			outcomes = append(outcomes, formatOutcome(ctx, cwd, cfg, scope, paths, flags.skipFrontends))
		}
	}
	os.Stdout = realStdout
	if err != nil {
		return err
	}

	if fixed > 0 {
		fmt.Fprintf(os.Stdout, "🔧 auto-fixed %d file(s) before gating (formatting)\n", fixed)
	}
	verdictErr := renderLintOutcomes(os.Stdout, outcomes, flags.quiet, scope)
	if flags.gateJSON != "" {
		return resolveGateRunResult(verdictErr, emitLintGate(flags.gateJSON, tallyFromOutcomes(outcomes), startedAt))
	}
	return verdictErr
}

// structuredFixPrePass applies the deterministic-safe formatters the default
// run applies, without their progress output, and returns how many files it
// changed. Under --scope only files inside the scope are touched and the
// frontend formatter does not run (frontend lint cannot be scoped).
// eslint --fix is applied inside its own lane (rc.fix), as in text mode;
// golangci-lint never fixes (lint_check.go).
func structuredFixPrePass(ctx context.Context, cwd string, cfg *config.ProjectConfig, scope *lintScope, skipFrontends bool) int {
	var keep func(rel string) bool
	if scope != nil {
		keep = scope.contains
	}
	changed, reverted, err := formatGoTreeVerified(cwd, keep, fixTypecheck)
	printRevertedGoFixes(os.Stderr, "auto-format pre-pass", reverted)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  auto-format pre-pass: %v\n", err)
	}
	n := len(changed)
	if scope == nil && !skipFrontends {
		feChanged, feErr := formatFrontendTrees(ctx, cwd, cfg)
		if feErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️  frontend auto-format pre-pass: %v\n", feErr)
		}
		n += len(feChanged)
	}
	return n
}

// renderLintOutcomes prints the structured report and returns the verdict:
// nil after printing a success line (the LAST line of stdout), or the
// failure error — which main prints as the last line of the run.
//
// quiet prints only failing lanes and their error findings; otherwise every
// lane with an error or warning is shown. Info findings (skip notes) are
// never printed here — a skipped lane is named in the verdict instead.
func renderLintOutcomes(w io.Writer, outcomes []lintStepOutcome, quiet bool, scope *lintScope) error {
	v := lintVerdict{scope: scope.String()}
	for _, o := range outcomes {
		switch {
		case o.unscoped:
			v.unscoped = append(v.unscoped, o.name)
			continue
		case !o.ran:
			if o.skipMsg != "" && o.gates {
				v.skipped = append(v.skipped, o.name)
			}
			continue
		}

		var errs, warns []lintJSONFinding
		for _, f := range o.findings {
			switch f.Severity {
			case lintSevError:
				errs = append(errs, f)
			case lintSevWarning:
				warns = append(warns, f)
			}
		}

		switch {
		case o.gap == nil:
			if o.gates {
				v.ran = append(v.ran, o.name)
			}
		case o.gap.verdict == gapFails:
			v.unavailable = append(v.unavailable, *o.gap)
		case o.gap.verdict == gapAllowed:
			v.allowed = append(v.allowed, *o.gap)
		default:
			v.advisoryUnavailable = append(v.advisoryUnavailable, o.name)
		}
		if o.failedBeyondGap() {
			v.failed = append(v.failed, o.name)
		}

		switch {
		case !quiet && !o.gated && len(errs)+len(warns) == 0 && o.gap == nil:
			// A clean lane says it ran, and what it checked: the line that
			// tells a clean run from a vacuous one.
			fmt.Fprintf(w, "✓ %s\n", o.name)
			for _, f := range o.findings {
				if f.Rule == ruleRanNote {
					fmt.Fprintf(w, "   · %s\n", f.Message)
				}
			}
		case o.gated:
			shown := errs
			if !quiet {
				shown = append(append([]lintJSONFinding{}, errs...), warns...)
			} else {
				v.hiddenWarnings += issueCount(warns)
			}
			if len(shown) == 0 {
				// A lane that failed without an error finding still has to
				// say why; show whatever it reported.
				shown = o.findings
			}
			fmt.Fprintf(w, "❌ %s — %s\n", o.name, countPhrase(issueCount(errs), issueCount(warns)))
			writeStructuredFindings(w, shown)
			if len(shown) == 0 {
				fmt.Fprintf(w, "   (the lane failed without a finding — run `forge lint` for its full output)\n")
			}
			if o.gap != nil && o.gap.verdict == gapFails {
				fmt.Fprintf(w, "      → %s\n", acceptSkipHint([]laneGap{*o.gap}))
			}
		case quiet:
			v.hiddenWarnings += issueCount(warns) + issueCount(errs)
		case len(errs)+len(warns) > 0:
			fmt.Fprintf(w, "⚠️  %s — %s\n", o.name, countPhrase(issueCount(errs), issueCount(warns)))
			writeStructuredFindings(w, append(append([]lintJSONFinding{}, errs...), warns...))
		}
	}
	return v.render(w)
}

// ruleRanNote marks an info finding a lane adds to say what it checked
// ("web: npm run lint passed"). It never gates; the human report prints it
// under the lane's ✓ line, so a clean run shows what it proved.
const ruleRanNote = "ran"

// ranNote is one such note.
func ranNote(msg string) lintJSONFinding {
	return lintJSONFinding{Severity: lintSevInfo, Rule: ruleRanNote, Message: msg}
}

// issueCount counts the issues a finding list reports. A sub-tool's raw
// lines (rule "external", no file) are the source snippet and summary that
// accompany its file-anchored issues — golangci-lint prints five lines for
// one issue — so they count only when the tool reported nothing anchored.
func issueCount(fs []lintJSONFinding) int {
	anchored := 0
	for _, f := range fs {
		if f.File != "" || f.Rule != "external" {
			anchored++
		}
	}
	if anchored > 0 {
		return anchored
	}
	return len(fs)
}

// countPhrase renders "1 error", "2 errors, 3 warnings", "1 warning".
func countPhrase(errs, warns int) string {
	var parts []string
	if errs > 0 {
		parts = append(parts, plural(errs, "error"))
	}
	if warns > 0 {
		parts = append(parts, plural(warns, "warning"))
	}
	if len(parts) == 0 {
		return "failed"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// writeStructuredFindings prints findings one per line, indented under their
// lane, with the fix hint beneath. A raw sub-tool line (rule "external", no
// file) prints verbatim, so golangci-lint's source snippet under an issue
// keeps its shape.
func writeStructuredFindings(w io.Writer, fs []lintJSONFinding) {
	for _, f := range fs {
		marker := "⚠"
		if f.Severity == lintSevError {
			marker = "✗"
		}
		switch {
		case f.File == "" && f.Rule == "external":
			fmt.Fprintf(w, "   %s\n", f.Message)
		case f.File == "":
			fmt.Fprintf(w, "   %s [%s] %s\n", marker, f.Rule, f.Message)
		default:
			fmt.Fprintf(w, "   %s %s [%s] %s\n", marker, findingLocation(f), f.Rule, f.Message)
		}
		if f.FixHint != "" {
			fmt.Fprintf(w, "      → %s\n", f.FixHint)
		}
	}
}

func findingLocation(f lintJSONFinding) string {
	loc := filepath.ToSlash(f.File)
	if f.Line > 0 {
		loc += fmt.Sprintf(":%d", f.Line)
		if f.Col > 0 {
			loc += fmt.Sprintf(":%d", f.Col)
		}
	}
	return loc
}

// tallyFromOutcomes builds the --gate-json lane tally from outcomes, with the
// same buckets the text driver records.
func tallyFromOutcomes(outcomes []lintStepOutcome) lintLaneTally {
	var t lintLaneTally
	for _, o := range outcomes {
		switch {
		case o.unscoped:
			t.skipped = append(t.skipped, o.name)
		case !o.ran:
			if o.skipMsg != "" && o.gates {
				t.skipped = append(t.skipped, o.name)
			}
		case o.gap != nil && o.gap.verdict == gapAllowed:
			t.allowed = append(t.allowed, o.name)
		case o.gap != nil:
			t.unavailable = append(t.unavailable, o.name)
		case o.gates:
			t.ran = append(t.ran, o.name)
		}
		t.failed = t.failed || o.gated
	}
	return t
}

// validateLintModeFlags rejects flag combinations whose meaning would be
// silently dropped, in the same "refuse loudly" spirit as --json's checks.
func validateLintModeFlags(flags lintFlags, explicitPaths bool) error {
	switch {
	case flags.quiet && flags.jsonOut:
		return cliutil.UserErr("forge lint --quiet", "--quiet cannot be combined with --json", "",
			"--json is already the machine-readable form; filter its findings by severity instead")
	case len(flags.scope) > 0 && explicitPaths:
		return cliutil.UserErr("forge lint --scope", "pass package paths or --scope, not both", "",
			"--scope already restricts the Go lanes to the scope's packages; drop the positional paths")
	case len(flags.scope) > 0 && flags.gateJSON != "":
		return cliutil.UserErr("forge lint --scope", "--scope cannot be combined with --gate-json", "",
			"a gate document records evidence about the whole project, and a scoped run proves only a slice — run the gate unscoped")
	case flags.showSuppressed && (flags.quiet || flags.gateJSON != "" || explicitPaths ||
		flags.suggestExcludes || flags.suggestBufExcepts || len(selectedLanes(flags)) > 0):
		return cliutil.UserErr("forge lint --show-suppressed", "--show-suppressed lists suppressions; it runs no linter", "",
			"run it on its own (it takes --json and --scope); run the lanes separately")
	case (flags.quiet || len(flags.scope) > 0) && (flags.suggestExcludes || flags.suggestBufExcepts):
		return cliutil.UserErr("forge lint", "--quiet/--scope cannot be combined with --suggest-excludes or --suggest-buf-excepts", "",
			"run the suggest modes on their own; their output is a YAML snippet, not findings")
	}
	return nil
}
