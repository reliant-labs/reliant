//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldKCLModuleFromTheBinary exercises how a project resolves
// `import forge` end to end, with the real binary and the real embedded
// kpm/kcl-go runtime (no external `kcl` needed). See
// docs/adr/0003-kcl-module-from-the-binary.md.
//
//  1. Born correct: `forge project new` emits deploy/kcl/kcl.mod with NO
//     forge dependency and materializes nothing into the project.
//  2. Renders from a bare tree: `forge ci validate-kcl` (the same
//     internal/kclrender seam `forge env deploy` uses) succeeds, and
//     rendering writes no `.forge-kcl/` — the state of every CI checkout
//     and container.
//  3. Migrates a legacy project: a kcl.mod pointing at `.forge-kcl/`, the
//     copy itself and a lock recording forge (what every project before
//     this change carried) are rejected at render with `forge generate`
//     named as the fix, and one `forge generate` migrates all three.
//  4. Idempotent: a second `forge generate` leaves kcl.mod byte-identical.
func TestE2EScaffoldKCLModuleFromTheBinary(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin,
		"project", "new", "kclmoduleapp",
		"--mod", "example.com/kclmoduleapp",
		"--service", "item",
	)
	projectDir := filepath.Join(dir, "kclmoduleapp")
	addCorpusForgePkgReplace(t, projectDir)
	deployMod := filepath.Join(projectDir, "deploy", "kcl", "kcl.mod")
	vendorDir := filepath.Join(projectDir, ".forge-kcl")

	// 1. Born correct.
	born := readFileE2EString(t, deployMod)
	if strings.Contains(born, "forge =") {
		t.Fatalf("scaffold declares the forge KCL module — the binary supplies it:\n%s", born)
	}
	if _, err := os.Stat(vendorDir); !os.IsNotExist(err) {
		t.Fatalf("scaffold materialized a project-local .forge-kcl/ (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "kcl.mod")); err == nil {
		t.Fatalf("scaffold emitted a legacy project-root kcl.mod; the KCL package root is deploy/kcl/")
	}

	// 2. Renders from a bare tree, writing nothing into the project.
	runCmd(t, projectDir, forgeBin, "ci", "validate-kcl")
	if _, err := os.Stat(vendorDir); !os.IsNotExist(err) {
		t.Fatalf("rendering materialized a project-local .forge-kcl/ (stat err %v)", err)
	}

	// 3. A legacy project: vendored path dep, the copy, and a lock that
	// records forge.
	legacyMod := born + "forge = { path = \"../../.forge-kcl\" }\n"
	if err := os.WriteFile(deployMod, []byte(legacyMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "schema.k"), []byte("# an older forge's schema\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := "[dependencies]\n  [dependencies.forge]\n    name = \"forge\"\n    full_name = \"forge_0.1.0\"\n    version = \"0.1.0\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, "deploy", "kcl", "kcl.mod.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(forgeBin, "ci", "validate-kcl")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "forge generate") {
		t.Fatalf("render of an unmigrated project must refuse and name `forge generate`; err=%v\n%s", err, out)
	}

	runCmd(t, projectDir, forgeBin, "generate")
	migrated := readFileE2EString(t, deployMod)
	if strings.Contains(migrated, "forge =") {
		t.Fatalf("generate did not remove the legacy dependency:\n%s", migrated)
	}
	if _, err := os.Stat(vendorDir); !os.IsNotExist(err) {
		t.Fatalf("generate did not remove .forge-kcl/ (stat err %v)", err)
	}
	if got := readFileE2EString(t, filepath.Join(projectDir, "deploy", "kcl", "kcl.mod.lock")); strings.Contains(got, "forge") {
		t.Fatalf("generate left forge in kcl.mod.lock:\n%s", got)
	}
	runCmd(t, projectDir, forgeBin, "ci", "validate-kcl")

	// 4. Byte-idempotent across a second generate.
	runCmd(t, projectDir, forgeBin, "generate")
	if again := readFileE2EString(t, deployMod); again != migrated {
		t.Fatalf("second generate churned kcl.mod:\n--- first ---\n%s\n--- second ---\n%s", migrated, again)
	}
}

// readFileE2EString reads a file or fails the test.
func readFileE2EString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
