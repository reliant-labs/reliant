package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestReleaseSourcemapsScript runs scripts/test-release-sourcemaps.sh, the
// hermetic self-test of how release.yml files the desktop renderer's source maps
// (reliant#2535): frontend, build id, env and project of the push, the pinned
// forge, and which failures fail.
func TestReleaseSourcemapsScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-release-sourcemaps.sh (bash and jq subprocesses)",
		testkit.Inputs("scripts/release-sourcemaps.sh", "scripts/test-release-sourcemaps.sh"))
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq is not on PATH: scripts/release-sourcemaps.sh needs it, as release.yml's runners have it")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-release-sourcemaps.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-release-sourcemaps.sh: %v\n%s", err, out)
	}
}

// TestReleaseFilesRendererSourcemaps holds release.yml to the job the script's
// self-test proves: it files the web-dist prepare built, with the credential
// deploy/kcl/desktop names, never on a tag that reuses a prebuild (whose renderer
// is what ships, and was filed by the prebuild run), never on the path of a
// platform build, and never failing a release that shipped.
func TestReleaseFilesRendererSourcemaps(t *testing.T) {
	wf := loadReleaseWorkflow(t)
	j := wf.job(t, "renderer-sourcemaps")
	if got := j.needs(); len(got) != 1 || got[0] != "prepare" {
		t.Errorf("renderer-sourcemaps needs %v, want [prepare]", got)
	}
	for _, c := range []struct {
		prepare, prebuilt string
		want              bool
	}{
		{"success", "", true},
		{"success", "4242", false},
		{"failure", "", false},
	} {
		ctx := map[string]any{"needs.prepare.outputs.prebuilt_run": c.prebuilt}
		if got := evalJobIf(t, j, map[string]string{"prepare": c.prepare}, ctx); got != c.want {
			t.Errorf("prepare=%s prebuilt_run=%q: renderer-sourcemaps runs=%v, want %v", c.prepare, c.prebuilt, got, c.want)
		}
	}
	for id, other := range wf.Jobs {
		for _, n := range other.needs() {
			if n == "renderer-sourcemaps" {
				t.Errorf("job %s waits on renderer-sourcemaps: filing maps must not hold up or ground a release", id)
			}
		}
	}

	var downloaded, filed bool
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["name"] == "web-dist" && s.With["path"] == "frontends/web/dist" {
			downloaded = true
		}
		if strings.Contains(s.Run, "scripts/release-sourcemaps.sh") {
			filed = true
			if want := `bash scripts/release-sourcemaps.sh "$VERSION" frontends/web/dist`; strings.TrimSpace(s.Run) != want {
				t.Errorf("filing step runs %q, want %q", s.Run, want)
			}
			if s.Env["VERSION"] != "${{ needs.prepare.outputs.version }}" {
				t.Errorf("filing step VERSION = %q, want prepare's version", s.Env["VERSION"])
			}
			if s.Env["RELIANT_DESKTOP_SOURCEMAPS_TOKEN"] != "${{ secrets.RELIANT_DESKTOP_SOURCEMAPS_TOKEN }}" {
				t.Errorf("filing step token = %q, want the RELIANT_DESKTOP_SOURCEMAPS_TOKEN secret", s.Env["RELIANT_DESKTOP_SOURCEMAPS_TOKEN"])
			}
		}
	}
	if !downloaded || !filed {
		t.Errorf("renderer-sourcemaps: downloads web-dist=%v, runs the script=%v; want both", downloaded, filed)
	}

	// The step continues on error: a shipped release is never marked failed for its maps.
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wfText := string(b)
	i := strings.Index(wfText, "- name: File the renderer's source maps")
	if i < 0 || !strings.Contains(wfText[i:min(i+200, len(wfText))], "continue-on-error: true") {
		t.Error("the filing step must continue on error")
	}

	// deploy/kcl/desktop names the same credential the step passes.
	k, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "kcl", "desktop", "main.k"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(k), `token_env = "RELIANT_DESKTOP_SOURCEMAPS_TOKEN"`) {
		t.Error(`deploy/kcl/desktop/main.k must read the credential from RELIANT_DESKTOP_SOURCEMAPS_TOKEN`)
	}
}
