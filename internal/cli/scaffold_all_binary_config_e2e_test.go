//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EAllBinaryConfigScaffoldCompiles pins the fr-config-all-binary
// template-layer fix: a project whose config messages are ALL
// binary-annotated (no unannotated root AppConfig left) must produce a
// tree that COMPILES after `forge generate`.
//
// Before this fix, GenerateConfigLoader emitted the per-binary Go surface
// correctly (config_all_binary_test.go pins that), but the ~20 templates
// outside config.go.tmpl — providers.go, compose.go,
// mounts_services_gen.go, every service/worker/internal-package Deps, the
// scaffold-once cmd-tree-*.go.tmpl files — all reference `config.Config` /
// `config.RegisterFlags(` / `config.Load(` / `config.Validate(`
// unconditionally, with no `Config` type emitted for them to resolve
// against. A test that only string-matches config_gen.go's own content (as
// the existing all-binary tests do) cannot catch that: the defect is a
// project that does NOT COMPILE, so the assertion has to be `go build`.
//
// The fix aliases Config/RegisterFlags/Load/Validate/ModeOf in
// config.go.tmpl onto the PRIMARY binary's own per-binary config message
// (see PrimaryConfigMessage in internal/codegen/config_gen.go), so those
// ~20 templates keep compiling against the same symbol names without
// needing to know per-binary config exists at all.
func TestE2EAllBinaryConfigScaffoldCompiles(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "allbinapp", "--mod", "example.com/allbinapp", "--service", "item")
	projectDir := filepath.Join(dir, "allbinapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Confirm the scaffold shape this test depends on: a plain, unannotated
	// `message AppConfig {` declaration (the scaffold's doc comment above it
	// mentions the binary_config syntax as an EXAMPLE, which is fine — only
	// the message declaration itself must be unannotated). If the scaffold
	// ever changes shape, this assertion should be the one that breaks, not
	// a downstream compile failure with no explanation.
	configProtoPath := filepath.Join(projectDir, "proto", filepath.Base(projectDir), "config", "v1", "config.proto")
	configProto := readFileE2E(t, configProtoPath)
	const unannotatedDecl = "message AppConfig {\n  int32 port = 1"
	if !strings.Contains(configProto, unannotatedDecl) {
		t.Fatalf("scaffold no longer emits an unannotated `message AppConfig { int32 port = 1 ...` — update this test's transform:\n%s", configProto)
	}

	// The migration this test simulates: annotate the ONLY root config
	// message with a binary_config binding to the project's own (primary)
	// binary — "allbinapp", matching --mod's project name — producing the
	// all-annotated shape with NO unannotated root message left.
	rewritten := strings.Replace(configProto, "message AppConfig {",
		"message AppConfig {\n  option (forge.v1.binary_config) = {binary: \"allbinapp\"};\n", 1)
	if rewritten == configProto {
		t.Fatal("rewrite of config.proto did not change anything")
	}
	writeFileE2E(t, configProtoPath, rewritten)

	runCmd(t, projectDir, forgeBin, "generate")

	// The regression this test exists to catch: config_gen.go must alias
	// Config onto the primary binary's own message so every other
	// template compiles against it.
	genPath := filepath.Join(projectDir, "pkg", "config", "config_gen.go")
	genContent := readFileE2E(t, genPath)
	if !strings.Contains(genContent, "type Config = configv1.AppConfig") {
		t.Errorf("pkg/config/config_gen.go does not alias Config onto the primary binary's config message:\n%s", genContent)
	}

	// The real assertion: the whole all-binary-annotated tree compiles.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}

// TestE2EAllBinaryConfigNoPrimaryBinding_RejectsAtGenerate pins the
// deliberate generate-time rejection: when NO config message is bound to
// the primary binary (every message is bound to some OTHER binary, or all
// binary_config annotations point elsewhere), there is nothing for the
// primary binary's own component graph to construct its Config from. Forge
// must refuse loudly at `forge generate`, naming the primary binary and the
// fix — not silently emit a tree with an undefined `config.Config` that
// fails 20 files deep into `go build` with no explanation.
func TestE2EAllBinaryConfigNoPrimaryBinding_RejectsAtGenerate(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "orphanapp", "--mod", "example.com/orphanapp", "--service", "item")
	projectDir := filepath.Join(dir, "orphanapp")
	addCorpusForgePkgReplace(t, projectDir)

	configProtoPath := filepath.Join(projectDir, "proto", filepath.Base(projectDir), "config", "v1", "config.proto")
	configProto := readFileE2E(t, configProtoPath)

	// Bind the only config message to a DIFFERENT binary than the primary
	// one ("orphanapp", from --mod's project name) — the primary binary
	// then has no config message bound to it at all.
	rewritten := strings.Replace(configProto, "message AppConfig {",
		"message AppConfig {\n  option (forge.v1.binary_config) = {binary: \"someotherbin\"};\n", 1)
	if rewritten == configProto {
		t.Fatal("rewrite of config.proto did not change anything")
	}
	writeFileE2E(t, configProtoPath, rewritten)

	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("forge generate: want error when the primary binary has no bound config message, got success:\n%s", out)
	}
	if !strings.Contains(out, "orphanapp") {
		t.Errorf("error output should name the primary binary %q:\n%s", "orphanapp", out)
	}
	if !strings.Contains(out, "binary_config") {
		t.Errorf("error output should point at the binary_config annotation fix:\n%s", out)
	}
}
