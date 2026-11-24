package cli

import (
	"regexp"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/templates"
)

// alpineReleases is when each Alpine branch was released. Alpine supports a
// stable branch's main repository for two years from its release
// (https://alpinelinux.org/releases/), after which it gets no security
// updates and scanners report the image as end-of-life.
var alpineReleases = map[string]time.Time{
	"3.21": time.Date(2024, time.December, 5, 0, 0, 0, 0, time.UTC),
	"3.22": time.Date(2025, time.May, 30, 0, 0, 0, 0, time.UTC),
	"3.23": time.Date(2025, time.December, 3, 0, 0, 0, 0, time.UTC),
	"3.24": time.Date(2026, time.May, 20, 0, 0, 0, 0, time.UTC),
}

// scaffoldedAlpineBases returns the Alpine branch of every runtime stage in
// the Dockerfile forge scaffolds (read from the embedded template).
func scaffoldedAlpineBases(t *testing.T) []string {
	t.Helper()
	src, err := templates.ProjectTemplates().Get("Dockerfile.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, m := range regexp.MustCompile(`(?m)^FROM alpine:(\d+\.\d+)\b`).FindAllStringSubmatch(string(src), -1) {
		bases = append(bases, m[1])
	}
	if len(bases) == 0 {
		t.Fatal("Dockerfile.tmpl names no alpine runtime base; update this test with the base it uses now")
	}
	return bases
}

// TestScaffoldedRuntimeBaseHasAKnownRelease is the clock-free half of NS
// FG-28: every Alpine branch the scaffolded Dockerfile names has a release
// date in alpineReleases, so the nightly end-of-life check
// (TestE2ENightlyScaffoldedRuntimeBaseIsSupported) can judge it.
func TestScaffoldedRuntimeBaseHasAKnownRelease(t *testing.T) {
	for _, base := range scaffoldedAlpineBases(t) {
		if _, ok := alpineReleases[base]; !ok {
			t.Errorf("alpine:%s has no release date in alpineReleases: add it", base)
		}
	}
}
