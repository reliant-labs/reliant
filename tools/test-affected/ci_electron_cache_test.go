// Copyright (c) 2025 Reliant Labs
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// electronCache is one cached download: the step that reads its key, the
// restore and save steps, and the directory they cache.
type electronCache struct {
	output  string // the version step's output naming the key (zip, builder)
	prefix  string // the key before -<OS>-<arch>-<version>
	pkg     string // the lockfile package whose version decides the contents
	path    string
	restore string
	save    string
}

var (
	electronZipCache = electronCache{
		output: "zip", prefix: "electron-zip", pkg: "electron", path: "~/.cache/electron",
		restore: "Restore the Electron zip", save: "Save the Electron zip (main only)",
	}
	electronBuilderCache = electronCache{
		output: "builder", prefix: "electron-builder", pkg: "electron-builder", path: "~/.cache/electron-builder",
		restore: "Restore electron-builder's tools", save: "Save electron-builder's tools (main only)",
	}
)

// electronLockVersions reads the versions the cache keys must follow from the
// checked-out lockfile. Reading it here also keeps the test cache-sound: the
// version step's script (run by a child bash) reads the same file.
func electronLockVersions(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "electron", "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatalf("parse electron/package-lock.json: %v", err)
	}
	out := map[string]string{}
	for _, pkg := range []string{"electron", "electron-builder"} {
		v := lock.Packages["node_modules/"+pkg].Version
		if v == "" {
			t.Fatalf("electron/package-lock.json pins no version of %s", pkg)
		}
		out[pkg] = v
	}
	return out
}

// runVersionStep runs the version step's script against the real lockfile and
// returns the outputs it wrote to $GITHUB_OUTPUT.
func runVersionStep(t *testing.T, script string) map[string]string {
	t.Helper()
	ghOut := filepath.Join(t.TempDir(), "github_output")
	cmd := exec.Command("bash", "-e", "-c", script)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "RUNNER_OS=Linux", "RUNNER_ARCH=X64", "GITHUB_OUTPUT="+ghOut)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("version step failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(ghOut)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("version step wrote %q to $GITHUB_OUTPUT, want key=value lines", line)
		}
		outputs[k] = v
	}
	return outputs
}

// checkElectronCaches asserts, for one job, that each cache's key follows the
// lockfile version (per OS and arch), its restore runs before the step that
// installs dependencies, and its save runs only after a green install, only
// on a push to main, and only on a cache miss (and, when partIf is set, only
// in that matrix part; the version step and the restores run in every part).
func checkElectronCaches(t *testing.T, jobID, versionStep, installStep, partIf string, caches ...electronCache) {
	t.Helper()
	requireShellTools(t)
	versions := electronLockVersions(t)
	j := loadCIWorkflow(t).job(t, jobID)

	vIdx, v := j.step(t, jobID, versionStep)
	installIdx, _ := j.step(t, jobID, installStep)
	if v.ID == "" {
		t.Fatalf("%s: step %q has no id, so no step can read its outputs", jobID, versionStep)
	}
	// Every part reads and restores: each one's npm phase installs electron.
	if v.If != "" {
		t.Errorf("%s: step %q if=%q, want it in every part", jobID, versionStep, v.If)
	}
	outputs := runVersionStep(t, v.Run)

	for _, c := range caches {
		wantKey := c.prefix + "-Linux-X64-" + versions[c.pkg]
		if got := outputs[c.output]; got != wantKey {
			t.Errorf("%s: %s cache key = %q, want %q (OS, arch and the lockfile's %s version)", jobID, c.output, got, wantKey, c.pkg)
		}
		keyRef := "${{ steps." + v.ID + ".outputs." + c.output + " }}"

		rIdx, r := j.step(t, jobID, c.restore)
		sIdx, s := j.step(t, jobID, c.save)
		if !strings.HasPrefix(r.Uses, "actions/cache/restore@") {
			t.Errorf("%s: %q uses %q, want actions/cache/restore (a combined cache action saves on every event)", jobID, c.restore, r.Uses)
		}
		if !strings.HasPrefix(s.Uses, "actions/cache/save@") {
			t.Errorf("%s: %q uses %q, want actions/cache/save", jobID, c.save, s.Uses)
		}
		for _, st := range []ciStep{r, s} {
			if st.With["path"] != c.path || st.With["key"] != keyRef {
				t.Errorf("%s: %q caches path=%v key=%v, want path=%s key=%s", jobID, st.Name, st.With["path"], st.With["key"], c.path, keyRef)
			}
		}
		if r.ID == "" {
			t.Fatalf("%s: %q has no id, so its save cannot skip a cache hit", jobID, c.restore)
		}
		if vIdx >= rIdx || rIdx >= installIdx {
			t.Errorf("%s: %q (step %d) must run after %q (step %d) and before %q (step %d)", jobID, c.restore, rIdx, versionStep, vIdx, installStep, installIdx)
		}
		if sIdx <= installIdx {
			t.Errorf("%s: %q (step %d) must run after %q (step %d), which fills the cache", jobID, c.save, sIdx, installStep, installIdx)
		}
		for _, want := range []string{
			"github.event_name == 'push'",
			"github.ref == 'refs/heads/main'",
			"steps." + r.ID + ".outputs.cache-hit != 'true'",
			partIf,
		} {
			if !strings.Contains(s.If, want) {
				t.Errorf("%s: %q if=%q lacks %q", jobID, c.save, s.If, want)
			}
		}
		// A status function would save after a failed step: a partial download.
		for _, bad := range []string{"always()", "failure()", "cancelled()"} {
			if strings.Contains(s.If, bad) {
				t.Errorf("%s: %q if=%q uses %s, so it could save after a failed step", jobID, c.save, s.If, bad)
			}
		}
		if r.If != "" {
			t.Errorf("%s: %q if=%q, want it in every part", jobID, c.restore, r.If)
		}
	}
}

// TestElectronPackageCachesElectronDownloads pins the Electron package smoke
// job's caches of the Electron zip and electron-builder's tools, which
// github.com release downloads otherwise fetch on every run.
func TestElectronPackageCachesElectronDownloads(t *testing.T) {
	checkElectronCaches(t, "electron-package", "Read the Electron versions", "task deps:web deps:electron", "",
		electronZipCache, electronBuilderCache)
}

// TestReleaseGateCachesElectronZip pins the release gate to the same Electron
// zip entry: every part's npm phase installs electron, whose postinstall
// downloads that zip (static and go 2/3 failed on it as well as web, at
// 1f81a402e). Every part restores it; only the web part saves it (partIf).
func TestReleaseGateCachesElectronZip(t *testing.T) {
	checkElectronCaches(t, "release-gate", "Read the Electron version", "task release:gate (part ${{ matrix.label }})",
		"matrix.part == 'web'", electronZipCache)
}
