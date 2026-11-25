package config

// inventory_load_seam_test.go — the load seam owns the frontend
// inventory, so every command that loads a forge.yaml gets one answer.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSeamProject(t *testing.T, forgeYAML string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(forgeYAML), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	return root
}

func mkSeamFrontendDir(t *testing.T, root, name, marker string) {
	t.Helper()
	dir := filepath.Join(root, "frontends", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if marker == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("export default {}\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

// TestLoadResolvesInventoryWithMarkerGate is §3 items 1 and 2: lint's
// typecheck lane and its dotenv rule used to reach the right frontend by
// ACCIDENT, via a bare directory scan with no marker gate. A scratch
// directory was therefore typechecked by lint and ignored by generate.
//
// Resolving at the load seam means both commands read the same
// marker-gated inventory, so the stray directory is excluded everywhere.
func TestLoadResolvesInventoryWithMarkerGate(t *testing.T) {
	root := writeSeamProject(t, "name: demo\nmodule_path: github.com/example/demo\n")
	mkSeamFrontendDir(t, root, "console", "next.config.ts")
	mkSeamFrontendDir(t, root, "scratch", "") // a half-deleted tree: no marker

	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(cfg.Frontends) != 1 {
		t.Fatalf("resolved %d frontend(s), want 1: %+v", len(cfg.Frontends), cfg.Frontends)
	}
	if cfg.Frontends[0].Name != "console" {
		t.Errorf("resolved %q, want console", cfg.Frontends[0].Name)
	}
	for _, fe := range cfg.Frontends {
		if fe.Name == "scratch" {
			t.Error("a directory with no framework marker must not enter the inventory — " +
				"generate has always excluded it, and lint must agree")
		}
	}
}

// TestLoadDerivedInventoryEnablesFrontendFeature is §3 item 4 and the
// gate question: features.frontend is derived from the inventory being
// non-empty. Resolving the inventory BEFORE feature derivation is what
// keeps the frontend pipeline steps (and gateFrontendHasFrontends) ungated.
func TestLoadDerivedInventoryEnablesFrontendFeature(t *testing.T) {
	root := writeSeamProject(t, "name: demo\nmodule_path: github.com/example/demo\n")
	mkSeamFrontendDir(t, root, "console", "next.config.ts")
	// features.frontend also requires codegen, which derives from .proto
	// files existing. control-plane has them; give the fixture one so this
	// test measures the inventory, not the codegen evidence.
	protoDir := filepath.Join(root, "proto", "services", "demo", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatalf("mkdir proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "demo.proto"), []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Features.FrontendEnabled() {
		t.Error("features.frontend must be on for a project whose inventory resolved to a real " +
			"frontend; otherwise every frontend generate step is gated off")
	}
}

// TestLoadRejectsFrontendsBlock: `frontends:` is gone, and the loader says so
// with the migration hint rather than honoring a key nothing documents.
func TestLoadRejectsFrontendsBlock(t *testing.T) {
	root := writeSeamProject(t, "name: demo\nmodule_path: github.com/example/demo\n"+
		"frontends:\n  - name: admin\n    type: nextjs\n    path: apps/admin\n")
	mkSeamFrontendDir(t, root, "console", "next.config.ts")

	_, err := LoadProjectDir(root)
	if err == nil {
		t.Fatal("a forge.yaml carrying `frontends:` must fail to load")
	}
	for _, want := range []string{`"frontends" is no longer a forge.yaml key`, "forge.Frontend", "frontends/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err.Error())
		}
	}
}
