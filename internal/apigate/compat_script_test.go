package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestCheckCompatScript runs scripts/test-check-compat.sh, the hermetic self-test of
// the pull-request gate's other compatibility checks (the same file in reliant and
// control-plane): buf breaking against the base, and a forge:breaks-rollback
// migration, each passing only on a label the owner's account applied.
func TestCheckCompatScript(t *testing.T) {
	testkit.Slow(t, "runs git (and buf, when on PATH) against a throwaway repository",
		testkit.Inputs("scripts/check-compat.sh", "scripts/test-check-compat.sh"))
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test-check-compat.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-check-compat.sh: %v\n%s", err, out)
	}
}
