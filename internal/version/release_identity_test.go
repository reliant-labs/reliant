// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedBuildsStampTheReleaseIdentity pins WHERE Version comes from on
// every reliant build path that ships a binary: scripts/release-identity.sh.
//
// Prod's About page read "version unknown" because its server binary was built
// with no -ldflags at all, and the paths that did stamp disagreed with each
// other (release.yml: the bare tag; build-images.yml: 0.0.0-dev-<sha7>; the
// Makefile and control-plane: `git describe`, which a shallow, tagless CI clone
// answers with a bare hash). One resolver makes the web bundle's identity and
// the server's comparable on the About page, and the script refuses — exits
// non-zero — rather than print nothing, which each caller's `set -e` turns
// into a failed build. The script's behaviour is tested against real git
// repositories in frontends/web/src/lib/__tests__/versionJson.test.ts.
//
// control-plane's scripts/build-reliant-image.sh (the prod image) calls the
// same script from the pinned tree and is pinned by its own test there.
func TestShippedBuildsStampTheReleaseIdentity(t *testing.T) {
	t.Parallel()

	root := repoRootFromTest(t)
	read := func(rel string) string {
		t.Helper()
		// In-process read: a child process's reads are invisible to Go's test
		// cache, so a cached pass could outlive an edit to these files.
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}

	if _, err := os.Stat(filepath.Join(root, "scripts", "release-identity.sh")); err != nil {
		t.Fatalf("scripts/release-identity.sh is gone (%v); every version stamp below depends on it", err)
	}

	for _, rel := range []string{
		"Taskfile.yml",
		".github/workflows/release.yml",
		".github/workflows/build-images.yml",
	} {
		if !strings.Contains(read(rel), "scripts/release-identity.sh") {
			t.Errorf("%s no longer derives the version stamp from scripts/release-identity.sh.\n"+
				"Every build that ships must stamp internal/version.Version from that one resolver, or the\n"+
				"About page's app and server versions stop being comparable (and an unstamped path ships\n"+
				"\"unknown\" again).", rel)
		}
	}

	// The specific regressions: release.yml stamping only the tag, and
	// build-images.yml inventing its own dev-version shape.
	if strings.Contains(read(".github/workflows/release.yml"), "internal/version.Version=${VERSION#v}") {
		t.Error("release.yml stamps Version from the raw tag again; use scripts/release-identity.sh's IDENTITY")
	}
	if strings.Contains(read(".github/workflows/build-images.yml"), "0.0.0-dev-") {
		t.Error("build-images.yml invents a 0.0.0-dev-<sha> stamp again; use scripts/release-identity.sh")
	}
}
