// Copyright (c) 2025 Reliant Labs
package version

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
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

// TestApplyBuildInfoReadsTheToolchainStamp pins where a binary built without
// -ldflags gets its identity: the module version and the VCS stamp the Go
// toolchain embeds. Before this, a `go install`ed reliant printed
// `commit: unknown` while its own version string ended in `+dirty`.
func TestApplyBuildInfoReadsTheToolchainStamp(t *testing.T) {
	vcs := func(revision, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: modified},
		}
	}
	for _, tc := range []struct {
		name                 string
		ldVersion, ldCommit  string // what -ldflags stamped ("unknown" = nothing)
		info                 debug.BuildInfo
		wantVersion, wantCmt string
		wantDirty            string
	}{
		{
			name:      "go install from a dirty checkout",
			ldVersion: "unknown", ldCommit: "unknown",
			info: debug.BuildInfo{
				Main:     debug.Module{Version: "v1.7.17-0.20260930090302-898d6af5ef74+dirty"},
				Settings: vcs("898d6af5ef74c0ffee00c0ffee00c0ffee00c0ff", "true"),
			},
			wantVersion: "v1.7.17-0.20260930090302-898d6af5ef74+dirty", wantCmt: "898d6af5ef74", wantDirty: DirtyTrue,
		},
		{
			name:      "go build from a clean checkout",
			ldVersion: "unknown", ldCommit: "unknown",
			info:        debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs("0123456789abcdef", "false")},
			wantVersion: "unknown", wantCmt: "0123456789ab", wantDirty: DirtyFalse,
		},
		{
			name:      "release build: -ldflags wins over the module graph",
			ldVersion: "v1.8.0", ldCommit: "abc1234",
			info:        debug.BuildInfo{Main: debug.Module{Version: "v1.8.0-0.20261001000000-ffffffffffff"}, Settings: vcs("ffffffffffffffff", "false")},
			wantVersion: "v1.8.0", wantCmt: "abc1234", wantDirty: DirtyFalse,
		},
		{
			name:      "-buildvcs=false: nothing to read",
			ldVersion: "v1.8.0", ldCommit: "abc1234",
			info:        debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantVersion: "v1.8.0", wantCmt: "abc1234", wantDirty: DirtyUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldV, oldC, oldD := Version, Commit, dirty
			t.Cleanup(func() { Version, Commit, dirty = oldV, oldC, oldD })
			Version, Commit, dirty = tc.ldVersion, tc.ldCommit, DirtyUnknown

			applyBuildInfo(&tc.info)

			got := Get()
			if got.Version != tc.wantVersion || got.Commit != tc.wantCmt || got.Dirty != tc.wantDirty {
				t.Errorf("got version=%q commit=%q dirty=%q, want %q %q %q",
					got.Version, got.Commit, got.Dirty, tc.wantVersion, tc.wantCmt, tc.wantDirty)
			}
		})
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
