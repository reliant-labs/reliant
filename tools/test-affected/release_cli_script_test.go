package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestReleaseCLIScript runs scripts/test-release-cli.sh, the hermetic self-test of
// the standalone CLI assets release.yml publishes: the six archives and
// checksums.txt, the daemon-channels.json manifest (one channel per release, never
// backwards), publishing the manifest last, and verify-published.
func TestReleaseCLIScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-release-cli.sh (bash, tar and jq subprocesses)",
		testkit.Inputs("scripts/release-cli.sh", "scripts/test-release-cli.sh", "LICENSE", "LICENSES.txt"))
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq is not on PATH: scripts/release-cli.sh needs it, as release.yml's runners have it")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-release-cli.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release-cli.sh: %v\n%s", err, out)
	}
}
