package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestTestCaptureScript runs scripts/test-test-capture.sh: a Vitest lane that
// scripts/test-capture.sh recorded in one checkout reads correctly in another
// checkout of the same commit (the release gate's snapshot → ci.yml's
// test-upload job). The record names its files repo-relative, and `forge test
// coverage` there places the TypeScript file with its git blob id.
func TestTestCaptureScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-test-capture.sh (git, jq and the pinned forge over a fixture repository)",
		testkit.Inputs("scripts/test-capture.sh", "scripts/test-test-capture.sh", "scripts/forge.sh", "forge.yaml"))
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(repoRoot, "scripts", "test-test-capture.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-test-capture.sh: %v\n%s", err, out)
	}
}
