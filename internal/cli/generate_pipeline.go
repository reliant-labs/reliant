// Package cli — typed step plan for `forge generate`.
//
// Pre-2026-05-06, runGeneratePipeline was a 584-line procedural function
// holding 25 numbered ordered steps gated by 93 Features.*Enabled() checks.
// New steps were 30 lines of boilerplate appended to a numbered-comment
// sequence (Step 0a, 0b, 0b.1, ..., 8d-iii, 8f.1) — the numbering itself
// pleaded for a data structure (FORGE_REVIEW_CODEBASE.md Tier 1.1).
//
// This file replaces that procedural blob with a typed []GenStep plan.
// Each step is a small named function operating on a shared
// pipelineContext. The pipeline becomes a loop over the slice that:
//   - calls step.Gate(ctx) — pure, side-effect-free predicate; false skips
//   - calls step.Run(ctx) — the action, returning an error to abort
//
// The shape unblocks several downstream wins documented in the codebase
// review:
//   - --plan / --explain print the plan without executing it
//   - a watch loop re-runs only steps whose Tag matches changes
//   - per-step unit tests against a synthetic pipelineContext
//   - one-time parse of services/entities into ctx (avoids re-parsing
//     ParseEntityProtos 3× — see Tier 2.5)
//
// As of 2026-05-07 the entire pre-refactor pipeline is now flat: every
// numbered legacy step has a dedicated stepXxx entry below, and
// runMidPipelineLegacy is gone. The single remaining shared-state hop
// (parse services + module path once for steps 4-6) is its own GenStep.
//
// (2026-05-06 polish-phase, completed 2026-05-07) — closes
// FORGE_REVIEW_CODEBASE.md Tier 1.1.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/kclmigrate"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/internal/projectstore"
	"github.com/reliant-labs/forge/internal/shellbuildtokens"
)

// GenStep is one ordered unit of the generate pipeline. Steps are pure
// data plus two functions: Gate decides whether the step should run
// for the current pipelineContext (must be side-effect free), and Run
// executes the step. Tag categorizes the step for filtering by future
// callers (--plan output, watch-mode dispatch).
type GenStep struct {
	// Name is the human-readable label printed by --plan / --explain
	// and used in error wrapping. Replaces the legacy "Step Nx" comment
	// numbering — make these stable; tests pin the order.
	Name string

	// Gate returns true when the step should execute for this context.
	// Gates MUST be pure: no I/O, no mutation. They're called by
	// stability tests and may be called repeatedly by future watch-mode
	// dispatchers.
	Gate func(*pipelineContext) bool

	// GateReason is the human-readable explanation rendered when Gate
	// returns false. Printed by `--plan` `[SKIP]` lines and by the
	// `--verbose` `⏩ skipped:` lines instead of the function-name-derived
	// `gate: <gateName>` label, which carried no semantic signal for
	// users debugging "why didn't generate touch X?".
	//
	// Conventions:
	//   - Phrase as the FALSE condition ("no services in forge.yaml",
	//     "features.codegen=false") so the user reads the skip reason
	//     directly without having to mentally invert a positive statement.
	//   - Keep under ~60 chars to fit a single `[SKIP] <name> (<reason>)`
	//     line at the column width `--plan` already uses (44 + reason).
	//   - Empty string falls back to the legacy "gate <gateName> returned
	//     false" rendering — useful during incremental adoption.
	GateReason string

	// Run is the action. It may mutate ctx (parsed services, derived
	// flags, checksums) so subsequent steps can reuse the work. Errors
	// abort the pipeline; warnings should be logged in-step.
	Run func(*pipelineContext) error

	// Tag categorizes the step for future filtering. Conventional
	// values: "config", "proto", "codegen", "migrations", "frontend",
	// "deploy", "tools", "validate".
	Tag string

	// Writes declares the project-relative paths (files, or directories,
	// captured recursively) an EXTERNAL tool this step runs may write.
	// The pipeline captures them into the rollback journal immediately
	// before Run, so a failed run restores tool output exactly as it
	// restores forge's own writes. Required for every step that shells a
	// tool which writes into the project (buf, go mod tidy, …) —
	// pinned by TestGenerateStepsDeclareExternalToolOutputs. See
	// generate_external_writes.go.
	Writes func(*pipelineContext) []string

	// ReadOnly declares that the step never writes to the project tree,
	// by any route — forge's journaled writers, a raw os.WriteFile, or an
	// external tool. It is what lets a failed run say "No files were
	// changed" as a computed fact: the claim is made only when every step
	// that ran is ReadOnly AND the rollback journal is empty. Default
	// false, so a step that is not explicitly vouched for can never
	// produce the claim. Pinned by TestGenerateStepsReadOnlySet.
	ReadOnly bool
}

// pipelineContext is the shared state passed between steps. Steps may
// read from it freely and write to it via the explicit fields. Anything
// derived from the project (services, entity defs, module path, has*
// flags) lives here so we parse once at step 0 instead of 3× across
// the pipeline (see FORGE_REVIEW_CODEBASE.md Tier 2.5).
type pipelineContext struct {
	// graphRoots is the composition graph's roots as processes, set by the
	// internal/app composition step (graphRootsSet) and read by the deploy
	// config step, which projects their config reach into config_gen.k.
	graphRoots    []codegen.GraphRoot
	graphRootsSet bool

	// Inputs (set in newPipelineContextWithFlags).
	ProjectDir string
	AbsPath    string
	Force      bool

	// SkipValidate suppresses the final `go build ./...` step. The
	// validate step is all-or-nothing — a single broken file in package
	// A blocks the validate gate for any unrelated change in package B.
	// During multi-lane migrations a tree spends extended periods in a
	// partial-build state; the `--skip-validate` flag lets the user run
	// `forge generate` when they know their lane is internal (no
	// proto/contract delta) and the rest of the tree's brokenness is
	// being worked on elsewhere. FRICTION 2026-06-02: cp-forge
	// per-commit port 8050178.
	SkipValidate bool

	// SoftFailures names each step that failed as a warning (warnOrFail
	// without --strict), for the closing summary.
	SoftFailures []string

	// ORMFailure is set when the ORM step failed, including when it was a
	// warning (no --strict) and the run went on. Steps that consume the ORM
	// read it to report themselves skipped, with the reason, rather than
	// emit or claim output built on an ORM that was never written.
	ORMFailure error

	// SkipPreChecks bypasses the pre-codegen Step 0c contract-shape
	// check (LintInternalContracts). Same motivation as SkipValidate:
	// during parallel-lane work an unrelated `internal/<other-lane>/
	// contract.go` violation would otherwise block regen across every
	// lane. Distinct from SkipValidate — that one gates the FINAL
	// `go build`; this one gates the PRE-codegen guard. Both are
	// available so the user can opt-out of either side independently.
	SkipPreChecks bool

	// SkipConfigCheck bypasses the forge.yaml ↔ filesystem cross-check
	// that stepLoadConfig runs after a successful LoadProject. See the
	// SkipConfigCheck field on pipelineFlags for the rationale.
	SkipConfigCheck bool

	// Strict promotes "Warning: ... failed" sites into hard errors via
	// the warnOrFail helper. See pipelineFlags.Strict for the rationale.
	Strict bool

	// Verbose toggles per-step skip messages. See pipelineFlags.Verbose
	// for the rationale; consumed by the gate-loop in runGeneratePipelineFlags.
	Verbose bool

	// ForceCleanup makes the stale-artifact sweep delete hand-edited
	// stale files too. Without it the sweep deletes only pristine ones —
	// certified files no emitter wrote this run, byte-identical to what
	// forge rendered, so a run mid-way through a rename loses nothing
	// the next run cannot re-render — and lists the hand-edited ones,
	// whose bytes are the user's. --force-cleanup is the user's answer
	// to that list (generate_cleanup.go).
	ForceCleanup bool

	// TemplatesOnly narrows the pipeline to template-driven render
	// steps. See the matching pipelineFlags.TemplatesOnly field for the
	// full rationale and the templatesOnlyStepAllow allowlist.
	TemplatesOnly bool

	// ExplainDrift means: on Tier-1 drift, don't abort at the guard —
	// redirect drifted paths' writes to .forge/render/ side renders,
	// run the pipeline, diff on-disk vs fresh render at the end, then
	// fail with the standard drift report. Explains, never approves.
	// Mechanics live in generate_explain_drift.go.
	ExplainDrift bool

	// ExplainDriftEntries is populated by prepareExplainDrift when
	// ExplainDrift triggers: the drift set the guard detected. The
	// drifted files themselves are never touched (their writes are
	// side-render redirected), so no state snapshot is needed — the
	// truth lives in the files.
	ExplainDriftEntries []checksums.Tier1DriftEntry

	// DriftPending is the in-scope drift the stomp guard quarantined
	// instead of failing on: each path's writes are side-render
	// redirected, and the fresh-render check step settles it before
	// validation (generate_drift_rescue.go). DriftMergeState is the git
	// operation in progress when the guard ran ("" when none), for the
	// report of whatever stays unresolved.
	DriftPending    []checksums.Tier1DriftEntry
	DriftMergeState string

	// LegacyUnverified is populated by the one-time legacy-manifest
	// migration (generate_legacy_migrate.go): paths whose on-disk bytes
	// matched nothing the legacy manifest recorded (the fr-9a54388f0b
	// different-lane corruption). Their writes are side-render
	// redirected this run; finishLegacyMigration adjudicates each at
	// the end (fresh-render body match → pristine, stamp; otherwise →
	// unverified-legacy sentinel + drift report).
	LegacyUnverified []string

	// Cfg may be nil — that's the directory-scan fallback path. It is the
	// underlying config of Store (Store.Config()), kept as a field so the
	// many generator-facing pipeline steps that take *config.ProjectConfig
	// don't each have to reach through the store; nil whenever Store is nil.
	Cfg *config.ProjectConfig

	// Store wraps the loaded project, or nil on the directory-scan fallback
	// path. Steps reading project/component/feature state should prefer it;
	// Cfg remains for the config-typed step helpers.
	Store *projectstore.Store

	// Checksums is loaded once at step 0b and saved on pipeline exit
	// by the caller. Steps mutate this in-place via WriteGeneratedFile
	// helpers and the Tier-1 emitters.
	Checksums *generator.FileChecksums

	// Proto-tree presence flags — populated once in stepDetectProtoDirs.
	HasServices  bool
	HasConfig    bool
	HasWorkers   bool
	HasOperators bool

	// Components is the project's component inventory, read from its real
	// sources by stepDiscoverComponents. It is EXPLICIT pipeline state, not
	// a field on Cfg: a config object that sometimes carries components is
	// a config object every step can read an empty answer from without ever
	// having looked. Nil until stepDiscoverComponents runs — which is after
	// descriptor extraction, because the descriptor is one of the sources.
	Components codegen.Inventory

	// Parsed once and reused by mid-pipeline steps. Nil until populated.
	//
	// Services is what forge MANAGES — every service-scoped emitter reads
	// it. DeclaredServices adds the `// forge:unmanaged` ones, and is read
	// only by steps that project the API contract itself (the
	// open-procedure set); see codegen.ParseDeclaredServices.
	Services         []codegen.ServiceDef
	DeclaredServices []codegen.ServiceDef
	ModulePath       string
	EntityDefs       []codegen.EntityDef
	ConfigFields     map[string]bool

	// rendered caches renderedFrontends for the run: the frontends each
	// environment's KCL declares, read once however many steps ask.
	rendered     []renderedEnvFrontend
	renderedRead bool

	// registry is the memoized parse of the user-owned
	// pkg/app/services.go registration file (what this binary serves —
	// see generate_serve.go). Lazily populated via
	// ctx.serviceRegistry(); registryErr is a sticky parse failure so
	// every consuming step reports the same pointed error.
	registry       *serviceRegistry
	registryErr    error
	registryLoaded bool

	// PriorExports holds the pre-codegen snapshot of each Tier-1 Go
	// file's exported top-level identifier names. Populated by
	// stepSnapshotTier1Exports before any codegen step runs; consumed
	// by stepDetectRenamedExports after the codegen passes to diff
	// against the freshly written files. The diff drives the rename-
	// detection warnings (callers of a dropped name may be orphaned).
	// FRICTION 2026-06-02: cp-forge `forgedb.Migrations()` →
	// `forgedb.MigrationsFS` rename left internal/db/migrations.go
	// orphaned with a silent compile error two runs later.
	PriorExports map[string]tier1Exports
}

// tier1Exports is the per-path snapshot captured pre-codegen:
// the file's package name + the sorted list of public top-level
// identifiers. PkgName drives `pkg.Name` search patterns when looking
// for stale callers; Names is the diff-source list.
type tier1Exports struct {
	PkgName string
	Names   []string
}

// newPipelineContextWithFlags builds the initial context. The caller (the
// cobra RunE) wires projectDir + flags; everything else is filled by the
// early steps so a unit test can construct a synthetic context without
// touching disk. New optional
// pipeline toggles land on the pipelineFlags struct rather than as a new
// positional argument — keeps the call-site self-documenting and avoids
// churning every test fixture when a flag is added.
func newPipelineContextWithFlags(projectDir string, flags pipelineFlags) (*pipelineContext, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project dir: %w", err)
	}
	return &pipelineContext{
		ProjectDir:      projectDir,
		AbsPath:         abs,
		Force:           flags.Force,
		ExplainDrift:    flags.ExplainDrift,
		SkipValidate:    flags.SkipValidate,
		SkipPreChecks:   flags.SkipPreChecks,
		SkipConfigCheck: flags.SkipConfigCheck,
		ForceCleanup:    flags.ForceCleanup,
		TemplatesOnly:   flags.TemplatesOnly,
		Strict:          flags.Strict,
		Verbose:         flags.Verbose,
	}, nil
}

// generateSteps returns the ordered step plan. The slice is the
// authoritative ordering — comments alongside each entry replace the
// pre-refactor "Step Nx" numbering. Reorder cautiously; the
// TestGenerateStepsPlanStable test pins the exact sequence.
//
// EXTRACTION STATUS (2026-05-07): complete. Every pre-refactor numbered
// step is now its own GenStep entry; runMidPipelineLegacy has been
// removed. The only step that touches shared state across step
// boundaries (stepParseServicesAndModule) is documented inline.
func generateSteps() []GenStep {
	return []GenStep{
		{Name: "load project config", Gate: always, Run: stepLoadConfig, Tag: "config", ReadOnly: true},
		// The REFUSALS come before anything that can write. A refusal is
		// only honest if it leaves the tree exactly as it found it, and the
		// steps below them are not all read-only: the legacy-manifest
		// migration stamps files, the frontend-inventory render can vendor
		// .forge-kcl/, and the KCL vendor sync rewrites it. The compat check
		// used to run AFTER that sync, so a newer forge refusing a
		// project's pin had already replaced the project's KCL schemas with
		// its own. Neither check reads anything a later step derives (not
		// the frontend inventory, not the checksums), so running them first
		// costs nothing.
		{Name: "forge version compatibility", Gate: gatePreChecksNotSkipped, GateReason: "--skip-pre-checks was passed", Run: stepPkgCompatHandshake, Tag: "validate", ReadOnly: true},
		{Name: "pre-codegen contract check", Gate: gatePreChecksNotSkipped, GateReason: "--skip-pre-checks was passed", Run: stepPreCodegenContractCheck, Tag: "validate", ReadOnly: true},
		{Name: "retired ShellBuild tokens", Gate: gatePreChecksNotSkipped, GateReason: "--skip-pre-checks was passed", Run: stepShellBuildTokens, Tag: "validate", ReadOnly: true},
		// Not skippable: a project on the shared config.v1 layout generates
		// a pkg/config that panics at init beside any other forge project's,
		// and the loader below would silently find no config at the new path.
		{Name: "legacy config proto layout", Gate: always, Run: stepLegacyConfigProto, Tag: "validate", ReadOnly: true},
		{Name: "migrate env registries onto images", Gate: gatePreChecksNotSkipped, GateReason: "--skip-pre-checks was passed", Run: stepMigrateImageRegistry, Tag: "config", Writes: writesKCLMigration},
		// Sits with the other config migration, AFTER the refusals: a
		// refusal is only honest if it leaves the tree as it found it, and
		// this step writes. The load above tolerates the old nesting (the
		// keys are in removedSchemaKeys, so they warn rather than fail),
		// and this step re-loads after rewriting so the migrated values
		// take effect on THIS run rather than the next one.
		// Before every gate and emitter that reads the frontend inventory,
		// so they all see one answer (the two pre-checks above read none). See
		// generate_frontend_inventory.go for why a project can reach here
		// with an empty inventory and a frontend sitting in its own tree.
		{Name: "derive frontend inventory", Gate: hasForgeYAML, GateReason: "no forge.yaml (directory-scan fallback)", Run: stepDeriveFrontendInventory, Tag: "config"},
		{Name: "load checksums", Gate: always, Run: stepLoadChecksums, Tag: "config"},
		// One-time conversion off the dead global manifest. Must run
		// between state load and the Tier-1 stomp guard: pristine files
		// get their forge:hash marker stamped here so the guard reads
		// the right ownership story. See generate_legacy_migrate.go.
		{Name: "migrate legacy checksums manifest", Gate: always, Run: stepMigrateLegacyManifest, Tag: "config"},
		{Name: "check Tier-1 file-stomp guard", Gate: always, Run: stepCheckTier1Drift, Tag: "validate"},
		{Name: "snapshot Tier-1 exports", Gate: always, Run: stepSnapshotTier1Exports, Tag: "validate"},
		// After every refusal and the stomp guard (a refused or drift-aborted
		// generate must not have touched forge.yaml), before anything reads
		// the pin. See generate_forge_pin.go.
		{Name: "reconcile forge_version with go.mod", Gate: hasForgeYAML, GateReason: "no forge.yaml (directory-scan fallback)", Run: stepReconcileForgePin, Tag: "config"},
		{Name: "announce project", Gate: always, Run: stepAnnounceProject, Tag: "config", ReadOnly: true},
		// After every refusal: this rewrites .forge-kcl/ (and kcl.mod), and
		// its writes are journaled so a later failure restores them.
		{Name: "sync forge KCL module vendor", Gate: always, Run: stepSyncForgeKCL, Tag: "config"},
		{Name: "detect proto directories", Gate: always, Run: stepDetectProtoDirs, Tag: "proto"},
		{Name: "ensure gen/go.mod", Gate: hasGoModule, GateReason: noGoModuleReason, Run: stepEnsureGenModule, Tag: "config", Writes: writesGenGoMod},
		{Name: "buf generate (Go stubs)", Gate: gateCodegenEnabled, GateReason: "features.codegen=false", Run: stepBufGenerateGo, Tag: "proto", Writes: writesBufGo},
		{Name: "descriptor extraction", Gate: gateCodegenEnabled, GateReason: "features.codegen=false", Run: stepDescriptorGenerate, Tag: "proto", Writes: writesDescriptor},
		{Name: "OpenAPI specs (protoc-gen-connect-openapi)", Gate: gateOpenAPIEnabled, GateReason: "api.openapi=false or features.codegen=false", Run: stepOpenAPIGenerate, Tag: "proto", Writes: writesOpenAPI},
		{Name: "frontend workspaces scaffold", Gate: and(feature(config.FeaturesConfig.FrontendEnabled), hasForgeYAML), GateReason: "features.frontend=false or no forge.yaml", Run: stepFrontendWorkspaces, Tag: "frontend"},
		{Name: "TypeScript stubs (frontends)", Gate: and(feature(config.FeaturesConfig.FrontendEnabled), hasForgeYAML), GateReason: "features.frontend=false or no forge.yaml", Run: stepFrontendBufTS, Tag: "frontend", Writes: writesFrontendTS},
		{Name: "config loader (proto/<project>/config/v1)", Gate: and(feature(config.FeaturesConfig.CodegenEnabled), hasConfig), GateReason: "no config protos in proto/<project>/config/v1 or features.codegen=false", Run: stepConfigLoader, Tag: "codegen"},
		// parse services: needed when the project has proto/services/
		// (handlers, mocks, CRUD, auth, bootstrap all read ctx.Services) OR
		// workers/operators (bootstrap still needs ModulePath, even if
		// Services is empty). Codegen-disabled projects skip it: the
		// consumers of ctx.Services are all themselves codegen-gated.
		{Name: "parse services + module path", Gate: and(feature(config.FeaturesConfig.CodegenEnabled), hasAnyEntrypoint), GateReason: "no services/workers/operators or features.codegen=false", Run: stepParseServicesAndModule, Tag: "codegen"},
		{Name: "frontend hooks", Gate: gateFrontendHasServices, GateReason: "no services or no frontends, or features.frontend=false", Run: stepFrontendHooks, Tag: "frontend"},
		{Name: "ensure frontend components", Gate: gateFrontendHasFrontends, GateReason: "no frontends (frontends/<name>/ or a KCL forge.Frontend) or features.frontend=false", Run: stepFrontendComponents, Tag: "frontend"},
		{Name: "frontend CRUD pages", Gate: gateFrontendHasServices, GateReason: "no services or no frontends, or features.frontend=false", Run: stepFrontendPages, Tag: "frontend"},
		{Name: "frontend nav + dashboard", Gate: gateFrontendHasFrontends, GateReason: "no frontends (frontends/<name>/ or a KCL forge.Frontend) or features.frontend=false", Run: stepFrontendNav, Tag: "frontend"},
		{Name: "service stubs", Gate: gateCodegenHasServices, GateReason: "no Connect services defined or features.codegen=false", Run: stepServiceStubs, Tag: "codegen"},
		{Name: "internal/db/ ORM (entity-driven)", Gate: gateORMHasServices, GateReason: "no services/workers/operators or features.orm=false", Run: stepInternalDBORM, Tag: "codegen"},
		{Name: "CRUD handlers", Gate: gateCRUDProjection, GateReason: "no Connect services defined, features.codegen=false, or features.orm=false", Run: stepCRUDHandlers, Tag: "codegen"},
		{Name: "service mocks", Gate: gateCodegenHasServices, GateReason: "no Connect services defined or features.codegen=false", Run: stepServiceMocks, Tag: "codegen"},
		{Name: "internal package contracts", Gate: and(gateContractsEnabled, hasGoModule), GateReason: "features.contracts=false, or " + noGoModuleReason, Run: stepInternalContracts, Tag: "codegen"},
		// Auth is owned code now (internal/app/auth.go's SetupAuth),
		// scaffolded alongside providers.go in the internal/app composition
		// step below — there is no auth-config codegen step.
		{Name: "open-procedure set (pkg/middleware)", Gate: gateCodegenHasServices, GateReason: "no Connect services defined or features.codegen=false", Run: stepAuthProcedures, Tag: "codegen"},
		{Name: "webhook routes", Gate: and(feature(config.FeaturesConfig.CodegenEnabled), hasForgeYAML), GateReason: "no forge.yaml or features.codegen=false", Run: stepWebhookRoutes, Tag: "codegen"},
		{Name: "go mod tidy (pre-composition)", Gate: feature(config.FeaturesConfig.CodegenEnabled), GateReason: "features.codegen=false", Run: stepGoModTidyPreComposition, Tag: "tools", Writes: writesBothTidies},
		{Name: "internal/app composition (hybrid DI)", Gate: feature(config.FeaturesConfig.CodegenEnabled), GateReason: "features.codegen=false", Run: stepInternalAppComposition, Tag: "codegen"},
		// The other half of the same projection: with no composition graph
		// there are no roots, and the command tree's root.go still calls
		// rootCommands.
		{Name: "root commands without a composition graph", Gate: and(gateCodegenDisabled, hasGoModule), GateReason: "features.codegen=true (the composition step projects the roots), or " + noGoModuleReason, Run: stepRootCommandsWithoutGraph, Tag: "codegen"},
		// AFTER composition: it projects the roots composition just read.
		{Name: "root env schema (pkg/config/roots_gen.go)", Gate: and(feature(config.FeaturesConfig.CodegenEnabled), hasConfig), GateReason: "no proto/<project>/config/v1/ directory or features.codegen=false", Run: stepRootsEnvSchema, Tag: "codegen"},
		{Name: "go mod tidy (pre-wiring)", Gate: gateCodegenHasAnyEntrypoint, GateReason: "no services/workers/operators or features.codegen=false", Run: stepGoModTidyPreWiring, Tag: "tools", Writes: writesBothTidies},
		{Name: "cmd/commands.go (user extension point)", Gate: gateCodegenHasAnyEntrypoint, GateReason: "no services/workers/operators or features.codegen=false", Run: stepCmdCommands, Tag: "codegen"},
		{Name: "per-service test helpers", Gate: gateCodegenHasAnyEntrypoint, GateReason: "no services/workers/operators or features.codegen=false", Run: stepBootstrapTesting, Tag: "codegen"},
		{Name: "db/embed.go (embedded migrations)", Gate: gateMigrateHasDriver, GateReason: "database.driver unset or features.migrations=false", Run: stepBootstrapMigrate, Tag: "codegen"},
		// The project's own commands (forge.yaml generate.hooks): after
		// forge's codegen, so a hook can read what forge emitted, and before
		// both tidies and the validation build, so what a hook emits is
		// tidied and compiled in this same run.
		{Name: generateHooksStep, Gate: hasGenerateHooks, GateReason: "no generate.hooks in forge.yaml", Run: stepGenerateHooks, Tag: "tools", Writes: writesGenerateHooks},
		{Name: "go mod tidy (gen/)", Gate: hasGoModule, GateReason: noGoModuleReason, Run: stepGoModTidyGen, Tag: "tools", Writes: writesGenTidy},
		// NOT gated on features.deploy. The Tier-1 files this sweep writes
		// each carry their own enabledFor predicate and none is a deploy
		// artifact (deploy/alloy-config.alloy gates on observability, not
		// deploy). A features.deploy=false project still has a command tree
		// and still has migrations, and gating the whole step on deploy has
		// historically withheld a file some scaffold-once code called —
		// leaving an `undefined:` build error with no way to regenerate out
		// of it. Per-file gates are the ones that decide; the step just runs
		// the manifest.
		{Name: "regenerate infra files", Gate: always, Run: stepRegenerateInfra, Tag: "codegen"},
		// MUST follow "regenerate infra files": that step (re)creates
		// cmd/<bin>/cmd/serve.go, which this step requires on disk before it
		// writes anything. Emitting the cmd-group subpackages + the composition
		// root here — after serve.go exists but before "go mod tidy (root)" /
		// "go build (validate generated code)" — is what lets a flat→nested
		// migration reach a buildable state without manual dir pre-creation or
		// skip-flags. See stepCmdGroups for the full ordering rationale.
		// Gate mirrors stepInternalAppComposition (features.codegen only, NOT
		// hasAnyEntrypoint): a degenerate tree with serve.go but zero parsed
		// services still needs cmd/<bin>/main.go and the three group anchors
		// (with zero items) so the tree builds. The step itself no-ops when
		// serve.go is absent (CLI/library/codegen-less trees).
		{Name: "cmd command groups (services/workers/operators)", Gate: feature(config.FeaturesConfig.CodegenEnabled), GateReason: "features.codegen=false", Run: stepCmdGroups, Tag: "codegen"},
		// Derive the deploy component set from the project's real sources
		// (the freshly-extracted proto descriptor) right before the deploy
		// data that consumes it is emitted. See stepDiscoverComponents.
		{Name: "discover components", Gate: hasForgeYAML, GateReason: "no forge.yaml (directory-scan fallback)", Run: stepDiscoverComponents, Tag: "deploy"},
		// deploy/kcl/workloads.k is SCAFFOLDED, never regenerated: it is the
		// user-owned declaration of what the project is made of, and `forge
		// generate` deliberately has no step that rewrites it. This step only
		// creates it when it is absent (an upgrade from a build that predates
		// it, or a project whose deploy/ was scaffolded before the first
		// component existed). Drift is reported by `forge lint`, not repaired.
		{Name: "workloads.k (scaffold once)", Gate: and(gateDeployEnabled, hasGoModule), GateReason: "features.deploy=false, or " + noGoModuleReason + ": there is no workload to declare", Run: stepWorkloadsKCL, Tag: "deploy"},
		// Dashboards are a projection of the services the descriptor and the
		// parsed protos describe, so they follow "parse services + module
		// path". Owned overrides live beside the _gen files; see obsdash.
		{Name: "observability dashboards", Gate: gateObservabilityDashboards, GateReason: "features.observability=false, not a service project, or no services", Run: stepObservabilityDashboards, Tag: "deploy"},
		// Gated on envs EXISTING, not on features.deploy. A service scaffold
		// always gets deploy/kcl/<env>/main.k (generator.project: "so the user
		// has a complete starting point"), and every main.k opens with
		// `import config_gen`. Withholding config_gen.k from a
		// features.deploy=false project therefore left those envs permanently
		// unrenderable (CannotFindModule), which surfaced as five red
		// `forge doctor` deploy checks on a project shape that is entirely
		// valid. The step is a function of what is on disk: no envs, no work.
		{Name: "per-env deploy config", Gate: and(hasForgeYAML, hasConfig, hasDeployEnvs), GateReason: "no proto/<project>/config/v1/ directory or no deploy/kcl/<env>/main.k", Run: stepPerEnvDeployConfig, Tag: "deploy"},
		// AFTER per-env deploy config, deliberately. The frontend's runtime
		// document is rendered from the dev environment's own KCL, which
		// means both halves of that render — the generated
		// frontend_config_gen.k module and the env's config.k instance —
		// must already be on disk. Both are emitted by the step above, so
		// running earlier (where this step used to sit, beside the other
		// frontend emitters) meant a first `forge generate` on a fresh
		// project had nothing to read and silently fell back to proto
		// defaults, which is the bug this ordering fixes.
		{Name: "frontend typed config", Gate: gateFrontendHasFrontends, GateReason: "no frontends (frontends/<name>/ or a KCL forge.Frontend) or features.frontend=false", Run: stepFrontendConfig, Tag: "frontend"},
		// AFTER the env config modules, deliberately. Which workflows exist
		// is read off a RENDER of every env (generate_ci_hosted.go: a hosted
		// env gets release.yml, not deploy.yml), and an env cannot render
		// before the config_gen.k / frontend_config_gen.k it imports are on
		// disk. Running earlier, the first generate of a fresh project saw
		// its hosted staging and prod fail to render, took them for cluster
		// envs, and wrote a deploy.yml beside the scaffold's release.yml.
		{Name: "CI workflows", Gate: and(feature(config.FeaturesConfig.CIEnabled), hasForgeYAML, hasGoModule), GateReason: "no forge.yaml, features.ci=false, or " + noGoModuleReason + ": the workflows install forge from go.mod and run the Go lanes", Run: stepCIWorkflows, Tag: "deploy"},
		// Ingress (Gateway API codegen — the k3d-ports fragment and, in
		// later phases, other ingress-derived artifacts) is off when
		// features.ingress is explicitly false OR features.deploy is off
		// (no cluster, no k3d, no ingress to wire up).
		{Name: "ingress k3d ports fragment", Gate: and(hasForgeYAML, feature(config.FeaturesConfig.DeployEnabled), feature(config.FeaturesConfig.IngressEnabled)), GateReason: "features.ingress=false or features.deploy=false", Run: stepIngressK3dPorts, Tag: "deploy"},
		// CRD manifests, projected from the operator's Go types. Gated on
		// features.deploy alone (not on operators being configured): the
		// emitter is a function of what is on disk, and a project with no
		// api/ directory — most of them — does nothing here.
		{Name: "CRD KCL manifests (api/ Go types)", Gate: and(gateDeployEnabled, hasGoModule), GateReason: "features.deploy=false, or " + noGoModuleReason, Run: stepCRDKCL, Tag: "deploy"},
		{Name: "entity-aware seed data", Gate: and(feature(config.FeaturesConfig.MigrationsEnabled), hasServices), GateReason: "no services (an entity is a table a service's CRUD rpcs name) or features.migrations=false", Run: stepEntitySeeds, Tag: "migrations"},
		{Name: "frontend mocks + transport", Gate: gateFrontendHasFrontends, GateReason: "no frontends (frontends/<name>/ or a KCL forge.Frontend) or features.frontend=false", Run: stepFrontendMocks, Tag: "frontend"},
		{Name: "repoint renamed *_gen frontend imports", Gate: gateFrontendHasFrontends, GateReason: "no frontends (frontends/<name>/ or a KCL forge.Frontend) or features.frontend=false", Run: stepFrontendRenamedImports, Tag: "frontend"},
		{Name: "retire delivered agent skills", Gate: always, Run: stepRetireAgentSkills, Tag: "tools"},
		{Name: "go mod tidy (root)", Gate: hasGoModule, GateReason: noGoModuleReason, Run: stepGoModTidyRoot, Tag: "tools", Writes: writesRootTidy},
		{Name: "goimports on generated Go", Gate: hasGoModule, GateReason: noGoModuleReason, Run: stepGoimports, Tag: "tools"},
		{Name: "cleanup stale codegen", Gate: gateCodegenHasServices, GateReason: "no Connect services defined or features.codegen=false", Run: stepCleanupStale, Tag: "codegen"},
		// Runs after every Tier-1 emitter so checksums.Tier1TargetSet is
		// complete: drop disowns whose path forge no longer Tier-1-owns
		// (now scaffold-once, or no longer emitted). Always — the
		// retirement reads the target set, which is populated regardless
		// of project shape.
		{Name: "retire obsolete disowns", Gate: always, Run: stepRetireObsoleteDisowns, Tag: "validate"},
		{Name: "rehash tracked files", Gate: always, Run: stepRehashTracked, Tag: "tools"},
		// After every emitter has parked its fresh render, before anything
		// spends time validating: settle the drift the stomp guard
		// quarantined. See generate_drift_rescue.go.
		{Name: adjudicateDriftStep, Gate: always, Run: stepAdjudicateTier1Drift, Tag: "validate"},
		{Name: "post-gen validation", Gate: always, Run: stepPostGenValidate, Tag: "validate"},
		{Name: "detect renamed Tier-1 exports", Gate: always, Run: stepDetectRenamedExports, Tag: "validate"},
		{Name: "check disowned-sibling dangling refs", Gate: always, Run: stepCheckDisownedDanglingRefs, Tag: "validate"},
		// Runs after the codegen steps so gen/ reflects the current protos.
		// One-shot scaffold tests are user-owned and never regenerated;
		// `go build` (the validate step) skips _test.go files, so a stale
		// pb reference there would otherwise surface only at the user's
		// next `go test`. See generate_stale_scaffold.go.
		{Name: "check stale scaffold tests", Gate: gateCodegenHasServices, GateReason: "no Connect services defined or features.codegen=false", Run: stepCheckStaleScaffoldTests, Tag: "validate"},
		{Name: "go build (validate generated code)", Gate: and(gateValidateNotSkipped, hasGoModule), GateReason: "--skip-validate was passed, or " + noGoModuleReason, Run: stepGoBuildValidate, Tag: "validate"},
		// The deploy tree must COMPILE, and a tree that does not is a hard
		// failure rather than a warning. See generate_kcl_loadable.go: a
		// generate that exits 0 on an unrenderable tree defers the discovery
		// to whatever runs next, which is how #322's closed-schema break
		// surfaced at render with nothing pointing back at the generate that
		// had already reported success. Runs after the emitters, because the
		// tree it checks is the one they just produced.
		// NOT marked ReadOnly, though it writes nothing itself: ReadOnly
		// licenses the "No files were changed" report, and it is only true of
		// a step that runs BEFORE the first writer. This one runs last, by
		// which point the emitters have written the tree it checks.
		{Name: "deploy KCL loads", Gate: gateValidateNotSkipped, GateReason: "--skip-validate was passed", Run: stepKCLLoadable, Tag: "validate"},
		// Last: record WHICH forge build produced this tree, so a later
		// run by a different build can say so instead of failing
		// confusingly. Runs only after everything above succeeded, so the
		// record always names a build that actually generated the tree.
		{Name: "record generating forge build", Gate: always, Run: stepRecordGeneratingBuild, Tag: "tools"},
	}
}

// stepPresetAllowlist maps a pipelineFlags.Steps value (a named "step
// preset") to the set of step.Name values the runner is allowed to
// execute under that preset. An empty Steps value (the historical
// default) bypasses this map entirely and runs every step that passes
// its Gate.
//
// Naming note: this used to be called "scope", but the word "scope" is
// overloaded across forge — it's load-bearing for the file-ownership
// concept addressed by the Tier-1 inspector in internal/checksums/
// inspector.go. The pipeline-step concept got renamed to "step preset"
// so "scope" stays free for the file-ownership concept where it
// carries weight.
//
// Adding a new step preset: pick a stable name, enumerate the
// step.Name values the preset covers, and document the caller's intent
// in the comment. Step names must match generateSteps() exactly — the
// TestStepPresetAllowlistMembersExist test verifies this so a typo or
// a step rename doesn't silently produce a no-op pipeline.
//
// The "bootstrap-only" preset covers `forge scaffold worker`: scaffold a
// new worker, regenerate ONLY pkg/app/{bootstrap,testing,migrate}.go
// and the validation tail, then exit. Sibling Tier-1 files
// (.github/workflows/ci.yml, cmd/server.go, frontend mocks,
// pkg/config/config.go) stay untouched so a sibling agent or hand-
// curated comment isn't stomped. FRICTION 2026-06-03: cp-forge
// port-workers ran `forge scaffold worker` 7× and watched regen rewrite 5
// unrelated files per call.
var stepPresetAllowlist = map[string]map[string]bool{
	"bootstrap-only": {
		"load project config":                  true,
		"load checksums":                       true,
		"check Tier-1 file-stomp guard":        true,
		"snapshot Tier-1 exports":              true,
		"announce project":                     true,
		"detect proto directories":             true,
		"ensure gen/go.mod":                    true,
		"parse services + module path":         true,
		"internal/app composition (hybrid DI)": true,
		// cmd-group anchors moved out of the composition step into their own
		// step (so a flat→nested migration anchors them AFTER infra creates
		// serve.go). `forge scaffold worker` runs on an already-nested project
		// where serve.go exists, so this step anchors the new worker's
		// cmd/<bin>/cmd/workers/<name>.go subcommand — the behavior the
		// preset previously got for free from the composition step.
		"cmd command groups (services/workers/operators)": true,
		"go mod tidy (pre-composition)":                   true,
		"go mod tidy (pre-wiring)":                        true,
		"cmd/commands.go (user extension point)":          true,
		"per-service test helpers":                        true,
		"db/embed.go (embedded migrations)":               true,
		"go mod tidy (gen/)":                              true,
		"go mod tidy (root)":                              true,
		"goimports on generated Go":                       true,
		"rehash tracked files":                            true,
		adjudicateDriftStep:                               true,
		"post-gen validation":                             true,
		"detect renamed Tier-1 exports":                   true,
		"check disowned-sibling dangling refs":            true,
		"go build (validate generated code)":              true,
	},
	// The "mocks" step preset covers the fast-path "I just edited
	// contract.go, regenerate mock_gen.go" workflow. Mocks live behind a
	// "DO NOT EDIT" banner and are deterministic from contract.go + the
	// service proto; they cannot stomp any Tier-1 file. So:
	//
	//   - Skip "check Tier-1 file-stomp guard" entirely. The guard exists
	//     to protect Tier-1 files from being overwritten by codegen
	//     emitters; these emitters only touch mock_gen.go (and its sibling
	//     middleware_gen.go), which forge itself Tier-1 owns. Forcing the user to
	//     reconcile unrelated Tier-1 drift (a hand-edited workflow yaml,
	//     a touched cmd/server.go) before they can regen a mock has no
	//     payoff — there's no file the mock step could clobber that the
	//     unrelated edits put at risk.
	//
	//   - Run only the strictly required prereqs for the two mock steps:
	//     load config + checksums (so the writer has somewhere to record),
	//     detect proto directories (HasServices feeds the gate),
	//     ensure gen/go.mod (parser-side dependency), parse services
	//     + module path (the service-mock emitter walks ctx.Services), and
	//     the mock steps themselves. Goimports + rehash tail keeps the just-
	//     written file consistent with the audit machinery, but we skip
	//     the validate `go build ./...` and the post-gen heuristic
	//     warnings — the user already has a tight inner-loop ("contract
	//     change → regen mock → run unit tests") that runs `go build`
	//     itself.
	//
	// FRICTION 2026-06-04: two downstream projects reported that they
	// could not regen mock_gen.go after a contract.go change without
	// first reconciling unrelated Tier-1 file edits sitting in their
	// tree (e.g. modified .github/workflows/ci.yml). The mocks-only
	// step preset makes the inner-loop hermetic.
	"mocks": {
		"load project config":          true,
		"load checksums":               true,
		"detect proto directories":     true,
		"ensure gen/go.mod":            true,
		"parse services + module path": true,
		"service mocks":                true,
		// Contract mocks. forge emits TWO kinds of mock_gen.go and the
		// preset must cover both, or the workflow it is named for silently
		// half-runs: "service mocks" emits internal/handlers/mocks from the
		// proto, while a package's mock_gen.go is derived from its
		// contract.go by "internal package contracts". A contract.go edit
		// only ever moves the latter — leaving it out made
		// `generate --steps mocks` report success and change nothing, and
		// the stale mock still COMPILES (an extra method is not an error),
		// so nothing downstream caught it. Needs no prereq beyond the
		// config + checksums already loaded above.
		"internal package contracts": true,
		"goimports on generated Go":  true,
		"rehash tracked files":       true,
	},
	// The "harness" step preset regenerates the per-component test
	// harness — each helpers_gen_test.go (NewTest<Svc>, With<Svc>Deps, the
	// Stub<Field> doubles) — and the contract mocks it installs, and nothing
	// else: the inner loop after a Deps
	// edit, where the next command is `go test` on that package and a full
	// run spends minutes re-deriving protos, frontends and deploy config
	// that the edit cannot have changed.
	//
	// It is sound for that edit because the harness is a function of the
	// component's own Go source (its Deps struct and the interfaces it
	// names, read fresh from disk), the mock_gen.go files (a field whose
	// interface forge mocks gets that mock instead of a stub — and a Deps
	// edit moves the Deps-discovered mocks, so "internal package contracts"
	// regenerates them first), the service list (read from the
	// descriptor a full run left, which a Deps edit does not move), and
	// the internal package set (walked fresh). The prereqs are the ones the
	// "mocks" preset runs, for the same reasons. A PROTO or migration edit
	// is not this preset's: those move the descriptor and the ORM, and
	// need the full run.
	//
	// Like "mocks", it skips the Tier-1 stomp guard: these files sit behind
	// a "DO NOT EDIT" banner and the writer still refuses to overwrite a
	// hand-edited one without --force, so unrelated drift elsewhere in the
	// tree has nothing to protect here. go mod tidy and the validate build
	// are left to the `go test` the user runs next.
	harnessStepPreset: {
		"load project config":          true,
		"load checksums":               true,
		"detect proto directories":     true,
		"ensure gen/go.mod":            true,
		"parse services + module path": true,
		"internal package contracts":   true,
		"per-service test helpers":     true,
		"goimports on generated Go":    true,
		"rehash tracked files":         true,
	},
	// The "env-config" step preset writes what every env's KCL imports
	// from generate — config_gen.k, frontend_config_gen.k and each env's
	// config.k instance — and nothing else, so an env renders. It is what
	// the full pipeline runs before its CI step, and for the same reason:
	// which workflows a project has is read off a render of every env.
	// `forge project rescaffold` runs it when an env does not render
	// before asking which CI files the project has (rescaffold.go). The
	// Tier-1 guard stays: these are forge-owned files, and a hand edit to
	// one must stop this preset exactly as it stops a full run. The
	// forge/pkg compat handshake does not: it guards generated Go against
	// a forge/pkg the project cannot fetch, and this preset emits no Go.
	envConfigStepPreset: {
		"load project config":           true,
		"load checksums":                true,
		"check Tier-1 file-stomp guard": true,
		// Sets ctx.HasConfig, which gates the step below; without it the
		// preset runs and writes nothing.
		"detect proto directories": true,
		"per-env deploy config":    true,
		adjudicateDriftStep:        true,
	},
}

// envConfigStepPreset names the step preset that writes the env config
// modules (see stepPresetAllowlist).
const envConfigStepPreset = "env-config"

// harnessStepPreset names the step preset that regenerates only the
// per-component test harness (see stepPresetAllowlist).
const harnessStepPreset = "harness"

// templatesOnlyStepAllow is the set of step.Name values that run when
// pipelineFlags.TemplatesOnly is set. The list is the "template-driven"
// subset: every step whose work is rendering a forge-owned template
// (Tier-1 codegen, Tier-2 scaffolds, infra/CI yaml, frontend hooks/nav/
// mocks, KCL per-env config). Everything else — drift guards,
// validation, external subprocess generators (buf/protoc/
// goimports/go mod tidy/KCL render), and the cleanup sweep — is
// excluded.
//
// Use case: a forge template change (e.g. bootstrap.go.tmpl gets a
// louder warning) needs to land in a project with WIP that can't
// tolerate a full regen. The full pipeline would either trip the
// Tier-1 drift guard on the user's WIP edits, or shell out to
// tooling the partial tree can't build, or surprise-delete a stale
// file that the WIP migration hasn't re-created yet. Filtering the
// pipeline down to just the template-emit steps lets the new template
// propagate without disturbing the rest of the tree.
//
// Excluded categories (with the step.Name values dropped):
//
//   - Drift / Tier-1 guards: "check Tier-1 file-stomp guard",
//     "snapshot Tier-1 exports", "detect renamed Tier-1 exports",
//     "check disowned-sibling dangling refs".
//   - Validation: "pre-codegen contract check", "post-gen validation",
//     "go build (validate generated code)".
//   - External generators: "buf generate (Go stubs)",
//     "descriptor extraction", "OpenAPI specs (protoc-gen-connect-openapi)",
//     "ORM generate (proto/db)", "TypeScript stubs (frontends)",
//     "go mod tidy (gen/)", "go mod tidy (root)",
//     "goimports on generated Go", "refresh ORM output mtimes",
//     "ingress k3d ports fragment" (calls `kcl run`).
//   - Cleanup: "cleanup stale codegen", "rehash tracked files" (the
//     rehash only matters when goimports has run; without goimports
//     it would mis-stamp the just-written files).
//   - Migration scaffolding: "initial migration scaffold",
//     "entity-aware migration", "entity-aware seed data" — these
//     mutate db/migrations and would be unsafe to fire mid-WIP.
//
// Composes with stepPresetAllowlist (--steps): when both are set, a
// step must pass BOTH allowlists to run (intersection). That makes
// `--steps=bootstrap-only --templates-only` a well-defined narrower
// run rather than a precedence riddle.
var templatesOnlyStepAllow = map[string]bool{
	"load project config":                       true,
	"load checksums":                            true,
	"migrate legacy checksums manifest":         true,
	"announce project":                          true,
	"detect proto directories":                  true,
	"ensure gen/go.mod":                         true,
	"frontend workspaces scaffold":              true,
	"config loader (proto/<project>/config/v1)": true,
	"parse services + module path":              true,
	"frontend hooks":                            true,
	"ensure frontend components":                true,
	"frontend CRUD pages":                       true,
	"frontend nav + dashboard":                  true,
	"service stubs":                             true,
	"internal/db/ ORM (entity-driven)":          true,
	"CRUD handlers":                             true,
	"service mocks":                             true,
	"internal package contracts":                true,
	"webhook routes":                            true,
	"internal/app composition (hybrid DI)":      true,
	"cmd/commands.go (user extension point)":    true,
	"per-service test helpers":                  true,
	"db/embed.go (embedded migrations)":         true,
	"CI workflows":                              true,
	"regenerate infra files":                    true,
	// cmd-group anchors are template-driven (cmd-svc/worker/operator-group +
	// the per-group anchors) and MUST stay after "regenerate infra files" in
	// the allowlist's logical group — the step itself no-ops until serve.go
	// exists, which infra regen provides.
	"cmd command groups (services/workers/operators)": true,
	"per-env deploy config":                           true,
	"frontend mocks + transport":                      true,
}

// knownStepPresetNames returns a comma-joined string of every
// registered step preset for error messages. Kept as a helper so the
// error in runGeneratePipelineFlags doesn't have to inline a sort +
// join.
func knownStepPresetNames() string {
	names := make([]string, 0, len(stepPresetAllowlist))
	for k := range stepPresetAllowlist {
		names = append(names, k)
	}
	// Tiny sort to keep the error message deterministic.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return strings.Join(names, ", ")
}

// always is the trivial gate. Used for steps that are unconditional
// (config loading, checksums, build validation) or whose internal
// no-op-when-not-applicable behavior matches the pre-refactor shape.
func always(_ *pipelineContext) bool { return true }

// gateValidateNotSkipped suppresses the final `go build ./...` step
// when the user passed `--skip-validate`. See the SkipValidate field
// doc on pipelineContext for the per-lane-migration rationale.
func gateValidateNotSkipped(ctx *pipelineContext) bool {
	return !ctx.SkipValidate
}

// gatePreChecksNotSkipped suppresses the pre-codegen Step 0c contract-
// shape check when --skip-pre-checks is set. The caller already prints a
// warn line about the bypass in runGeneratePipelineFlags so users see
// the skip in their generate output.
func gatePreChecksNotSkipped(ctx *pipelineContext) bool {
	return !ctx.SkipPreChecks
}

// Gate combinators. Gates are pure predicates over ctx — no I/O. Tests
// assert they don't mutate ctx by calling them twice and comparing
// field-wise. Most gates are a mechanical AND of "the feature is on" and
// one or two preconditions on what's present on disk; the combinators
// below let those gates be spelled out inline in the GenStep table
// instead of each getting a one-off named func.

// feature returns the canonical "nil-cfg-or-enabled" feature check: a gate
// that is true when there is no forge.yaml (directory-scan fallback — the
// feature is implicitly on) OR the named feature flag is enabled. f is a
// method expression over config.FeaturesConfig (e.g.
// config.FeaturesConfig.CodegenEnabled).
func feature(f func(config.FeaturesConfig) bool) func(*pipelineContext) bool {
	return func(ctx *pipelineContext) bool {
		return ctx.Cfg == nil || f(ctx.Cfg.Features)
	}
}

// and combines predicates with short-circuiting logical AND. An empty
// list is vacuously true.
func and(preds ...func(*pipelineContext) bool) func(*pipelineContext) bool {
	return func(ctx *pipelineContext) bool {
		for _, p := range preds {
			if !p(ctx) {
				return false
			}
		}
		return true
	}
}

// Context preconditions — the on-disk / config-presence facts that gates
// AND against a feature check.
func hasServices(ctx *pipelineContext) bool { return ctx.HasServices }
func hasConfig(ctx *pipelineContext) bool   { return ctx.HasConfig }

// hasForgeYAML is true when a forge.yaml was loaded (Cfg != nil). Gates
// that emit forge.yaml-derived artifacts AND it in so the directory-scan
// fallback (Cfg == nil) skips them rather than panicking on a nil Cfg.
func hasForgeYAML(ctx *pipelineContext) bool { return ctx.Cfg != nil }

// hasGoModule is true when the project is a Go module (a go.mod at its
// root; config.ProjectConfig.HasGoModule). A static site has none, and the
// steps gated on it — the tidies, goimports, the compile check, the contract
// and CRD emitters — have no Go to act on: run, they fail on `pattern ./...`.
// The directory-scan fallback (no forge.yaml) keeps the old behaviour.
func hasGoModule(ctx *pipelineContext) bool { return ctx.Cfg == nil || ctx.Cfg.HasGoModule() }

const noGoModuleReason = "no go.mod (a project with no Go code, e.g. a static site)"

// hasDeployEnvs is true when at least one deploy/kcl/<env>/main.k exists.
// It is the precondition for the file those envs IMPORT (config_gen.k)
// — an env on disk that cannot resolve its imports is
// worse than no env at all, so this asks about the envs themselves rather
// than about features.deploy, which only governs whether forge DEPLOYS them.
func hasDeployEnvs(ctx *pipelineContext) bool {
	envs, err := ListEnvs(ctx.AbsPath)
	return err == nil && len(envs) > 0
}

// hasAnyEntrypoint is true when the project has any service, worker, or
// operator — the precondition for the bootstrap/wiring family of steps.
func hasAnyEntrypoint(ctx *pipelineContext) bool {
	return ctx.HasServices || ctx.HasWorkers || ctx.HasOperators
}

// The gates below are referenced from other files (generate_tier1_scope.go,
// generate_retire_disowns.go) or directly from tests, so they keep their
// named definitions; their bodies are expressed via the combinators for
// consistency with the inline gates in the table.

func gateCodegenEnabled(ctx *pipelineContext) bool {
	return feature(config.FeaturesConfig.CodegenEnabled)(ctx)
}

// gateCodegenDisabled is the steps that stand in for codegen in a project
// that has none. The directory-scan fallback (no forge.yaml) counts as
// codegen on, as everywhere else.
func gateCodegenDisabled(ctx *pipelineContext) bool { return !gateCodegenEnabled(ctx) }

// gateOpenAPIEnabled fires the OpenAPI spec step. Off by default — the
// flag is opt-in (api.openapi: true in forge.yaml) so existing projects
// regenerate byte-identically until a user opts in. Requires both the
// codegen feature and the explicit flag because the spec consumes the
// same proto inputs as the Go-stub buf step and there's no value in
// emitting a spec if we're not also emitting the handlers it documents.
func gateOpenAPIEnabled(ctx *pipelineContext) bool {
	return and(
		feature(config.FeaturesConfig.CodegenEnabled),
		hasForgeYAML,
		func(c *pipelineContext) bool { return c.Cfg.API.OpenAPI },
	)(ctx)
}

// gateORMHasServices gates the ORM projection. The store is projected from
// the applied schema, not from the services' CRUD rpcs, so it needs no
// service — only an entrypoint, because the go mod tidy that resolves the
// ORM's imports (forge/pkg/crud, bun) is gated on one. features.orm is
// already off without a database driver or under `//forge:no-orm`.
func gateORMHasServices(ctx *pipelineContext) bool {
	return and(feature(config.FeaturesConfig.ORMEnabled), hasAnyEntrypoint)(ctx)
}

// gateCRUDProjection gates the CRUD handler projection. It AND-s the ORM
// gate into the codegen one because the projection does not merely
// accompany the ORM — it COMPILES AGAINST IT. Every op the template
// renders names `db.<Entity>`, `db.Create<Entity>`, `db.Get<Entity>ByID`,
// `db.List<Entity>`, all of which exist only in the `internal/db/
// <entity>_orm_gen.go` that stepInternalDBORM emits under gateORMHasServices.
//
// Pre-fix the two steps disagreed: the ORM half was gated on features.orm
// and the projection half was not, so a project with `features.orm: false`
// and a hand-written internal/db got a handlers_crud_ops_gen.go referring
// to generated symbols that were never generated, and `forge generate`
// failed its own `go build` validate step. That is not a recoverable
// misconfiguration — turning the ORM off is the supported way to say "this
// project's store layer is mine", and it must turn off everything that
// consumes the ORM's output, not just the emitter.
//
// The trigger was a NAME COLLISION, which is what made it so surprising:
// entity matching resolves a proto message to a table by name, so merely
// naming a table `domains` alongside a `Domain` message was enough to fire
// a projection the project could not compile. control-plane had to rename
// the table to `custom_domains` to dodge it. Nothing about the project
// asked for CRUD; the collision alone was sufficient, because no gate
// asked whether the generated store this projection calls into exists.
func gateCRUDProjection(ctx *pipelineContext) bool {
	return and(gateCodegenHasServices, feature(config.FeaturesConfig.ORMEnabled))(ctx)
}

func gateDeployEnabled(ctx *pipelineContext) bool {
	return feature(config.FeaturesConfig.DeployEnabled)(ctx)
}

func gateFrontendHasFrontends(ctx *pipelineContext) bool {
	return and(
		hasForgeYAML,
		feature(config.FeaturesConfig.FrontendEnabled),
		func(c *pipelineContext) bool { return len(c.Cfg.Frontends) > 0 },
	)(ctx)
}

func gateFrontendHasServices(ctx *pipelineContext) bool {
	return and(feature(config.FeaturesConfig.FrontendEnabled), hasForgeYAML, hasServices)(ctx)
}

func gateCodegenHasServices(ctx *pipelineContext) bool {
	return and(feature(config.FeaturesConfig.CodegenEnabled), hasServices)(ctx)
}

func gateContractsEnabled(ctx *pipelineContext) bool {
	return feature(config.FeaturesConfig.ContractsEnabled)(ctx)
}

func gateCodegenHasAnyEntrypoint(ctx *pipelineContext) bool {
	return and(feature(config.FeaturesConfig.CodegenEnabled), hasAnyEntrypoint)(ctx)
}

func gateMigrateHasDriver(ctx *pipelineContext) bool {
	return and(
		hasForgeYAML,
		feature(config.FeaturesConfig.MigrationsEnabled),
		func(c *pipelineContext) bool { return c.Cfg.Database.Driver != "" },
	)(ctx)
}

// ──────────────────────────────────────────────────────────────────────
// Step implementations.
//
// Each step is a small function over *pipelineContext returning error.
// The pre-refactor numbered comments are inlined here as the step's
// doc comment, then dropped from the call-site so generate.go's loop
// stays uncluttered.
// ──────────────────────────────────────────────────────────────────────

// stepLoadConfig — was Step 0a.
// Loads forge.yaml. Missing-file is treated as the directory-scan
// fallback path (ctx.Cfg stays nil); other errors abort.
//
// After a successful load, runs validateConfigVsFilesystem to cross-
// check declarations against on-disk reality (loud-by-default
// architecture). Pass --skip-config-check to bypass.
func stepLoadConfig(ctx *pipelineContext) error {
	store, err := loadProjectStoreFrom(filepath.Join(ctx.ProjectDir, defaultProjectConfigFile))
	if err != nil && !errors.Is(err, ErrProjectConfigNotFound) {
		return fmt.Errorf("failed to load project config: %w", err)
	}
	if errors.Is(err, ErrProjectConfigNotFound) {
		// Announce the fallback so users running outside a forge project
		// (or in a tree where forge.yaml was accidentally deleted) don't
		// silently get the directory-scan path. The historical behavior
		// was a fully-silent fallback that turned every "I deleted
		// forge.yaml by mistake" into "why is my generate emitting
		// nothing structural?" — see B2 in the loud-by-default backlog.
		fmt.Fprintln(os.Stderr, "ℹ️  No forge.yaml found; generating from proto/ alone (the steps that read forge.yaml are skipped)")
		ctx.Cfg = nil
		return nil
	}
	ctx.Store = store
	ctx.Cfg = store.Config()

	// Loud-by-default config ↔ filesystem cross-check. Failures here are
	// hard errors so the user fixes the asymmetry at load time, not at a
	// confusing downstream "missing import" failure. See
	// generate_config_check.go for the rule set.
	if !ctx.SkipConfigCheck {
		if err := validateConfigVsFilesystem(ctx.ProjectDir, ctx.Cfg); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "⚠️  --skip-config-check: forge.yaml ↔ filesystem cross-check bypassed")
	}
	return nil
}

// stepLoadChecksums — was Step 0b.
// Loads the .forge ownership state (disowned.json + hashes.json) and
// stamps the active forge version. The
// caller (runGeneratePipeline) is responsible for calling SaveChecksums
// on exit so partial pipeline runs still persist what they wrote.
//
// Also resets the per-run written-this-run set in the checksums package
// — pipeline-run state, not persistent state, so it must start empty on
// every invocation.
func stepLoadChecksums(ctx *pipelineContext) error {
	cs, err := generator.LoadChecksums(ctx.AbsPath)
	if err != nil {
		return fmt.Errorf("failed to load checksums: %w", err)
	}
	cs.ForgeVersion = buildinfo.Version()
	ctx.Checksums = cs
	checksums.ResetSkipWrite()
	return nil
}

// stepCheckTier1Drift is the pre-pipeline file-stomp guard. Before any
// codegen step runs, scan the project for self-certifying Tier-1 files
// (embedded forge:hash markers) and report every one whose marker fails
// verification — evidence of a hand-edit (LLM or human), unless the bytes
// turn out to be forge's own render under a stale record. The check is
// purely local to each file, so it gives the same answer in a fresh
// clone, a parallel work lane, or a partially-committed tree. Every hit
// is quarantined for the fresh-render check (generate_drift_rescue.go),
// which runs after the emitters and before validation: files matching
// this run's render are re-recorded silently, and the rest fail the run
// with a single batched report listing every conflict — the user gets one
// error to react to, not N runs to discover N conflicts — and the run's
// writes are rolled back.
//
// Escape hatches:
//
//   - move the customization into the designated extension point (or any
//     Tier-2 file) — the preferred answer, regeneration keeps working.
//   - --force: skip the check and let codegen overwrite. Documents the
//     "throw my changes away and regenerate from the templates" intent.
//   - `forge project disown <path> --reason "<why>"`: one-way transfer to user
//     ownership.
//
// Tier-2 files are intentionally not checked: Tier-2 means "scaffold
// once, never overwrite", so a user edit IS the steady state.
func stepCheckTier1Drift(ctx *pipelineContext) error {
	if ctx.Checksums == nil {
		return nil
	}
	if ctx.Force {
		// Scope --force before anything else: an installed-but-empty
		// scope makes force INERT until this guard widens it to the
		// exact drift set it reports below. --force means "discard the
		// edits the guard told me about", never "overwrite anything any
		// emitter touches this run" — journey fr-a04f8c0609 watched a
		// --force recovery from one Tier-1 trip clobber unrelated
		// files. (Presets that deliberately exclude this guard keep the
		// legacy unscoped force: no scope is installed at all.)
		checksums.SetForceScope(nil)
	}
	allDrift := scanProjectDrift(ctx.AbsPath, ctx.Checksums)
	// Paths quarantined by the legacy-manifest migration are this run's
	// side-render rescue candidates — finishLegacyMigration adjudicates
	// them after the emitters have produced fresh renders to compare
	// against. Reporting them here would abort before the rescue runs.
	if len(ctx.LegacyUnverified) > 0 {
		quarantined := make(map[string]bool, len(ctx.LegacyUnverified))
		for _, p := range ctx.LegacyUnverified {
			quarantined[p] = true
		}
		kept := allDrift[:0]
		for _, d := range allDrift {
			if !quarantined[d.Path] {
				kept = append(kept, d)
			}
		}
		allDrift = kept
	}
	if len(allDrift) == 0 {
		return nil
	}
	// The scope filter's gates consult ctx.HasServices / HasWorkers /
	// HasOperators, which stepDetectProtoDirs only populates LATER in
	// the pipeline. Populate them here first — otherwise every gate
	// that branches on component presence reads false at guard time
	// and in-scope drift (pkg/app/wire_gen.go on a full run!) gets
	// waved through as out-of-scope, letting the emitters silently
	// stomp the user's hand edits.
	populateComponentPresence(ctx)
	// Scope drift to files this run's enabled emitters would actually
	// touch. Out-of-scope drift is announced as a warning (so a parallel
	// lane's hand-edit doesn't go fully silent) but does not block the
	// pipeline. See generate_tier1_scope.go for the registry rationale.
	drift, outOfScope := filterTier1DriftInScope(ctx, allDrift,
		func(d checksums.Tier1DriftEntry) string { return d.Path })
	if len(outOfScope) > 0 {
		// Friendly heads-up rather than a bare warning: out-of-scope drift
		// is the common case when iterating with `--steps=…` and the user
		// needs to know (a) what "drifted" means here and (b) how to
		// resolve it. Two escape hatches mirror the in-scope branch.
		fmt.Fprintf(os.Stderr, "ℹ️  Tier-1 drift detected in %d file(s) — skipped because their emitter step is not in this run's scope:\n", len(outOfScope))
		for _, d := range outOfScope {
			fmt.Fprintf(os.Stderr, "   - %s\n", d.Path)
		}
		fmt.Fprintf(os.Stderr, "   To regenerate them, re-run without `--steps=…` (or run `forge project disown <path> --reason \"<why>\"` to permanently take ownership).\n")
	}
	if len(drift) == 0 {
		return nil
	}

	if ctx.Force {
		// Widen the force scope to EXACTLY the in-scope drift set — the
		// files this message names are the only ones --force may
		// clobber. Out-of-scope drift stays outside the scope too: its
		// emitters won't run this invocation, and the warning above
		// already routed the user to a scope-complete re-run.
		paths := make([]string, 0, len(drift))
		for _, d := range drift {
			paths = append(paths, d.Path)
		}
		checksums.SetForceScope(paths)
		fmt.Fprintf(os.Stderr, "⚠️  --force: overwriting %d hand-edited Tier-1 file(s) (and nothing else):\n", len(drift))
		for _, d := range drift {
			fmt.Fprintf(os.Stderr, "   - %s\n", d.Path)
		}
		return nil
	}

	// --explain-drift: don't abort — redirect drifted paths to side
	// renders, let the pipeline produce fresh content to diff against,
	// and fail with the report at the end of the run. Mechanics +
	// rationale in generate_explain_drift.go.
	if ctx.ExplainDrift {
		prepareExplainDrift(ctx, drift)
		return nil
	}

	// Default: a mismatch with forge's record is not yet proof of a
	// hand-edit — a killed run or a merge leaves forge's own bytes beside
	// a stale record. Park a fresh render of each drifted path and let
	// the fresh-render check decide, before validation; whatever it
	// cannot match is reported there exactly as this guard used to.
	quarantineDriftForRescue(ctx, drift)
	return nil
}

// errMidMergeTier1Drift is the typed error returned by
// stepCheckTier1Drift when it has detected the host repo is mid-merge /
// mid-cherry-pick / mid-rebase. Callers that want to render a different
// fixup hint can type-assert with errors.As — the default cobra error
// path just prints msg.
type errMidMergeTier1Drift struct {
	state string // "merge" / "cherry-pick" / "rebase"
	msg   string
}

func (e errMidMergeTier1Drift) Error() string { return e.msg }

// GitMergeState reports the in-flight git operation state, if any.
// Returns "" when no merge / cherry-pick / rebase is in progress.
func (e errMidMergeTier1Drift) GitMergeState() string { return e.state }

// detectGitMergeState returns the in-flight git operation if any:
// "merge", "cherry-pick", "rebase", or "" when none. The probe order
// matches `git status`'s — MERGE_HEAD wins over CHERRY_PICK_HEAD which
// wins over the rebase directories. .git is also accepted as a regular
// file (worktrees use a gitfile pointing at the main repo's .git/
// worktrees/<name>/ — the merge-state files still live in that
// per-worktree directory, so stat-ing relative to <projectDir>/.git
// works there too when .git is a directory).
func detectGitMergeState(projectDir string) string {
	gitDir := resolveGitDir(projectDir)
	if gitDir == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err == nil {
		return "merge"
	}
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		return "cherry-pick"
	}
	if fi, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err == nil && fi.IsDir() {
		return "rebase"
	}
	if fi, err := os.Stat(filepath.Join(gitDir, "rebase-apply")); err == nil && fi.IsDir() {
		return "rebase"
	}
	return ""
}

// resolveGitDir locates the .git directory for the project. Returns
// .git when it's a regular directory; for git-worktree checkouts (.git
// is a file containing `gitdir: <path>`) it follows the redirect.
// Returns "" when neither shape applies.
func resolveGitDir(projectDir string) string {
	gitPath := filepath.Join(projectDir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return gitPath
	}
	// .git is a file — typically a worktree pointer.
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	prefix := "gitdir:"
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		dir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(projectDir, dir)
		}
		return dir
	}
	return ""
}

// short truncates a hex digest for human-readable error messages. Full
// digests live in the files' own forge:hash markers for the user to grep.
func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// stepAnnounceProject prints the "📦 Generating code for project: …"
// banner. Was inline at lines 145-161 of the pre-refactor pipeline.
// Also emits the forge_version mismatch warning when applicable.
func stepAnnounceProject(ctx *pipelineContext) error {
	if ctx.Cfg != nil {
		// A project whose go.mod requires forge has had its pin converged to
		// that require (stepReconcileForgePin); skew between the running
		// binary and go.mod is the compatibility handshake's call, not a
		// reason to `forge project upgrade`.
		_, followsGoMod := goModForgeRequire(ctx.AbsPath)
		if warning := forgeVersionMismatchWarning(ctx.Cfg.ForgeVersion, buildinfo.Version()); warning != "" && !followsGoMod {
			// Per-binary-path sentinel keeps the nudge from spamming every
			// `forge generate` invocation — fires once per shell session
			// (approximated via $TMPDIR).
			binPath, _ := os.Executable()
			if shouldEmitVersionWarn(warning, binPath) {
				fmt.Fprintln(os.Stderr, warning)
			}
		}
		fmt.Printf("📦 Generating code for project: %s\n\n", ctx.Cfg.Name)
		return nil
	}
	// Directory-scan fallback: verify we're in a forge project at all.
	if _, err := os.Stat(filepath.Join(ctx.ProjectDir, "proto")); os.IsNotExist(err) {
		return fmt.Errorf("no 'proto' directory found. Are you in a forge project?")
	}
	fmt.Println("📦 Generating code (directory-scan mode)")
	fmt.Println()
	return nil
}

// populateComponentPresence fills the proto-tree presence flags
// (HasServices/HasConfig) and the worker/operator presence
// flags on ctx. Returns the RAW operator presence (before the
// experimental-feature suppression) so stepDetectProtoDirs can print
// its one-line skip message exactly once.
//
// Idempotent and cheap (a few ReadDirs) — called from BOTH
// stepDetectProtoDirs (its original home) and stepCheckTier1Drift. The
// stomp guard's scope filter consults gates that branch on these flags
// (gateCodegenHasAnyEntrypoint, gateCodegenHasServices, …), and the
// guard runs BEFORE the detect step. Pre-fix, the flags were all false
// at guard time, so EVERY registered pkg/app/handlers/middleware path
// was misclassified as out-of-scope on a full run — the guard waved the
// drift through and the emitters silently stomped the user's hand
// edits (caught by the fixture-corpus fork round-trip).
//
// discover errors are deferred here: presence only needs the has-any
// flag, and the bootstrap step re-runs discovery and surfaces the
// disk-first resolution error with full context.
func populateComponentPresence(ctx *pipelineContext) (rawHasOperators bool) {
	// HasServices reflects "the project defines >=1 Connect service in ANY
	// proto package" — sourced from the protoc-gen-forge descriptor (with a
	// proto-source fallback for the pre-descriptor first run), NOT the mere
	// existence of a proto/services/ directory. This unblocks flat /
	// multi-service layouts (e.g. control-plane's proto/controlplane/v1/)
	// whose services would otherwise be invisible to every HasServices-gated
	// step. stepParseServicesAndModule later reconciles this to the exact
	// descriptor count. See projectDefinesConnectServices.
	ctx.HasServices = projectDefinesConnectServices(ctx.ProjectDir)
	// Config protos, not the directory: an empty proto/<project>/config/v1 (a CLI
	// scaffold leaves one) would have the loader write a pkg/config that
	// imports gen/config/v1, which nothing generates.
	ctx.HasConfig = codegen.HasConfigProtos(ctx.ProjectDir)
	// HasWorkers / HasOperators are BOOLEAN presence gates (does the project
	// have any worker/operator at all), NOT an enumeration of the set — generate
	// does zero worker/operator discovery. They gate the bootstrap family of
	// steps (hasAnyEntrypoint) exactly like HasConfig gates on config protos.
	ctx.HasWorkers = hasComponentDir(ctx.ProjectDir, "internal/workers")
	rawHasOperators = hasComponentDir(ctx.ProjectDir, "internal/operators")
	ctx.HasOperators = rawHasOperators
	return rawHasOperators
}

// stepPreCodegenContractCheck — was Step 0c.
// Asserts internal-package contract.go files use the canonical
// Service/Deps/New(Deps) Service shape BEFORE any generators emit
// files. Catches the "errors point at generated code" failure mode at
// validation time, with a clear error pointing at the user's
// contract.go rather than at the bootstrap.go that would otherwise
// fail to compile.
func stepPreCodegenContractCheck(ctx *pipelineContext) error {
	return preCodegenContractCheck(ctx.ProjectDir, ctx.Cfg)
}

// stepShellBuildTokens refuses a ShellBuild `cmd` that still writes one of the
// retired ${NAME} substitution tokens.
//
// forge runs a `cmd` verbatim, so a leftover token is a shell variable nothing
// sets. Refusing at generate — rather than warning — is because one of them is
// silent: `GOARCH=${TARGETARCH}` becomes `GOARCH=`, which builds for the host
// arch and surfaces only as `exec format error` on the cluster. Generate is the
// last point before a build consumes the declaration, and it is read-only here,
// so the refusal leaves the tree untouched.
//
// The same check is a gating `forge lint` rule, so a project sees it whichever
// command it runs first.
func stepShellBuildTokens(ctx *pipelineContext) error {
	// A project with no deploy/kcl tree scans clean (ScanKCLTree treats a
	// missing dir as "nothing to check"), so CLI and library projects pass.
	findings, err := shellbuildtokens.ScanKCLTree(filepath.Join(ctx.ProjectDir, "deploy", "kcl"))
	if err != nil {
		return err
	}
	return shellbuildtokens.Error(stripFileLocations(findings))
}

// stepLegacyConfigProto refuses a project whose config protos still declare
// the package every forge project used to share — `package config.v1` at
// proto/<project>/config/v1 — and names the codemod that moves them.
//
// It is a refusal rather than a fallback reader because the old layout is
// the defect: the protobuf runtime registers each file by path and each
// message by full name, process-wide, so two projects on it cannot be
// linked into one binary (internal/configproto). Reading it would keep
// generating a pkg/config that panics at init the moment another repo
// imports it beside its own.
func stepLegacyConfigProto(ctx *pipelineContext) error {
	files := configproto.LegacyFiles(ctx.ProjectDir)
	if len(files) == 0 {
		return nil
	}
	layout := configproto.ForProject(ctx.ProjectDir)
	return cliutil.UserErr("forge generate",
		fmt.Sprintf("the config protos declare the shared package %s, which collides with every other forge project's config at init (\"file config/v1/config.proto is already registered\")", configproto.LegacyPackage),
		strings.Join(files, ", "),
		fmt.Sprintf("run `%s fix %s` — it moves them to %s, declares package %s and rewrites every import; then re-run `%s generate`",
			cmdutil.Name(), configproto.LegacyCodemod, layout.Dir(), layout.Package(), cmdutil.Name()))
}

// stepMigrateImageRegistry moves a project off env-level registries: it removes
// `registry = …` from every ClusterTarget / ControlPlane in deploy/kcl and
// prefixes the removed value onto a workload's bare `image`, where that is
// unambiguous.
//
// It runs here, in generate, because the old tree NO LONGER COMPILES: `registry`
// is not a field on either schema now, so every render — including generate's
// own validate, and every forge command after it — fails until the tree moves.
// A project that hit that error with no migration would have to hand-edit every
// env, and would have to work out for itself which registry each workload's
// image should carry.
//
// It is a NO-OP on an already-migrated tree (no `registry = …` to find, no bare
// image to complete), so it costs nothing after the first run.
//
// AMBIGUITY IS REFUSED, not guessed. When the same workload is bound to a
// pulling runtime in two envs that declared different registries, there is no
// single reference that is right in both, and choosing one silently would point
// an env at a repository its image was never pushed to. Nothing is written in
// that case — a partial migration leaves a tree neither the old nor the new
// forge understands — and the exact per-env KCL is printed instead.
func stepMigrateImageRegistry(ctx *pipelineContext) error {
	res, err := kclmigrate.ImageRegistry(ctx.ProjectDir, true)
	if err != nil {
		return err
	}
	if res.Refused() {
		var b strings.Builder
		b.WriteString("this project declares an image registry on an environment, and an environment no longer has one — a WORKLOAD declares its registry, as part of its image.\n\n")
		b.WriteString("forge could not migrate it automatically, and wrote nothing:\n\n")
		for _, a := range res.Ambiguous {
			b.WriteString("  " + strings.ReplaceAll(a.Runbook(), "\n", "\n  ") + "\n")
		}
		// The unaccounted set is printed with the SAME weight as the
		// ambiguous one. "forge found a registry it could not read" and
		// "forge found two registries and will not choose" both leave a tree
		// that does not render, and reporting the first as a lesser note is
		// how it gets skimmed past.
		if len(res.Unaccounted) > 0 {
			b.WriteString("  forge found a registry declaration it could not account for. Each one is a\n")
			b.WriteString("  `registry = …` that the new schema rejects, so leaving it in place would not\n")
			b.WriteString("  render — and forge will not drop a registry it never understood:\n\n")
			for _, u := range res.Unaccounted {
				b.WriteString("    " + u.String() + "\n")
			}
			b.WriteString("\n  Put the full reference on each affected workload's image in\n")
			b.WriteString("  deploy/kcl/workloads.k (e.g. `image = \"ghcr.io/<owner>/<name>\"`), remove the\n")
			b.WriteString("  registry declarations above, and re-run forge generate.\n")
		}
		return cliutil.UserErr("forge generate", "cannot migrate env registries onto images automatically", "deploy/kcl/", b.String())
	}
	for _, r := range res.Rewrites {
		fmt.Printf("   - migrated: %s\n", r)
	}
	for _, d := range res.Dropped {
		fmt.Printf("   - %s\n", d)
	}
	return nil
}

// stripFileLocations flattens located findings back to the bare Finding list
// shellbuildtokens.Error formats. The file:line pairs stay in the lint output,
// which has a column-aware renderer; generate's refusal is prose.
func stripFileLocations(in []shellbuildtokens.FileFinding) []shellbuildtokens.Finding {
	out := make([]shellbuildtokens.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, f.Finding)
	}
	return out
}

// stepPkgCompatHandshake checks that the forge this project compiles against
// can satisfy the code this binary generates, BEFORE any codegen mutates the
// tree (kalshi fr-ac69216583). A pin older than the generating binary — or an
// unreleased binary with no source bridge — would otherwise let generate
// rewrite the whole tree and only then fail its own validate, leaving the
// repo mid-regen. Failing here keeps the tree untouched and names the fix.
//
// Gated by gatePreChecksNotSkipped (same opt-out as the contract check):
// --skip-pre-checks bypasses it for the parallel-lane case where the
// module graph is intentionally mid-migration.
func stepPkgCompatHandshake(ctx *pipelineContext) error {
	return checkPkgCompat(ctx.ProjectDir)
}

// stepRecordGeneratingBuild stamps this forge build's identity into
// .forge/generating-build after a successful generate.
//
// Two forge builds can share one PATH — `forge` (standalone) and
// `reliant forge` (compiled into reliant) — and drift apart silently. When
// the other one later refuses at the forge/pkg handshake, this record is
// what lets the refusal say "a different forge build generated this tree"
// instead of blaming the forge/pkg pin.
//
// Never fails the pipeline: the record is a diagnostic aid, not a
// correctness input, and a generate that succeeded must not be reported as
// failed because a note could not be written.
func stepRecordGeneratingBuild(ctx *pipelineContext) error {
	recordGeneratingBuild(ctx.ProjectDir)
	return nil
}

// stepDetectProtoDirs populates the proto-tree presence flags
// (HasServices/HasConfig) and worker/operator presence. Without forge.yaml
// the protos are all generate has to go on, so a tree with none is an error.
func stepDetectProtoDirs(ctx *pipelineContext) error {
	rawHasOperators := populateComponentPresence(ctx)
	if rawHasOperators && !ctx.HasOperators {
		fmt.Println("[generate] operator scaffolds detected but features.operators is off — skipping operator codegen")
	}

	if ctx.Cfg == nil && len(discoverProtoSubdirs(ctx.ProjectDir)) == 0 {
		return fmt.Errorf("no forge.yaml and no .proto files under proto/: nothing to generate from")
	}
	return nil
}

// stepEnsureGenModule bootstraps a missing `gen/go.mod` before any step
// that runs `buf generate` / `go list` / `go build` fires. Fresh git
// worktrees can carry a `go.work` that declares `use gen` but lack the
// actual `gen/go.mod` file (it's gitignored in some setups), which makes
// every Go-tooling invocation fail with "cannot load module gen". Doing
// the synthesis here — once, before the pipeline hits the proto/tools
// steps — keeps the rest of the pipeline ignorant of the bootstrap
// concern. Best-effort: see ensureGenGoMod for the no-op fallthrough
// conditions.
func stepEnsureGenModule(ctx *pipelineContext) error {
	if err := ensureGenGoMod(ctx.ProjectDir); err != nil {
		return err
	}
	changed, err := ensureGenForgeRequire(ctx.ProjectDir)
	if err != nil {
		return fmt.Errorf("repair gen/go.mod's forge requirement: %w", err)
	}
	if changed {
		fmt.Println("🔧 gen/go.mod now requires github.com/reliant-labs/forge (the retired forge/pkg module is dropped).")
	}
	return nil
}

// stepBufGenerateGo — was Step 1.
// Runs `buf generate` to produce Go stubs (protoc-gen-go +
// protoc-gen-connect-go). Failure aborts: every downstream step
// depends on the *.pb.go shapes.
func stepBufGenerateGo(ctx *pipelineContext) error {
	if err := runBufGenerateGo(ctx.ProjectDir); err != nil {
		return fmt.Errorf("buf generate (Go) failed: %w", err)
	}
	return nil
}

// stepDescriptorGenerate — was Step 1b.
// Extracts services/entities/configs into forge_descriptor.json.
//
// HARD gate, deliberately not warnOrFail: the descriptor drives the
// service-facing codegen (handlers, CRUD, mocks, frontend hooks).
// When it was warn-only, a failed plugin invocation
// (e.g. a misrouted self-invocation — see forgeExecCommand) printed one
// warning and the pipeline then "succeeded" while silently emitting
// none of that code. A generate without a descriptor must error, never
// quietly emit nothing.
func stepDescriptorGenerate(ctx *pipelineContext) error {
	if err := runDescriptorGenerate(ctx.ProjectDir); err != nil {
		return fmt.Errorf("descriptor generation failed — the proto descriptor drives handler/CRUD/mock codegen, so continuing would silently generate nothing: %w", err)
	}
	return nil
}

// stepOpenAPIGenerate is the api.openapi: true projection. Runs
// protoc-gen-connect-openapi over each proto/services/<svc>/ to emit
// `openapi/<service>.yaml`. Best-effort: a missing plugin or a single
// failing service surfaces as a warning so the rest of the pipeline
// still completes. The hard "plugin not on PATH" case bubbles up as an
// error because that's an actionable misconfiguration (the user opted
// in via forge.yaml but didn't install the binary).
func stepOpenAPIGenerate(ctx *pipelineContext) error {
	if err := runOpenAPIGenerate(ctx.ProjectDir, ctx.Cfg); err != nil {
		return fmt.Errorf("openapi generation: %w", err)
	}
	return nil
}

// stepFrontendWorkspaces emits the project-level pnpm-workspace
// scaffolding (pnpm-workspace.yaml + packages/api + packages/hooks)
// when the project opted into `frontend.workspaces: true`. The
// underlying writer is idempotent — re-running `forge generate` after
// the user edits e.g. packages/api/package.json does NOT clobber the
// changes. When workspaces is false the writer no-ops.
//
// Runs before the TypeScript-stubs step so packages/api/ exists for
// buf to emit into when (in a future cycle) the workspace-mode buf
// gen target lands.
func stepFrontendWorkspaces(ctx *pipelineContext) error {
	if !ctx.Cfg.IsFrontendWorkspacesEnabled() {
		return nil
	}
	if err := ctx.warnOrFail("frontend workspace scaffold",
		generator.WriteFrontendWorkspaceFiles(ctx.ProjectDir, ctx.Cfg.Name, true)); err != nil {
		return err
	}
	// The native primitives package only matters when there's an RN
	// frontend to consume it — gating on HasReactNativeFrontend keeps
	// projects with only a Next.js or Vite SPA frontend from getting
	// useless files under packages/ui-native/.
	if ctx.Cfg.HasReactNativeFrontend() {
		layout := generator.NewFrontendWorkspaceLayout(ctx.Cfg.Name)
		if err := ctx.warnOrFail("ui-native package scaffold",
			generator.WriteUINativePackageFiles(ctx.ProjectDir, layout)); err != nil {
			return err
		}
	}
	return nil
}

// stepFrontendBufTS — was Step 3.
// Per-frontend `buf generate` for TypeScript stubs. Only runs for
// nextjs / react-native frontends. Best-effort per-frontend.
//
// In workspaces mode the per-frontend buf step is replaced by a single
// shared invocation that emits into packages/api/src/gen — see
// runBufGenerateTypeScriptWorkspace. Without the early-return below the
// per-frontend loop would call the workspace helper N times, which is
// idempotent but spams the output.
func stepFrontendBufTS(ctx *pipelineContext) error {
	// This step may run `npm install` (protoc-gen-es missing), so the dev
	// bridge's gitignored npm config must already be in place: a project
	// bridged by an older forge has none and the install would crash.
	generator.EnsureDevWebRuntimeLink(ctx.ProjectDir)
	if ctx.Cfg.IsFrontendWorkspacesEnabled() {
		return runBufGenerateTypeScriptWorkspace(ctx.Cfg, ctx.ProjectDir)
	}
	for _, fe := range ctx.Cfg.Frontends {
		if generatesTypeScript(fe.Type) {
			// FATAL, deliberately not warnOrFail. TypeScript stubs are a
			// REQUIRED codegen step, not a best-effort one: the hooks and
			// mocks steps downstream emit TypeScript importing
			// `@/gen/<svc>/v1/<svc>_pb` for every service, and this step is
			// what writes those modules.
			//
			// Routed through warnOrFail, a frontend with no node_modules
			// printed one warning and `forge generate` exited 0 having
			// written no stubs at all — indistinguishable from a run that
			// had nothing to write. The stale stubs already on disk then
			// stayed stale, and the diagnostic surfaced much later as a
			// build and lint failure in another repo, blamed on whichever
			// service was added most recently. runBufGenerateTypeScript
			// now installs the missing deps itself; if it still cannot
			// produce stubs, that is an error, not a warning.
			if err := runBufGenerateTypeScript(fe, ctx.Cfg, ctx.ProjectDir); err != nil {
				return fmt.Errorf("TypeScript generation for %s failed — TS stubs are required "+
					"codegen (hooks and mocks import the _pb modules this step writes), so "+
					"continuing would emit a frontend that cannot compile: %w", fe.Name, err)
			}
		}
	}
	return nil
}

// stepConfigLoader — was Step 3b.
// Generate `pkg/config/config.go` from proto/<project>/config/v1 annotations.
// Populates ctx.ConfigFields so the bootstrap step (6) can wire env-
// var loading without re-parsing the descriptor.
func stepConfigLoader(ctx *pipelineContext) error {
	var features config.FeaturesConfig
	if ctx.Cfg != nil {
		features = ctx.Cfg.Features
	}
	configFields, cfgErr := generateConfigLoader(ctx.ProjectDir, features, ctx.Checksums)
	if cfgErr != nil {
		return fmt.Errorf("config loader generation failed: %w", cfgErr)
	}
	ctx.ConfigFields = configFields

	return nil
}

// stepParseServicesAndModule parses proto/services/ once and stashes
// the result on ctx for the half-dozen mid-pipeline steps that
// previously each re-parsed it (or assumed a sibling step had).
//
// Pre-refactor this lived between steps 3b and 3c as a "parse services
// and module path once for steps 4-6" comment — making it a real GenStep
// keeps the contract explicit: all subsequent steps that read
// ctx.Services or ctx.ModulePath assume this step has run, and its gate
// (gateNeedsServices) is the union of those consumers' needs.
//
// Module-path resolution has two paths: ParseServicesFromProtos already
// reads it for each ServiceDef, so we hoist it from the first parsed
// service. When there are no proto services but there ARE workers or
// operators, fall back to a direct GetModulePath() call.
func stepParseServicesAndModule(ctx *pipelineContext) error {
	var services, declared []codegen.ServiceDef
	var modulePath string
	var err error

	// Services are read from the protoc-gen-forge descriptor, which
	// enumerates every Connect service in ANY proto package — flat /
	// multi-service (proto/controlplane/v1/) and per-service
	// (proto/services/<svc>/v1/) layouts alike. The dir arg has been
	// vestigial since ParseServicesFromProtos became descriptor-backed.
	//
	// We do NOT reconcile ctx.HasServices to len(services) here. HasServices
	// is the project's declared shape (descriptor-or-proto-source presence,
	// established at detect time); the descriptor *enumeration* can be empty
	// for a window or a transient reason (descriptor generation is
	// best-effort / warnOrFail, and a fresh tree may not have produced it
	// yet). Flipping HasServices false off an empty enumeration would
	// silently disable every service codegen step — the exact silent-stomp
	// failure mode the loud-by-default architecture forbids. The downstream
	// per-service emitters already iterate ctx.Services, so an empty slice
	// is a self-correcting no-op without disabling the steps wholesale.
	if ctx.HasServices {
		services, declared, modulePath, err = parseDeclaredServicesAndModule(ctx)
		if err != nil {
			return err
		}
	}

	// Resolve module path for workers/operators if not already set (no proto services)
	if modulePath == "" && (ctx.HasWorkers || ctx.HasOperators) {
		modulePath, err = codegen.GetModulePath(ctx.ProjectDir)
		if err != nil {
			return fmt.Errorf("failed to read module path: %w", err)
		}
	}

	ctx.Services = services
	ctx.DeclaredServices = declared
	ctx.ModulePath = modulePath
	return nil
}

// parseDeclaredServicesAndModule is the HasServices half of
// stepParseServicesAndModule: read the declared services, refuse the proto
// shapes no emitter can handle, and resolve the module path.
func parseDeclaredServicesAndModule(ctx *pipelineContext) (services, declared []codegen.ServiceDef, modulePath string, err error) {
	declared, err = codegen.ParseDeclaredServices(ctx.ProjectDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse service protos: %w", err)
	}
	services = codegen.ManagedServices(declared)
	announceUnmanagedServices(codegen.UnmanagedServices(declared))
	// Catch a proto service name that generates a Go identifier
	// incompatible with the one forge's own bootstrap wiring derives
	// from the proto/services/<dir>/ directory name — BEFORE any
	// downstream step (handlers, mounts, bootstrap testing) emits code
	// that assumes they agree. buf has already emitted Connect stubs
	// named from the proto by this point (stepBufGenerateGo runs
	// earlier), so those files are correct on their own terms; the
	// mismatch only becomes a compile error once forge's directory-
	// derived Mount<X>/NewTest<X> wiring tries to reference them under
	// a different name. Checking here, right after the descriptor is
	// parsed and before any consumer touches ctx.Services, means the
	// proto can be edited at ANY time (not just at scaffold time) and
	// still gets caught before code that won't compile is written.
	for _, svc := range services {
		dir := codegen.ServiceNameFromProtoFile(svc.ProtoFile)
		if dir == "" {
			continue
		}
		if verr := cmdutil.ValidateServiceDirConsistency(svc.Name, dir); verr != nil {
			return nil, nil, "", fmt.Errorf("%s: %w", svc.ProtoFile, verr)
		}
	}
	// `// forge:custom` decides whether an rpc gets a stub or CRUD
	// wiring, so a marker the descriptor lost, or a custom rpc whose
	// shim still calls the CRUD op forge stops generating, is refused
	// here too — before any emitter classifies an rpc.
	if err := checkCustomMarkerPlacement(services, ctx.ProjectDir); err != nil {
		return nil, nil, "", err
	}
	if err := checkCustomRPCsDelegate(services, ctx.ProjectDir); err != nil {
		return nil, nil, "", err
	}
	// ParseServicesFromProtos already reads the module path and sets it on each ServiceDef.
	// Extract it from the first service to avoid a redundant GetModulePath() call.
	if len(services) > 0 {
		modulePath = services[0].ModulePath
	} else {
		modulePath, err = codegen.GetModulePath(ctx.ProjectDir)
		if err != nil {
			return nil, nil, "", fmt.Errorf("failed to read module path: %w", err)
		}
	}
	return services, declared, modulePath, nil
}

// announceUnmanagedServices prints one line naming the services generate is
// deliberately leaving alone, so "why is there no internal/handlers/<svc>/?"
// has its answer in the output. A project mid-adoption can carry dozens, so a
// long list collapses to a count; `forge project audit` lists every one.
func announceUnmanagedServices(unmanaged []codegen.ServiceDef) {
	if len(unmanaged) == 0 {
		return
	}
	const maxNamed = 5
	what := fmt.Sprintf("%d services", len(unmanaged))
	if len(unmanaged) <= maxNamed {
		names := make([]string, 0, len(unmanaged))
		for _, svc := range unmanaged {
			names = append(names, svc.Name)
		}
		what = strings.Join(names, ", ")
	}
	fmt.Printf("  ⏭️  Unmanaged (// %s): %s — buf still generates their code; forge scaffolds nothing for them. List: `%s project audit`\n",
		codegen.ProtoMarkerUnmanaged, what, Name())
}

// stepFrontendHooks — was Step 3c.
// Emit React Query hooks for each Connect-driven service. Requires
// services to be parsed (gate ensures HasServices).
func stepFrontendHooks(ctx *pipelineContext) error {
	if len(ctx.Services) == 0 {
		return nil
	}
	if err := ctx.warnOrFail("frontend hooks generation", generateFrontendHooks(ctx.Cfg, ctx.Services, ctx.ProjectDir, ctx.Checksums)); err != nil {
		return err
	}
	// The hooks import owned seams (@/lib/connect, @/lib/test-utils) that a
	// frontend forge did not scaffold never had. See generate_frontend_seams.go.
	return ctx.warnOrFail("frontend seams (adopted frontends)", ensureAdoptedFrontendSeams(ctx.Cfg, ctx.Services, ctx.ProjectDir))
}

// stepFrontendComponents — was Step 3d.
// Idempotent: copies the shared shadcn/ui component set into each
// frontend if missing. No-op when components are already present.
func stepFrontendComponents(ctx *pipelineContext) error {
	return ctx.warnOrFail("frontend components install", ensureFrontendComponents(ctx.Cfg, ctx.ProjectDir))
}

// stepFrontendPages — was Step 3e.
// Generate CRUD pages per service. Pages require entity descriptors
// matching the service so generated pages reference real fields rather
// than RPC-name-derived guesses; gated on len(entities) > 0 here.
func stepFrontendPages(ctx *pipelineContext) error {
	if len(ctx.Services) == 0 {
		return nil
	}
	pageEntities, _ := codegen.ParseEntityProtos(ctx.ProjectDir)
	if len(pageEntities) == 0 {
		return nil
	}
	return ctx.warnOrFail("frontend page generation",
		generateFrontendPages(ctx.Cfg, ctx.Services, ctx.ProjectDir, pageEntities, ctx.Checksums))
}

// stepFrontendNav — was Step 3f.
// Re-render nav and dashboard with current entity data. Tier-1 files
// (nav_gen.tsx / dashboard_gen.tsx) are owned by forge; sibling Tier-2
// nav.tsx / dashboard.tsx files are user-owned scaffolds.
func stepFrontendNav(ctx *pipelineContext) error {
	// Parse the SAME entity set stepFrontendPages gates page emission on,
	// so nav_gen/dashboard_gen never advertise a route whose page was
	// never written. Parse errors degrade to an empty set — identical to
	// how stepFrontendPages treats them (no pages → no routes).
	navEntities, _ := codegen.ParseEntityProtos(ctx.ProjectDir)
	return ctx.warnOrFail("frontend nav generation",
		generateFrontendNav(ctx.Cfg, ctx.Services, ctx.ProjectDir, navEntities, ctx.Checksums))
}

// stepFrontendConfig emits each frontend's typed config module
// (src/lib/config_gen.ts) plus its dev runtime document (public/config.js)
// for every config message bound by (forge.v1.frontend_config).
//
// A parse failure here is NOT degraded to an empty set the way the entity
// parsers above are. The frontend config path carries the sensitive-field
// refusal, and a guard that silently disarms when its input fails to parse
// is worse than no guard: a project could grow a secret in a frontend
// config and generate cleanly. If the config protos cannot be read, that
// is the error.
func stepFrontendConfig(ctx *pipelineContext) error {
	messages, err := codegen.ParseConfigProtosFromDir(
		filepath.Join(ctx.ProjectDir, "proto", "config"))
	if err != nil {
		return fmt.Errorf("parse config protos: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}
	if err := codegen.ValidateFrontendConfigs(messages); err != nil {
		return err
	}
	// Frontend fields take no env_var_aliases (they read no environment);
	// refused here too, since this step runs without the Go config loader.
	if err := codegen.ValidateConfigEnvAliases(messages); err != nil {
		return err
	}
	return generateFrontendConfigModules(ctx.Cfg, messages, ctx.ProjectDir, ctx.Checksums)
}

// stepCleanupStale — was Step 3z.
// Sweep of stale codegen artifacts after a service is removed or
// renamed. Marker-driven: only files carrying a forge:hash
// certification marker (or a scoped .forge/hashes.json record) are
// candidates; unmarked paths are user-owned and inherently safe.
//
// Positioned LATE in the plan (after every emit step) so the
// per-run `WrittenThisRun` set captures every path the current run
// touched. A certified file NOT in that set is what we delete.
// Pre-2026-06-05 the step ran early and re-derived the "expected"
// set from forge.yaml — that re-derivation disagreed with on-disk
// snake_case proto layouts and deleted user code.
//
// Only runs when len(services) > 0 — on a freshly-scaffolded project
// the descriptor may be empty (or buf hasn't run successfully yet);
// the empty-services short-circuit keeps the legacy contract.
//
// Skipped when an earlier step failed as a warning (no --strict): that
// step wrote none of its outputs, so they look exactly like files no
// emitter produces any more, and deleting them would turn a warning
// into missing code.
func stepCleanupStale(ctx *pipelineContext) error {
	if len(ctx.Services) == 0 {
		return nil
	}
	if len(ctx.SoftFailures) > 0 {
		fmt.Fprintf(os.Stderr, "\n⏭️  Stale-file sweep skipped: %s failed this run, so files it would have written look stale. "+
			"Fix it and re-run `%s generate` to remove the files no emitter writes any more.\n",
			strings.Join(ctx.SoftFailures, ", "), Name())
		return nil
	}
	sweep, cerr := cleanupStaleArtifacts(ctx)
	if cerr != nil {
		return ctx.warnOrFail("stale-artifact cleanup", cerr)
	}
	printStaleSweep(os.Stdout, os.Stderr, sweep, ctx.ForceCleanup)
	return nil
}

// printStaleSweep reports a stale-file sweep: what it removed (stdout),
// and the hand-edited files it kept with the two ways to settle them
// (stderr, so a kept file is not lost in a long run's output).
func printStaleSweep(out, errOut io.Writer, sweep staleSweep, forceCleanup bool) {
	if len(sweep.Removed) > 0 {
		handEdited := make(map[string]bool, len(sweep.HandEdited))
		for _, rel := range sweep.HandEdited {
			handEdited[rel] = true
		}
		fmt.Fprintf(out, "\n🧹 Removed %d stale generated file(s) — no emitter writes them any more:\n", len(sweep.Removed))
		for _, rel := range sweep.Removed {
			if handEdited[rel] {
				fmt.Fprintf(out, "  - %s (hand-edited; --force-cleanup)\n", rel)
				continue
			}
			fmt.Fprintf(out, "  - %s\n", rel)
		}
	}
	if len(sweep.HandEdited) > 0 && !forceCleanup {
		fmt.Fprintf(errOut, "\n⚠️  Kept %d stale generated file(s) with hand-edits — no emitter writes them any more, but the edits are yours. "+
			"Delete them with `%s generate --force-cleanup`, or keep one as your own with `%s project disown <path>`:\n",
			len(sweep.HandEdited), Name(), Name())
		for _, rel := range sweep.HandEdited {
			fmt.Fprintf(errOut, "  - %s\n", rel)
		}
	}
}

// stepServiceStubs — was Step 4.
// Non-destructive scaffold of service.go / one rpc_<name>.go per custom
// RPC / wrapper.go for each service. Existing service directories get only
// missing-RPC handler stubs. CRUD method names are precomputed so the stub
// generator doesn't double-up on names the CRUD handler step (4b)
// will emit.
func stepServiceStubs(ctx *pipelineContext) error {
	// Tombstoned services (mentioned only in a pkg/app/services.go
	// comment — types-only) get NO handlers/<svc>/ scaffold — announce
	// each skip so "why is my handlers dir missing?" has a loud answer
	// in the generate output. Registered AND unlisted (newly added)
	// services both scaffold; the unlisted ones additionally get a
	// register-me notice so the user knows the binary won't serve them
	// until they add the row.
	reg, err := ctx.serviceRegistry()
	if err != nil {
		return err
	}
	rows, tombstoned := splitServiceDefs(reg, ctx.Services)
	for _, svc := range tombstoned {
		fmt.Printf("  ⏭️  Skipped handlers/%s/ (types-only — not registered in %s)\n", naming.ServicePackage(svc.Name), serviceRegistryRelPath)
	}
	for _, svc := range rows {
		if reg.state(svc.Name) == registrationUnlisted {
			fmt.Printf("  📝 %s is generated but NOT served: add `%s(app, cfg, logger, opts...),` to RegisteredServices in %s\n",
				svc.Name, codegen.ServiceRowFuncName(svc.Name), serviceRegistryRelPath)
		}
	}
	// The stub step skips exactly what the CRUD step will implement, under
	// the CRUD step's own gate — see collectCRUDMethodNames.
	crudMethodNames := collectCRUDMethodNames(rows, ctx.ProjectDir, gateCRUDProjection(ctx))
	if err := generateServiceStubs(ctx.Cfg, rows, ctx.ProjectDir, crudMethodNames, ctx.Checksums); err != nil {
		return fmt.Errorf("service stub generation failed: %w", err)
	}
	return nil
}

// stepInternalDBORM — was Step 4a.
// Emit internal/db/<entity>_orm.go — entity struct + CRUD helpers —
// projected from the APPLIED schema (the entity's introspected
// Columns). The entity struct lives in the same file as its ORM; the
// old internal/db/types.go proto-alias file is gone (entities are no
// longer proto messages).
//
// The entity set is every applied table (codegen.ParseORMEntities), not the
// CRUD join: CRUD wiring keys on CRUD rpcs, the store does not.
func stepInternalDBORM(ctx *pipelineContext) error {
	entities, entErr := codegen.ParseORMEntities(ctx.ProjectDir)
	if entErr != nil {
		ctx.ORMFailure = entErr
		return ctx.warnOrFail("entity parsing for ORM generation", entErr)
	}
	if len(entities) == 0 {
		return nil
	}
	planEntities := codegen.EntityDefsToPlanEntities(entities)
	orm := config.ORMPackage(ctx.Cfg)
	if err := generator.GeneratePlanORM(ctx.ProjectDir, orm, planEntities, ctx.Checksums); err != nil {
		// Recorded whether or not --strict makes it fatal: without --strict
		// the run continues, and every step that consumes the ORM must say
		// it was skipped instead of reporting output it never wrote.
		ctx.ORMFailure = err
		return ctx.warnOrFail("ORM generation", err)
	}
	// GeneratePlanORM retires a forge-written types.go (the proto-entity
	// era's alias file) itself — and only a forge-written one.
	fmt.Printf("  ✅ Generated %s/ (%d entity ORM files)\n", orm.PackageDir(), len(entities))
	return nil
}

// stepCRUDHandlers — was Step 4b.
// Emits handler implementations for the CRUD method shapes detected
// against entity descriptors.
//
// FATAL, not a warning. This step used to run through ctx.warnOrFail,
// which prints and continues unless --strict — so the two checks the CRUD
// generator makes precisely because the alternative is silent data loss (a
// list filter naming no column, a proto field whose column has no
// conversion) both reported green on a default `forge generate` and
// shipped the broken code anyway. An unsupported entity does not "brick
// the rest of generate"; it means the handlers for that entity are wrong,
// and emitting the rest around them is how the wrongness reaches
// production.
//
// The removal was itself unguarded for a while: the only test that ran the
// gate end to end carried `//go:build e2e`, so reinstating the wrapper broke
// nothing `go test ./...` runs. TestStepCRUDHandlers_IsFatalOnADefaultGenerate
// now pins the fatality with no build tag.
func stepCRUDHandlers(ctx *pipelineContext) error {
	// CRUD wiring is written against the ORM the step above emits. When
	// that step failed (a warning without --strict), generating wiring for
	// an ORM that does not exist would either break the build or claim
	// files nothing wrote; say it was skipped, and why, instead.
	if ctx.ORMFailure != nil {
		fmt.Println("\n🔧 Generating CRUD handlers...")
		fmt.Println("  ⏭️  Skipped: the ORM step above failed, and CRUD wiring is written against the ORM it would have " +
			"generated. Nothing under internal/handlers/*/handlers_crud*.go was written or changed; resolve the ORM " +
			"failure and re-run `forge generate`.")
		return nil
	}
	rows, err := ctx.rowServiceDefs()
	if err != nil {
		return err
	}
	// A CRUD-named rpc whose entity has a wire message but no table would
	// fall through to a pb-through stub with nothing saying why. Refused by
	// name here, under the CRUD step's own gate: with the ORM off every
	// CRUD-named rpc is a stub by design.
	unmatched, err := codegen.ParseUnmatchedCRUDRPCs(ctx.ProjectDir)
	if err != nil {
		return fmt.Errorf("CRUD handler generation failed: match CRUD rpcs to tables: %w", err)
	}
	if err := codegen.UnmatchedCRUDRPCsError(unmatched); err != nil {
		return fmt.Errorf("CRUD handler generation failed: %w", err)
	}
	if err := generateCRUDHandlers(rows, ctx.ModulePath, ctx.ProjectDir, ctx.Checksums); err != nil {
		return fmt.Errorf("CRUD handler generation failed: %w", err)
	}
	return nil
}

// stepServiceMocks — was Step 5.
// Always regenerate. Mocks are Tier-1 files; their content is owned by
// forge and is the test seam frontends import statically.
func stepServiceMocks(ctx *pipelineContext) error {
	rows, err := ctx.rowServiceDefs()
	if err != nil {
		return err
	}
	if err := generateServiceMocks(rows, ctx.ProjectDir, ctx.Checksums); err != nil {
		return fmt.Errorf("mock generation failed: %w", err)
	}
	return nil
}

// stepInternalContracts — was Step 5b.
// Emit `<pkg>/contracts_gen.go` for every internal package. Reads the
// canonical Service / Deps / New(Deps) shape from contract.go and
// surfaces it as the bootstrap step's source of truth.
func stepInternalContracts(ctx *pipelineContext) error {
	if err := generateInternalPackageContracts(ctx.ProjectDir, ctx.Cfg, ctx.Checksums); err != nil {
		return fmt.Errorf("internal package contract generation failed: %w", err)
	}
	return nil
}

// stepWebhookRoutes — was Step 5e.
// Emit webhook router from any webhook annotations in the descriptor.
// No-op when no webhooks are declared.
// stepAuthProcedures emits pkg/middleware/procedures_gen.go: the set of RPCs
// the project declared callable without credentials, derived from
// `(forge.v1.method).auth_required = false`. The interceptor runs fail-closed
// against it, so this is the projection of the declaration that actually gates
// requests — see codegen.BuildOpenProcedures for why the hand-written map it
// replaced was a second, disagreeing declaration surface.
//
// It reads DeclaredServices, not Services: whether an rpc is public is part
// of the API contract, true whoever implements the handler. A
// `// forge:unmanaged` service's handlers may be mounted on this server by
// hand, behind this same interceptor, and dropping its declared-public rpcs
// would make them fail closed.
func stepAuthProcedures(ctx *pipelineContext) error {
	content, err := codegen.RenderOpenProcedures(ctx.DeclaredServices, ctx.ModulePath)
	if err != nil {
		return err
	}
	relPath := filepath.Join("pkg", "middleware", "procedures_gen.go")
	if _, err := generator.WriteGeneratedFileTier1(ctx.ProjectDir, relPath, content, ctx.Checksums, true); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	fmt.Printf("  ✅ Generated %s\n", relPath)
	return nil
}

func stepWebhookRoutes(ctx *pipelineContext) error {
	reg, err := ctx.serviceRegistry()
	if err != nil {
		return err
	}
	if err := generateWebhookRoutes(reg, ctx.ProjectDir, ctx.Checksums); err != nil {
		return fmt.Errorf("webhook route generation failed: %w", err)
	}
	return nil
}

// deriveOrmEnabled is the probe for "should bootstrap.go include the
// ORM wire-up?". Extracted so the rule can be golden-tested
// independently of the pipeline.
//
// Entities are projections of the applied schema (db/migrations →
// internal/db/*_orm.go via stepInternalDBORM, which runs upstream), so
// the ORM is enabled exactly when internal/db contains generated ORM
// output: any *_orm.go file, or the legacy internal/db/types.go from
// older projects.
//
// Returns an error only when the ORM package scan itself fails (e.g. I/O
// permission). A missing directory is simply "off". The directory is
// database.orm.package (default internal/db).
func deriveOrmEnabled(projectDir string) (bool, error) {
	ormDir := codegen.ProjectORMPackage(projectDir).PackageDir()
	entries, err := os.ReadDir(filepath.Join(projectDir, filepath.FromSlash(ormDir)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("scan %s for ORM output: %w", ormDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), "_orm_gen.go") || e.Name() == "types.go" {
			return true, nil
		}
	}
	return false, nil
}

// stepInternalAppComposition emits the §2 hybrid-DI composition layer under
// internal/app (auth.go / compose.go / lifecycle.go / mounts_services.go /
// providers.go). compose.go is the explicit per-binary construction site that
// REPLACED the retired generated injector — see GenerateCompose in
// internal/codegen/inject_gen.go.
//
// It is gated on the primary binary's cmd/<bin>/cmd/serve.go EXISTING — not
// on len(services|workers|operators) — because that shared serve pipeline
// imports internal/app unconditionally (OpenInfra → Build → PostBuild → typed
// mount → serverkit.Run). If internal/app were only emitted on the
// entrypoint-bearing path, a degenerate tree (no proto service parses / no
// forge_descriptor.json, so ctx.Services is empty) would leave internal/app
// EMPTY while serve.go still imports it — and `go mod tidy` would 404 trying
// to fetch the local package remotely. The generators + templates render
// valid empty Build/Inventory/Services/lifecycle so the package always compiles.
//
// Runs BEFORE the go-mod-tidy steps so the local package is resolvable.
// Skipped silently when serve.go doesn't exist (CLI/library kinds and
// codegen-less trees have no serve pipeline that imports internal/app).
// stepRootCommandsWithoutGraph writes the command tree's roots_gen.go for a
// project without codegen: no composition graph, so no roots, and root.go's
// rootCommands call still resolves. A project with no command tree (CLI,
// library) is a no-op. See codegen.EmitRootCommandsWithoutGraph.
func stepRootCommandsWithoutGraph(ctx *pipelineContext) error {
	return codegen.EmitRootCommandsWithoutGraph(ctx.ProjectDir, ctx.Checksums)
}

func stepInternalAppComposition(ctx *pipelineContext) error {
	bin := bootstrapBinaryName(ctx.ProjectDir)
	if _, err := os.Stat(filepath.Join(ctx.ProjectDir, "cmd", bin, "cmd", "serve.go")); err != nil {
		return nil
	}

	rows, err := ctx.rowServiceDefs()
	if err != nil {
		return err
	}
	packages, err := discoverPackages(ctx.ProjectDir)
	if err != nil {
		return fmt.Errorf("discover internal packages: %w", err)
	}
	webhookServices := discoverWebhookServices(ctx.ProjectDir)

	// ctx.ModulePath is only populated by the entrypoint-gated parse step, so
	// it may be empty here (the very case this step exists to cover). Read it
	// directly from go.mod as a fallback so the generated import lines resolve.
	modulePath := ctx.ModulePath
	if modulePath == "" {
		modulePath, err = codegen.GetModulePath(ctx.ProjectDir)
		if err != nil {
			return fmt.Errorf("read module path for internal/app composition: %w", err)
		}
	}

	var dbDriver string
	if ctx.Cfg != nil {
		dbDriver = ctx.Cfg.Database.Driver
	}
	ormEnabled, err := deriveOrmEnabled(ctx.ProjectDir)
	if err != nil {
		return err
	}

	roots, err := generateHybridComposition(rows, packages, modulePath, dbDriver, ormEnabled, ctx.ProjectDir, webhookServices, ctx.Checksums)
	if err != nil {
		return fmt.Errorf("internal/app composition generation failed: %w", err)
	}
	ctx.graphRoots, ctx.graphRootsSet = roots, true
	return nil
}

// stepRootsEnvSchema writes pkg/config/roots_gen.go: each root's config reach
// as Go another module imports (codegen.GenerateRootsGo) — the reach
// config_gen.k's ROOTS projects, for a project with or without deploy envs.
func stepRootsEnvSchema(ctx *pipelineContext) error {
	var roots []codegen.GraphRoot
	if ctx.graphRootsSet {
		roots = ctx.graphRoots
	}
	return codegen.GenerateRootsGo(ctx.ProjectDir, roots, ctx.Checksums)
}

// rootsForDeployConfig is the roots config_gen.k projects: the ones the
// composition step read this run, else (a run that did not compose — a
// narrowed --steps run) the graph assembled afresh. A graph that does not
// assemble projects no ROOTS; generate's own composition step reports why.
func (ctx *pipelineContext) rootsForDeployConfig() []codegen.GraphRoot {
	if ctx.graphRootsSet {
		return ctx.graphRoots
	}
	roots, err := codegen.GraphRoots(ctx.ProjectDir)
	if err != nil {
		return nil
	}
	return roots
}

// stepCmdGroups anchors the dir-nested per-component command-group subpackages
// under cmd/<bin>/cmd/{services,workers,operators} (one file per service /
// worker / operator + an anchor per group so the subpackage compiles with zero
// items: Tier-1 register_gen.go for services, which projects the built-in
// collision NOTEs, and scaffold-once register.go for workers/operators, which
// project nothing).
//
// It ALSO writes cmd/<bin>/main.go — the composition root that NAMES every
// group constructor. That write is scaffold-once (write-if-absent), and this is
// the ONLY place it happens for a codegen project, because this is the only
// pass that knows the service inventory: `forge project new` deliberately does
// not pre-write a bare main.go, since write-if-absent would then make the empty
// root permanent and leave every generated per-service subcommand unreferenced.
//
// Ordering is load-bearing: this MUST run AFTER stepRegenerateInfra, which is
// what (re)creates cmd/<bin>/cmd/serve.go. On a flat→nested migration (an old
// project with a flat cmd/*.go layout running `forge generate` for the first
// time on the nested layout) serve.go does NOT exist when
// stepInternalAppComposition runs upstream, so the group subpackages cannot be
// anchored there. main.go imports the three group subpackages, so if they
// stayed Go-file-less the subsequent `go mod tidy (root)` / `go build
// (validate)` steps would 404 the empty local import and abort the pipeline —
// exactly the kalshi migration friction (the mid-run `go mod tidy` failed on
// the not-yet-created group subpackages before they could be anchored).
// Emitting here, after serve.go exists but before those tidy/build steps, lets
// a flat→nested project reach a buildable state with no manual dir
// pre-creation and no skip-flags.
//
// Idempotent: GenerateCmdGroups writes byte-identical content on re-run, so a
// normal already-nested project (where the anchors already exist) is a no-op.
// Skipped silently when serve.go doesn't exist (CLI/library kinds and
// codegen-less trees have no serve pipeline to delegate to).
func stepCmdGroups(ctx *pipelineContext) error {
	rows, err := ctx.rowServiceDefs()
	if err != nil {
		return err
	}
	// No worker/operator discovery: cmd groups only (re)write the proto-derived
	// service subcommands + the anchor files. The per-worker/operator subcommand
	// files are scaffold-once OWNED code written by `forge scaffold worker/operator`,
	// and main.go is scaffold-once. Generate learns nothing about the
	// worker/operator set.
	return generateCmdGroups(rows, ctx.ProjectDir, ctx.Checksums)
}

// stepCmdCommands ensures the user-owned cmd/<bin>/cmd/commands.go extension
// point exists (the generated cmd/<bin>/cmd/root.go calls userCommands(), so
// a missing file would break the build on first regenerate).
//
// String-projected per-service subcommands (the old cmd/services_gen.go)
// are retired (FORGE_SHAPE_REDESIGN §1/§2): per-subcommand mount
// selection lives in the cmd layer over the data-only internal/app
// Inventory, and `server [services...]` handles the subset surface.
//
// Skipped silently when the primary binary's cmd/<bin>/cmd/serve.go doesn't
// exist: CLI/library kinds and codegen-less trees have no serve pipeline to
// delegate to.
func stepCmdCommands(ctx *pipelineContext) error {
	bin := bootstrapBinaryName(ctx.ProjectDir)
	if _, err := os.Stat(filepath.Join(ctx.ProjectDir, "cmd", bin, "cmd", "serve.go")); err != nil {
		return nil
	}
	if err := codegen.GenerateCmdCommands(ctx.ProjectDir, bin); err != nil {
		return fmt.Errorf("scaffold cmd/%s/cmd/commands.go: %w", bin, err)
	}
	return nil
}

// stepBootstrapTesting — was Step 6b.
// Emits internal/handlers/<svc>/helpers_gen_test.go (one per component).
func stepBootstrapTesting(ctx *pipelineContext) error {
	rows, err := ctx.rowServiceDefs()
	if err != nil {
		return err
	}
	if err := generateBootstrapTesting(rows, ctx.ModulePath, ctx.ProjectDir, ctx.Checksums); err != nil {
		return fmt.Errorf("bootstrap testing generation failed: %w", err)
	}
	return nil
}

// stepBootstrapMigrate — was Step 6c.
// Emits db/embed.go (the embedded migration set) when a database driver is
// configured, and retires the pkg/app/migrate.go older projects carry — the
// golang-migrate ceremony that used to sit there now lives in
// pkg/migratekit.AutoMigrate. Uses cfg.ModulePath rather than ctx.ModulePath
// because ctx.ModulePath may be empty for projects with no proto/services
// (CLI / library kinds); migrate-gen is fine with cfg.ModulePath alone.
func stepBootstrapMigrate(ctx *pipelineContext) error {
	if err := generateMigrate(ctx.ProjectDir, ctx.Cfg.ModulePath, ctx.Checksums); err != nil {
		return fmt.Errorf("migrate generation failed: %w", err)
	}
	return nil
}

// stepGoModTidyGen — was Step 8.
// `go mod tidy` inside gen/ (the standalone module that holds proto
// stubs). PROMOTED 2026-06-07 from "best-effort warn" to hard error
// (regardless of --strict): a failed tidy in gen/ guarantees the next
// `go build ./...` validate step will fail with confusing missing-import
// errors that point at GENERATED code, not at the missing-go-sum-entry
// that caused them. Failing loudly here puts the actionable signal
// (gen/go.mod or gen/go.sum needs attention) right at the point of
// failure.
//
// If a project legitimately can't tidy gen/ (e.g. mid-migration with an
// unresolvable replace directive) the workaround is to fix go.mod / go.sum
// directly rather than asking forge to ignore the failure — silent skip
// would just push the failure surface to a different generated file later.
func stepGoModTidyGen(ctx *pipelineContext) error {
	if err := runGoModTidyGen(ctx.ProjectDir); err != nil {
		return fmt.Errorf("go mod tidy in gen/ failed: %w (subsequent `go build ./...` will fail with confusing missing-import errors; fix gen/go.mod or gen/go.sum)", err)
	}
	return nil
}

// stepGoModTidyPreWiring runs `go mod tidy` (gen/ first, then root)
// BEFORE the pkg/app wiring emitters. The bootstrap/testing/wire_gen
// generators use go/packages type loads (deps-assignability matcher,
// cross-package auto-stub synthesis); on a cold tree — fresh scaffold,
// no go.sum, replace-only forge/pkg requirement — those loads fail
// until the FIRST tidy runs, which historically happened AFTER the
// wiring emitters. Net effect: generate run 1 rendered the degraded
// shape (TODO stubs, unproven matches) and run 2 rendered the resolved
// shape — `forge generate` output was not a pure function of the
// project's source state (the fixture-corpus idempotency assertion
// caught the test-helper files flipping between runs).
//
// Best-effort by design: a mid-edit tree whose imports don't resolve
// yet must degrade exactly as before (warn + emit the degraded shape),
// not abort. The authoritative tidy steps later in the pipeline stay
// loud. On a converged tree both tidies are fast no-ops.
func stepGoModTidyPreWiring(ctx *pipelineContext) error {
	if err := runGoModTidyGen(ctx.ProjectDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  pre-wiring go mod tidy (gen/) failed (continuing; wiring codegen may degrade to its unproven/TODO fallbacks): %v\n", err)
	}
	if err := runGoModTidyRoot(ctx.ProjectDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  pre-wiring go mod tidy (root) failed (continuing; wiring codegen may degrade to its unproven/TODO fallbacks): %v\n", err)
	}
	return nil
}

// stepGoModTidyPreComposition runs `go mod tidy` (gen/ first, then root)
// BEFORE the composition step, on a project generate has composed before.
//
// The composition graph reads what each node IS off one go/packages load of
// internal/app and the component packages: the type a provider produces, the
// lifecycle methods a listener has, whether a node is the database. That load
// fails outright when go.mod or go.sum is behind the tree ("updates to go.mod
// needed") — a fresh clone whose replace was just added, a hand-edited
// go.mod, a dependency bump — and the graph then degrades to names alone. It
// is not a small degradation: no node is typed, so no root field is a
// listener, the server root holds no nodes, and generate writes a
// graph_gen.go with no lifecycle, a roots_gen.go that reaches no database
// and a config_gen.k that refuses the scaffold's own
// `config_secrets = ["DATABASE_URL"]`. The tidy after composition
// (stepGoModTidyPreWiring) repairs go.mod, so the NEXT run is right: output
// that depends on the run count, not the tree.
//
// A project with no internal/app/compose.go yet has nothing for the load to
// read (and its cmd imports an internal/app that does not exist, so a tidy
// would only fail), so this is a no-op there; the post-composition tidy
// covers the cold tree. Best-effort like that one: a tree whose imports do
// not resolve degrades as before, with a warning saying so.
func stepGoModTidyPreComposition(ctx *pipelineContext) error {
	if _, err := os.Stat(filepath.Join(ctx.ProjectDir, "internal", "app", "compose.go")); err != nil {
		return nil
	}
	if err := runGoModTidyGen(ctx.ProjectDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  pre-composition go mod tidy (gen/) failed (continuing; the composition graph may not type-check and degrade to names): %v\n", err)
	}
	if err := runGoModTidyRoot(ctx.ProjectDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  pre-composition go mod tidy (root) failed (continuing; the composition graph may not type-check and degrade to names): %v\n", err)
	}
	return nil
}

// stepCIWorkflows — was Step 8b.
// Renders .github/workflows/*.yml from forge.yaml. Non-fatal: CI/CD
// is opt-in and most local-dev iterations don't need it regenerated.
func stepCIWorkflows(ctx *pipelineContext) error {
	fmt.Println("\n🔧 Generating CI/CD workflows...")
	if err := generateCIWorkflows(ctx.AbsPath, ctx.Cfg, ctx.Checksums, ctx.Force); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  CI/CD generation warning: %v\n", err)
	}
	return nil
}

// stepRegenerateInfra — was Step 8d.
// Regenerates Tier-1 infrastructure files (Dockerfile, Taskfile,
// .dockerignore, .gitignore, deploy/k3d.yaml, etc.) from templates.
// Fatal: the Tier-1 emitter is the source of truth for these files;
// silent failure produces drift that bites at deploy time.
func stepRegenerateInfra(ctx *pipelineContext) error {
	fmt.Println("\n── Regenerating infrastructure files ──")
	// Tracked variant: routes every write through the checksums
	// chokepoint so forked entries are honored (side-rendered, not
	// stomped) and the manifest records what this run emitted.
	if err := generator.RegenerateInfraFilesTracked(ctx.AbsPath, ctx.Cfg, ctx.Checksums); err != nil {
		return fmt.Errorf("regenerate infrastructure files: %w", err)
	}
	return nil
}

// stepDiscoverComponents reads the project's component inventory off its real
// sources into ctx.Components, for the steps that consume it downstream.
//
// WHY HERE, and not at config load: one of the sources IS a pipeline output.
// Servers are declared by the forge descriptor (gen/forge_descriptor.json),
// which THIS pipeline regenerates in the "descriptor extraction" step, so
// reading at step 0 would see a stale-or-absent descriptor on a fresh
// project. Discovery therefore runs AFTER the descriptor is materialized and
// before the steps that read the inventory — the workloads.k scaffold (only
// on a project that has none yet) and the drift lint that reports what the
// tree declares but deploy/kcl/workloads.k does not.
func stepDiscoverComponents(ctx *pipelineContext) error {
	if ctx.Cfg == nil {
		return nil
	}
	ctx.Components = codegen.DiscoverProjectComponents(ctx.AbsPath, ctx.Cfg.Name)
	return nil
}

// stepWorkloadsKCL creates deploy/kcl/workloads.k when the project does not
// have one, seeded with the components discovered so far.
//
// It is a NO-OP when the file exists, and that is the point: workloads.k is
// the user-owned declaration of what the project is made of, so the only safe
// automatic write is the first one. `forge scaffold <kind>` appends new
// entries; drift between the tree and this file is REPORTED by `forge lint`
// (with the exact stanza to paste) rather than silently repaired, because
// forge cannot tell an oversight from a deliberate omission.
// stepCRDKCL projects the project's CRD Go types into
// deploy/kcl/lib/crd_gen.k. See generateCRDKCL for why the Go types are the
// single source and what the hand-authored alternative silently costs.
func stepCRDKCL(ctx *pipelineContext) error {
	kclDir := ctx.Cfg.K8s.KCLDir
	if kclDir == "" {
		kclDir = "deploy/kcl"
	}
	return ctx.warnOrFail("CRD KCL manifest generation",
		generateCRDKCL(ctx.AbsPath, filepath.Join(ctx.AbsPath, kclDir), ctx.Checksums))
}

// gateObservabilityDashboardsEnabled is the config half of the gate: the
// observability feature, and codegen to describe the services with. It reads
// nothing the pipeline derives, so the Tier-1 stomp guard (which runs before
// services are parsed) can use it too.
//
// The feature is the ONE switch. This used to require the project kind to be
// "service" as well, which observability already implies when derived (it
// follows a server's sources) and which a `features: {observability: ...}`
// pin could not reach.
func gateObservabilityDashboardsEnabled(ctx *pipelineContext) bool {
	return ctx.Cfg != nil && ctx.Cfg.Features.ObservabilityEnabled() && ctx.Cfg.Features.CodegenEnabled()
}

// gateObservabilityDashboards adds the other half: there is nothing to draw
// without a Connect service.
func gateObservabilityDashboards(ctx *pipelineContext) bool {
	return gateObservabilityDashboardsEnabled(ctx) && len(ctx.Services) > 0
}

// stepObservabilityDashboards writes deploy/observability/dashboards/*_gen.json
// (HyperDX v2 dashboards), the renderer-neutral dashboard-spec_gen.json beside
// it (which also carries the pending alert definitions) and the saved searches.
func stepObservabilityDashboards(ctx *pipelineContext) error {
	// The database panels read spans the ORM's query hook emits, so they track
	// whether the project has a schema. Database.Driver cannot say: it is
	// derived from the db/migrations DIRECTORY, which every service scaffold
	// creates (empty, under a .gitkeep), so it is never "" and names a
	// database before the first migration exists.
	in := obsdash.Input{Project: ctx.Cfg.Name, HasDatabase: codegen.ProjectHasSQLMigrations(ctx.AbsPath)}
	for _, s := range ctx.Services {
		svc := obsdash.Service{Name: naming.ServicePackage(s.Name), Package: s.Package, ProtoService: s.Name}
		for _, m := range s.Methods {
			svc.Methods = append(svc.Methods, obsdash.Method{Name: m.Name})
		}
		in.Services = append(in.Services, svc)
	}
	for _, fe := range ctx.Cfg.Frontends {
		if fe.Type != "react-native" {
			in.Frontends = append(in.Frontends, fe.Name)
		}
	}
	announceDerivedObservability(os.Stdout, ctx)
	return ctx.warnOrFail("observability dashboards", func() error {
		funnels, err := declaredFunnels(ctx.renderedFrontends())
		if err != nil {
			return err
		}
		in.Funnels = funnels
		written, err := obsdash.Write(ctx.AbsPath, in, ctx.Checksums)
		if err != nil {
			return err
		}
		fmt.Printf("  ✅ Generated %d observability file(s) under deploy/observability/\n", len(written))
		return nil
	}())
}

// announceDerivedObservability says so, once, when a DERIVED observability
// feature is about to write deploy/observability/ for the first time.
//
// Nothing the user did asked for dashboards. The feature follows a server's
// sources, so the first internal/handlers/ — a CLI-shaped project adopting
// forge's handler model one service at a time — turns it on, and the run
// that adopts the service starts writing a directory of JSON nobody asked
// for (P3b friction F15). The structural change is legitimate; doing it
// without a word is not. The moment worth a line is exactly this one: the
// feature is on, nothing it writes exists yet, and nobody pinned it. Every
// later run finds its outputs on disk and says nothing.
func announceDerivedObservability(w io.Writer, ctx *pipelineContext) {
	if ctx.Cfg.Features.Pinned(config.FeatureObservability) || observabilityOutputsExist(ctx.AbsPath) {
		return
	}
	because := "a server's sources exist"
	for _, rel := range []string{"internal/handlers", "pkg/app", "proto/services"} {
		if info, err := os.Stat(filepath.Join(ctx.AbsPath, rel)); err == nil && info.IsDir() {
			because = rel + "/ exists"
			break
		}
	}
	fmt.Fprintf(w, "  ℹ️  observability is on (derived: %s), so forge now writes deploy/observability/: dashboards, alerts and saved searches for each service.\n", because)
	fmt.Fprintf(w, "      To keep it off, pin it in forge.yaml:  features: {observability: false}   (`forge project features` lists every feature and why)\n")
}

// observabilityOutputsExist reports whether any generated observability file
// is already on disk.
func observabilityOutputsExist(root string) bool {
	for _, rel := range []string{obsdash.ProjectSpecFile, obsdash.AlertDefaultsFile, obsdash.LegacySpecFile} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(obsdash.DashboardsDir), "*_gen.json"))
	return len(matches) > 0
}

func stepWorkloadsKCL(ctx *pipelineContext) error {
	return ctx.warnOrFail("workloads.k scaffold",
		generator.ScaffoldWorkloadsKCL(ctx.AbsPath, ctx.Cfg.ModulePath, ctx.Cfg.Name, ctx.Components, ctx.Cfg.Features.MigrationsEnabled()))
}

// stepPerEnvDeployConfig — was Step 8d-0.
// Emits the KCL config files: the project-level config_gen.k (typed
// schema + projection lambda), and a per-env config.k scaffolded from
// proto defaults for each environment on disk. The env's `main.k` imports
// config_gen + its config.k to project config into env_vars.
func stepPerEnvDeployConfig(ctx *pipelineContext) error {
	return ctx.warnOrFail("per-env deploy config generation",
		generatePerEnvDeployConfig(ctx.ProjectDir, ctx.Cfg, ctx.Checksums, ctx.rootsForDeployConfig))
}

// stepIngressK3dPorts derives the k3d host→cluster port mappings
// from the dev env's KCL gateway listeners and writes
// `deploy/k3d-ports.yaml`. `forge cluster up` merges this
// fragment into the user-owned `deploy/k3d.yaml` at create time —
// see internal/codegen/ingress_k3d_gen.go for the rendered shape
// and the why behind the merge-at-create model.
//
// When the dev env has no gateways (e.g. the project just enabled
// features.ingress but hasn't authored ingress.k yet), the existing
// fragment is removed so a stale port-mapping doesn't linger.
func stepIngressK3dPorts(ctx *pipelineContext) error {
	listeners, err := collectDevGatewayListeners(ctx)
	if err != nil {
		// KCL render failures here are non-fatal by default — the project
		// may not have a dev env yet, or kcl may not be installed locally.
		// --strict promotes the warning to fatal.
		return ctx.warnOrFail("k3d-ports.yaml generation (dev gateway listener collection)", err)
	}
	if len(listeners) == 0 {
		return ctx.warnOrFail("remove stale k3d-ports.yaml", codegen.RemoveK3dPorts(ctx.ProjectDir))
	}
	// The fragment maps every reachable stack's listener ports, not just the
	// default stack's — see K3dPortsGenInput.MaxStacks for why the range is
	// derived from the ceiling instead of hand-maintained in k3d.yaml.
	return ctx.warnOrFail("write deploy/k3d-ports.yaml", codegen.GenerateK3dPorts(codegen.K3dPortsGenInput{
		GenContext: codegen.GenContext{ProjectDir: ctx.ProjectDir, Checksums: ctx.Checksums},
		Listeners:  listeners,
		MaxStacks:  ctx.Cfg.DevStack.EffectiveMaxStacks(),
		BlockSize:  ctx.Cfg.DevStack.EffectiveBlockSize(),
	}))
}

// collectDevGatewayListeners evaluates the dev env's KCL and projects
// the Gateway listener set into the shape ingress_k3d_gen expects.
// Returns (nil, nil) when the dev env isn't configured yet — that's
// a normal first-scaffold state.
func collectDevGatewayListeners(ctx *pipelineContext) ([]codegen.K3dListener, error) {
	devKCL := filepath.Join(ctx.ProjectDir, "deploy", "kcl", "dev")
	if _, err := os.Stat(devKCL); err != nil {
		return nil, nil
	}
	// renderKCLPure, not RenderKCL: this is a READ (the dev gateway listener
	// set), but evaluating the module fires any file.write in it, and a write
	// that lands on a tracked file makes `forge generate` non-reproducible.
	// See internal/cli/kcl_render_purity.go for the incident.
	entities, restored, err := renderKCLPure(context.Background(), ctx.ProjectDir, "dev")
	if err != nil {
		return nil, err
	}
	reportImpureRender("dev", restored)
	if entities == nil {
		return nil, nil
	}
	var out []codegen.K3dListener
	for _, gw := range entities.Gateways {
		for _, l := range gw.Listeners {
			out = append(out, codegen.K3dListener{
				GatewayName:  gw.Name,
				ListenerName: l.Name,
				Port:         l.Port,
			})
		}
	}
	return out, nil
}

// stepEntitySeeds — was Step 8d-ii.
// Seed data is no longer projected into the user project as .sql files:
// under the vertical-scaffolding law forge writes no new forge-owned files
// into user space. Seeds now materialize at RUNTIME via `forge db seed`
// (pkg/seedplan) and `forge env up` auto-seed — this step no longer emits
// anything. It survives only to parse the entity protos once and stash the
// EntityDefs that stepFrontendMocks consumes (frontend mock values still
// mirror the seed vocabulary).
func stepEntitySeeds(ctx *pipelineContext) error {
	entityDefs, parseErr := codegen.ParseEntityProtos(ctx.ProjectDir)
	if parseErr != nil {
		return ctx.warnOrFail("entity proto parsing for seeds", parseErr)
	}
	ctx.EntityDefs = entityDefs
	return nil
}

// stepFrontendMocks — was Step 8d-iii.
// Always runs when frontends are configured even with no entities or
// services: the inner emitter falls back to a no-op `mock-transport_gen.ts`
// stub so connect.ts's `require('@/lib/mock-transport_gen')` resolves
// at build time. Without that stub, `npm run build` fails during the
// `/_not-found` prerender with a webpack module-resolution error that
// doesn't finger-point at the missing file.
func stepFrontendMocks(ctx *pipelineContext) error {
	return ctx.warnOrFail("frontend mock generation",
		generateFrontendMocks(ctx.Cfg, ctx.Services, ctx.EntityDefs, ctx.ProjectDir, ctx.Checksums))
}

// stepFrontendRenamedImports repoints imports of the frontend Tier-1
// modules that gained a `_gen` suffix.
//
// It runs AFTER every frontend emit step, for the ordering reason a rename
// always has: the new module must exist on disk before anything is pointed
// at it. The importers it fixes are the user-owned files forge does not
// rewrite — connect.ts, providers.tsx, hand-written scenarios, the
// scaffold-once hook tests — which would otherwise name a module that the
// retirement sweep just deleted.
//
// A no-op for any project scaffolded after the rename, which is why it is
// silent unless it actually changes something.
func stepFrontendRenamedImports(ctx *pipelineContext) error {
	var hookFiles []string
	for _, svc := range ctx.Services {
		hookFiles = append(hookFiles, naming.ServiceHookFile(svc.Name))
	}
	rewriteRenamedFrontendImports(ctx.Cfg, ctx.ProjectDir, hookModuleRenames(hookFiles))
	// Same lane, same justification: a user-owned frontend file that forge's
	// own change would otherwise leave broken. See generate_frontend_dedupe.go.
	reconcileFrontendDedupe(ctx.Cfg, ctx.ProjectDir)
	// And the tsc half of the same hazard: the bundler configs above never
	// run under `tsc --noEmit`, which is what `forge lint` invokes. See
	// generate_frontend_tsconfig_peers.go.
	reconcileFrontendTsconfigPeers(ctx.Cfg, ctx.ProjectDir)
	return nil
}

// stepGoModTidyRoot — was Step 8e.
// `go mod tidy` in the project root. PROMOTED 2026-06-07 from "best-effort
// warn" to hard error (regardless of --strict) for the same reason as
// stepGoModTidyGen: a failed tidy guarantees the subsequent `go build
// ./...` validate step fails with missing-import errors that look like
// codegen bugs but are actually go.mod skew.
func stepGoModTidyRoot(ctx *pipelineContext) error {
	if err := runGoModTidyRoot(ctx.ProjectDir); err != nil {
		return fmt.Errorf("go mod tidy in project root failed: %w", err)
	}
	return nil
}

// stepGoimports — was Step 8f.
// Runs goimports on every generated Go file to canonicalize import
// groups. Resolves the module path lazily because some projects
// (library/CLI kinds with no proto services) won't have set ctx.ModulePath
// in the mid-pipeline cluster.
func stepGoimports(ctx *pipelineContext) error {
	if ctx.ModulePath == "" {
		ctx.ModulePath, _ = codegen.GetModulePath(ctx.ProjectDir)
	}
	if ctx.ModulePath == "" {
		return nil
	}
	return ctx.warnOrFail("goimports", runGoimportsOnGenerated(ctx.ProjectDir, ctx.ModulePath))
}

// stepRehashTracked — was Step 8f.1.
// Re-stamps every file written this run after goimports. goimports
// rewrites imports in-place after the chokepoint stamped the
// pre-formatted content; without the re-stamp, the embedded hash would
// be stale and the next run's guard would flag every formatted file as
// "hand-edited". Also flushes the deferred heal notices: a "heal" whose
// post-format bytes converged back to the replaced content was
// formatting noise, not a content change, and stays silent.
func stepRehashTracked(ctx *pipelineContext) error {
	checksums.RestampWritten(ctx.AbsPath, ctx.Checksums)
	checksums.FlushHealNotices(ctx.AbsPath)
	return nil
}

// stepPostGenValidate — was Step 8g.
// Runs the heuristic post-gen warnings (orphan handlers, etc.).
// Always non-fatal: warnings only.
//
// It no longer reports proto-vs-migration "schema drift". That check paired
// proto message fields with migration columns BY NAME, which only made sense
// while proto entity annotations declared the schema. Migrations are now the
// only schema truth and proto is the wire shape, so the pairing compares two
// things that were never required to agree: an API response message carries
// derived fields (users.onboarding_completed) and fields the table stores
// inside JSONB (plans.price_cents), and every one of them was reported as a
// missing column with a suggested ALTER that must not be applied. In
// control-plane it produced 19 such suggestions on a clean tree, so the
// signal was noise by construction rather than by tuning.
func stepPostGenValidate(ctx *pipelineContext) error {
	if warnings := validateGeneratedProject(ctx.ProjectDir, ctx.Cfg, ctx.Services, ctx.EntityDefs); len(warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\n⚠️  Post-generation warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "  • %s\n", w)
		}
	}
	// Scaffold-once files forge has written before and that are now absent.
	// The run that motivated this deleted one expecting a re-derivation, got
	// a silent success, and could not find the one edit that restores it.
	// See generate_missing_scaffold.go — informational, never fatal.
	reportMissingScaffolds(os.Stderr, ctx.AbsPath)
	return nil
}

// stepGoBuildValidate — was Step 9.
// Runs `go build ./...` to catch any codegen that produced
// non-compiling Go. Includes the pre-refactor heuristic hints
// (forge/pkg pin skew / pkg/config) on failure to nudge the user
// toward the most likely fix.
func stepGoBuildValidate(ctx *pipelineContext) error {
	return runGoBuildValidate(ctx.ProjectDir)
}
