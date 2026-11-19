package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestForgeScript runs scripts/test-forge.sh, the hermetic self-test of
// scripts/forge.sh, the forge every task and script runs: FORGE_BIN, then a
// forge on PATH or in .tools/bin only at the pin, then `reliant forge`, then
// `go run …@<pin>`, else exit 127; and --machine (the heavy slot) takes any
// forge and never `go run`.
func TestForgeScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-forge.sh (bash subprocesses and fake forge, reliant and go)",
		testkit.Inputs("scripts/forge.sh", "scripts/test-forge.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-forge.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-forge.sh: %v\n%s", err, out)
	}
}
