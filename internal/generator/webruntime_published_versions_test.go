// File: internal/generator/webruntime_published_versions_test.go
//
// webRuntimePublishedRange must name a version npm SERVES.
//
// THE DEFECT. #2654 raised the floor to ^0.14.0 before npm served 0.14.0.
// `forge generate` raises every consumer to the floor, so every scaffold's
// npm/pnpm install failed, but only full-mode tests install anything, so the
// merge tier passed it and main went red. The runtime is published only at a
// release, and a red main blocks the release: a deadlock.
//
// The registry cannot be asked from a unit test, so the release records what
// it published in webruntime_published_versions.txt (scripts/release-forge.sh's
// bookkeeping, in the same commit that raises the floor), and this test checks
// the floor against that record. It reads the file in-process, so Go's test
// cache sees an edit to it, and it runs under -short, in the merge tier.
package generator

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

const webRuntimePublishedVersionsFile = "webruntime_published_versions.txt"

var exactVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// readPublishedWebRuntimeVersions parses the recorded list: '#' comments and
// blank lines are skipped, every other line is one X.Y.Z.
func readPublishedWebRuntimeVersions(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(webRuntimePublishedVersionsFile)
	if err != nil {
		t.Fatalf("read %s: %v", webRuntimePublishedVersionsFile, err)
	}
	var versions []string
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !exactVersionRe.MatchString(line) {
			t.Fatalf("%s:%d: %q is not an X.Y.Z version", webRuntimePublishedVersionsFile, i+1, line)
		}
		versions = append(versions, line)
	}
	return versions
}

func TestWebRuntimeFloorIsAPublishedVersion(t *testing.T) {
	floor, ok := strings.CutPrefix(webRuntimePublishedRange, "^")
	if !ok || !exactVersionRe.MatchString(floor) {
		t.Fatalf("webRuntimePublishedRange = %q, want a caret range ^X.Y.Z", webRuntimePublishedRange)
	}
	for _, v := range readPublishedWebRuntimeVersions(t) {
		if v == floor {
			return
		}
	}
	t.Fatalf("webRuntimePublishedRange %q: %s is not in %s, the versions of %s the release recorded as published. "+
		"The floor names a version npm does not serve, so every scaffold's install fails; "+
		"raise it only after the version is published, in the release bookkeeping (scripts/release-forge.sh), never in a pull request",
		webRuntimePublishedRange, floor, webRuntimePublishedVersionsFile, WebRuntimePackage)
}

// TestWebRuntimePublishedVersionsAreSorted keeps the record in the shape the
// release writes (`sort -V -u`), so a release's diff of it is one added line.
func TestWebRuntimePublishedVersionsAreSorted(t *testing.T) {
	versions := readPublishedWebRuntimeVersions(t)
	if len(versions) == 0 {
		t.Fatalf("%s lists no versions", webRuntimePublishedVersionsFile)
	}
	for i := 1; i < len(versions); i++ {
		if semver.Compare("v"+versions[i-1], "v"+versions[i]) >= 0 {
			t.Errorf("%s: %s follows %s; the list is in strictly ascending semver order, one version per line",
				webRuntimePublishedVersionsFile, versions[i], versions[i-1])
		}
	}
}
