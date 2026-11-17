package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// kclVendoredMod is what forge scaffolded before the module came from the
// binary: a relative path to a project-local .forge-kcl/.
const kclVendoredMod = `[package]
name = "proj-deploy"
edition = "v0.11.0"
version = "0.0.1"

[dependencies]
forge = { path = "../../.forge-kcl" }
`

// kclLegacyGitTagMod is the shape even older scaffolds emitted — a git tag
// that was never published.
const kclLegacyGitTagMod = `[package]
name = "proj-deploy"
edition = "v0.11.0"
version = "0.0.1"

[dependencies]
forge = { git = "https://github.com/reliant-labs/forge.git", tag = "kcl-v0.1.0" }
`

func writeProjectFile(t *testing.T, projectDir, rel, content string) string {
	t.Helper()
	path := filepath.Join(projectDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestSyncForgeKCL_MigratesBothManagedKclModsAndRemovesTheCopy: the generate
// step removes the legacy forge declaration from BOTH managed kcl.mod
// locations and deletes the project-local .forge-kcl/, after which the
// project passes the render-time check.
func TestSyncForgeKCL_MigratesBothManagedKclModsAndRemovesTheCopy(t *testing.T) {
	dir := t.TempDir()
	deployMod := writeProjectFile(t, dir, "deploy/kcl/kcl.mod", kclVendoredMod)
	rootMod := writeProjectFile(t, dir, "kcl.mod", kclLegacyGitTagMod)
	writeProjectFile(t, dir, ".forge-kcl/schema.k", "# an old forge's schema\n")

	if err := syncForgeKCL(dir); err != nil {
		t.Fatalf("syncForgeKCL: %v", err)
	}
	for _, p := range []string{deployMod, rootMod} {
		if has, _ := kclvendor.HasForgeDep(p); has {
			got, _ := os.ReadFile(p)
			t.Errorf("%s still declares forge:\n%s", p, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".forge-kcl")); !os.IsNotExist(err) {
		t.Errorf(".forge-kcl/ was not removed (stat err %v)", err)
	}
	if err := kclvendor.CheckKclMods(dir); err != nil {
		t.Errorf("migrated project still fails the render check: %v", err)
	}

	// Idempotent second run.
	before, _ := os.ReadFile(deployMod)
	if err := syncForgeKCL(dir); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if after, _ := os.ReadFile(deployMod); string(before) != string(after) {
		t.Errorf("second sync changed kcl.mod bytes")
	}
}

// TestSyncForgeKCL_OutputIsIdenticalUnderEveryBuild: a committed kcl.mod
// must not depend on which forge generated it. A release build and a dev
// build migrating the same project produce byte-identical files — the
// dev/release split ADR 0001 deleted must not come back through this step.
//
// NOT parallel: buildinfo is process-global.
func TestSyncForgeKCL_OutputIsIdenticalUnderEveryBuild(t *testing.T) {
	migrate := func(release bool) string {
		if release {
			buildinfo.SetDevBuild(false)
		} else {
			buildinfo.SetDevBuild(true)
		}
		defer buildinfo.ClearDevBuild()
		dir := t.TempDir()
		mod := writeProjectFile(t, dir, "deploy/kcl/kcl.mod", kclVendoredMod)
		if err := syncForgeKCL(dir); err != nil {
			t.Fatalf("syncForgeKCL(release=%v): %v", release, err)
		}
		got, _ := os.ReadFile(mod)
		return string(got)
	}
	release, dev := migrate(true), migrate(false)
	if release != dev {
		t.Errorf("kcl.mod differs by forge build:\n--- release ---\n%s\n--- dev ---\n%s", release, dev)
	}
	if strings.Contains(release, "forge =") {
		t.Errorf("migrated kcl.mod still declares forge:\n%s", release)
	}
}

// TestSyncForgeKCL_UnmanagedShapeIsLeftAlone: a kcl.mod carrying a forge
// dependency in a shape forge never wrote is not edited.
func TestSyncForgeKCL_UnmanagedShapeIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	unmanaged := `[package]
name = "p"

[dependencies.forge]
path = "../../.forge-kcl"
`
	modPath := writeProjectFile(t, dir, "deploy/kcl/kcl.mod", unmanaged)
	if err := syncForgeKCL(dir); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got, _ := os.ReadFile(modPath); string(got) != unmanaged {
		t.Errorf("unmanaged kcl.mod was edited:\n%s", got)
	}
}

// TestSyncForgeKCL_MigratesTheRetiredRegistryHelper: `forge generate` rewrites
// an existing project's forge.registry("X") calls to the literal "X" — the
// value each call already evaluated to — so a project scaffolded against the
// old module renders unchanged instead of failing on a missing attribute.
func TestSyncForgeKCL_MigratesTheRetiredRegistryHelper(t *testing.T) {
	dir := t.TempDir()
	main := writeProjectFile(t, dir, "deploy/kcl/prod/main.k",
		"import forge\n_registry = forge.registry(\"ghcr.io/acme\")\n")
	if err := kclvendor.CheckRegistryHelper(dir); err == nil {
		t.Fatal("fixture precondition: CheckRegistryHelper must refuse the unmigrated project")
	}

	out := captureStdout(t, func() {
		if err := syncForgeKCL(dir); err != nil {
			t.Fatalf("syncForgeKCL: %v", err)
		}
	})
	got, _ := os.ReadFile(main)
	if !strings.Contains(string(got), `_registry = "ghcr.io/acme"`) || strings.Contains(string(got), "forge.registry") {
		t.Errorf("prod/main.k not migrated to the literal:\n%s", got)
	}
	if !strings.Contains(out, "deploy/kcl/prod/main.k") {
		t.Errorf("generate should name the migrated file; output:\n%s", out)
	}
	if err := kclvendor.CheckRegistryHelper(dir); err != nil {
		t.Errorf("after the migration the render check must pass; got %v", err)
	}
}
