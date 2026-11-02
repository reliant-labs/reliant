// File: internal/cli/lint_steps.go
//
// The single source of truth for the `forge lint` (no-flag) linter
// pipeline. Historically runAllLinters (text, in lint.go) and
// collectAllLintersJSON (JSON, in lint_json.go) each hand-encoded the
// SAME ordered 14-step sequence — feature gate, directory check, gating
// verdict — and were kept in lockstep by comment ("Step numbers track
// runAllLinters for diffability", "mirrors text mode step 13c"). That
// mirrored dispatch was the single biggest structural debt in the lint
// surface.
//
// This file models each linter as a value (linterStep) and builds ONE
// ordered []linterStep. Each step declares ONCE:
//
//   - name        — stable identity (the lane name the verdict prints)
//   - gates       — whether a hard collection error fails the build
//   - shouldRun   — the feature-gate / dir-exists / tool-on-PATH guard,
//                   returning a skip message when the step is a no-op
//   - collect     — the collector (findings + per-step gated)
//   - scope       — how the lane honours `forge lint --scope` (by file, by
//                   Go package, or not at all for a whole-project lane);
//                   see lint_structured.go
//
// ONE driver walks the table: collectLintOutcomes, the outcome engine every
// output mode renders (text, --quiet, --scope, --json, --gate-json). It
// grades each lane's findings through lint.rules (lint_rules.go), so a
// lane's verdict cannot depend on which mode printed it. There used to be a
// second, text-only driver with a bespoke printer per lane; lint.rules only
// reached the four lanes whose printers remembered to apply it.
//
// Steps that are advisory by default (tests, banners, optional-deps-guard,
// config-deps, check-workarounds) carry gates=false: their findings are
// warnings and a hard collection error degrades to a warning finding. A
// project grades their rules to error through lint.rules like any other.

package lint

import (
	"context"
	"io"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// lintRunCtx carries the shared inputs every step needs. cwd is resolved
// once up front (text mode historically re-called os.Getwd per cwd-using
// step and skipped that step on error; resolving once and treating an
// empty cwd as "skip" preserves that behavior without the repetition).
type lintRunCtx struct {
	ctx context.Context
	fix bool
	// strict escalates advisory findings to errors in the steps that
	// honor it (the forge-convention proto step's method-auth-annotation
	// rule, the contract gate's codegen-unavailable downgrade, and an
	// ADVISORY lane's own "could not run" report). It does not touch a
	// gating lane that could not run: that fails without it
	// (lint_skip.go). Plumbed from `forge lint --strict`.
	strict bool
	// skipFrontends drops the whole frontend lane — both the eslint /
	// stylelint step and the typecheck step. Plumbed from
	// `forge lint --skip-frontends`, for callers that want a backend-only
	// gate without paying for the Node toolchain.
	skipFrontends bool
	paths         []string
	cfg           *config.ProjectConfig
	cwd           string
	// scope is `forge lint --scope`; nil is the whole project. Only the
	// structured drivers (collectLintOutcomes) read it — see
	// lint_structured.go.
	scope *lintScope

	// allowSkip is the lanes this run accepts may not run (--allow-skip and
	// forge.yaml lint.allow_skip), lane name → reason. See lint_skip.go.
	allowSkip skipAllowances

	// targeted reports that the lane was named on the command line
	// (`forge lint --tracing`) rather than run by the full sweep. The
	// tracing lane fixes its warnings too when it was named.
	targeted bool

	// progress, when set, gets one line as each lane starts, so a long
	// human run shows where it is. Nil in the machine-readable modes.
	progress io.Writer

	// golangciMemo is this run's golangci-lint resolution (see
	// golangci_toolchain.go), so the gating lane and the advisory guardrail
	// share one binary — and at most one provisioning build. A pointer, so
	// the per-step scoped copies the structured driver makes share it; the
	// run's constructor sets it.
	golangciMemo *golangciMemo
}

// linterStep is one entry in the ordered `forge lint` pipeline. See the
// file header for the field contract.
type linterStep struct {
	name  string
	gates bool
	// scope declares how the lane honours `forge lint --scope`
	// (scopeByFile / scopeByPackages / scopeWholeProject). Required: the
	// zero value is invalid, so a new lane must decide.
	scope scopeMode

	// gatesIn, when set, narrows gates for this project: a lane the
	// project dialled to advisory (lint.frontend.typecheck: warn) does not
	// fail the run when it could NOT run either, since its findings could
	// not fail it (lint_skip.go). nil means gates as declared.
	gatesIn func(rc *lintRunCtx) bool

	// shouldRun reports whether the step executes. When it returns
	// run=false with a non-empty skipMsg, the verdict names the skip and
	// JSON mode emits a skippedFinding(skipMsg). A false/empty pair means
	// "silently absent" (directory not present) — no output.
	shouldRun func(rc *lintRunCtx) (run bool, skipMsg string)

	// collect runs the lane. The returned bool is the per-step gating
	// verdict before lint.rules grades it. A non-nil error is a hard
	// collection failure, converted by the engine into an "external"
	// finding whose severity/gating is governed by step.gates.
	collect func(rc *lintRunCtx) ([]lintJSONFinding, bool, error)
}

// lintPipeline returns the ordered linter table. Step numbers in the
// comments are the historical labels (gaps — 3, 6, 12 — are intentional;
// they tracked removed linters and are preserved for diffability against
// git history).
//
//nolint:funlen // declarative 14-entry linter registry, not branching complexity
func lintPipeline() []linterStep {
	return []linterStep{
		// 1. Standard Go linters (golangci-lint).
		{
			name:  "golangci-lint",
			gates: true,
			// Takes package arguments, so --scope narrows what it RUNS on
			// (the slow lane), not just what it reports.
			scope: scopeByPackages,
			// Always runs. A golangci-lint missing from PATH is NOT a skip:
			// it used to be, and `forge lint` passed on a machine that had
			// never run golangci-lint (reliant's CI). The resolution builds
			// forge's pin instead, or fails this lane naming the fix.
			// The one exception is a project with no Go module at all (a
			// static site, config.ProjectConfig.HasGoModule): there is no
			// package to lint, and golangci-lint fails on `./...`. That is a
			// lane that does not apply, so it skips silently.
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cfg != nil && !rc.cfg.HasGoModule() {
					return false, ""
				}
				return true, ""
			},
			// The binary is chosen per module (golangci_toolchain.go): the
			// one on PATH may be missing, built with a Go older than the
			// module's go directive, or unable to read the export data the
			// module's toolchain writes, and each way golangci-lint cannot
			// lint it.
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, g := collectGolangciLintJSON(rc)
				return fs, g, nil
			},
		},

		// 1b. Typed-config guardrail (forbidigo) — ADVISORY arm of
		// config.enforce_typed_access. Only runs in `warn` mode: there the
		// generated .golangci.yml deliberately omits forbidigo from its
		// gating `linters.enable` list, so this non-gating step surfaces the
		// os.Getenv / os.LookupEnv / os.Environ findings as warnings. In
		// `error` mode forbidigo is enabled in the main gating golangci run
		// (step 1) and this step is skipped; `off` skips it too.
		//
		// Why the warn/error switch lives in .golangci.yml's linters.enable
		// (not here, by having forge own the gating decision): the PRIMARY
		// consumer of the guardrail is CI, which runs `golangci-lint run`
		// DIRECTLY via golangci-lint-action — it never routes through
		// `forge lint`. So `linters.enable` membership is the only thing that
		// can make CI fail. Centralizing the decision in `forge lint` would
		// silently stop gating CI in error mode. This step exists purely to
		// give warn-mode users LOCAL visibility of findings golangci is
		// configured to ignore.
		{
			name:  "typed-config guardrail",
			gates: false,
			scope: scopeByPackages,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cfg == nil || rc.cfg.Config.EffectiveEnforceTypedAccess() != config.EnforceTypedAccessWarn {
					return false, ""
				}
				// No Go module, no env reads to guard (see golangci-lint).
				if !rc.cfg.HasGoModule() {
					return false, ""
				}
				// No PATH check: it shares the gating lane's resolution,
				// which provisions a missing golangci-lint.
				return true, ""
			},
			collect: collectTypedAccessGuardJSON,
		},

		// 2. Contract interface enforcement.
		{
			name:  "contract linter",
			gates: true,
			scope: scopeByPackages,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cfg != nil && !rc.cfg.Features.ContractsEnabled() {
					return false, "contracts feature disabled — skipping contract linter"
				}
				// No availability gate: the analysis runs in-process
				// (contract_inprocess.go) — there is no separate
				// contractlint binary to be missing or stale.
				return true, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				// The analysis loads the untagged build only: a scope reaching
				// nothing but tagged Go has nothing for it, and go/packages
				// fails such a package ("build constraints exclude all Go
				// files") rather than reporting nothing.
				paths, run := defaultPassPackages(rc.moduleDir(), rc.paths, nil)
				if !run {
					return nil, false, nil
				}
				return collectContractLintJSON(rc.ctx, paths, contractExcludesFromConfig(rc.cfg), contractGateOptions(rc.cfg, rc.strict))
			},
		},

		// 4. Buf lint.
		{
			name:  "buf lint",
			gates: true,
			scope: scopeByFile,
			// Always runs: with no buf.yaml there is nothing to lint and the
			// lane passes trivially; with one and no buf on PATH the lane
			// FAILS (errBufMissing) rather than skipping — a gating lane
			// that did not run must never let `forge lint` pass.
			shouldRun: func(*lintRunCtx) (bool, string) { return true, "" },
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, g := collectBufLintJSON(rc.ctx)
				return fs, g, nil
			},
		},

		// 5. Frontend linters (eslint / stylelint via npm scripts).
		// TypeScript typechecking is step 5b, not this one — see there.
		{
			name:  "frontend lint",
			gates: true,
			// The frontend's own `npm run lint` over the whole frontend: its
			// output is eslint's grouped report, not file:line findings, so
			// nothing can attribute it to a slice.
			scope: scopeWholeProject,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.skipFrontends {
					return false, "--skip-frontends — skipping frontend lint"
				}
				// No declared frontend and no frontends/ directory: a silent
				// no-op, as the typecheck lane below already treats it. It
				// used to "run" over nothing and count as a gating linter that
				// passed, and a --scope run named it as a lane left unchecked
				// on a project that has no frontend at all. A DECLARED
				// frontend whose directory is missing still runs, so it is
				// reported as could-not-run rather than vanishing.
				if len(frontendDirsForLint()) == 0 {
					return false, ""
				}
				return true, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, g := collectFrontendLintJSON(rc)
				return fs, g, nil
			},
		},

		// 5b. Frontend TypeScript typecheck. forge generates these
		// frontends, so forge — not the caller's shell loop — owns knowing
		// where they live and how to typecheck them. Its own step (rather
		// than a limb of step 5) so it has a name in the table, its own
		// severity dial (lint.frontend.typecheck), and can be skipped
		// independently; the lane's full rationale is in
		// lint_frontend_typecheck.go.
		//
		// gates=true is the STEP's error-reporting posture. Whether a type
		// error actually fails the run is decided inside the lane by the
		// severity dial, which is what a project downgrades or disables —
		// so `warn` mode returns nil here and never trips this gate.
		{
			name:    "frontend typecheck",
			gates:   true,
			gatesIn: func(rc *lintRunCtx) bool { return typecheckGates(rc.cfg) },
			scope:   scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.skipFrontends {
					return false, "--skip-frontends — skipping frontend typecheck"
				}
				if rc.cfg != nil && rc.cfg.Lint.Frontend.EffectiveTypecheck() == "off" {
					return false, "lint.frontend.typecheck is \"off\" — skipping frontend typecheck"
				}
				// No declared frontend and no frontends/ directory: a clean
				// silent no-op, not a finding. A backend-only project must
				// not be told about a lane that does not apply to it.
				if len(frontendTypecheckTargets(rc.cfg)) == 0 {
					return false, ""
				}
				return true, ""
			},
			collect: collectFrontendTypecheckJSON,
		},

		// 5c. Frontend lockfile drift. A package.json whose dependency
		// ranges the lockfile does not record fails every frozen install
		// (`npm ci`), and forge's own web-runtime floor bump used to cause
		// it. Offline and cheap: it reads two files per frontend. See
		// lint_frontend_lock.go.
		{
			name:  "frontend lockfile",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.skipFrontends {
					return false, "--skip-frontends — skipping frontend lockfile check"
				}
				return rc.cwd != "" && len(frontendTypecheckTargets(rc.cfg)) > 0, ""
			},
			collect: collectFrontendLockJSON,
		},

		// 7. SQL migration safety lint.
		{
			name:  "migration safety lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cfg != nil && !rc.cfg.Features.MigrationsEnabled() {
					return false, "migrations feature disabled — skipping migration safety lint"
				}
				return true, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectMigrationSafetyJSON(rc.cfg)
			},
		},

		// 8. Forge convention rules (proto + internal-package contracts).
		// Errors gate the build; warnings are surfaced but tolerated.
		{
			name:  "forge convention lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// Also runs for a frontend-only project: the forge-owned
				// dotenv rule (collectConventionFindings) needs to see a
				// project that declares a frontend or has a frontends/ dir.
				if dirExists("proto") || dirExists("internal") || dirExists("frontends") {
					return true, ""
				}
				if rc.cfg != nil && len(rc.cfg.Frontends) > 0 {
					return true, ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectConventionsJSON(forgeconv.LintOptions{Strict: rc.strict})
			},
		},

		// 10. Scaffold ownership lint — gen-header errors gate the build;
		// surviving FORGE_SCAFFOLD markers (scaffold-not-customized) are
		// warnings, since a fresh scaffold always carries them.
		{
			name:  "scaffold ownership lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return true, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectScaffoldsJSON(rc.cwd)
			},
		},

		// 10b. Generated-file drift — hand-edits to files carrying the
		// `Code generated by forge. DO NOT EDIT.` banner. ERROR-gated: the
		// next `forge generate` destroys those edits, and lint is the only
		// check that runs between the edit and that regenerate (rationale
		// in lint_generated_drift.go). Unlike the generate-time stomp
		// guard, this scan is unscoped — every drifted forge-owned file
		// gates, not just the ones this invocation's emitters would touch.
		{
			name:  "generated-file drift lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// Ownership state is project-scoped: outside a forge project
				// there is nothing forge claims to own. Silent skip.
				if rc.cwd == "" || !fileExists("forge.yaml") {
					return false, ""
				}
				return true, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectGeneratedDriftJSON(rc.cwd)
			},
		},

		// 11. Test-convention lint: the handler and hook-test PRESENCE
		// rules, both warnings. The hook-test SHAPE rule, which gates (a
		// starter row asserting the opposite kind from its hook can never
		// pass), runs in the forge convention lint (step 8) — the lane
		// projects gate on.
		{
			name:  "test convention lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if dirExists("internal/handlers") || dirExists("frontends") {
					return true, ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectTestsJSON(rc.cwd)
			},
		},

		// 11b. Lifecycle-banner lint — forge repo only; warnings only.
		{
			name:  "banner lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if dirExists(filepath.Join("internal", "templates")) ||
					dirExists(filepath.Join("internal", "packs")) {
					return true, ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectBannersJSON(rc.cwd)
				return fs, false, err
			},
		},

		// 13c. Optional-deps-guard — flags unguarded derefs of
		// `// forge:optional-dep` Deps fields. Warnings only.
		{
			name:  "optional-deps-guard lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// All component trees (handlers/workers/operators + internal
				// packages) now live under internal/, so a single check covers
				// every project that has any wireable component.
				if dirExists("internal") {
					return rc.cwd != "", ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectOptionalDepsGuardJSON(rc.cwd)
				return fs, false, err
			},
		},

		// 13d. Config-deps — flags scalar Deps fields (configuration, not
		// collaborators). Warnings only.
		{
			name:  "config-deps lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// All component trees (handlers/workers/operators + internal
				// packages) now live under internal/, so a single check covers
				// every project that has any wireable component.
				if dirExists("internal") {
					return rc.cwd != "", ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectConfigDepsJSON(rc.cwd)
				return fs, false, err
			},
		},

		// 13d-bis2. Composition graph — every component and provider is
		// reached by a root, and every listener serves its chain's
		// audience. GATES on errors: in graph mode a service no root
		// reaches is a service no process serves, which the boot-time
		// RequireComplete gate used to fail on. A whole-graph fact, so it
		// cannot be scoped to a file.
		{
			name:  "graph lint",
			gates: true,
			scope: scopeWholeProject,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if !dirExists("internal/app") {
					return false, ""
				}
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectGraphJSON(rc.cwd, rc.cfg)
				return fs, anyErrorFinding(fs), err
			},
		},

		// 13d-ter. Component-drift — reports components the code declares
		// that deploy/kcl/workloads.k does not (and vice versa). forge does
		// not regenerate that file, so this is what notices when the two
		// drift apart. Warnings only: both directions have legitimate
		// deliberate cases (see lint_component_drift.go).
		{
			name:  "component-drift lint",
			gates: false,
			// Compares the code's components against deploy/kcl/workloads.k:
			// a fact about the two trees together, not about any one file.
			scope: scopeWholeProject,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cwd == "" || rc.cfg == nil {
					return false, ""
				}
				// Only meaningful once deploy is scaffolded. A project
				// without workloads.k has nothing to drift from.
				if !codegen.WorkloadsKCLExists(rc.cwd) {
					return false, ""
				}
				return true, ""
			},
			collect: collectComponentDriftJSON,
		},

		// 13d-quater. Hosted-image-base — flags a host-bearing image on a
		// forge.OnHosted item, which the control plane admits only from its
		// own registry (ADR-0003 F1). Warnings only: `forge env render <env>`
		// performs the authoritative check against the real render, and this
		// text scan under-reports by construction, so it must not gate.
		{
			name:  "hosted-image-base lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.cwd == "" {
					return false, ""
				}
				// Nothing to judge without a deploy tree.
				if !dirExists(filepath.Join(rc.cwd, deployKCLDirFor(rc.cfg))) {
					return false, ""
				}
				return true, ""
			},
			collect: collectHostedImageBaseJSON,
		},

		// 13d-ter-bis. PDB-blocks-drain — a PodDisruptionBudget that can
		// never allow a disruption (see lint_pdb.go). Warnings only.
		{
			name:  "pdb-blocks-drain lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(filepath.Join(rc.cwd, deployKCLDirFor(rc.cfg))), ""
			},
			collect: collectPDBJSON,
		},

		// 13d-ter-ter. Flux-safe — a Job with a TTL or without a spec hash is
		// wrong only under a reconciler (see lint_flux_safe.go). An error for
		// an env a reconciler may apply, a warning for one forge applies.
		{
			name:  "flux-safe lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(filepath.Join(rc.cwd, deployKCLDirFor(rc.cfg))), ""
			},
			collect: collectFluxSafeJSON,
		},

		// 13d-ter-quater. Secrets — the platform secrets model judged per
		// env (see lint_secrets.go and internal/lint/secrets). Gates on an
		// error finding; a project that declares none of the model is not
		// rendered and passes.
		{
			name:  "secrets lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(filepath.Join(rc.cwd, deployKCLDirFor(rc.cfg))), ""
			},
			collect: collectSecretsJSON,
		},

		// 13d-quater. identity-share-with: a forge.ManagedIdentity share_with
		// whose owner env is missing, declares no identity, is itself a sharer
		// or is not hosted. One env's render cannot see another's, so this
		// judges them together (see lint_identity_share.go).
		{
			name:  "identity-share-with lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(filepath.Join(rc.cwd, deployKCLDirFor(rc.cfg))), ""
			},
			collect: collectIdentityShareJSON,
		},

		// 13d-bis. Column-markers — flags a COMMENT ON TABLE/VIEW/COLUMN/
		// CONSTRAINT carrying a forge: marker that object does not read
		// (see lint_column_markers.go). Gates: `forge generate` refuses the
		// same comment, and this is its file:line half.
		{
			name:  "column-markers lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if dirExists(migrationsDirFor(rc.cfg)) {
					return true, ""
				}
				return false, ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectColumnMarkersJSON(rc.cfg)
				return fs, len(fs) > 0, err
			},
		},

		// 13d-ter. Closed-sets — a CHECK declared `forge:closed-set <set>`
		// must list exactly that Go set (see lint_closed_sets.go). Gates: a
		// value Go grants and the CHECK refuses fails every write of it.
		{
			name:  "closed-sets lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(migrationsDirFor(rc.cfg)), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectClosedSetsJSON(rc.cwd, rc.cfg)
				return fs, len(fs) > 0, err
			},
		},

		// 13d-ter-bis. DB-grants — a credential-shaped column nobody declared
		// forge:secret or forge:readable. Gates: the read-only role is built
		// to be safe by construction, and an undecided column is a decision
		// somebody skipped (see lint_db_grants.go).
		{
			name:  "db-grants lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && dirExists(migrationsDirFor(rc.cfg)), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectDBGrantsJSON(rc.cwd, rc.cfg)
				return fs, len(fs) > 0, err
			},
		},

		// 13d-quinquies. ShellBuild-tokens — flags a retired forge
		// substitution token (${TARGETARCH}, ${IMAGE}, …) left in a
		// ShellBuild cmd. GATES, unlike its advisory neighbours, because
		// `GOARCH=${TARGETARCH}` becomes `GOARCH=` and silently builds for
		// the host arch — see lint_shellbuild_tokens.go.
		{
			name:  "shellbuild-tokens lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// No env KCL means no ShellBuild to check.
				return dirExists(deployKCLDirDefault), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectShellBuildTokensJSON(deployKCLDirDefault)
			},
		},

		// 13d-ter. Proto-markers — the same check one layer up: flags a
		// .proto comment whose text contains forge: but matches no known
		// proto marker (see lint_proto_markers.go). A misspelled marker
		// compiles fine and does nothing, so this is the only place it
		// surfaces. Warnings only.
		{
			name:  "proto-markers lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				// No proto tree at all (CLI / library projects) → silent
				// skip, matching the column check's missing-migrations arm.
				return dirExists(protoDirDefault), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectProtoMarkersJSON(protoDirDefault)
				return fs, false, err
			},
		},

		// 13d-ter-bis. Go-markers — the same check for .go comments, which
		// GATES: a `//forge:` directive comment has one purpose, so a name no
		// recognizer reads, or a known marker in a spelling its parser
		// ignores (`//forge:exclude-contract — why`), is a no-op the author
		// cannot see (lint_go_markers.go).
		{
			name:  "go-markers lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectGoMarkersJSON(rc.cwd)
			},
		},

		// 13d-quater. Create-nullability — a field's `optional` label must
		// agree between an entity message and its Create<Entity>Request.
		// The Create request is the one envelope that FLATTENS the entity,
		// so it re-declares the label, and a label lost in the
		// re-declaration collapses absent and zero: the create writes ""
		// into a nullable column and postgres answers with a foreign-key
		// violation naming a constraint rather than the proto line that
		// caused it. GATES when forge generates the create (those are the
		// op's failure modes); a hand-written create owns what omission
		// means, so it warns or passes (lint_create_nullability.go).
		{
			name:  "create-nullability lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return dirExists(protoDirDefault), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectCreateNullabilityJSON(protoDirDefault, rc.cwd)
			},
		},

		// 13d-quinquies. Computed-fields — a `// forge:computed` field is a
		// DECLARED obligation that something derives the value. Nothing
		// forge generates does, so an unmet obligation means the insert
		// takes the column default: $0.00 money with no error anywhere,
		// found only by a human reading a screen.
		//
		// GATES, at least as hard as its read-only twin: computed is the
		// stronger promise ("my app derives this"). It used to warn on the
		// theory that a marker added before its hook is a legitimate
		// intermediate state; the deterministic form of that state —
		// forge's own unwired stubs still in the service — is pending-stub
		// mode, which holds the finding at warning and names the stubs
		// (lint_pending_stubs.go). Everything else fails.
		{
			name:  "computed-fields lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return dirExists(protoDirDefault) && rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectComputedFieldsJSON(rc.cwd)
				return fs, anyErrorFinding(fs), err
			},
		},

		// 13d-sexies. Read-only-fields — the twin of computed-fields for the
		// marker where the same failure is SILENT. `// forge:read-only`
		// strips a field from every write envelope; when nothing else
		// populates the column and its DEFAULT is the type's zero, every
		// row ships as 0 — no constraint violated, no test failed (the born
		// CRUD test derives its fixtures from the same schema and agrees
		// with the defect by construction), no log line. Schema-gated to
		// stay quiet on GENERATED columns, managed timestamps, and real
		// defaults.
		//
		// GATES, like its computed-field twin. An unwritten read-only
		// column is a shipped defect whose ONLY symptom is a human reading
		// $0.00 on a screen. A warning inside a hundred-line lint run is
		// very close to the "no log line anywhere" the rule exists to fix,
		// and the audited run caught its two only because someone was
		// deliberately looking for them. The one exception is pending-stub
		// mode: right after `forge scaffold` the rpc that will write the
		// column is still forge's own unwired stub, so the finding is a
		// warning naming those stubs until they are implemented
		// (lint_pending_stubs.go).
		//
		// Gating is defensible because every exclusion is pinned by a test
		// (GENERATED, managed timestamps, real defaults, NOT NULL-without-
		// DEFAULT, forge:fill=, forge:computed), and because the two write
		// paths the Go scan structurally could not see — a trigger body and
		// a column-named UPDATE — are now read out of the migrations and
		// the Go SQL literals. See columnsWrittenBySQL.
		{
			name:  "read-only-fields lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return dirExists(protoDirDefault) && rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectReadOnlyFieldsJSON(rc.cwd, migrationsDirFor(rc.cfg))
				return fs, anyErrorFinding(fs), err
			},
		},

		// 13d-sexies-bis. Fixture-drift — scaffold-once lifecycle tests
		// born with LITERAL fixture SQL, aging out of a schema the author
		// keeps hardening. A later migration that makes a column GENERATED,
		// adds a UNIQUE, a foreign key or a one-way status CHECK turns a
		// seeded row into a pq error in test SETUP, naming neither the
		// fixture nor the migration.
		//
		// The lane EXECUTES each fixture statement against the shadow
		// schema in a rolled-back transaction and reports postgres's own
		// verdict per statement — every failure class, not the few a text
		// matcher anticipated. It used to be three matchers in two lanes
		// (this one and crud-fixtures); a CHECK added after birth matched
		// none of them, so the lanes reported clean over fixtures postgres
		// rejects. Newly scaffolded lifecycle tests carry no literal SQL
		// (they call the regenerated create-request factories), so for them
		// this lane costs nothing and opens no database.
		//
		// Warnings only, matching guarded-fields: the fix is an edit to a
		// file forge does not own and may legitimately be mid-edit, so
		// gating would be a generator holding a user's file hostage.
		{
			name:  "fixture-drift lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectFixtureDriftJSON(rc.cwd, rc.cfg)
				return fs, false, err
			},
		},

		// 13d-sexies-ter. Time-bucketing — the reporting-query twin of
		// read-only-fields, and the same silent-wrong-data class. forge
		// maps google.protobuf.Timestamp to TIMESTAMPTZ, which is right,
		// but a two-argument `date_trunc` over one truncates in the
		// SESSION timezone — which the driver sets from the CLIENT HOST.
		// So the same rows bucket differently on a UTC CI box and a
		// UTC-5 laptop, and every reported total is attributed to the
		// wrong day by a fraction of one.
		//
		// Nothing fails: no constraint, no type error, no failing test,
		// no log line. The chart renders and the bars are simply wrong,
		// which reads as flakiness rather than as a timezone bug. It is
		// also squarely in forge's path — the generated ORM cannot
		// express GROUP BY / SUM, so forge itself routes authors to raw
		// SQL for any reporting screen, which is exactly where this
		// construct lives.
		//
		// Warnings only, matching the fixture lanes: a local-time bucket
		// can be deliberate for a shift report or a business-day rollup,
		// and naming the zone explicitly is how the author says so.
		{
			name:  "time-bucketing lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectTimeBucketingJSON(rc.cwd, rc.cfg)
				return fs, false, err
			},
		},

		// 13d-sexies-quater. Static-export — a Next.js frontend served by a
		// static runtime (or declared `output: static`) must not use what an
		// export cannot serve: a `[id]` route without generateStaticParams,
		// a server action, cookies(), next/image's server loader, a rewrite.
		// `next dev` serves all of them, so nothing else notices before the
		// export fails in CI — or, for the constructs the export accepts,
		// before production quietly lacks what dev had.
		//
		// GATES on error findings: each is a build `next build` refuses, or
		// a build that is not an export at all (nothing to publish). The
		// silently-dropped constructs are warnings. Scoped by file: every
		// finding is anchored to the source line that causes it.
		{
			name:  "static-export lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				if rc.skipFrontends {
					return false, "--skip-frontends — skipping static-export lint"
				}
				return rc.cwd != "" && hasNextJSFrontend(rc.cfg), ""
			},
			collect: collectStaticExportJSON,
		},

		// 13d-septies. Guarded-fields — the marker's blind spot. The page
		// generator honours `forge:guards` for every page it emits, but
		// pages are WRITE-IF-ABSENT: the natural order (scaffold CRUD,
		// write the custom rpc, then add the marker) leaves an edit page
		// whose update_mask still names the guarded column, and no
		// regenerate will ever touch it again. The frontend typechecks,
		// nothing logs, and the symptom is a user saving a form and
		// getting a 500 from the raw CHECK the rpc exists to avoid.
		// Warnings only — the file is the user's, so forge must not
		// rewrite it.
		{
			name:  "guarded-fields lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return dirExists(protoDirDefault) && rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectGuardedFieldsJSON(rc.cwd, frontendDirsForLint())
				return fs, false, err
			},
		},

		// 13e. Tracing — the places a request's trace is cut: a log line
		// written without the context in scope (no trace_id), an untraced
		// *http.Client or database pool, a contract constructed without its
		// generated decorator. GATES on error findings; a repo adopting a
		// rule dials it to warn in lint.rules. Under --fix the full sweep
		// rewrites only where a rule is at error; `forge lint --tracing
		// --fix` is the migration. See lint_tracing.go.
		{
			name:  "tracing lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && hasGoModule(rc.cwd), ""
			},
			collect: collectTracingJSON,
		},

		// 14b. No-dotenv — forge projects declare config in KCL and keep
		// secret VALUES in the secret store. A .env file bypasses
		// both (it is injected wholesale, so nothing has to be
		// declared), so its presence is an error, not a warning.
		{
			name:  "no-dotenv lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectNoDotenvJSON(rc.cwd)
				return fs, len(fs) > 0, err
			},
		},

		// 14c. Commit policy — generated code is committed; machine-local
		// state (.forge-kcl/, a frontend's dev public/config.js) is not.
		// ERROR-gated: an ignored generated file breaks every fresh clone
		// and CI checkout, and `forge ci verify-generated` (git status)
		// cannot see it — this is the check that can. See
		// internal/commitpolicy.
		{
			name:  "commit-policy lint",
			gates: true,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "" && fileExists("forge.yaml"), ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				return collectCommitPolicyJSON(rc.cwd)
			},
		},

		// 14. Check-workarounds — flags canonical cross-lane workarounds.
		// Warnings only.
		{
			name:  "check-workarounds lint",
			gates: false,
			scope: scopeByFile,
			shouldRun: func(rc *lintRunCtx) (bool, string) {
				return rc.cwd != "", ""
			},
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				fs, err := collectWorkaroundsJSON(rc.cwd)
				return fs, false, err
			},
		},
	}
}
