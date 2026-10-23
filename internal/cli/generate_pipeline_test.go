// Tests for the typed step-plan refactor of runGeneratePipeline.
//
// (2026-05-06 polish-phase) — guards FORGE_REVIEW_CODEBASE.md Tier 1.1.
//
// Two contracts are pinned here:
//
//  1. The step plan is deterministic — generateSteps() returns the same
//     ordered slice on every call. A test that asserts the names+order
//     prevents accidental drift (re-ordering steps changes generation
//     output in subtle ways: e.g. running goimports BEFORE the rehash
//     step would re-flag every emitted file as user-edited).
//
//  2. Gates are pure — calling step.Gate(ctx) twice returns the same
//     answer and does not mutate ctx. Watch-mode dispatchers and
//     `forge generate --plan` will both call gates without running the
//     associated Run, so any I/O or state mutation in a gate would leak.
package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateStepsPlanStable pins the step order. If a step is added,
// removed, or reordered, this test fails — an intentional speed bump
// that forces the change to be reviewed against the goimports-vs-rehash
// ordering trap documented in generate_pipeline.go.
//
// Test bodies and downstream consumers (--plan flag, future watch-mode
// dispatch table) read this list to confirm they're up to date.
func TestGenerateStepsPlanStable(t *testing.T) {
	want := []string{
		"load project config",
		// The refusals, before ANY step that can write: a refused generate
		// must leave the tree byte-identical. See
		// generate_refusal_no_writes_test.go.
		"forge version compatibility",
		"pre-codegen contract check",
		"retired ShellBuild tokens",
		"legacy config proto layout",
		"migrate env registries onto images",
		// Before every later gate and emitter that reads the frontend
		// inventory, so it is settled before any of them runs. See
		// generate_frontend_inventory.go.
		"derive frontend inventory",
		"load checksums",
		"migrate legacy checksums manifest",
		"check Tier-1 file-stomp guard",
		"snapshot Tier-1 exports",
		"reconcile forge_version with go.mod",
		"announce project",
		"sync forge KCL module vendor",
		"detect proto directories",
		"ensure gen/go.mod",
		"buf generate (Go stubs)",
		"descriptor extraction",
		"OpenAPI specs (protoc-gen-connect-openapi)",
		"frontend workspaces scaffold",
		"TypeScript stubs (frontends)",
		"config loader (proto/<project>/config/v1)",
		"parse services + module path",
		"frontend hooks",
		"ensure frontend components",
		"frontend CRUD pages",
		"frontend nav + dashboard",
		"service stubs",
		"internal/db/ ORM (entity-driven)",
		"CRUD handlers",
		"service mocks",
		"internal package contracts",
		"open-procedure set (pkg/middleware)",
		"webhook routes",
		"go mod tidy (pre-composition)",
		"internal/app composition (hybrid DI)",
		// Its features.codegen=false twin: no graph, so no roots — but the
		// command tree's root.go still calls rootCommands.
		"root commands without a composition graph",
		"root env schema (pkg/config/roots_gen.go)",
		"go mod tidy (pre-wiring)",
		"cmd/commands.go (user extension point)",
		"per-service test helpers",
		"db/embed.go (embedded migrations)",
		"generate hooks (forge.yaml)",
		"go mod tidy (gen/)",
		"regenerate infra files",
		"cmd command groups (services/workers/operators)",
		"discover components",
		"workloads.k (scaffold once)",
		"observability dashboards",
		"per-env deploy config",
		// AFTER per-env deploy config: the frontend's runtime document is
		// rendered from the dev env's KCL, so the generated
		// frontend_config_gen.k module and the env's config.k instance —
		// both emitted by the step above — must exist first.
		"frontend typed config",
		// AFTER the env config modules: which workflows exist is read off a
		// render of every env (a hosted env gets release.yml), and an env
		// cannot render before the config modules it imports exist.
		"CI workflows",
		"ingress k3d ports fragment",
		// Gated on features.deploy — absent from a project that declares no
		// CRDs, present here because the guard drives the full plan.
		"CRD KCL manifests (api/ Go types)",
		"entity-aware seed data",
		"frontend mocks + transport",
		"repoint renamed *_gen frontend imports",
		"retire delivered agent skills",
		"go mod tidy (root)",
		"goimports on generated Go",
		"cleanup stale codegen",
		"retire obsolete disowns",
		"rehash tracked files",
		// After every emitter parked its fresh render, before validation:
		// the drift the guard quarantined is settled here. See
		// generate_drift_rescue.go.
		"Tier-1 file-stomp guard: compare drift with fresh renders",
		"post-gen validation",
		"detect renamed Tier-1 exports",
		"check disowned-sibling dangling refs",
		"check stale scaffold tests",
		"go build (validate generated code)",
		// After the emitters, because the tree it checks is the one they
		// just produced. A deploy tree that does not compile fails generate
		// rather than warning — see generate_kcl_loadable.go.
		"deploy KCL loads",
		"record generating forge build",
	}

	steps := generateSteps()
	if len(steps) != len(want) {
		t.Fatalf("generateSteps() returned %d steps, want %d", len(steps), len(want))
	}
	for i, s := range steps {
		if s.Name != want[i] {
			t.Errorf("steps[%d].Name = %q, want %q", i, s.Name, want[i])
		}
	}
}

// TestGenerateStepsPlanDeterministic verifies generateSteps() returns
// the same names on repeat calls. Catches the (hypothetical) regression
// where a step list is built from a map (Go map iteration is
// randomized).
func TestGenerateStepsPlanDeterministic(t *testing.T) {
	first := stepNames(generateSteps())
	for i := 0; i < 5; i++ {
		next := stepNames(generateSteps())
		if !reflect.DeepEqual(first, next) {
			t.Fatalf("generateSteps() returned different order on call %d:\n first=%v\n next=%v", i+1, first, next)
		}
	}
}

// TestGenerateStepsHaveRunAndGate is a mechanical wellformedness check:
// every step needs a non-nil Gate (for the loop's predicate) and a
// non-nil Run (for the loop's action). Catches a step entry copy-pasted
// without filling in the function fields.
func TestGenerateStepsHaveRunAndGate(t *testing.T) {
	for i, s := range generateSteps() {
		if s.Name == "" {
			t.Errorf("steps[%d] has empty Name", i)
		}
		if s.Gate == nil {
			t.Errorf("steps[%d] %q has nil Gate", i, s.Name)
		}
		if s.Run == nil {
			t.Errorf("steps[%d] %q has nil Run", i, s.Name)
		}
	}
}

// TestGenerateStepsGatesAreSideEffectFree exercises each gate against a
// realistic spread of contexts and asserts:
//   - calling the gate twice returns the same answer (idempotent)
//   - calling the gate does not mutate any field on the context (pure)
//
// Future watch-mode and --plan callers depend on this: they invoke
// gates without running the associated body, often repeatedly.
func TestGenerateStepsGatesAreSideEffectFree(t *testing.T) {
	cases := []struct {
		name string
		ctx  *pipelineContext
	}{
		{"empty (dir-scan fallback)", &pipelineContext{ProjectDir: ".", AbsPath: "/abs/."}},
		{"with cfg, no protos", &pipelineContext{
			ProjectDir: ".", AbsPath: "/abs/.",
			Cfg: &config.ProjectConfig{Name: "sample"},
		}},
		{"with cfg + services + db", &pipelineContext{
			ProjectDir: ".", AbsPath: "/abs/.",
			Cfg:         &config.ProjectConfig{Name: "sample"},
			HasServices: true, HasConfig: true,
		}},
		{"with cfg + frontends", &pipelineContext{
			ProjectDir: ".", AbsPath: "/abs/.",
			Cfg: &config.ProjectConfig{
				Name:      "sample",
				Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := generateSteps()
			for _, step := range steps {
				snapshot := *tc.ctx
				first := step.Gate(tc.ctx)
				second := step.Gate(tc.ctx)
				if first != second {
					t.Errorf("gate %q non-idempotent: first=%v second=%v", step.Name, first, second)
				}
				if !pipelineContextEqual(&snapshot, tc.ctx) {
					t.Errorf("gate %q mutated context", step.Name)
				}
			}
		})
	}
}

// TestGateValidateNotSkipped pins the per-lane-migration escape hatch:
// when the user passes --skip-validate the final `go build ./...` gate
// flips OFF (returns false), so a tree that's mid-port and unrelated to
// the current lane can still complete codegen without the broken-tree
// build error blocking unrelated work. Default (no --skip-validate)
// leaves the gate ON.
//
// FRICTION 2026-06-02: cp-forge dogfood pass — one broken file in one
// package was failing the whole validate step for every parallel agent.
func TestGateValidateNotSkipped(t *testing.T) {
	on := &pipelineContext{SkipValidate: false}
	if !gateValidateNotSkipped(on) {
		t.Error("gateValidateNotSkipped(SkipValidate=false) = false, want true (default-on)")
	}
	off := &pipelineContext{SkipValidate: true}
	if gateValidateNotSkipped(off) {
		t.Error("gateValidateNotSkipped(SkipValidate=true) = true, want false (--skip-validate honored)")
	}
}

// TestGatePreChecksNotSkipped mirrors TestGateValidateNotSkipped for
// the --skip-pre-checks counterpart. Default is OFF (the gate is ON,
// the check runs) until the user passes --skip-pre-checks.
func TestGatePreChecksNotSkipped(t *testing.T) {
	on := &pipelineContext{SkipPreChecks: false}
	if !gatePreChecksNotSkipped(on) {
		t.Error("gatePreChecksNotSkipped(SkipPreChecks=false) = false, want true (default-on)")
	}
	off := &pipelineContext{SkipPreChecks: true}
	if gatePreChecksNotSkipped(off) {
		t.Error("gatePreChecksNotSkipped(SkipPreChecks=true) = true, want false (--skip-pre-checks honored)")
	}
}

// TestTemplatesOnlyAllowlistMembersExist asserts every step.Name
// listed in templatesOnlyStepAllow corresponds to a real step in
// generateSteps(). A typo or a step rename that drops a member from
// the live plan would otherwise silently turn `--templates-only` into
// a no-op — exactly the surface the rest of this file exists to
// prevent.
func TestTemplatesOnlyAllowlistMembersExist(t *testing.T) {
	live := make(map[string]bool, len(generateSteps()))
	for _, s := range generateSteps() {
		live[s.Name] = true
	}
	for name := range templatesOnlyStepAllow {
		if !live[name] {
			t.Errorf("templatesOnlyStepAllow lists %q, but no step in generateSteps() has that name", name)
		}
	}
}

// TestTemplatesOnlyExcludesCleanupAndValidate pins the contract that
// `--templates-only` skips the cleanup sweep, drift guards, validation
// tail, and external generators. Updating templatesOnlyStepAllow to
// include any of these names should be a deliberate, reviewed change —
// the whole point of the flag is to avoid them.
func TestTemplatesOnlyExcludesCleanupAndValidate(t *testing.T) {
	mustExclude := []string{
		// Cleanup / rehash.
		"cleanup stale codegen",
		"rehash tracked files",
		// Drift / Tier-1 guards.
		"check Tier-1 file-stomp guard",
		"Tier-1 file-stomp guard: compare drift with fresh renders",
		"snapshot Tier-1 exports",
		"detect renamed Tier-1 exports",
		"check disowned-sibling dangling refs",
		// Validation.
		"pre-codegen contract check",
		"post-gen validation",
		"go build (validate generated code)",
		// External generators / subprocess tooling.
		"buf generate (Go stubs)",
		"descriptor extraction",
		"OpenAPI specs (protoc-gen-connect-openapi)",
		"TypeScript stubs (frontends)",
		"generate hooks (forge.yaml)",
		"go mod tidy (gen/)",
		"go mod tidy (root)",
		"goimports on generated Go",
		"ingress k3d ports fragment",
		// Migration scaffolding (mutates db/migrations — unsafe mid-WIP).
		"entity-aware seed data",
	}
	for _, name := range mustExclude {
		if templatesOnlyStepAllow[name] {
			t.Errorf("templatesOnlyStepAllow must NOT include %q — it's a cleanup/drift/validate/external-generator step", name)
		}
	}
}

// TestTemplatesOnlyIncludesTemplateRenderSteps pins the positive side:
// the flag must keep the template-driven Tier-1 / Tier-2 / frontend
// emit steps that the documented use case ("propagate a template
// change to a WIP project") relies on. If one of these gets dropped
// the flag silently fails to do its job — the downstream project would
// run `forge generate --templates-only` and the changed bootstrap.go
// template would not re-render.
func TestTemplatesOnlyIncludesTemplateRenderSteps(t *testing.T) {
	mustInclude := []string{
		"service stubs",
		"per-service test helpers",
		"db/embed.go (embedded migrations)",
		"CI workflows",
		"regenerate infra files",
		"frontend nav + dashboard",
		"frontend mocks + transport",
		"service mocks",
		"internal package contracts",
		"CRUD handlers",
	}
	for _, name := range mustInclude {
		if !templatesOnlyStepAllow[name] {
			t.Errorf("templatesOnlyStepAllow MUST include %q — it's a template-driven render step the flag's use case depends on", name)
		}
	}
}

// TestTemplatesOnlyFilterShape exercises the same filter logic
// runGeneratePipelineFlags applies (allowlist intersection). When the
// flag is off, every step survives. When the flag is on,
// stepCleanupStale and stepGoBuildValidate are dropped while
// stepServiceStubs and stepBootstrap are kept. Mirrors the cobra
// runtime path without spinning up a real project.
func TestTemplatesOnlyFilterShape(t *testing.T) {
	all := generateSteps()

	// Flag off: filter is a no-op — every step survives.
	t.Run("off", func(t *testing.T) {
		got := filterByTemplatesOnly(all, false)
		if len(got) != len(all) {
			t.Errorf("with TemplatesOnly=false, filter dropped %d steps; want 0", len(all)-len(got))
		}
	})

	// Flag on: cleanup + validate drop, template-emit steps survive.
	t.Run("on", func(t *testing.T) {
		got := filterByTemplatesOnly(all, true)
		names := make(map[string]bool, len(got))
		for _, s := range got {
			names[s.Name] = true
		}
		if names["cleanup stale codegen"] {
			t.Error("--templates-only should drop \"cleanup stale codegen\" but it survived the filter")
		}
		if names["go build (validate generated code)"] {
			t.Error("--templates-only should drop \"go build (validate generated code)\" but it survived the filter")
		}
		if names["check Tier-1 file-stomp guard"] {
			t.Error("--templates-only should drop \"check Tier-1 file-stomp guard\" but it survived the filter")
		}
		if !names["service stubs"] {
			t.Error("--templates-only must keep \"service stubs\" — it's a template-driven render step")
		}
		if !names["per-service test helpers"] {
			t.Error("--templates-only must keep \"per-service test helpers\" — the per-component test harness is template-driven")
		}
		if !names["regenerate infra files"] {
			t.Error("--templates-only must keep \"regenerate infra files\" — Tier-1 infra is template-driven")
		}
	})
}

// filterByTemplatesOnly mirrors the allowlist-intersection filter
// runGeneratePipelineFlags applies when flags.TemplatesOnly is set.
// Extracted as a test-only helper so we can exercise the exact filter
// shape without standing up the full cobra entrypoint.
func filterByTemplatesOnly(steps []GenStep, on bool) []GenStep {
	if !on {
		return steps
	}
	out := steps[:0:0]
	for _, s := range steps {
		if templatesOnlyStepAllow[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// stepNames extracts the ordered names from a step slice. Helper for
// the determinism test.
func stepNames(steps []GenStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name
	}
	return out
}

// TestDeriveOrmEnabledMatrix is the golden test guarding step 6's
// ORM-enable probe. Entities are projections of the applied schema:
// stepInternalDBORM (upstream) writes internal/db/*_orm.go from
// db/migrations, so the ORM wire-up belongs in pkg/app/bootstrap.go
// exactly when internal/db contains generated ORM output — any
// *_orm.go file, or the legacy internal/db/types.go from older
// projects. proto/db is no longer consulted (the proto-entity path is
// retired).
func TestDeriveOrmEnabledMatrix(t *testing.T) {
	cases := []struct {
		name          string
		makeDBDir     bool // create internal/db/ (possibly empty)?
		makeOrmFile   bool // create internal/db/users_orm.go?
		makeTypesFile bool // create internal/db/types.go?
		makeProtoFile bool // create proto/db/v1/*.proto (must be ignored)?
		want          bool
	}{
		{name: "no internal/db: ORM off", want: false},
		{name: "empty internal/db: ORM off", makeDBDir: true, want: false},
		{name: "internal/db/users_orm.go: ORM on", makeOrmFile: true, want: true},
		{name: "legacy internal/db/types.go: ORM on", makeTypesFile: true, want: true},
		{name: "both _orm.go and types.go: ORM on", makeOrmFile: true, makeTypesFile: true, want: true},
		{name: "proto/db .proto alone no longer enables ORM", makeProtoFile: true, want: false},
		{name: "proto/db .proto plus _orm.go: ORM on (from internal/db, not proto)", makeProtoFile: true, makeOrmFile: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.makeDBDir || tc.makeOrmFile || tc.makeTypesFile {
				if err := os.MkdirAll(filepath.Join(dir, "internal", "db"), 0o755); err != nil {
					t.Fatalf("mkdir internal/db: %v", err)
				}
			}
			if tc.makeOrmFile {
				if err := os.WriteFile(filepath.Join(dir, "internal", "db", "users_orm_gen.go"), []byte("package db\n"), 0o644); err != nil {
					t.Fatalf("write users_orm.go: %v", err)
				}
			}
			if tc.makeTypesFile {
				if err := os.WriteFile(filepath.Join(dir, "internal", "db", "types.go"), []byte("package db\n"), 0o644); err != nil {
					t.Fatalf("write types.go: %v", err)
				}
			}
			if tc.makeProtoFile {
				p := filepath.Join(dir, "proto", "db", "v1")
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatalf("mkdir proto/db/v1: %v", err)
				}
				if err := os.WriteFile(filepath.Join(p, "thing.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
					t.Fatalf("write proto: %v", err)
				}
			}
			got, err := deriveOrmEnabled(dir)
			if err != nil {
				t.Fatalf("deriveOrmEnabled: %v", err)
			}
			if got != tc.want {
				t.Errorf("deriveOrmEnabled(orm=%v, types=%v, proto=%v) = %v, want %v",
					tc.makeOrmFile, tc.makeTypesFile, tc.makeProtoFile, got, tc.want)
			}
		})
	}
}

// pipelineContextEqual compares the fields gates legitimately read from
// the context. We compare scalars directly; for slices/maps we compare
// length to keep the test focused on "did the gate write anything new"
// rather than deep-diffing config payloads.
func pipelineContextEqual(a, b *pipelineContext) bool {
	if a.ProjectDir != b.ProjectDir || a.AbsPath != b.AbsPath || a.Force != b.Force {
		return false
	}
	if a.HasServices != b.HasServices || a.HasConfig != b.HasConfig {
		return false
	}
	if a.HasWorkers != b.HasWorkers || a.HasOperators != b.HasOperators {
		return false
	}
	if (a.Cfg == nil) != (b.Cfg == nil) {
		return false
	}
	if (a.Checksums == nil) != (b.Checksums == nil) {
		return false
	}
	if len(a.Services) != len(b.Services) {
		return false
	}
	if a.ModulePath != b.ModulePath {
		return false
	}
	if len(a.EntityDefs) != len(b.EntityDefs) {
		return false
	}
	if len(a.ConfigFields) != len(b.ConfigFields) {
		return false
	}
	return true
}

// TestGenerateConfigLoaderPrecedesEveryGoTypeCheck pins that pkg/config's
// generated loader exists before any step compiles or type-checks the
// project's Go. Hand-written code routinely calls a Load<Msg> /
// Register<Msg>Flags that the same run generates (a new command config and
// its first caller land in one change); a step that type-checks the tree
// before the config loader step would fail on `undefined: config.Load<Msg>`
// and force a temporary shim (reliant P4 reported needing one).
func TestGenerateConfigLoaderPrecedesEveryGoTypeCheck(t *testing.T) {
	names := stepNames(generateSteps())
	at := map[string]int{}
	for i, n := range names {
		at[n] = i
	}
	loader, ok := at["config loader (proto/<project>/config/v1)"]
	if !ok {
		t.Fatal("no config loader step")
	}
	// Each of these loads the project's Go with go/packages or the compiler.
	for _, n := range []string{
		"go mod tidy (pre-composition)",
		"internal/app composition (hybrid DI)",
		"go mod tidy (pre-wiring)",
		"go mod tidy (root)",
		"go build (validate generated code)",
	} {
		i, ok := at[n]
		if !ok {
			t.Errorf("step %q is gone; re-check which steps type-check Go", n)
			continue
		}
		if i < loader {
			t.Errorf("step %q (#%d) runs before the config loader (#%d): hand-written code calling a Load<Msg> this run generates would not compile there", n, i, loader)
		}
	}
}

// The composition graph types every node with one go/packages load, which
// fails outright on a go.mod that is behind the tree — and the graph then
// degrades to names: no listener, a server root holding no nodes, a
// config_gen.k refusing the scaffold's own DATABASE_URL (TestE2ESelfCert*
// on a fresh clone). So a tidy must run BEFORE it, not only after.
func TestGenerateTidiesBeforeTheCompositionTypeLoad(t *testing.T) {
	names := stepNames(generateSteps())
	at := map[string]int{}
	for i, n := range names {
		at[n] = i
	}
	tidy, ok := at["go mod tidy (pre-composition)"]
	if !ok {
		t.Fatal("no pre-composition tidy step")
	}
	if compose := at["internal/app composition (hybrid DI)"]; tidy > compose {
		t.Errorf("the pre-composition tidy (#%d) runs after the composition step (#%d)", tidy, compose)
	}
}
