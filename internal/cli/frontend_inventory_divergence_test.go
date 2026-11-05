package cli

// frontend_inventory_divergence_test.go — the regression that motivated
// moving inventory resolution to the config LOAD seam.
//
// The inventory question ("which frontends does this project have?") used
// to be answered in a step registered ONLY on the generate pipeline. Every
// other command loaded the same forge.yaml and got a different answer, so
// a project that declares its frontends in KCL instead of forge.yaml was
// generated for and then neither linted, typechecked, nor built.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// writeDerivedInventoryProject lays down a project shaped like
// control-plane: a forge.yaml with NO `frontends:` block, and a real
// frontend on disk carrying its framework marker.
func writeDerivedInventoryProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	forgeYAML := "name: divergence\n" +
		"module_path: github.com/example/divergence\n"
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(forgeYAML), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	feDir := filepath.Join(root, "frontends", "console")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	// next.config.ts is the marker DiscoverInRepoFrontends admits a
	// directory on; without it the directory is (correctly) not a frontend.
	if err := os.WriteFile(filepath.Join(feDir, "next.config.ts"), []byte("export default {}\n"), 0o600); err != nil {
		t.Fatalf("write next.config.ts: %v", err)
	}
	return root
}

// TestInventoryAgreesAcrossCommands is the reproduction. It asks the same
// project the same question through the two seams that disagreed:
//
//   - the generate pipeline's derivation step, and
//   - a plain config load, which is all `forge lint`, `forge doctor` and
//     `forge build` ever do.
//
// Before the fix the first saw 1 frontend and the second saw 0.
func TestInventoryAgreesAcrossCommands(t *testing.T) {
	root := writeDerivedInventoryProject(t)

	// What `forge generate` sees: the pipeline's derivation, run against a
	// project with no deploy/kcl at all, so this exercises the render-free
	// half and needs no kcl binary.
	genCtx, err := newPipelineContextWithFlags(root, pipelineFlags{})
	if err != nil {
		t.Fatalf("pipeline context: %v", err)
	}
	genCfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("load config for generate: %v", err)
	}
	genCtx.Cfg = genCfg
	if err := stepDeriveFrontendInventory(genCtx); err != nil {
		t.Fatalf("derive step: %v", err)
	}
	generateSees := len(genCtx.Cfg.Frontends)

	// What every OTHER command sees: a plain load, no pipeline.
	lintCfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("load config for lint: %v", err)
	}
	lintSees := len(lintCfg.Frontends)

	if generateSees != lintSees {
		t.Errorf("inventory diverges by command: forge generate sees %d frontend(s), "+
			"a plain load (lint/doctor/build) sees %d — same project, same forge.yaml",
			generateSees, lintSees)
	}
	if lintSees != 1 {
		t.Errorf("a plain load resolved %d frontend(s), want 1 (frontends/console is on disk with a next.config.ts marker)", lintSees)
	}
}

// TestToolchainFrontendsVisibleWithoutDeclaredBlock pins the consequence
// that reached users: ToolchainFrontends is the set `forge build` builds
// and `forge lint`'s frontend lane typechecks. An undeclared-but-present
// frontend must be in it.
func TestToolchainFrontendsVisibleWithoutDeclaredBlock(t *testing.T) {
	root := writeDerivedInventoryProject(t)

	cfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	toolchain := cfg.ToolchainFrontends()
	if len(toolchain) != 1 {
		t.Fatalf("ToolchainFrontends() = %d entries, want 1 — `forge build` and `forge lint`'s "+
			"frontend lane read this set, so an empty one silently builds and checks nothing", len(toolchain))
	}
	if got, want := toolchain[0].DeclaredDir(), filepath.ToSlash(filepath.Join("frontends", "console")); got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
	if toolchain[0].Type != "nextjs" {
		t.Errorf("resolved type = %q, want nextjs (from the next.config.ts marker)", toolchain[0].Type)
	}
}

// TestToolchainFrontendsDoesNotInventPathForUnresolvedSource covers the
// second mutation. ToolchainFrontends used to assign
// `fe.Path = fe.DeclaredDir()` onto its copies, which fabricates
// `frontends/<name>` for a cross-repo frontend whose source has NOT been
// materialized — a directory that does not exist and never will.
//
// ToolchainFrontends is the set forge SHELLS INTO (`npm run build`,
// `tsc`), so handing it an invented directory is the failure mode, not a
// convenience. Such a frontend must be excluded until its source resolves.
func TestToolchainFrontendsDoesNotInventPathForUnresolvedSource(t *testing.T) {
	cfg := &config.ProjectConfig{
		Frontends: []config.FrontendConfig{
			{
				Name: "sibling-web",
				Type: "vite-spa",
				// No Path: the code is in another repository.
				Source: &config.GitSource{Repo: "github.com/example/sibling", Ref: "v1.0.0"},
			},
		},
	}

	for _, fe := range cfg.ToolchainFrontends() {
		if fe.DeclaredDir() == "" {
			continue
		}
		if fe.DeclaredDir() == filepath.Join("frontends", "sibling-web") {
			t.Errorf("ToolchainFrontends invented path %q for a frontend whose source is "+
				"unresolved; that directory does not exist and forge would shell into it", fe.DeclaredDir())
		}
	}
}
