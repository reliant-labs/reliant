// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file guards the property that makes THIS package's version number mean
// anything: that a release tag is reachable from main.
//
// Every tag from v1.7.8 through v1.7.12 was not. scripts/release.sh cut a
// `release-*` branch, bumped the version there, tagged that commit and opened
// a PR; main squash-merges, so the merged commit was a different object and
// the tag stayed behind on the branch. `git describe --tags --abbrev=0
// origin/main` answered v1.7.7 for five consecutive releases and nothing
// noticed, because from the release's point of view everything succeeded.
//
// The consequence that forced the fix: `go get` derives a pseudo-version from
// the highest tag REACHABLE from the pinned commit, so pinning reliant's main
// produced v1.7.8-0.<date>-<sha> — sorting BELOW the released v1.7.12. Pinning
// main was a content upgrade that was a version-number downgrade, which Go's
// minimum-version selection can silently undo.
//
// The tests below are cheap and hermetic: they read the release scripts as
// text and assert on their structure. They deliberately do NOT shell out to
// git for ancestry — that lives in scripts/check-release-tags.sh, which CI
// runs on the tag push where the tag actually exists. A unit test cannot see a
// tag that has not been cut yet, and a test that depends on remote refs is a
// test that fails on an airplane.

// repoRootFromTest locates the repository root relative to this file, which
// works regardless of the working directory the test binary is run from.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the repo root")
	}
	// internal/version/release_tags_test.go -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func readScript(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), "scripts", name)
	out, err := exec.Command("cat", path).Output()
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(out)
}

// TestReleaseScriptDoesNotTag is the direct regression test for the defect.
//
// scripts/release.sh runs BEFORE the release PR merges, so any commit it can
// reach is a commit that main will squash into something else. It must
// therefore never create a tag — that is the whole bug, in one line of shell.
//
// The assertion is on the absence of `git tag`, not on the presence of some
// comment, because the comment is not what breaks the release.
func TestReleaseScriptDoesNotTag(t *testing.T) {
	t.Parallel()

	script := readScript(t, "release.sh")

	for i, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		// `git tag` with any argument creates or moves a tag. A bare
		// `git tag` (listing) and `git tag --list` are reads, but neither
		// belongs in this script, so the simplest correct rule is: no
		// `git tag` at all.
		if strings.Contains(trimmed, "git tag") {
			t.Errorf("scripts/release.sh:%d creates a tag: %q\n\n"+
				"release.sh runs before the release PR merges. main squash-merges, so the\n"+
				"commit this script can reach is NOT the commit that lands — tagging here is\n"+
				"exactly what stranded v1.7.8..v1.7.12 off the trunk.\n"+
				"Tagging belongs in scripts/release-tag.sh, which runs after the merge.",
				i+1, trimmed)
		}
	}

	// And it must push no tags either — `git push origin vX.Y.Z` would
	// publish a tag created some other way.
	if strings.Contains(script, `git push origin "v$NEW_VERSION"`) {
		t.Error("scripts/release.sh pushes a version tag; tag publishing belongs in release-tag.sh")
	}
}

// TestReleaseTagScriptChecksAncestry pins the guard inside phase 2.
//
// release-tag.sh is the one place that decides which commit gets the tag. If
// its ancestry check is ever removed or weakened, the original defect returns
// silently and the next person to notice is whoever runs `git describe` months
// later.
func TestReleaseTagScriptChecksAncestry(t *testing.T) {
	t.Parallel()

	script := readScript(t, "release-tag.sh")

	if !strings.Contains(script, "git merge-base --is-ancestor") {
		t.Error("scripts/release-tag.sh no longer verifies the tagged commit is an ancestor of main.\n" +
			"That check is the entire point of the script — without it, phase 2 can tag a commit\n" +
			"that is not on the trunk, which is the defect that stranded v1.7.8..v1.7.12.")
	}

	// It must reason about origin/main, not a local main. A local main can be
	// stale or hold another agent's work; the tag has to describe what is
	// actually published.
	if !strings.Contains(script, "origin/main") {
		t.Error("scripts/release-tag.sh must check ancestry against origin/main, not a local branch")
	}

	// It must refuse to overwrite an existing tag. Published tags are
	// referenced by Electron artifacts and the Homebrew cask; moving one is
	// the same class of error as re-cutting a burned Go module version.
	if !strings.Contains(script, "git ls-remote --exit-code --tags origin") {
		t.Error("scripts/release-tag.sh must check the REMOTE for an existing tag before creating one")
	}
	if strings.Contains(script, "git tag -f") || strings.Contains(script, "push --force") {
		t.Error("scripts/release-tag.sh must never force-create or force-push a tag")
	}
}

// TestBaselineTagIsPinned stops the historical baseline from quietly becoming
// a suppression mechanism.
//
// check-release-tags.sh forgives tags at or below BASELINE_TAG because they
// are off main AND published — the Electron artifacts on R2 and the Homebrew
// cask reference them by name, so moving one would break real installs for no
// gain. (The audit that motivated this found twenty such tags, back to
// v1.2.3-rc1; the flow has been stranding them intermittently for months.)
//
// That baseline is a record of when the two-phase flow landed, not a dial. The
// cheapest way to make a future off-main tag "pass" is to nudge BASELINE_TAG
// past it — a one-word change that looks like routine maintenance in a diff.
// This test converts that into a deliberate edit to an assertion with a reason
// attached, which is the difference between a decision and an accident.
func TestBaselineTagIsPinned(t *testing.T) {
	t.Parallel()

	script := readScript(t, "check-release-tags.sh")

	const wantBaseline = `BASELINE_TAG="v1.7.12"`
	if !strings.Contains(script, wantBaseline) {
		t.Errorf("check-release-tags.sh's BASELINE_TAG changed.\n\n"+
			"expected exactly: %s\n\n"+
			"The baseline marks the last release cut by the old single-phase flow. Tags after\n"+
			"it MUST be ancestors of main. Raising it forgives a real off-main release instead\n"+
			"of fixing it — if a new tag landed off main, cut the next version through the\n"+
			"two-phase flow (release.sh, merge, release-tag.sh) rather than moving this.",
			wantBaseline)
	}

	// The floor must be applied by version comparison, not by string equality
	// against a list — that is what keeps it readable as the set grows.
	if !strings.Contains(script, "version_le") {
		t.Error("check-release-tags.sh must compare tags against BASELINE_TAG by version order")
	}
}
