package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestTestFlakeScript runs scripts/test-test-flake.sh, the hermetic self-test
// of how scripts/test-flake.sh reads `forge test flakes gate`: "no new or
// edited tests" passes whether it arrives as forge's exit 2 or as the exit 1
// `go run` turned it into, "could not run" and a FAILED gate fail, and "could
// not identify" exits 2.
func TestTestFlakeScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-test-flake.sh (bash and git subprocesses, a fake forge)",
		testkit.Inputs("scripts/test-flake.sh", "scripts/test-test-flake.sh", "scripts/forge.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-test-flake.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-test-flake.sh: %v\n%s", err, out)
	}
}
