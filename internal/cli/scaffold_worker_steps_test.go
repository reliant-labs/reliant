// Regression guard for the kalshi-trader friction report
// "forge scaffold worker runs the full pipeline and scaffolds a Next.js
// dashboard" (forge-add-worker-runs-full-pipeline).
//
// Symptom: `forge scaffold worker bar --kind cron` on a freshly-scaffolded
// `forge project new x --kind service` project (no `--frontend`) was reported
// to (a) scaffold a complete `frontends/dashboard/` Next.js tree with
// node_modules, (b) append a `frontends:` block to forge.yaml, and
// (c) flip `features.frontend: false → true`.
//
// On audit, the current `runWorker` code path does NOT exhibit any
// of those side effects: every frontend-tagged step in the generate
// pipeline gates on either `FrontendEnabled()` (false in the friction
// scenario) or `len(cfg.Frontends) > 0` (zero), and `cfg.Features.Frontend`
// is only written from `runFrontend` — never from `runWorker`.
//
// This file pins those invariants so a future refactor that
// accidentally relaxes a gate (e.g. drops the `len(Frontends) > 0`
// check) or introduces a second write site for `Features.Frontend`
// fails CI before the regression ships.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestAddWorkerPipelineSkipsFrontendSteps reconstructs the
// pipelineContext that `runGeneratePipeline` would see during
// `forge scaffold worker bar --kind cron` on a freshly-scaffolded
// service-only project, and asserts every step tagged "frontend" is
// gated OFF.
//
// Mirrors the friction repro exactly:
//   - cfg.Kind = "service"
//   - the frontend feature is off (no frontend exists in the repo)
//   - cfg.Frontends is empty
//   - proto/services/ is empty (no --service was passed)
//   - workers/ has the just-scaffolded bar/ dir (HasWorkers=true)
//
// If any frontend-tagged gate returns true here, the pipeline would
// proceed to render a Next.js dashboard — the exact failure mode the
// friction report describes.
func TestScaffoldWorkerPipelineSkipsFrontendSteps(t *testing.T) {
	ctx := &pipelineContext{
		ProjectDir: ".",
		AbsPath:    "/abs/.",
		Cfg: &config.ProjectConfig{
			Name:       "x",
			ModulePath: "github.com/x/x",
			Kind:       "service",
			Features:   config.FeaturesConfig{}.With(config.FeatureFrontend, false),
			// No Frontends entries.
		},
		HasServices:  false,
		HasWorkers:   true,
		HasOperators: false,
	}

	var leaked []string
	for _, step := range generateSteps() {
		if step.Tag != "frontend" {
			continue
		}
		if step.Gate(ctx) {
			leaked = append(leaked, step.Name)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("frontend pipeline step(s) gated ON during `forge scaffold worker` on a service-only project:\n  - %s\n\n"+
			"This would scaffold Next.js artifacts the user never asked for. See friction "+
			"forge-add-worker-runs-full-pipeline (kalshi-trader migration round).",
			strings.Join(leaked, "\n  - "))
	}
}

// TestScaffoldWorkerNeverForcesTheFrontendFeature is a structural pairing. The
// friction report claimed `forge scaffold worker` flipped the frontend feature
// on; on inspection no such write existed. Features are derived now, so the
// only way to force one is FeaturesConfig.With, and the only file that may
// force the FRONTEND feature is new.go's `--disable frontend` handler. This
// fails if a refactor adds a second site, which would reintroduce the
// regression.
func TestScaffoldWorkerNeverForcesTheFrontendFeature(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob cli sources: %v", err)
	}
	subFiles, err := filepath.Glob(filepath.Join("*", "*.go"))
	if err != nil {
		t.Fatalf("glob cli subpackages: %v", err)
	}
	files = append(files, subFiles...)

	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if !strings.Contains(line, "config.FeatureFrontend") || !strings.Contains(line, ".With(") {
				continue
			}
			// new.go forces features generically from the --disable list
			// (it names no feature on this line), so it never matches.
			offenders = append(offenders, f+": "+line)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("unexpected site(s) forcing the frontend feature:\n  %s\n\n"+
			"The frontend feature derives from a frontend existing. Forcing it from a "+
			"scaffold command risks reintroducing the kalshi-trader friction "+
			"forge-add-worker-runs-full-pipeline.", strings.Join(offenders, "\n  "))
	}
}

// TestAddWorkerUsesBootstrapOnlyStepPreset is the structural pairing
// for the cp-forge port-workers friction: `forge scaffold worker` × 7
// rewrote 5 UNRELATED Tier-1 files per call because runWorker
// invoked the full generate pipeline. Fix: the worker path now passes
// Steps: "bootstrap-only" so only the bootstrap regen subset runs.
//
// This test pins that contract by scanning scaffold.go for the worker-path
// call site and asserting it passes the bootstrap-only step preset. A
// future refactor that accidentally drops the Steps field (or switches
// it back to the unpreset runGeneratePipeline) trips the test before
// the regression ships.
//
// The worker post-scaffold generate step was extracted into the shared
// helper runPostScaffoldGenerate (the worker is the only component kind
// with a --no-generate flag, so the no-generate / bootstrap-only /
// partial-failure tail lives there); this test now scans that helper, and
// separately asserts runWorker actually routes through it.
func TestScaffoldWorkerUsesBootstrapOnlyStepPreset(t *testing.T) {
	// scaffold.go lives in the internal/cli/scaffold group; the bootstrap-only preset
	// is now applied by the factory.GenAPI closure (groups.go) that
	// runPostScaffoldGenerate calls through f.Gen.RunPipelineBootstrapOnly.
	// This guard pins (a) the worker path delegates to the shared helper,
	// and (b) that helper takes the bootstrap-only pipeline, not the full
	// one — running the full pipeline rewrites unrelated Tier-1 files
	// (.github/workflows/ci.yml, cmd/server.go, frontend mocks,
	// pkg/config/config.go) the worker scaffold has no business touching.
	// See FRICTION cp-forge-2026-06-03 port-workers.
	data, err := os.ReadFile(filepath.Join("scaffold", "scaffold.go"))
	if err != nil {
		t.Fatalf("read scaffold/scaffold.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "runPostScaffoldGenerate(f, p.root, p.name, noGenerate)") {
		t.Errorf("runWorker must delegate its post-scaffold generate to " +
			"runPostScaffoldGenerate(f, p.root, p.name, noGenerate) via spec.postScaffold.")
	}
	// Find the runPostScaffoldGenerate body (unique name); the generate
	// invocation sits between the definition and the next top-level "\nfunc ".
	idx := strings.Index(src, "func runPostScaffoldGenerate(")
	if idx < 0 {
		t.Fatal("runPostScaffoldGenerate not found in scaffold/scaffold.go (the worker post-scaffold generate helper)")
	}
	tail := src[idx:]
	end := strings.Index(tail, "\nfunc ")
	if end < 0 {
		end = len(tail)
	}
	body := tail[:end]
	if !strings.Contains(body, "f.Gen.RunPipelineBootstrapOnly(") {
		t.Errorf("runPostScaffoldGenerate must invoke the bootstrap-only pipeline " +
			"(f.Gen.RunPipelineBootstrapOnly). Running the full pipeline rewrites " +
			"unrelated Tier-1 files (.github/workflows/ci.yml, cmd/server.go, frontend " +
			"mocks, pkg/config/config.go) that the worker scaffold has no business " +
			"touching. See FRICTION cp-forge-2026-06-03 port-workers.")
	}
	if strings.Contains(body, "f.Gen.RunPipeline(") {
		t.Errorf("runPostScaffoldGenerate is calling the FULL pipeline (f.Gen.RunPipeline). " +
			"The worker post-scaffold generate must use f.Gen.RunPipelineBootstrapOnly.")
	}
}

// TestStepPresetAllowlistMembersExist guards against typo / rename
// drift between stepPresetAllowlist (generate_pipeline.go) and
// generateSteps(). Every key in every preset's allowlist MUST match a
// step.Name in the canonical plan — otherwise a renamed step would
// silently fall out of the preset's pipeline and produce a no-op
// generate.
func TestStepPresetAllowlistMembersExist(t *testing.T) {
	stepNames := map[string]bool{}
	for _, step := range generateSteps() {
		stepNames[step.Name] = true
	}
	for preset, allow := range stepPresetAllowlist {
		for name := range allow {
			if !stepNames[name] {
				t.Errorf("step preset %q allowlists step %q, but no GenStep with that name exists in generateSteps(). "+
					"Either the step was renamed (update the allowlist) or the name has a typo.",
					preset, name)
			}
		}
	}
}

// TestBootstrapOnlyStepPresetExcludesStompedSteps pins the
// FRICTION-named step set: the bootstrap-only preset must NOT run any
// of the steps whose outputs were stomped in the cp-forge port-workers
// report. If a future refactor adds one of these step names to the
// allowlist, this test trips before the regression hits a user.
func TestBootstrapOnlyStepPresetExcludesStompedSteps(t *testing.T) {
	allow := stepPresetAllowlist["bootstrap-only"]
	if allow == nil {
		t.Fatal("stepPresetAllowlist is missing the bootstrap-only entry")
	}
	stomped := []string{
		"CI workflows", // .github/workflows/ci.yml
		"config loader (proto/<project>/config/v1)", // pkg/config/config.go (+ cmd/server.go re-render)
		"frontend mocks + transport",                // frontends/<name>/src/lib/mock-transport.ts
		"regenerate infra files",                    // deploy/ / Dockerfile.* / etc.
		"per-env deploy config",                     // deploy/ env-specific KCL
		"service stubs",                             // service.go / handlers.go scaffolds
		"CRUD handlers",                             // handlers/<svc>/handlers_crud_gen.go
		"service mocks",                             // internal/<svc>/mock_gen.go
		"frontend hooks",                            // frontends/<name>/src/hooks/*-hooks.ts
		"frontend CRUD pages",                       // frontends/<name>/src/app/<svc>/page.tsx
		"frontend nav + dashboard",                  // frontends/<name>/src/components/nav.tsx
	}
	for _, name := range stomped {
		if allow[name] {
			t.Errorf("bootstrap-only step preset must NOT include step %q — it was named in the cp-forge port-workers FRICTION report as one of the stomped emitters. "+
				"Adding a worker should not regenerate this output.",
				name)
		}
	}
}

// TestMocksStepPresetSkipsTier1DriftGuard pins the load-bearing
// semantic of the `mocks` step preset: the Tier-1 file-stomp guard
// MUST NOT be in the allowlist. The whole point of the preset is to
// let users regen mock_gen.go after a contract.go change without first
// reconciling unrelated Tier-1 drift (a hand-edited workflow yaml, a
// touched cmd/server.go) — mocks live behind a "DO NOT EDIT" banner
// and are deterministic from contract.go, so they cannot stomp any
// Tier-1 file. FRICTION 2026-06-04: two downstream projects reported
// this exact friction.
func TestMocksStepPresetSkipsTier1DriftGuard(t *testing.T) {
	allow := stepPresetAllowlist["mocks"]
	if allow == nil {
		t.Fatal("stepPresetAllowlist is missing the mocks entry")
	}
	if allow["check Tier-1 file-stomp guard"] {
		t.Errorf("mocks step preset must NOT include \"check Tier-1 file-stomp guard\" — " +
			"the preset's reason to exist is letting users regen mock_gen.go without " +
			"first reconciling unrelated Tier-1 drift. Mocks are deterministic from " +
			"contract.go and cannot stomp any Tier-1 file.")
	}
}

// TestMocksStepPresetRunsMockStep pins the other half of the semantic:
// the mock-regen steps themselves MUST be in the allowlist (otherwise the
// preset is a no-op).
//
// BOTH are required, because forge emits two different mock_gen.go files
// from two different sources: "service mocks" writes
// internal/handlers/mocks from the proto, while a package's own
// mock_gen.go is derived from its contract.go by "internal package
// contracts". The preset is named for the contract.go workflow, so
// omitting the latter breaks exactly the case it advertises.
//
// This failed silently in the field and is the reason the test covers
// both: `generate --steps mocks` after a contract.go edit printed
// "Code generation complete!" and changed nothing, and because a stale
// mock merely implements an EXTRA method, it still compiled — so neither
// the build nor any downstream test caught the drift.
func TestMocksStepPresetRunsMockStep(t *testing.T) {
	allow := stepPresetAllowlist["mocks"]
	if allow == nil {
		t.Fatal("stepPresetAllowlist is missing the mocks entry")
	}
	for _, step := range []string{"service mocks", "internal package contracts"} {
		if !allow[step] {
			t.Errorf("mocks step preset must include %q — without it the preset skips one of "+
				"the two mock emitters and silently produces a partial (or entirely no-op) "+
				"generate. A stale mock still compiles, so nothing downstream catches it.", step)
		}
	}
}

// TestHarnessStepPresetShape pins the `harness` preset: it runs the
// harness emitter and the prereqs it reads (the service list and module
// path), and none of the emitters a Deps edit cannot have moved — nor the
// Tier-1 stomp guard, whose reason to skip is the mocks preset's. That it
// writes the bytes a full run writes is TestE2EGenerateStepsHarness.
func TestHarnessStepPresetShape(t *testing.T) {
	allow := stepPresetAllowlist[harnessStepPreset]
	if allow == nil {
		t.Fatalf("stepPresetAllowlist is missing the %s entry", harnessStepPreset)
	}
	// "internal package contracts" is an input, not a bystander: the
	// harness installs the mocks it writes (a Deps field whose interface
	// forge mocks gets that mock, not a stub), and a Deps edit moves them —
	// a new foreign field of a deps.go-marked handler is a new
	// Deps-discovered mock. Without it the preset would stub what the full
	// run mocks.
	for _, step := range []string{"per-service test helpers", "parse services + module path", "load checksums", "internal package contracts"} {
		if !allow[step] {
			t.Errorf("harness preset must include %q", step)
		}
	}
	for _, step := range []string{
		"check Tier-1 file-stomp guard",
		"buf generate (Go stubs)",
		"descriptor extraction",
		"internal/app composition (hybrid DI)",
		"service mocks",
		"CRUD handlers",
		"frontend hooks",
		"regenerate infra files",
		"per-env deploy config",
		"cleanup stale codegen",
		"go build (validate generated code)",
	} {
		if allow[step] {
			t.Errorf("harness preset must NOT include %q — it regenerates the test harness and nothing a Deps edit cannot move", step)
		}
	}
}

// TestMocksStepPresetExcludesUnrelatedHeavyEmitters keeps the preset's
// surface area tight. The point is a fast preset-scoped regen — if a
// future refactor drags in the full bootstrap/CI/frontend emitter set,
// the preset loses its reason to exist (it becomes just "the full
// pipeline minus the Tier-1 guard", which is what `--force` is for).
// This test trips when any of the unrelated-to-mocks heavyweight
// emitters slip into the allowlist.
func TestMocksStepPresetExcludesUnrelatedHeavyEmitters(t *testing.T) {
	allow := stepPresetAllowlist["mocks"]
	if allow == nil {
		t.Fatal("stepPresetAllowlist is missing the mocks entry")
	}
	unrelated := []string{
		"CI workflows",                       // .github/workflows/ci.yml
		"regenerate infra files",             // deploy/ / Dockerfile.* / etc.
		"per-env deploy config",              // deploy/ env-specific KCL
		"frontend hooks",                     // frontends/<name>/src/hooks/*-hooks.ts
		"frontend CRUD pages",                // frontends/<name>/src/app/<svc>/page.tsx
		"frontend nav + dashboard",           // frontends/<name>/src/components/nav.tsx
		"frontend mocks + transport",         // frontends/<name>/src/lib/mock-transport.ts
		"pkg/app substrate (app_gen/setup)",  // bootstrap-only preset's territory
		"per-service test helpers",           // bootstrap-only preset's territory
		"pkg/app/migrate.go",                 // bootstrap-only preset's territory
		"service stubs",                      // hand-editable service.go scaffolds
		"go build (validate generated code)", // user runs go test in their loop
	}
	for _, name := range unrelated {
		if allow[name] {
			t.Errorf("mocks step preset must NOT include step %q — the preset is a "+
				"fast-path for mock-only regen. Including heavyweight or unrelated "+
				"emitters defeats the reason the preset exists.",
				name)
		}
	}
}
