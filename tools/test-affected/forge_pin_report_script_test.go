package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestForgePinReportScript runs scripts/test-forge-pin-report.sh, the hermetic
// self-test of forge-pin-check.yml's reporting: a pass or an already-pinned or
// cached pair closes the forge-pin:red issue, a pin failure opens or updates
// it, and a check that cannot run reports to forge-pin:broken instead.
func TestForgePinReportScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in full scoped tests")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-forge-pin-report.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-forge-pin-report.sh: %v\n%s", err, out)
	}
}
