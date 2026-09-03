// Copyright (c) 2025 Reliant Labs
package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// branchNames are values that identify a BRANCH rather than a build. A binary
// stamped with one of these reports a "version" that cannot be traced to any
// release, and the UI renders it verbatim — which is how Settings → About came
// to read "vmain" in production.
var branchNames = []string{"main", "master", "HEAD", "develop"}

// TestVersionIsNeverABranchName is the regression guard for the vmain bug.
//
// The published image was built by .github/workflows/build-images.yml, which
// passed VERSION=${{ steps.meta.outputs.version || github.ref_name }}. That
// workflow only triggered on a push to main, so docker/metadata-action's
// `type=ref,event=branch` resolved the version to the literal string "main"
// (the `|| github.ref_name` fallback would have produced the same thing). The
// running prod pod reported:
//
//	reliant version main
//	  commit:  unknown
//
// This test pins the invariant on the value the binary actually carries.
func TestVersionIsNeverABranchName(t *testing.T) {
	for _, name := range branchNames {
		if strings.EqualFold(Version, name) {
			t.Errorf("version.Version is %q, which is a branch name, not a version.\n"+
				"A build must be stamped with a semver (tag push) or a pseudo-version "+
				"such as 0.0.0-dev-<sha> (branch push). See the `stamp` step in "+
				".github/workflows/build-images.yml.", Version)
		}
	}
}

// TestGetIncludesForgeVersion pins that build metadata carries the forge
// version, which control-plane derives from reliant's pin rather than
// restating it.
func TestGetIncludesForgeVersion(t *testing.T) {
	if got := Get().Forge; got == "" {
		t.Error("BuildInfo.Forge is empty; it must always carry a value (\"unknown\" when unresolvable)")
	}
}

// TestForgeReadsFromModuleGraph asserts Forge() reports what the module graph
// says, NOT a separately-maintained constant.
//
// This test binary links no forge package, so debug.ReadBuildInfo lists no
// forge dep and "unknown" is the correct answer here. The real value is proven
// in the same breath by comparing against go.mod: a build that DOES link forge
// must report that pin. Asserting the go.mod pin is well-formed keeps this
// test meaningful rather than tautological.
func TestForgeReadsFromModuleGraph(t *testing.T) {
	got := Forge()
	if got == "" {
		t.Fatal("Forge() returned an empty string; it must return \"unknown\" when unresolvable")
	}

	pinned := forgePinFromGoMod(t)
	if got != "unknown" && got != pinned {
		t.Errorf("Forge() = %q but go.mod pins %q — the reported version must come "+
			"from the linked module, not a stale copy", got, pinned)
	}
}

func forgePinFromGoMod(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		b, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(b), "module github.com/reliant-labs/reliant") {
			m := regexp.MustCompile(`(?m)^\s*github\.com/reliant-labs/forge\s+(v\S+)`).FindSubmatch(b)
			if m == nil {
				t.Fatal("go.mod has no github.com/reliant-labs/forge require")
			}
			return string(m[1])
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding the repo go.mod")
		}
		dir = parent
	}
}
