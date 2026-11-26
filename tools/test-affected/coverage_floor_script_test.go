package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestCoverageFloorScript runs scripts/test-coverage-floor.sh, the hermetic
// self-test of the release gate's coverage floor (the same pair of files in
// control-plane): the profiles' union counts a block covered when any `go test`
// invocation hit it, generated code stays out of the denominator, check fails
// below the floor, and raise moves the floor only up unless --lower has a reason.
func TestCoverageFloorScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-coverage-floor.sh (bash and awk subprocesses over a scratch copy)",
		testkit.Inputs("scripts/coverage-floor.sh", "scripts/test-coverage-floor.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-coverage-floor.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-coverage-floor.sh: %v\n%s", err, out)
	}
}
