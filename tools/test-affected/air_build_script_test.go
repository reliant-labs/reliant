package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestAirBuildScript runs scripts/test-air-build.sh, the hermetic test of
// scripts/air-build.sh (the one build per save the dev stack's Air watchers
// share; the same file in control-plane) with a fake go on PATH: every caller
// runs go build under one lock and gets the shared binary, concurrent callers
// are serialized and link once, a failed build leaves the shared binary
// intact, and a lock holder killed mid-build does not wedge the next caller.
func TestAirBuildScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-air-build.sh (sh subprocesses, the lock tools and a fake go)",
		testkit.Inputs("scripts/air-build.sh", "scripts/test-air-build.sh"))
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(repoRoot, "scripts", "test-air-build.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-air-build.sh: %v\n%s", err, out)
	}
}
