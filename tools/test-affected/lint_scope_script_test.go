package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestLintScopeScript runs scripts/test-lint-scope.sh, the hermetic self-test
// of the merge gate's forge lint scope: a nested Go module (sdk/go) and its
// descendants leave it, ordinary root-module directories and a sibling sharing
// only the module's prefix stay, and a checkout with no nested module keeps
// every changed directory.
func TestLintScopeScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-lint-scope.sh (bash, git and awk subprocesses)",
		testkit.Inputs("scripts/lint-scope.sh", "scripts/test-lint-scope.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-lint-scope.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-lint-scope.sh: %v\n%s", err, out)
	}
}
