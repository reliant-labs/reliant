package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot is the repository root, relative to this package's directory.
const repoRoot = "../../.."

// TestReleaseMetadataMatchesElectronVersion pins the release bookkeeping a
// `chore: release vX.Y.Z` commit writes: the Electron version names a release
// record, that record declares the same version, and the generated changelog
// page is current and carries the release. (electron/pnpm-lock.yaml records no
// version of the app, so there is no lockfile copy to agree.) The files are read by this test process, so Go's test cache sees
// every input.
func TestReleaseMetadataMatchesElectronVersion(t *testing.T) {
	var pkg struct {
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join(repoRoot, "electron", "package.json"), &pkg)
	if pkg.Version == "" {
		t.Fatal("electron/package.json has no version")
	}
	version := "v" + pkg.Version

	releasesDir := filepath.Join(repoRoot, "docs", "data", "releases")
	recordPath := filepath.Join(releasesDir, version+".yaml")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("electron/package.json is %s but its release record is unreadable: %v", pkg.Version, err)
	}
	var record Release
	if err := yaml.Unmarshal(data, &record); err != nil {
		t.Fatalf("parsing %s: %v", recordPath, err)
	}
	if record.Version != version {
		t.Errorf("%s declares version %q, want %q (its filename)", recordPath, record.Version, version)
	}

	releases, err := loadReleases(releasesDir)
	if err != nil {
		t.Fatalf("loading releases: %v", err)
	}
	changelogPath := filepath.Join(repoRoot, "docs", "changelog.mdx")
	committed, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("reading %s: %v", changelogPath, err)
	}
	if entry := "<Update label=" + jsxString(version); !strings.Contains(string(committed), entry) {
		t.Errorf("docs/changelog.mdx has no %s entry", entry)
	}
	if string(committed) != generateMDX(releases) {
		t.Errorf("docs/changelog.mdx is stale against docs/data/releases: run task generate:docs:changelog")
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}
