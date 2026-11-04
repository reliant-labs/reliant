package cli

// generate_frontend_inventory.go — recovering the codegen inventory for a
// project that declares its frontends in KCL instead of forge.yaml.
//
// forge.yaml's `frontends:` key is what every frontend emitter iterates:
// the Connect hooks, config_gen.ts, the mock transport, the CRUD pages,
// nav. It is a CODEGEN inventory, and when it is absent those emitters
// have nothing to walk.
//
// That used to be a safe assumption, because a frontend had nowhere else
// to be declared. It no longer is: a project can declare its frontend
// topology in deploy/kcl/<env>/main.k, and control-plane did exactly
// that — deleting the block on the (correct) grounds that the topology
// had moved. What went with it was not topology but the codegen
// inventory, and the symptoms were entirely silent:
//
//   - `forge generate` emitted nothing for frontends/internal-console and
//     reported its ~30 existing generated files as STALE, offering to
//     delete them;
//   - config_gen.ts was never refreshed, so the app kept declaring an
//     env var whose proto field had been removed — a compile-clean
//     frontend reading a value that no longer exists.
//
// The recovery has to be narrower than "use the KCL frontends", because
// the deploy graph legitimately contains frontends this project must NOT
// generate into. See config.KCLFrontend.OwnsFrontendCode: the admission
// test is that the code lives inside THIS repository.
//
// Deriving happens ONLY when forge.yaml declares no frontends at all. A
// project with a block keeps exactly the inventory it wrote, and its KCL
// is never rendered for this — so this cannot add a frontend an author
// deliberately left out, which is the other half of how the sibling-repo
// case has always been expressed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// stepDeriveFrontendInventory overlays the DEPLOY GRAPH onto the inventory
// the loader found on disk.
//
// There is no `frontends:` key in forge.yaml: the inventory is derived. The
// cheap half already ran — config.ResolveInventoryAtLoad scans frontends/<name>
// inside LoadProject, so every command (lint, doctor, build, this pipeline)
// agrees on it. What is left here is the half that costs a KCL render per
// environment (1.6-2.2s each, four environments on control-plane), which is
// why it stays on the commands that already render rather than moving to the
// load seam and taxing shell completion.
//
// The overlay is what supplies every field a directory cannot carry: the KCL
// forge.Frontend's port, dev_runner, base_path and `routes` allowlist, and a
// frontend at a path the frontends/<name> convention cannot express.
//
// Best-effort. A project whose KCL does not render (no dev env yet, a
// half-written main.k, kcl not installed) keeps whatever the cheap half
// resolved rather than failing generate: failing here would turn a degraded
// generate into no generate at all.
func stepDeriveFrontendInventory(ctx *pipelineContext) (err error) {
	if ctx.Cfg == nil {
		return nil
	}

	fromKCL := config.DeriveFrontendsFromKCL(ctx.ProjectDir, kclDeclaredFrontends(ctx.renderedFrontends()))
	if len(fromKCL) == 0 {
		return frontendCodegenProblems(ctx.Cfg.Frontends)
	}
	defer func() {
		if err == nil {
			err = frontendCodegenProblems(ctx.Cfg.Frontends)
		}
	}()

	before := len(ctx.Cfg.Frontends)
	ctx.Cfg.Frontends = config.MergeFrontendInventory(ctx.Cfg.Frontends, fromKCL)
	// The frontend feature derives from the inventory being non-empty, and it
	// was resolved at load time against the disk-only one. Re-resolve so the
	// frontend steps are not gated off by a flag computed before the KCL
	// declarations were folded in.
	config.ApplyDerivedDefaults(ctx.Cfg)

	if len(ctx.Cfg.Frontends) > before {
		names := make([]string, 0, len(ctx.Cfg.Frontends))
		for _, fe := range ctx.Cfg.Frontends {
			names = append(names, fmt.Sprintf("%s (%s)", fe.Name, fe.DeclaredDir()))
		}
		fmt.Printf("  ℹ️  generating for %d frontend(s) found in frontends/ or declared in deploy/kcl: %v\n",
			len(names), names)
	}
	return nil
}

// renderedFrontends reads the frontends every renderable environment
// declares, in environment order. It is the one KCL read both the inventory
// and the observability dashboards draw on, so a generate run renders each
// environment once for them.
//
// Every environment is read, not just dev: a frontend may be declared in
// prod alone (control-plane's internal-console is), and generating for it
// only when a dev env happens to mention it would make the codegen
// inventory depend on which environments exist — the same class of
// silent, order-dependent emptiness this file exists to remove.
//
// Renders go through renderKCLPure for the reason its own header gives:
// evaluating a module fires any file.write in it, and a write that lands
// on a tracked file makes `forge generate` non-reproducible. An
// environment that fails to render contributes nothing and is skipped
// silently — the deploy checks in `forge doctor` are what report an
// unrenderable environment, and duplicating that here would put a KCL
// stack trace in front of every generate on a project mid-edit.
func renderedFrontends(projectDir string) []renderedEnvFrontend {
	return renderEnvironments(projectDir).frontends
}

// renderedEnvironments is what one pass over every renderable environment's
// KCL yields for generate: the frontends each declares (above), and every
// workload any of them declares, which the observability dashboards derive
// their deployed processes from (the same source a release bundle uses for
// its one environment; see renderedDashboardProcesses).
type renderedEnvironments struct {
	frontends []renderedEnvFrontend
	workloads []WorkloadEntity
}

func renderEnvironments(projectDir string) renderedEnvironments {
	var out renderedEnvironments
	for _, env := range deployEnvNames(projectDir) {
		entities, restored, err := renderKCLPure(context.Background(), projectDir, env)
		if err != nil || entities == nil {
			continue
		}
		reportImpureRender(env, restored)
		for _, fe := range entities.Frontends {
			out.frontends = append(out.frontends, renderedEnvFrontend{Env: env, Frontend: fe})
		}
		out.workloads = append(out.workloads, entities.Workloads...)
	}
	return out
}

// renderedEnvs is renderEnvironments(ctx.ProjectDir), read once per run.
func (ctx *pipelineContext) renderedEnvs() renderedEnvironments {
	if !ctx.renderedRead {
		ctx.rendered = renderEnvironments(ctx.ProjectDir)
		ctx.renderedRead = true
	}
	return ctx.rendered
}

// renderedFrontends is the frontends of renderedEnvs.
func (ctx *pipelineContext) renderedFrontends() []renderedEnvFrontend {
	return ctx.renderedEnvs().frontends
}

// renderedEnvFrontend is a frontend as one environment declares it.
type renderedEnvFrontend struct {
	Env      string
	Frontend FrontendEntity
}

// kclDeclaredFrontends is the narrow view of renderedFrontends the inventory
// rules consume.
func kclDeclaredFrontends(rendered []renderedEnvFrontend) []config.KCLFrontend {
	var out []config.KCLFrontend
	for _, r := range rendered {
		fe := r.Frontend
		out = append(out, config.KCLFrontend{
			Name:      fe.Name,
			Type:      fe.Type,
			Path:      fe.Path,
			HasSource: fe.Source != nil && fe.Source.Repo != "",
			Port:      fe.Port,
			DevRunner: fe.DevRunner,
			BasePath:  fe.BasePath,
			Routes:    fe.Routes,
		})
	}
	return out
}

// deployEnvNames lists the environments with a deploy/kcl/<env>/main.k,
// sorted so the derived inventory does not depend on directory order.
func deployEnvNames(projectDir string) []string {
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(kclDir, e.Name(), "main.k")); statErr != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// frontendCodegenProblems refuses a run while any frontend's package.json
// forge block cannot be honoured. A knob forge silently ignored — a
// misspelt key, a components_dir outside the frontend — would produce the
// very output its author wrote the block to prevent.
func frontendCodegenProblems(fes []config.FrontendConfig) error {
	var lines []string
	for _, fe := range fes {
		for _, p := range fe.CodegenProblems {
			lines = append(lines, fmt.Sprintf("  frontend %s: %s", fe.Name, p))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("frontend codegen settings could not be honoured (fix the \"forge\" block in the frontend's package.json):\n%s",
		strings.Join(lines, "\n"))
}
