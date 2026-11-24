//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EGenerateRegeneratesStaleMockBeforeValidation: a contract edit
// that leaves the derived contract_gen.go and the scaffolded mock_gen.go
// uncompilable must not block the regeneration of those very files —
// including under `generate --force`, which installs an empty force scope
// and used to make the heal guard leave the pristine-but-older mock in
// place, so the validation build then failed on the generated file.
//
// The default package scaffold declares its contract with `//forge:contract`
// on the service type, so the edit is to service.go: contract_gen.go is
// derived from it, and mock_gen.go from that.
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
	contractPath := filepath.Join(pkgDir, "contract_gen.go")
	mockPath := filepath.Join(pkgDir, "mock_gen.go")
	implPath := filepath.Join(pkgDir, "service.go")
	t.Logf("contract_gen.go:\n%s", readFileE2E(t, contractPath))

	// Round 1: add a method returning a new type to the //forge:contract type.
	impl := readFileE2E(t, implPath) +
		"\n// Report is a release health report.\ntype Report struct{ Healthy bool }\n" +
		"\n// Report returns the current release health.\nfunc (s *service) Report() (Report, error) { return Report{}, nil }\n"
	if err := os.WriteFile(implPath, []byte(impl), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	if c := readFileE2E(t, contractPath); !strings.Contains(c, "Report() (Report, error)") {
		t.Fatalf("contract_gen.go lacks Report after generate:\n%s", c)
	}
	if m := readFileE2E(t, mockPath); !strings.Contains(m, "Report") {
		t.Fatalf("mock lacks Report after generate:\n%s", m)
	}

	// Round 2: the type the contract and mock name is renamed away. The
	// stale contract_gen.go and mock no longer compile; generate must still
	// regenerate both.
	impl = readFileE2E(t, implPath)
	impl = strings.ReplaceAll(impl, "type Report struct", "type Summary struct")
	impl = strings.ReplaceAll(impl, "(Report, error) { return Report{}", "(Summary, error) { return Summary{}")
	if err := os.WriteFile(implPath, []byte(impl), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runCmdKeep(t, projectDir, forgeBin, "generate", "--force")
	if c := readFileE2E(t, contractPath); !strings.Contains(c, "Report() (Summary, error)") {
		t.Fatalf("contract_gen.go was not re-derived from the edited service.go:\n%s\n--- generate output ---\n%s", c, out)
	}
	m := readFileE2E(t, mockPath)
	if !strings.Contains(m, "Summary") || strings.Contains(m, "(Report,") {
		t.Fatalf("mock was not regenerated from the re-derived contract:\n%s\n--- generate output ---\n%s", m, out)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}
