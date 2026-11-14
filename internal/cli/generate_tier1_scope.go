// Tier-1 stomp-guard scoping.
//
// The pre-pipeline stomp guard (stepCheckTier1Drift) used to fail on
// ANY drifted Tier-1 file the scan found, even when the
// current `forge generate` invocation would never re-emit that file.
// In multi-lane migrations that hard-failed the guard for sibling work
// (e.g. agent A is porting internal/proxy/ and the guard rejected
// because agent B left pkg/app/migrate.go drifted in a separate
// changeset). FRICTION 2026-06-02: cp-forge dogfood pass.
//
// Resolution: the guard now filters drift to the set of paths whose
// owning emitter step would actually run for this pipelineContext. A
// drifted file emitted by a step whose Gate returns false this run is
// silently ignored — that step wouldn't touch the file, so its drift
// cannot manifest as a stomp.
//
// The path → owning-gate registry is intentionally explicit (small map
// at the bottom of this file). Adding a new Tier-1 emitter SHOULD add
// an entry here so the scoping logic stays accurate; the registry is
// fail-open (an unmapped path falls through to "in scope" so missing
// entries err on the safe side of preserving the loud-fail behavior).
//
// The registry is keyed by exact path, trailing-slash prefix, or
// suffix-glob (path/segment pattern with a leading '*' wildcard). The
// matcher walks the entries in declaration order and returns the first
// match, so order from most-specific to least-specific.
package cli

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

// tier1OwnerGate returns the predicate function that decides whether
// the emitter for relPath would actually run for the current pipeline
// context. A nil return means "no registered owner" — the caller should
// treat this as in-scope (fail-closed for unknown paths so newly added
// emitters don't accidentally silence drift).
func tier1OwnerGate(relPath string) func(*pipelineContext) bool {
	for _, e := range tier1OwnerRegistry {
		if e.match(relPath) {
			return e.gate
		}
	}
	return nil
}

// tier1OwnerEntry pairs a path matcher with the gate function controlling
// the step that emits the file. Exactly one of `exact`, `prefix`, or
// `glob` is set per entry.
//
//   - exact:  full relative-path equality (e.g. "pkg/app/migrate.go").
//   - prefix: trailing-slash directory prefix matching every path under a
//     directory (e.g. "internal/db/").
//   - suffix: base-name suffix, in any directory (e.g. "_orm_gen.go", the
//     ORM's files wherever database.orm.package puts them).
//   - glob:   suffix-aware shell glob applied per segment using
//     path.Match semantics (e.g.
//     "internal/handlers/*/handlers_crud_ops_gen.go",
//     "internal/db/*_orm.go",
//     "frontends/*/src/hooks/*-hooks.ts").
type tier1OwnerEntry struct {
	exact  string
	prefix string
	suffix string
	glob   string
	gate   func(*pipelineContext) bool
}

func (e tier1OwnerEntry) match(relPath string) bool {
	if e.exact != "" {
		return relPath == e.exact
	}
	if e.prefix != "" {
		return strings.HasPrefix(relPath, e.prefix)
	}
	if e.suffix != "" {
		return strings.HasSuffix(path.Base(relPath), e.suffix)
	}
	if e.glob != "" {
		// path/filepath.Match handles `*` per-segment (does NOT cross
		// `/`). For a multi-segment wildcard we'd need doublestar, but
		// the registered globs only need single-segment `*` (e.g.
		// `<svc>` or `<frontend-name>`), so the stdlib is enough.
		ok, err := filepath.Match(e.glob, relPath)
		if err != nil {
			return false
		}
		return ok
	}
	return false
}

// tier1OwnerRegistry maps Tier-1 file paths (or prefixes) to the gate
// that controls their emitter step. Order matters — entries are tried
// most-specific first.
//
// Coverage is intentionally narrow: only the FRICTION-surfaced classes
// of file (pkg/app/* and db/embed.go) are registered. Other Tier-1
// paths fall through to "no registered owner" and remain in scope so
// the stomp guard fails loudly on drift, preserving the original
// behavior. As new friction surfaces, add the relevant emitter here.
var tier1OwnerRegistry = []tier1OwnerEntry{
	// db/embed.go is emitted by stepBootstrapMigrate, gated on the project
	// having a database driver configured. A project without a driver
	// shouldn't see its drift block `forge generate` runs.
	//
	// pkg/app/migrate.go used to be listed here beside it. It is no longer
	// emitted at all — the migration logic moved to pkg/migratekit and the
	// step retires the on-disk copy — so there is no gate to register.
	{exact: "db/embed_gen.go", gate: gateMigrateHasDriver},

	// pkg/app/app_gen.go (the minimal *App carrier) is emitted by
	// stepAppSubstrate, gated on the project having at least one entrypoint
	// (services, workers, operators). A pure-CLI project shouldn't see it
	// blocking the guard. (The old name-matched DI files — bootstrap.go,
	// wire_gen.go, services_gen.go — are retired, FORGE_SHAPE_REDESIGN §2;
	// the live DI lives under internal/app.)
	{exact: "pkg/app/app_gen.go", gate: gateCodegenHasAnyEntrypoint},

	// The per-component test harness, emitted by stepBootstrapTesting as one
	// helpers_gen_test.go per service / internal package (it replaced the
	// single pkg/app/testing.go, whose `testing` import reached cmd/ through
	// pkg/app). Same entrypoint gate as before, now expressed per component.
	{glob: "internal/handlers/*/helpers_gen_test.go", gate: gateCodegenHasAnyEntrypoint},
	{glob: "internal/*/helpers_gen_test.go", gate: gateCodegenHasAnyEntrypoint},

	// The typed entity factories, now one factories_gen_test.go per handler
	// package that owns entities (they were a separate importable package,
	// internal/testfactory, whose non-test .go file imported `testing`).
	// Gated on codegen + services: a project with no entities never emits it.
	{glob: "internal/handlers/*/factories_gen_test.go", gate: gateCodegenHasServices},

	// internal/handlers/<svc>/handlers_crud_ops_gen.go is emitted by
	// stepCRUDHandlers (the Tier-1 projection half of the CRUD split; the
	// RPC implementations live in user-owned handlers_crud.go). The gate
	// must be the emitter's own (gateCRUDProjection — codegen AND services
	// AND features.orm), not a looser one: when the projection step is
	// gated off, absence from WrittenThisRun says nothing about staleness,
	// exactly as for the ORM entries below.
	{glob: "internal/handlers/*/handlers_crud_ops_gen.go", gate: gateCRUDProjection},

	// <orm package>/orm_shared_gen.go + *_orm_gen.go are emitted by
	// stepInternalDBORM (M3: the ORM emitter records its outputs in the
	// manifest so `forge project disown` works on them and audit doesn't see
	// orphans). When the ORM step is gated off (features.orm=false or
	// no services), absence from WrittenThisRun is uninformative and
	// the stale sweep must leave the tracked entity code alone. Matched by
	// name in any directory: database.orm.package moves them out of
	// internal/db.
	{suffix: "orm_shared_gen.go", gate: gateORMHasServices},
	{suffix: "_orm_gen.go", gate: gateORMHasServices},

	// frontends/<name>/src/hooks/*-hooks.ts is emitted by
	// stepFrontendHooks. Gated on frontend feature + HasServices
	// (no services → no proto → nothing to hook).
	{glob: "frontends/*/src/hooks/*-hooks.ts", gate: gateFrontendHasServices},

	// packages/hooks/src/generated/*-hooks.ts is the same emitter's
	// workspace-mode output (cfg.IsFrontendWorkspacesEnabled()) — one
	// shared file per service instead of one per frontend. Same gate.
	{glob: "packages/hooks/src/generated/*-hooks.ts", gate: gateFrontendHasServices},

	// internal/<pkg>/mock_gen.go is emitted by stepInternalContracts
	// (gateContractsEnabled). When features.contracts=false the emitter
	// never runs, so absence-from-WrittenThisRun is uninformative and
	// the stale sweep must leave the mocks alone — the sweep
	// deleting live mocks that package tests compile against is the
	// disaster case (kalshi FORGE_BACKLOG #15). Two depth variants
	// because filepath.Match's `*` doesn't cross `/` and the contracts
	// walk descends into nested packages (internal/mcp/database).
	// Deeper nesting falls through to "no registered owner" which is
	// fail-closed (stays in scope) — same posture as every other
	// unregistered path.
	// Generated observability files are emitted by stepObservabilityDashboards;
	// when it is gated off (observability off, no services) absence from
	// WrittenThisRun says nothing about staleness.
	{glob: "deploy/observability/dashboards/*_gen.json", gate: gateObservabilityDashboardsEnabled},
	{glob: "deploy/observability/dashboards/*_gen.ndjson", gate: gateObservabilityDashboardsEnabled},
	{glob: "deploy/observability/spec/*_gen.json", gate: gateObservabilityDashboardsEnabled},
	// The whole-project spec the per-service parts replaced: the step
	// retires it, so it too waits for the step to run.
	{exact: "deploy/observability/dashboard-spec_gen.json", gate: gateObservabilityDashboardsEnabled},
	{exact: "deploy/observability/alert-defaults_gen.json", gate: gateObservabilityDashboardsEnabled},

	{glob: "internal/*/mock_gen.go", gate: gateContractsEnabled},
	{glob: "internal/*/*/mock_gen.go", gate: gateContractsEnabled},
}

// filterTier1DriftInScope returns the subset of `drift` whose owning
// emitter would run in the current pipeline context. Unknown paths
// (no entry in tier1OwnerRegistry) are passed through unchanged —
// fail-closed for the registry's blind spots.
//
// Drift on an artifact whose package has opted OUT of contract codegen
// is dropped from both buckets: the guard exists to warn before an
// emitter overwrites the bytes in place, and no emitter will. The
// contracts step reconciles those paths instead (contract/retire.go),
// and reports each one itself — silence here, a decision there.
func filterTier1DriftInScope[T any](ctx *pipelineContext, drift []T, path func(T) string) (inScope, outOfScope []T) {
	for _, d := range drift {
		rel := path(d)
		if contractArtifactOptedOut(ctx, rel) {
			continue
		}
		gate := tier1OwnerGate(rel)
		if gate == nil {
			inScope = append(inScope, d)
			continue
		}
		if gate(ctx) {
			inScope = append(inScope, d)
		} else {
			outOfScope = append(outOfScope, d)
		}
	}
	return inScope, outOfScope
}

// contractArtifactOptedOut reports whether relPath is a contract-codegen
// artifact (mock_gen.go and the observability wrappers) sitting in a
// package that has opted out of contract codegen — centrally via
// forge.yaml `contracts.exclude`, or locally via the
// `//forge:exclude-contract` directive. Both sources are consulted as a
// union, matching every other walk that honours the opt-out.
//
// Such a path is nobody's emit target. `forge generate` will not rewrite
// it; the contracts step either retires it (when forge can prove the
// bytes are its own) or leaves it untouched and says so. Telling the
// user "re-run with --force to discard your edits" about a file forge is
// not going to write would be a lie, and — before retirement existed —
// was an instruction that could not resolve anything.
func contractArtifactOptedOut(ctx *pipelineContext, relPath string) bool {
	if ctx == nil || !contract.IsRetirableArtifact(path.Base(relPath)) {
		return false
	}
	pkgPath := path.Dir(relPath)
	if ctx.Cfg != nil && ctx.Cfg.Contracts.IsExcluded(pkgPath) {
		return true
	}
	return codegen.HasExcludeContractDirective(
		filepath.Join(ctx.ProjectDir, filepath.FromSlash(pkgPath)))
}
