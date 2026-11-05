package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestSemverNextScript runs scripts/test-semver-next.sh, the hermetic self-test of
// the version scripts/release.sh proposes (the same file in forge and control-plane,
// whose release train cuts reliant with it): feat is a minor, anything else a patch,
// a breaking change is refused naming its PR, and rc/ tags are never the base.
func TestSemverNextScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-semver-next.sh (git subprocesses over a throwaway repository)",
		testkit.Inputs("scripts/semver-next.sh", "scripts/test-semver-next.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-semver-next.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-semver-next.sh: %v\n%s", err, out)
	}
}
