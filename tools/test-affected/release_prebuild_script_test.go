package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleasePrebuildScript runs scripts/test-release-prebuild.sh, the hermetic
// self-test of release.yml's Electron prebuild reuse: exact version and commit
// selection, the renderer's release identity, every platform's update feeds,
// feeds published last, and a dry run that uploads nothing.
func TestReleasePrebuildScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs bash and jq subprocesses; runs in full scoped tests")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-release-prebuild.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release-prebuild.sh: %v\n%s", err, out)
	}
}

// TestReleaseWorkflowUsesPrebuildScript holds release.yml to the script the
// self-test above proves: each platform publishes a prebuild through it, the
// prebuild's platform artifacts carry every update feed it requires, and the
// renderer is built with the release's version.
func TestReleaseWorkflowUsesPrebuildScript(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	for _, want := range []string{
		"scripts/release-prebuild.sh find ",
		"scripts/release-prebuild.sh marker ",
		"scripts/release-prebuild.sh publish mac ",
		"scripts/release-prebuild.sh publish linux ",
		"scripts/release-prebuild.sh publish win ",
		// The feeds publish requires must be in the artifacts it publishes.
		"electron/dist/*-mac.yml",
		"electron/dist/*-linux.yml",
		"electron/dist/*-linux-arm64.yml",
		"electron/dist/latest.yml",
		"electron/dist/alpha.yml",
		"RELEASE_VERSION: ${{ startsWith(steps.version.outputs.version, 'v') && steps.version.outputs.version || '' }}",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml no longer contains %q", want)
		}
	}
}
