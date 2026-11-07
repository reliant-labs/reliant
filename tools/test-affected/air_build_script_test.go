package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestAirBuildScript runs scripts/test-air-build.sh, the hermetic test of
// scripts/air-build.sh (the one build per save the dev stack's Air watchers
// share; the same file in control-plane) with a fake go on PATH: later callers
// reuse a build, any input change rebuilds, concurrent callers make one build,
// and a failed build fails every caller for the same source without replacing
// a delivered binary.
func TestAirBuildScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-air-build.sh (sh subprocesses and builds that sleep to hold the lock)",
		testkit.Inputs("scripts/air-build.sh", "scripts/test-air-build.sh"))
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(repoRoot, "scripts", "test-air-build.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-air-build.sh: %v\n%s", err, out)
	}
}
