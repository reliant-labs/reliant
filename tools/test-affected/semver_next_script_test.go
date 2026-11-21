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

// TestReleaseScript runs scripts/test-release.sh, the hermetic test of
// scripts/release.sh over a throwaway checkout with a local bare origin and gh,
// task and yq stubbed: an auto patch and an auto minor are proposed, an explicit
// bump the PR titles do not compute and a breaking change (prerelease included)
// are refused before anything reaches origin, --owner-override proposes what the
// owner chose, and a release candidate takes the computed core version.
func TestReleaseScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-release.sh (git, node and npm subprocesses over throwaway repositories)",
		testkit.Inputs("scripts/release.sh", "scripts/semver-next.sh", "scripts/test-release.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-release.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release.sh: %v\n%s", err, out)
	}
}

// TestReleaseHotfixScript runs scripts/test-release-hotfix.sh, the hermetic test of the
// hotfix release path control-plane's rc-train hotfix lane drives: a hotfix gate record
// passes release-gate.sh check only on a green base that is its ancestor, release-gate.sh
// hotfix refuses any other base, release-tag.sh --hotfix tags the RC head's own version
// (above every release it contains) on a release branch, and hotfix-bump.sh writes only
// the version, the notes and the changelog.
func TestReleaseHotfixScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-release-hotfix.sh (git, node and jq subprocesses over a throwaway repository)",
		testkit.Inputs("scripts/release-gate.sh", "scripts/release-tag.sh", "scripts/release-lock.sh", "scripts/rc-shape.sh",
			"scripts/hotfix-bump.sh", "scripts/test-release-hotfix.sh"))
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-release-hotfix.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release-hotfix.sh: %v\n%s", err, out)
	}
}
