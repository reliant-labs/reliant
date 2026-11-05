package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestCheckCompatScript runs scripts/test-check-compat.sh, the hermetic self-test of
// the merge gate's compatibility checks (the same file in forge and control-plane):
// buf breaking against the base, and a forge:breaks-rollback migration, each passing
// only on a label the owner's account applied.
func TestCheckCompatScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-check-compat.sh (git, and buf when on PATH, over a throwaway repository)",
		testkit.Inputs("scripts/check-compat.sh", "scripts/test-check-compat.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-check-compat.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-check-compat.sh: %v\n%s", err, out)
	}
}
