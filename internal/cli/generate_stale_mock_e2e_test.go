//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EGenerateRegeneratesStaleMockBeforeValidation: a contract.go edit
// that leaves the scaffolded mock_gen.go uncompilable must not block the
// regeneration of that very mock — including under `generate --force`, which
// installs an empty force scope and used to make the heal guard leave the
// pristine-but-older mock in place, so the validation build then failed on
// the generated file.
func TestE2EGenerateRegeneratesStaleMockBeforeValidation(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "stalemock", "--mod", "example.com/stalemock", "--service", "api")
	projectDir := filepath.Join(dir, "stalemock")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "releasehealth")
	runCmd(t, projectDir, forgeBin, "generate")

	pkgDir := filepath.Join(projectDir, "internal", "releasehealth")
	contractPath := filepath.Join(pkgDir, "contract.go")
	mockPath := filepath.Join(pkgDir, "mock_gen.go")
	t.Logf("contract.go:\n%s", readFileE2E(t, contractPath))

	// Round 1: add a method returning a new type and implement it.
	contractSrc := readFileE2E(t, contractPath)
	contractSrc = strings.Replace(contractSrc, "type Service interface {",
		"// Report is a release health report.\ntype Report struct{ Healthy bool }\n\ntype Service interface {\n\tReport() (Report, error)\n", 1)
	if err := os.WriteFile(contractPath, []byte(contractSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	implPath := filepath.Join(pkgDir, "service.go")
	impl := readFileE2E(t, implPath) +
		"\nfunc (s *service) Report() (Report, error) { return Report{}, nil }\n"
	if err := os.WriteFile(implPath, []byte(impl), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	if m := readFileE2E(t, mockPath); !strings.Contains(m, "Report") {
		t.Fatalf("mock lacks Report after generate:\n%s", m)
	}

	// Round 2: the type the mock names is renamed away. The stale mock no
	// longer compiles; generate must still regenerate it.
	contractSrc = readFileE2E(t, contractPath)
	contractSrc = strings.ReplaceAll(contractSrc, "Report struct", "Summary struct")
	contractSrc = strings.ReplaceAll(contractSrc, "(Report, error)", "(Summary, error)")
	if err := os.WriteFile(contractPath, []byte(contractSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	impl = readFileE2E(t, implPath)
	impl = strings.ReplaceAll(impl, "(Report, error) { return Report{}", "(Summary, error) { return Summary{}")
	if err := os.WriteFile(implPath, []byte(impl), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runCmdKeep(t, projectDir, forgeBin, "generate", "--force")
	m := readFileE2E(t, mockPath)
	if !strings.Contains(m, "Summary") || strings.Contains(m, "(Report,") {
		t.Fatalf("mock was not regenerated from the edited contract.go:\n%s\n--- generate output ---\n%s", m, out)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}
