// File: internal/cli/lint_json.go
//
// `forge lint --json` — machine-readable lint output for sub-agents
// and CI, following the same conventions as `forge project audit --json` and
// `forge doctor --json` (flag name `--json`, indented json.Encoder to
// stdout, non-zero exit via a sentinel error after the report prints).
//
// Output contract (stable; extensions are additive, same policy as the
// audit-json skill documents for `forge project audit --json`):
//
//	{
//	  "findings": [
//	    {
//	      "file": "handlers/api/handlers.go",  // omitted when not file-scoped
//	      "line": 42,                          // 1-based; omitted when unknown
//	      "col": 7,                            // 1-based; omitted when unknown
//	      "severity": "error",                 // "error" | "warning" | "info"
//	      "rule": "forge-config-deps",         // rule id; "external" for raw sub-tool lines
//	      "message": "...",
//	      "fix_hint": "..."                    // omitted when the rule has none
//	    }
//	  ],
//	  "summary": { "errors": 1, "warnings": 3, "infos": 0, "total": 4 },
//	  "ok": false
//	}
//
// Exit-code semantics are IDENTICAL to text mode: `ok` is false exactly
// when text mode would have exited non-zero, and in that case the
// command returns a sentinel error after the JSON has been written so
// cobra exits 1. Linters that are warnings-only in text mode (db,
// tests, banners, check-workarounds, orm-sync,
// frontend-stores, config-deps) never flip `ok`.
//
// New rules arrive as NEW `rule` values on the existing finding shape —
// the additive-extension contract: no field is renamed or repurposed, so
// a consumer that filters on the rules it knows keeps working. The
// generated-file ownership gate contributes two:
// `forge-generated-file-edited` (error; flips `ok` to false) and
// `forge-generated-file-unverified` (warning; never flips `ok`).
//
// The frontend typecheck lane contributes two more:
// `forge-frontend-typecheck` (TypeScript diagnostics, file/line/col
// anchored at the project-relative source path; severity follows
// forge.yaml's lint.frontend.typecheck, error by default) and
// `forge-frontend-typecheck-unavailable` (the check could NOT run because
// deps aren't installed or no compiler resolves; graded like the rest of
// the family below). A consumer that treats "unavailable"
// as clean is asserting something the report never claims: a typecheck
// that could not run is not a typecheck that passed.
//
// THE `-unavailable` FAMILY. That last sentence is a contract, not a note
// about one lane, and every lane that can fail to execute names the
// condition with its own rule id:
//
//	golangci-lint-unavailable              — no golangci-lint can analyze the module
//	buf-lint-unavailable                   — buf.yaml present, buf not on PATH
//	forge-frontend-typecheck-unavailable   — no compiler / deps not installed
//	forge-frontend-lint-unavailable        — frontend dir missing / deps not installed
//	typed-config-guardrail-unavailable     — golangci-lint never reported
//
// One policy grades them all (applyGapPolicy, lint_skip.go): from a GATING
// lane the finding is an error and flips `ok` to false; from an advisory
// lane (the guardrail, or a typecheck dialled to warn) it is a warning,
// promoted to an error by --strict; and a lane accepted by --allow-skip or
// forge.yaml lint.allow_skip reports it at info, its message ending
// "SKIPPED (allowed: <reason>)". The rule id is what makes them machine-legible: before it
// existed, the guardrail reported "could not run" under the SAME rule id
// and severity as its real findings, so `forge lint --json` answered
// "ok": true over a check that never executed and no consumer could tell.
// New lanes join the family rather than inventing a spelling.
//
// External sub-tools (golangci-lint, contractlint, buf, npm scripts)
// have their output captured and normalized: lines matching the
// conventional `file:line[:col]: message` shape become file-scoped
// findings; anything else is preserved verbatim as a finding with
// rule "external" — sub-tool output is never silently dropped.
// Skipped linters (tool not on PATH, feature disabled, directory
// missing) surface as severity "info" findings with rule "skipped" so
// an agent can tell "clean" apart from "didn't run".

package lint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/linter/contract"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/linter/migrationlint"
	"github.com/reliant-labs/forge/internal/linter/scaffolds"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// Severity values used in the JSON report. These now match the canonical
// internal/linter/finding spellings exactly, so no normalization is
// needed when mapping linter findings onto the JSON contract.
const (
	lintSevError   = string(finding.SeverityError)
	lintSevWarning = string(finding.SeverityWarning)
	lintSevInfo    = string(finding.SeverityInfo)
)

// lintJSONFinding is one normalized diagnostic. See the file header for
// the field contract.
type lintJSONFinding struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	FixHint  string `json:"fix_hint,omitempty"`
}

// lintJSONSummary counts findings by severity. Total is the slice
// length, included so consumers don't have to add the buckets.
type lintJSONSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
	Total    int `json:"total"`
}

// lintJSONReport is the top-level `forge lint --json` structure.
// Findings is always non-nil so consumers see `[]`, not `null`.
type lintJSONReport struct {
	Findings []lintJSONFinding `json:"findings"`
	Summary  lintJSONSummary   `json:"summary"`
	OK       bool              `json:"ok"`
}

// errLintJSONFailed is the sentinel returned after the JSON report has
// been written when text mode would have exited non-zero. Same pattern
// as errDoctorFailed: the report carries the detail; this line just
// makes cobra exit 1 with a one-line stderr reason.
var errLintJSONFailed = fmt.Errorf("lint reported errors; see JSON report above")

// buildLintJSONReport assembles the report envelope from collected
// findings plus the gating verdict computed by the caller (which
// mirrors text mode's hasFailed logic exactly — see file header).
func buildLintJSONReport(findings []lintJSONFinding, gated bool) *lintJSONReport {
	if findings == nil {
		findings = []lintJSONFinding{}
	}
	var sum lintJSONSummary
	for _, f := range findings {
		switch f.Severity {
		case lintSevError:
			sum.Errors++
		case lintSevInfo:
			sum.Infos++
		default:
			sum.Warnings++
		}
	}
	sum.Total = len(findings)
	return &lintJSONReport{Findings: findings, Summary: sum, OK: !gated}
}

// skippedFinding records a linter that did not run (tool missing,
// feature disabled, directory absent) so JSON consumers can tell
// "clean" from "didn't run". Never gates.
func skippedFinding(msg string) lintJSONFinding {
	return lintJSONFinding{Severity: lintSevInfo, Rule: "skipped", Message: msg}
}

// featureDisabledReport is the --json counterpart of errFeatureDisabled: a
// targeted lint whose feature is off reports NOT OK.
//
// The skippedFinding form was info-severity and left "ok": true, which every
// CI consumer reads as a clean pass for the lane it asked about — the exact
// silent-green this whole fix is about, on the surface where it does the most
// damage because no human reads it.
func featureDisabledReport(flag, feature string) *lintJSONReport {
	return buildLintJSONReport([]lintJSONFinding{{
		Severity: lintSevError,
		Rule:     "feature-disabled",
		Message: fmt.Sprintf("`forge lint %s` was requested but the %s feature is off for this project, "+
			"so nothing was checked", flag, feature),
		FixHint: fmt.Sprintf("%s — or drop %s from this command. `forge project features` shows why it is off.",
			featureTurnOn(feature), flag),
	}}, true)
}

// runLintJSON is the --json counterpart of runLint. It mirrors the
// same flag dispatch (targeted single-linter modes, else all linters)
// but collects findings instead of printing, then writes one JSON
// document to stdout.
//
// Stray human prints are a hazard here: several shared helpers
// (loadProjectConfig warnings and friends) write to
// os.Stdout. For the collection phase we point os.Stdout at stderr so
// stdout stays pure JSON and nothing a helper says is lost — it just
// lands on stderr where humans (and CI logs) still see it.
func runLintJSON(ctx context.Context, flags lintFlags, paths []string) error {
	// Suggestion / mutation modes emit YAML snippets or rewrite files —
	// neither has a sensible findings shape. Refuse loudly instead of
	// emitting JSON that silently ignored the flag.
	if flags.fix || flags.suggestExcludes || flags.suggestBufExcepts {
		return cliutil.UserErr("forge lint --json",
			"--json cannot be combined with --fix, --suggest-excludes, or --suggest-buf-excepts",
			"",
			"run those modes without --json; their output is suggestion- or mutation-shaped, not findings-shaped")
	}

	realStdout := os.Stdout
	os.Stdout = os.Stderr
	report, err := collectLintJSON(ctx, flags, paths)
	os.Stdout = realStdout
	if err != nil {
		// Hard failure (I/O, config parse, …) — same as text mode:
		// no report, non-zero exit with the underlying reason.
		return err
	}

	enc := json.NewEncoder(realStdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(report); encErr != nil {
		return encErr
	}
	if !report.OK {
		return errLintJSONFailed
	}
	return nil
}

// collectLintJSON runs the lanes the targeted-linter flags name, exactly the
// set runLint runs (selectedLanes), falling through to the all-linters sweep
// when none is set.
func collectLintJSON(ctx context.Context, flags lintFlags, paths []string) (*lintJSONReport, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}

	// Project config: same tolerance as text mode — missing is fine,
	// parse errors are fatal.
	store, cfgErr := loadProjectStore()
	if cfgErr != nil && !errors.Is(cfgErr, ErrProjectConfigNotFound) {
		return nil, fmt.Errorf("failed to load project config: %w", cfgErr)
	}
	var cfg *config.ProjectConfig
	if store != nil {
		cfg = store.Config()
	}

	scope, err := parseLintScope(cwd, flags.scope)
	if err != nil {
		return nil, err
	}
	// Targeted lanes go through the outcome engine, which knows how each
	// lane honours --scope, and one report covers every lane named.
	if outcomes, targeted, err := collectTargetedOutcomes(ctx, flags, paths, cwd, store, cfg, scope, nil); targeted {
		if err != nil {
			return nil, err
		}
		findings, gated := flattenLintOutcomes(outcomes)
		return buildLintJSONReport(findings, gated), nil
	}

	allow, err := lintAllowances(flags, cfg)
	if err != nil {
		return nil, err
	}
	return collectAllLintersJSON(ctx, lintRunOptions{
		reportFormat:  !flags.autoFix(),
		strict:        flags.strict,
		skipFrontends: flags.skipFrontends,
		allowSkip:     allow,
		paths:         paths,
		cfg:           cfg,
		scope:         scope,
	}, cwd)
}

// collectSingleLinterJSON runs the ONE linter flags select — callers narrow
// flags to one lane first (onlyLane), so a combined invocation reaches here
// once per lane. handled=false means no lane flag was set; it is distinct
// from a nil report, which a selected linter can legitimately return
// alongside an error.
func collectSingleLinterJSON(
	ctx context.Context,
	flags lintFlags,
	paths []string,
	cwd string,
	store *projectstore.Store,
	cfg *config.ProjectConfig,
) (*lintJSONReport, bool, error) {
	// Each arm returns through this closure so the switch stays a flat
	// one-line-per-linter dispatch table.
	done := func(r *lintJSONReport, err error) (*lintJSONReport, bool, error) {
		if err != nil {
			return nil, true, err
		}
		return r, true, nil
	}
	report := func(fs []lintJSONFinding, gated bool, err error) (*lintJSONReport, bool, error) {
		if err != nil {
			return nil, true, err
		}
		return done(buildLintJSONReport(fs, gated), nil)
	}
	// reportUngated is report for the linters that return findings only —
	// they cannot gate the build, so gated is always false.
	reportUngated := func(fs []lintJSONFinding, err error) (*lintJSONReport, bool, error) {
		return report(fs, false, err)
	}
	// reportBySeverity is report for the linters whose findings carry their
	// own verdict — an error-severity finding gates, a warning does not.
	reportBySeverity := func(fs []lintJSONFinding, err error) (*lintJSONReport, bool, error) {
		return report(fs, anyErrorFinding(fs), err)
	}

	switch {
	case flags.contract, flags.exportedVars:
		if store != nil && !store.Features().ContractsEnabled() {
			return done(featureDisabledReport("--contract", "contracts"), nil)
		}
		return report(collectContractLintJSON(ctx, paths, contractExcludesFromConfig(cfg), contractGateOptions(cfg, flags.strict)))
	case flags.migrationSafety:
		if store != nil && !store.Features().MigrationsEnabled() {
			return done(featureDisabledReport("--migration-safety", "migrations"), nil)
		}
		return report(collectMigrationSafetyJSON(cfg))
	case flags.conventions:
		return report(collectConventionsJSON(forgeconv.LintOptions{Strict: flags.strict}))
	case flags.generatedDrift:
		return report(collectGeneratedDriftJSON(cwd))
	case flags.frontendStores:
		return reportUngated(collectFrontendStoresJSON(cwd))
	case flags.scaffolds:
		return report(collectScaffoldsJSON(cwd))
	case flags.tests:
		return report(collectTestsJSON(cwd))
	case flags.banners:
		return reportUngated(collectBannersJSON(cwd))
	case flags.checkWorkarounds:
		return reportUngated(collectWorkaroundsJSON(cwd))
	case flags.optionalDepsGuard:
		return reportUngated(collectOptionalDepsGuardJSON(cwd))
	case flags.configDeps:
		return reportUngated(collectConfigDepsJSON(cwd))
	case flags.columnMarkers:
		return reportBySeverity(collectColumnMarkersJSON(cfg))
	case flags.closedSets:
		return reportBySeverity(collectClosedSetsJSON(cwd, cfg))
	case flags.dbGrants:
		return reportBySeverity(collectDBGrantsJSON(cwd, cfg))
	case flags.fixtureDrift || flags.crudFixtures:
		return reportUngated(collectFixtureDriftJSON(cwd, cfg))
	case flags.timeBucketing:
		return reportUngated(collectTimeBucketingJSON(cwd, cfg))
	case flags.protoMarkers:
		return reportUngated(collectProtoMarkersJSON(protoDirDefault))
	case flags.goMarkers:
		return report(collectGoMarkersJSON(cwd))
	case flags.createNullability:
		return report(collectCreateNullabilityJSON(protoDirDefault, cwd))
	case flags.computedFields:
		return reportBySeverity(collectComputedFieldsJSON(cwd))
	case flags.readOnlyFields:
		// Missing from this table until now, so `--read-only-fields --json`
		// fell through to the WHOLE suite — a targeted gate that silently
		// ran everything else and reported it under one flag.
		return reportBySeverity(collectReadOnlyFieldsJSON(cwd, migrationsDirFor(cfg)))
	case flags.secrets:
		return report(collectSecretsJSON(&lintRunCtx{ctx: ctx, cfg: cfg, cwd: cwd}))
	case flags.guardedFields:
		return reportUngated(collectGuardedFieldsJSON(cwd, frontendDirsForLint()))
	case flags.staticExport:
		return report(collectStaticExportJSON(&lintRunCtx{ctx: ctx, cfg: cfg, cwd: cwd}))
	case flags.tracing:
		// `forge lint --tracing --fix` is the migration: the named lane fixes
		// its warnings too (--json refuses --fix, so it stays detect-only).
		return report(collectTracingJSON(&lintRunCtx{ctx: ctx, cfg: cfg, cwd: cwd, fix: flags.autoFix(), targeted: true}))
	case flags.protoOptions:
		return reportUngated(collectProtoOptionsJSON(protoDirDefault))
	case flags.vendoredProtos:
		return report(collectVendoredProtosJSON(cwd))
	case flags.configReach:
		return reportUngated(collectConfigReachJSON(cwd, cfg))
	case flags.graph:
		return reportBySeverity(collectGraphJSON(cwd, cfg))
	case flags.slowTests:
		return report(collectSlowTestsJSON(ctx, paths))
	}

	return nil, false, nil
}

// collectAllLintersJSON mirrors runAllLinters step-for-step. Each step
// contributes findings; `gated` flips on exactly the conditions that
// set hasFailed in text mode. Per-step collection errors (I/O etc.)
// degrade to an error-severity finding rather than aborting the sweep
// — matching text mode, which prints the failure and keeps walking
// only for the advisory linters but hard-fails the run for the gating
// ones via hasFailed.
//
// The walk itself is collectLintOutcomes (lint_structured.go), shared with
// --quiet and --scope; this flattens it. A skip message surfaces as an info
// finding so JSON consumers can tell "clean" from "didn't run"; a silent skip
// (directory absent) contributes nothing.
func collectAllLintersJSON(ctx context.Context, opts lintRunOptions, cwd string) (*lintJSONReport, error) {
	outcomes := collectLintOutcomes(&lintRunCtx{
		ctx:           ctx,
		fix:           false,
		strict:        opts.strict,
		skipFrontends: opts.skipFrontends,
		allowSkip:     opts.allowSkip,
		paths:         opts.paths,
		cfg:           opts.cfg,
		cwd:           cwd,
		scope:         opts.scope,
	})
	if opts.reportFormat {
		outcomes = append(outcomes, formatOutcome(ctx, cwd, opts.cfg, opts.scope, opts.paths, opts.skipFrontends))
	}
	findings, gated := flattenLintOutcomes(outcomes)
	return buildLintJSONReport(findings, gated), nil
}

// ---------------------------------------------------------------------------
// Structured collectors — thin maps over the internal linter packages.
// ---------------------------------------------------------------------------

// findingsToJSON is the single canonical mapper from the shared
// finding.Finding (emitted by every internal linter — forgeconv,
// scaffolds, migrationlint) onto the lint --json contract. It replaces
// the near-identical per-package mappers that existed before the finding
// package was introduced.
//
// Field mapping rules, unified:
//   - Severity passes through directly: the canonical finding severities
//     ("error"/"warning"/"info") ARE the JSON contract values, so no
//     normalization shim is needed.
//   - File comes from f.File, falling back to f.Path for whole-file
//     (line-less) scaffold findings — exactly one of the two is ever set.
//   - Remediation becomes fix_hint (forgeconv's actionable hints).
func findingsToJSON(fs []finding.Finding) []lintJSONFinding {
	out := make([]lintJSONFinding, 0, len(fs))
	for _, f := range fs {
		file := f.File
		if file == "" {
			file = f.Path
		}
		out = append(out, lintJSONFinding{
			File:     file,
			Line:     f.Line,
			Severity: string(f.Severity),
			Rule:     f.Rule,
			Message:  f.Message,
			FixHint:  f.Remediation,
		})
	}
	return out
}

func collectConventionsJSON(opts forgeconv.LintOptions) ([]lintJSONFinding, bool, error) {
	combined, notes, _, err := collectConventionFindings(opts)
	if err != nil {
		return nil, false, err
	}
	out := make([]lintJSONFinding, 0, len(notes)+len(combined.Findings))
	for _, n := range notes {
		out = append(out, skippedFinding(n))
	}
	out = append(out, findingsToJSON(combined.Findings)...)
	return out, combined.HasErrors(), nil
}

func collectMigrationSafetyJSON(cfg *config.ProjectConfig) ([]lintJSONFinding, bool, error) {
	migrationsDir := filepath.Join("db", "migrations")
	ruleConfig := migrationlint.DefaultConfig()
	if cfg != nil {
		if cfg.Database.MigrationsDir != "" {
			migrationsDir = cfg.Database.MigrationsDir
		}
		ruleConfig = migrationlint.ConfigFromProject(cfg.Database.MigrationSafety)
	}
	result, err := migrationlint.LintMigrationsDir(migrationsDir, ruleConfig)
	if err != nil {
		return nil, false, fmt.Errorf("migration safety lint failed: %w", err)
	}
	out := findingsToJSON(result.Findings)
	// Per-RULE remediation. These findings carry no Remediation of their
	// own, and stamping every one with the destructive text told an author
	// whose NOT NULL add was rejected to reach for an allowlist that does
	// not silence it.
	for i := range out {
		out[i].FixHint = migrationlint.RemediationFor(result.Findings[i].Rule)
	}
	return out, result.HasErrors(), nil
}

func collectFrontendStoresJSON(cwd string) ([]lintJSONFinding, error) {
	res, err := forgeconv.LintFrontendStores(cwd)
	if err != nil {
		return nil, fmt.Errorf("frontend-stores lint failed: %w", err)
	}
	return findingsToJSON(res.Findings), nil
}

func collectScaffoldsJSON(cwd string) ([]lintJSONFinding, bool, error) {
	res, err := scaffolds.LintRoot(cwd)
	if err != nil {
		return nil, false, fmt.Errorf("scaffold lint failed: %w", err)
	}
	return findingsToJSON(res.Findings), res.HasErrors(), nil
}

// collectTestsJSON mirrors runTestsLint on the JSON path. The second
// return reports whether anything gates — only the shape rule can, so the
// two paths agree on which findings are fatal.
func collectTestsJSON(cwd string) ([]lintJSONFinding, bool, error) {
	handlerRes, err := forgeconv.LintHandlerTests(cwd)
	if err != nil {
		return nil, false, fmt.Errorf("handler-test lint failed: %w", err)
	}
	frontendRes, err := forgeconv.LintFrontendHookTests(cwd)
	if err != nil {
		return nil, false, fmt.Errorf("frontend-hook-test lint failed: %w", err)
	}
	shapeRes, err := forgeconv.LintFrontendHookTestShape(cwd)
	if err != nil {
		return nil, false, fmt.Errorf("frontend-hook-test-shape lint failed: %w", err)
	}
	out := findingsToJSON(handlerRes.Findings)
	out = append(out, findingsToJSON(frontendRes.Findings)...)
	out = append(out, findingsToJSON(shapeRes.Findings)...)
	return out, shapeRes.HasErrors(), nil
}

func collectBannersJSON(cwd string) ([]lintJSONFinding, error) {
	hasTemplates := dirExists(filepath.Join("internal", "templates")) ||
		dirExists(filepath.Join("internal", "packs"))
	if !hasTemplates {
		return nil, nil
	}
	res, err := scaffolds.BannerLintRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("banner lint failed: %w", err)
	}
	return findingsToJSON(res.Findings), nil
}

// collectOptionalDepsGuardJSON maps optional-deps-guard findings onto
// the JSON contract. Always warnings — the walker is deliberately not
// full dataflow, so findings never gate (see
// lint_optional_deps_guard.go's header for the conservatism contract).
func collectOptionalDepsGuardJSON(projectDir string) ([]lintJSONFinding, error) {
	findings, err := collectOptionalDepsGuardFindings(projectDir)
	if err != nil {
		return nil, fmt.Errorf("optional-deps-guard lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Col:      f.Col,
			Severity: lintSevWarning,
			Rule:     "forge-optional-deps-guard",
			Message:  optionalDepsGuardMessage(f),
			FixHint:  optionalDepsGuardFixHint(f),
		})
	}
	return out, nil
}

// collectConfigDepsJSON maps config-deps findings onto the JSON
// contract. Severity warning across the board — scalar Deps fields
// compile (and may be hand-wired today); the finding is the nudge
// toward the component config-block declaration.
func collectConfigDepsJSON(projectDir string) ([]lintJSONFinding, error) {
	findings, err := collectConfigDepsFindings(projectDir)
	if err != nil {
		return nil, fmt.Errorf("config-deps lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Col:      f.Col,
			Severity: lintSevWarning,
			Rule:     "forge-config-deps",
			Message:  fmt.Sprintf("%s/%s Deps.%s is a naked scalar (%s) — scalar Deps fields are configuration, not collaborators", f.Role, f.Package, f.Field, f.Type),
			FixHint:  configDepsFixHint(f),
		})
	}
	return out, nil
}

// collectColumnMarkersJSON maps column-markers findings onto the JSON
// contract. Severity error across the board: a forge:* marker forge does not
// read where it is declared does nothing, and `forge generate` refuses the
// same catalog comment (schemadef.CheckMarkers).
func collectColumnMarkersJSON(cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	findings, err := collectColumnMarkerFindings(migrationsDirFor(cfg))
	if err != nil {
		return nil, fmt.Errorf("column-markers lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevError,
			Rule:     "forge-column-markers",
			Message:  fmt.Sprintf("%s on %s does nothing", f.Marker, f.Object),
			FixHint:  columnMarkerFixHint(f),
		})
	}
	return out, nil
}

// collectFixtureDriftJSON maps fixture-drift findings onto the JSON
// contract. Severity warning across the board: the fixture is genuinely
// broken and its test genuinely fails, but handlers_crud_test.go is the
// user's — forge scaffolded it once and does not own it — so the finding
// locates the problem rather than gating the build on an edit only the
// author can make. A run that found literal fixtures but could not execute
// them reports that too, as its own rule, so "not checked" never reads as
// "clean".
func collectFixtureDriftJSON(cwd string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	return collectFixtureDriftJSONAt(cwd, migrationsDirFor(cfg))
}

// collectFixtureDriftJSONAt is the config-free half, so a test can drive the
// JSON shape against a temp project without building a ProjectConfig.
func collectFixtureDriftJSONAt(cwd, migrationsDir string) ([]lintJSONFinding, error) {
	rep, err := collectFixtureDriftFindings(cwd, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("fixture-drift lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(rep.Findings)+1)
	if rep.Unverified != "" {
		out = append(out, lintJSONFinding{
			File:     rep.UnverifiedFile,
			Severity: lintSevWarning,
			Rule:     fixtureDriftUnverifiedRule,
			Message:  "literal fixtures were not executed: " + rep.Unverified,
		})
	}
	for _, f := range rep.Findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevWarning,
			Rule:     fixtureDriftRule,
			Message:  f.message(),
			FixHint:  fixtureDriftFixHint(f),
		})
	}
	return out, nil
}

// collectTimeBucketingJSON maps time-bucketing findings onto the JSON
// contract. Severity warning across the board: unlike the read-only rule,
// a local-time bucket can be exactly what the author wants for a shift
// report or a business-day rollup, so the finding makes the choice visible
// rather than making it.
func collectTimeBucketingJSON(cwd string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	return collectTimeBucketingJSONAt(cwd, migrationsDirFor(cfg))
}

// collectTimeBucketingJSONAt is the config-free half, so a test can drive
// the JSON shape against a temp project without building a ProjectConfig.
func collectTimeBucketingJSONAt(cwd, migrationsDir string) ([]lintJSONFinding, error) {
	findings, err := collectTimeBucketFindings(cwd, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("time-bucketing lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Col:      f.Col,
			Severity: lintSevWarning,
			Rule:     timeBucketRuleID,
			Message:  f.message(),
			FixHint:  timeBucketFixHint(f),
		})
	}
	return out, nil
}

// collectProtoMarkersJSON maps proto-markers findings onto the JSON
// contract. Severity warning across the board, for the same reason as its
// column-marker sibling — an unrecognized forge:* marker might be a future
// forge version's, or unrelated prose, so the finding is a nudge to check
// the spelling rather than a hard failure.
func collectProtoMarkersJSON(protoDir string) ([]lintJSONFinding, error) {
	findings, err := collectProtoMarkerFindings(protoDir)
	if err != nil {
		return nil, fmt.Errorf("proto-markers lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		msg := fmt.Sprintf("unrecognized forge:* proto marker %q", f.Marker)
		if f.Renamed != "" {
			msg = fmt.Sprintf("removed forge:* proto marker %q (renamed to %q)", f.Marker, f.Renamed)
		}
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevWarning,
			Rule:     "forge-proto-markers",
			Message:  msg,
			FixHint:  protoMarkerFixHint(f),
		})
	}
	return out, nil
}

// collectCreateNullabilityJSON maps create-nullability findings onto the
// JSON contract. Severity follows provenance (see lint_create_nullability.go):
// ERROR when forge generates the create, WARNING for a hand-written one,
// and suppression directives are already applied. The returned bool is the
// gating verdict: any error-severity finding.
func collectCreateNullabilityJSON(protoDir, projectRoot string) ([]lintJSONFinding, bool, error) {
	findings, err := createNullabilityLintFindings(protoDir, projectRoot)
	if err != nil {
		return nil, false, fmt.Errorf("create-nullability lint failed: %w", err)
	}
	return findingsToJSON(findings), countErrors(findings) > 0, nil
}

// collectComputedFieldsJSON maps computed-fields findings onto the JSON
// contract. Severity ERROR — forge:computed is the stronger promise, so it
// gates at least as hard as its read-only twin — except while pending-stub
// mode holds a finding at warning (lint_pending_stubs.go), in which case the
// message names the forge-scaffolded stubs it is waiting on. The gating
// verdict is anyErrorFinding over the result.
func collectComputedFieldsJSON(cwd string) ([]lintJSONFinding, error) {
	findings, err := collectComputedFieldFindings(cwd)
	if err != nil {
		return nil, fmt.Errorf("computed-fields lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: f.Pending.severity(),
			Rule:     computedFieldRuleID,
			Message: withPending(fmt.Sprintf("%s.%s is marked %s but no non-generated Go file assigns %s",
				f.Entity, f.Field, codegen.ProtoMarkerComputed, f.GoField), f.Pending),
			FixHint: computedFieldFixHint(f),
		})
	}
	return out, nil
}

// collectReadOnlyFieldsJSON maps read-only-fields findings onto the JSON
// contract.
//
// Severity ERROR. The fix is indeed a migration or app logic only the author
// can write — but that argues for a clear message, not a soft verdict: this
// defect has no symptom other than a human noticing $0.00 on a screen, so a
// warning inside a long lint run is very close to the silence the rule
// exists to break. The one exception is pending-stub mode
// (lint_pending_stubs.go): while the declaring service still carries forge's
// own unwired stubs, the finding is a warning naming them. See
// lint_read_only_fields_gating_test.go for the false-positive cases that had
// to be closed before it could gate.
func collectReadOnlyFieldsJSON(cwd, migrationsDir string) ([]lintJSONFinding, error) {
	findings, err := collectReadOnlyFieldFindings(cwd, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read-only-fields lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: f.Pending.severity(),
			Rule:     readOnlyFieldRuleID,
			Message: withPending(fmt.Sprintf("%s.%s is marked %s but no non-generated Go file assigns %s, and %s.%s has no DEFAULT that populates it",
				f.Entity, f.Field, codegen.ProtoMarkerReadOnly, f.GoField, f.Table, f.Field), f.Pending),
			FixHint: readOnlyFieldFixHint(f),
		})
	}
	return out, nil
}

// collectGuardedFieldsJSON maps guarded-fields findings onto the JSON
// contract. Severity warning: the file is one forge scaffolded and then
// handed over, so the fix is an edit only its owner can make — gating the
// build on it would block a project on a change forge is forbidden to apply.
func collectGuardedFieldsJSON(cwd string, frontendDirs []string) ([]lintJSONFinding, error) {
	findings, err := collectGuardedFieldFindings(cwd, frontendDirs)
	if err != nil {
		return nil, fmt.Errorf("guarded-fields lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevWarning,
			Rule:     "forgeconv-guarded-field-written",
			Message: fmt.Sprintf("this page's update_mask writes %s.%s, which %s declares %#q",
				f.Table, f.Column, f.GuardedBy, codegen.ProtoMarkerGuards),
			FixHint: guardedFieldFixHint(f),
		})
	}
	return out, nil
}

// collectProtoOptionsJSON maps proto-options findings onto the JSON
// contract. Severity warning: the scan is a source-level parse that does
// not resolve imports, so it cannot see a project that legitimately
// extended forge's option messages — the vendored-protos rule gates the
// root cause instead. See lint_proto_options.go for the full rationale.
func collectProtoOptionsJSON(protoDir string) ([]lintJSONFinding, error) {
	findings, err := collectProtoOptionFindings(protoDir)
	if err != nil {
		return nil, fmt.Errorf("proto-options lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		msg := fmt.Sprintf("(%s) is not an annotation this forge binary defines", f.Extension)
		if f.Field != "" {
			msg = fmt.Sprintf("(%s).%s is not a field %s defines — the annotation is read by nothing",
				f.Extension, f.Field, f.Message)
		}
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevWarning,
			Rule:     "forge-proto-options",
			Message:  msg,
			FixHint:  protoOptionFixHint(f),
		})
	}
	return out, nil
}

// collectVendoredProtosJSON maps vendored-proto drift onto the JSON
// contract, returning the gating verdict alongside: drift in forge.proto
// is an ERROR that flips `ok` to false (a field-number collision with an
// upstream `reserved` is a correctness bug and the fix is mechanical),
// while validate.proto drift is a warning. See lint_vendored_protos.go.
func collectVendoredProtosJSON(projectDir string) ([]lintJSONFinding, bool, error) {
	findings, err := collectVendoredProtoFindings(projectDir)
	if err != nil {
		return nil, false, fmt.Errorf("vendored-protos lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		severity := lintSevWarning
		if f.Severity == vendoredSeverityError {
			severity = lintSevError
		}
		out = append(out, lintJSONFinding{
			File:     f.File,
			Severity: severity,
			Rule:     "forge-vendored-proto-drift",
			Message: fmt.Sprintf("%s differs from the copy embedded in this forge binary:\n%s",
				f.File, strings.TrimRight(f.Diff, "\n")),
			FixHint: vendoredProtoFixHint(f),
		})
	}
	return out, vendoredProtosGate(findings), nil
}

// collectConfigReachJSON maps config-reach findings onto the JSON
// contract. Severity warning: an unreachable field is dead weight rather
// than a defect, and the remediation is a deletion the author has to
// judge. See lint_config_reach.go.
func collectConfigReachJSON(projectDir string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	findings, err := collectConfigReachFindingsForProject(projectDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("config-reach lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			Severity: lintSevWarning,
			Rule:     "forge-config-unreachable",
			Message: fmt.Sprintf("config field %s.%s is loaded by no binary or frontend",
				f.Message, f.Field),
			FixHint: configReachFixHint(f),
		})
	}
	return out, nil
}

func collectWorkaroundsJSON(cwd string) ([]lintJSONFinding, error) {
	res, err := scaffolds.LintWorkaroundsRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("check-workarounds lint failed: %w", err)
	}
	return findingsToJSON(res.Findings), nil
}

// ---------------------------------------------------------------------------
// External-tool collectors — capture + normalize subprocess output.
// ---------------------------------------------------------------------------

// reFileLineCol matches the conventional `file:line[:col]: message`
// diagnostic shape emitted by golangci-lint, go vet–style analyzers
// (contractlint), and buf lint. Group 1 file, 2 line, 3 optional col,
// 4 message.
var reFileLineCol = regexp.MustCompile(`^([^\s:]+):(\d+)(?::(\d+))?:\s*(.+)$`)

// reTrailingLinter extracts golangci-lint's trailing `(lintername)`
// attribution so the finding can carry the concrete rule instead of a
// generic "golangci-lint".
var reTrailingLinter = regexp.MustCompile(`\(([A-Za-z0-9_-]+)\)$`)

// externalLinesToFindings normalizes captured sub-tool output. Lines in
// `file:line[:col]: message` shape become file-scoped findings with the
// given rule; everything else is kept verbatim with rule "external" so
// no output is silently dropped. severity applies to every produced
// finding (the caller knows whether the tool's exit gated the build).
func externalLinesToFindings(output, rule, severity string) []lintJSONFinding {
	var out []lintJSONFinding
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := reFileLineCol.FindStringSubmatch(line); m != nil {
			f := lintJSONFinding{
				File:     m[1],
				Line:     parseDigitsLint(m[2]),
				Col:      parseDigitsLint(m[3]),
				Severity: severity,
				Rule:     rule,
				Message:  m[4],
			}
			// golangci-lint suffixes the message with "(lintername)";
			// promote it to the rule field when present.
			if rule == "golangci-lint" {
				if lm := reTrailingLinter.FindStringSubmatch(m[4]); lm != nil {
					f.Rule = lm[1]
				}
			}
			out = append(out, f)
			continue
		}
		out = append(out, lintJSONFinding{
			Severity: severity,
			Rule:     "external",
			Message:  line,
		})
	}
	return out
}

// parseDigitsLint parses a digits-only string (pre-validated by regex);
// returns 0 for empty.
func parseDigitsLint(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// collectGolangciLintJSON runs golangci-lint with captured output.
// Non-zero exit gates (same as text mode); the captured diagnostics
// become findings at error severity. A clean exit contributes nothing.
// It runs the same never-fix invocation as text mode (golangciGateArgs):
// under `forge lint --fix --quiet` / `--scope` this lane used to pass --fix
// on, applying the staticcheck/gocritic rewrites #1041 took out of the text
// driver. Files behind a project-defined build tag get a pass of their own
// (golangci_build_tags.go). A golangci-lint that cannot analyze the module gates as one
// finding, the runbook.
func collectGolangciLintJSON(rc *lintRunCtx) ([]lintJSONFinding, bool) {
	fs, gated := golangciPass(rc, golangciGateArgs(rc))
	if len(fs) > 0 && fs[0].Rule == ruleGolangciUnavailable {
		return fs, gated
	}
	// One more pass per build tag that gates files in the tree, over the
	// packages holding them (golangci_build_tags.go).
	inConfig := map[string]bool{}
	configTags := golangciConfigBuildTags(rc.moduleDir())
	for _, t := range configTags {
		inConfig[t] = true
	}
	for _, set := range scopeBuildTags(discoverBuildTags(rc.moduleDir()), rc.goPackages()) {
		if inConfig[set.tag] {
			continue
		}
		tags := strings.Join(append(append([]string{}, configTags...), set.tag), ",")
		more, g := golangciPass(rc, golangciRunArgs([]string{"--build-tags=" + tags}, set.dirs))
		fs, gated = mergeFindings(fs, more), gated || g
	}
	return fs, gated
}

// golangciPass is one golangci-lint run: its findings, and whether it gates.
func golangciPass(rc *lintRunCtx, args []string) ([]lintJSONFinding, bool) {
	out, err := rc.execGolangci(args)
	if err != nil {
		return []lintJSONFinding{golangciUnresolvedFinding(err, ruleGolangciUnavailable)}, false
	}
	if out.err != nil {
		fs := externalLinesToFindings(out.combined(), "golangci-lint", lintSevError)
		if len(fs) == 0 {
			fs = []lintJSONFinding{{Severity: lintSevError, Rule: "external", Message: fmt.Sprintf("golangci-lint failed: %v", out.err)}}
		}
		return fs, true
	}
	return nil, false
}

// mergeFindings appends the findings of more that base does not already hold:
// a package with both default and tagged files is linted by two passes, and a
// finding in a default file would otherwise print twice.
func mergeFindings(base, more []lintJSONFinding) []lintJSONFinding {
	seen := make(map[lintJSONFinding]bool, len(base))
	for _, f := range base {
		seen[f] = true
	}
	for _, f := range more {
		if !seen[f] {
			seen[f] = true
			base = append(base, f)
		}
	}
	return base
}

// Rule ids contributed by the typed-config guardrail lane.
//
// They are SEPARATE ids for the same reason the frontend typecheck lane's
// are: a consumer must be able to tell "the guardrail ran and had nothing to
// say" from "the guardrail never ran", and one shared id forces it to parse
// English out of a free-text message to do that. Both conditions used to
// arrive as rule "typed-config-guardrail" at warning severity, so a machine
// reading `ok` — or filtering by rule — could not distinguish them at all.
const (
	// ruleTypedAccessGuard tags a forbidigo finding the advisory pass
	// actually reported.
	ruleTypedAccessGuard = "typed-config-guardrail"
	// ruleTypedAccessGuardUnavailable tags the pass producing NO verdict:
	// golangci-lint never got far enough to report. Warning by default,
	// error (and gating) under --strict.
	ruleTypedAccessGuardUnavailable = "typed-config-guardrail-unavailable"
)

// collectTypedAccessGuardJSON mirrors runTypedAccessGuardAdvisory with
// captured output. It is the `warn` arm of config.enforce_typed_access:
// forbidigo FINDINGS are surfaced as warnings that never gate.
//
// Run with --issues-exit-code=0, so a non-zero exit cannot mean "found
// something" — it can only mean the invocation never reported. That is not a
// clean lane: it is emitted under ruleTypedAccessGuardUnavailable, and under
// --strict it is an error that flips `ok` to false. The old code returned the
// same rule id at warning severity with gated=false, so `forge lint --json`
// answered "ok": true over a check that never executed.
//
// A golangci-lint config that gives forbidigo no env-read pattern is the
// same could-not-run condition, reported before forbidigo is invoked: it
// would check its built-in fmt.Print patterns instead. A run that completes
// contributes only its file-anchored issues; its summary line ("0 issues.")
// and source snippets are not findings (lint_typed_access_guard.go).
func collectTypedAccessGuardJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	if reason := typedAccessGuardUnconfigured(rc.cwd); reason != "" {
		return []lintJSONFinding{{
			Severity: lintSevWarning,
			Rule:     ruleTypedAccessGuardUnavailable,
			Message:  "typed-config guardrail did NOT run: " + reason,
			FixHint:  typedAccessGuardUnconfiguredHint,
		}}, false, nil
	}
	run, err := rc.execGolangci(golangciRunArgs([]string{"--enable-only=forbidigo", "--issues-exit-code=0"}, rc.goPackages()))
	if err != nil {
		return []lintJSONFinding{golangciUnresolvedFinding(err, ruleTypedAccessGuardUnavailable)}, false, nil
	}
	if run.err != nil {
		out := []lintJSONFinding{{
			Severity: lintSevWarning,
			Rule:     ruleTypedAccessGuardUnavailable,
			Message:  fmt.Sprintf("typed-config guardrail did NOT run: golangci-lint exited %v", run.err),
			FixHint:  typedAccessGuardUnavailableHint,
		}}
		// Whatever golangci-lint managed to emit is the EVIDENCE for why it
		// could not run ("parallel golangci-lint is running"). This
		// collector used to capture it and then drop it on the floor,
		// leaving the consumer an exit status and no cause.
		out = append(out, externalLinesToFindings(run.combined(), ruleTypedAccessGuardUnavailable, lintSevInfo)...)
		return out, false, nil
	}
	fs := anchoredFindings(externalLinesToFindings(run.combined(), ruleTypedAccessGuard, lintSevWarning))
	return fs, false, nil
}

// collectContractLintJSON mirrors runContractLinter: the same
// IN-PROCESS analysis (contract_inprocess.go), with diagnostics mapped
// to findings instead of printed. Diagnostics gate; an analysis failure
// (broken packages, analyzer error) gates with the error preserved as a
// finding.
func collectContractLintJSON(ctx context.Context, paths []string, excludes []string, gate contract.ExcludeGateOptions) ([]lintJSONFinding, bool, error) {
	diags, err := runContractAnalysisInProcess(ctx, paths, excludes, gate)
	if err != nil {
		return []lintJSONFinding{{
			Severity: lintSevError,
			Rule:     "contract",
			Message:  fmt.Sprintf("contract linter failed: %v", err),
		}}, true, nil
	}
	if len(diags) == 0 {
		return nil, false, nil
	}
	fs := make([]lintJSONFinding, 0, len(diags))
	gated := false
	for _, d := range diags {
		sev, hint := lintSevError, d.FixHint
		if d.Warning {
			sev = lintSevWarning
		} else {
			gated = true
		}
		if hint == "" {
			hint = "either declare the exported method in the contract interface, or unexport it (lowercase) if it's helper-only"
		}
		fs = append(fs, lintJSONFinding{
			File:     d.Pos.Filename,
			Line:     d.Pos.Line,
			Col:      d.Pos.Column,
			Severity: sev,
			Rule:     "contract",
			Message:  fmt.Sprintf("%s (%s)", d.Message, d.Analyzer),
			FixHint:  hint,
		})
	}
	return fs, gated, nil
}

// collectBufLintJSON runs `buf lint` with captured output. Missing
// buf.yaml is a silent no-op, same as text mode.
func collectBufLintJSON(ctx context.Context) ([]lintJSONFinding, bool) {
	if _, err := os.Stat("buf.yaml"); os.IsNotExist(err) {
		return nil, false
	}
	if _, err := exec.LookPath("buf"); err != nil {
		return []lintJSONFinding{{Severity: lintSevWarning, Rule: ruleBufLintUnavailable,
			Message: bufMissingReason, FixHint: bufMissingFix()}}, false
	}
	cmd := exec.CommandContext(ctx, "buf", "lint")
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		fs := externalLinesToFindings(buf.String(), "buf", lintSevError)
		if len(fs) == 0 {
			fs = []lintJSONFinding{{Severity: lintSevError, Rule: "buf", Message: fmt.Sprintf("buf lint failed: %v", err)}}
		}
		return fs, true
	}
	return nil, false
}

// ruleFrontendLintUnavailable tags a frontend whose eslint lane could NOT
// run (declared directory missing, or deps not installed). It used to arrive
// as rule "skipped" at info severity — indistinguishable from a lane that
// genuinely did not apply, over a GATING step. Warning by default, error
// (and gating) under --strict, matching ruleFrontendTypecheckUnavailable.
const ruleFrontendLintUnavailable = "forge-frontend-lint-unavailable"

// ruleBufLintUnavailable tags a project with a buf.yaml on a machine with no
// buf: the buf lint lane could NOT run.
const ruleBufLintUnavailable = "buf-lint-unavailable"

// collectFrontendLintJSON mirrors runFrontendLinters / lintFrontendDir
// but captures npm output instead of streaming it. Failed scripts gate
// (matching text mode); their output is preserved line-by-line via
// externalLinesToFindings. A frontend the lane could not check surfaces
// under ruleFrontendLintUnavailable; a frontend with no lint script (the
// project's own choice) stays an info "skipped".
//
// TypeScript typechecking is collected by collectFrontendTypecheckJSON
// (its own pipeline step), not here.
func collectFrontendLintJSON(rc *lintRunCtx) ([]lintJSONFinding, bool) {
	ctx, cfg := rc.ctx, rc.cfg
	type fe struct{ name, dir, feType string }
	var frontends []fe
	// css_health is a CONFIG setting, so it is read from config — never from
	// which branch discovered the frontends. Reading it inside the
	// `len(cfg.Frontends) > 0` arm silently disabled CSS-health linting for
	// every project whose inventory is DERIVED rather than declared (forge.yaml
	// with no `frontends:` block — control-plane today): the directory scan
	// found the frontend and linted it, with css_health stuck at false and no
	// "skipped" finding to say so. Every other reader of this field
	// (lint.go:1315, generate_ci.go:181) reads it unconditionally.
	cssHealth := false
	if cfg != nil {
		cssHealth = cfg.Lint.Frontend.CSSHealth
	}
	if cfg != nil && len(cfg.Frontends) > 0 {
		for _, f := range cfg.Frontends {
			dir, ok := f.Dir(".")
			if !ok {
				continue
			}
			frontends = append(frontends, fe{name: f.Name, dir: dir, feType: f.Type})
		}
	} else if dirExists("frontends") {
		entries, err := os.ReadDir("frontends")
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					frontends = append(frontends, fe{name: e.Name(), dir: filepath.Join("frontends", e.Name())})
				}
			}
		}
	}

	var out []lintJSONFinding
	gated := false
	for _, f := range frontends {
		fs, g := collectFrontendDirJSON(ctx, f.name, f.dir, cssHealth, rc.fix)
		out = append(out, fs...)
		gated = gated || g
	}
	return out, gated
}

// collectFrontendDirJSON lints one frontend: its npm lint script (and
// lint:styles under css_health), with eslint --fix when the run fixes. A
// frontend with no node_modules gets its dependencies installed first, as a
// fresh worktree needs; a lane that still cannot run (the install failed, the
// directory is missing) is a `-unavailable` finding the gap policy grades.
func collectFrontendDirJSON(ctx context.Context, name, dir string, cssHealth, fix bool) ([]lintJSONFinding, bool) {
	var out []lintJSONFinding
	gated := false
	// Graded by applyGapPolicy (lint_skip.go): this lane gates, so a
	// frontend it could not lint fails the run unless the skip is accepted.
	unavailable := func(msg, hint string) {
		out = append(out, lintJSONFinding{
			Severity: lintSevWarning,
			Rule:     ruleFrontendLintUnavailable,
			Message:  msg,
			FixHint:  hint,
		})
	}
	if !dirExists(dir) {
		unavailable(fmt.Sprintf("%s: directory %s not found — eslint did NOT run", name, dir),
			fmt.Sprintf("fix the `path:` for frontend %q in forge.yaml, or remove the entry", name))
		return out, gated
	}
	pkgJSON := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgJSON); err != nil {
		// Not a Node project — does not apply, so it contributes nothing.
		return out, gated
	}
	// Walk to the workspace root: an npm/pnpm workspace HOISTS a member's
	// dependencies, so <feDir>/node_modules is legitimately absent in a
	// fully installed tree (forge's own dev bridge produces exactly that
	// layout). A frontend-local-only probe turns that into "eslint did NOT
	// run" — a lane silently downgraded to advisory on a tree that is
	// perfectly lintable.
	if !anyAncestorHasNodeModules(dir, ".") {
		if err := frontenddeps.Ensure(ctx, "[lint]", name, dir, "", true); err != nil {
			unavailable(fmt.Sprintf("%s: dependencies are not installed and installing them failed — eslint did NOT run: %v", name, err),
				npmInstallFix(dir))
			return out, gated
		}
	}
	if !anyAncestorHasNodeModules(dir, ".") {
		unavailable(fmt.Sprintf("%s: node_modules not found in %s or any ancestor — eslint did NOT run", name, dir),
			npmInstallFix(dir))
		return out, gated
	}
	scripts, err := readPackageScripts(pkgJSON)
	if err != nil {
		out = append(out, lintJSONFinding{Severity: lintSevError, Rule: "external", Message: fmt.Sprintf("%s: read package.json scripts: %v", name, err)})
		gated = true
		return out, gated
	}

	runScript := func(script string) {
		args := []string{"run", script}
		// eslint --fix for the lint script when the run auto-fixes (never
		// for --json, which refuses --fix).
		if fix && script == "lint" {
			args = append(args, "--", "--fix")
		}
		cmd := exec.CommandContext(ctx, "npm", args...)
		cmd.Dir = dir
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		if err == nil {
			out = append(out, ranNote(fmt.Sprintf("%s: npm run %s passed", name, script)))
		}
		if err != nil {
			gated = true
			out = append(out, lintJSONFinding{
				Severity: lintSevError,
				Rule:     "external",
				Message:  fmt.Sprintf("%s: npm run %s failed: %v", name, script, err),
			})
			out = append(out, externalLinesToFindings(buf.String(), "external", lintSevError)...)
		}
	}

	if hasPackageScript(scripts, "lint") {
		runScript("lint")
	} else {
		out = append(out, skippedFinding(fmt.Sprintf("%s: no npm lint script found, skipping lint", name)))
	}
	if cssHealth {
		if hasPackageScript(scripts, "lint:styles") {
			runScript("lint:styles")
		} else {
			out = append(out, skippedFinding(fmt.Sprintf("%s: css_health enabled but no npm lint:styles script found", name)))
		}
	}
	return out, gated
}
