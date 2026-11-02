package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestReviewMarkerScript runs scripts/test-ci-review-marker.sh, the hermetic
// self-test of the `Reliant review` required check: an exact-head pass marker
// passes a branch of this repository, and nothing passes a fork's head
// (control-plane finding CPI-1).
func TestReviewMarkerScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in full scoped tests")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-ci-review-marker.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-ci-review-marker.sh: %v\n%s", err, out)
	}
}

// TestReviewRerunScript runs scripts/test-ci-review-rerun.sh, the hermetic
// self-test of the healer behind the `Reliant review` check: a passed head's
// red and cancelled runs are re-run one at a time, a head that has not passed
// and merge-group runs are never touched.
func TestReviewRerunScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in full scoped tests")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-ci-review-rerun.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-ci-review-rerun.sh: %v\n%s", err, out)
	}
}
