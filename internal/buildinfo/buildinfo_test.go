package buildinfo

import (
	"runtime/debug"
	"testing"
)

// TestIsDevBuildClassification pins the release-vs-dev discriminator the
// scaffolder relies on before ever writing a local-forge go.work. Only a
// clean, tagged semver from an unmodified tree counts as a release; "(devel)",
// pseudo-versions, dirty trees, and garbage are all dev.
func TestIsDevBuildClassification(t *testing.T) {
	cases := []struct {
		name        string
		mainVersion string
		vcsModified bool
		wantDev     bool
	}{
		{"release tag clean tree", "v1.2.3", false, false},
		{"prerelease tag clean tree", "v1.2.3-rc.1", false, false},
		{"release tag dirty tree is dev", "v1.2.3", true, true},
		{"devel marker is dev", "(devel)", false, true},
		{"empty version is dev", "", false, true},
		{"pseudo-version is dev", "v0.0.0-20260612070344-a3e3b883c97c", false, true},
		{"pseudo-version on tag base is dev", "v1.2.3-0.20260612070344-a3e3b883c97c", false, true},
		{"garbage is dev", "latest", false, true},
		{"missing v prefix is dev", "1.2.3", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDevBuildFrom(c.mainVersion, c.vcsModified); got != c.wantDev {
				t.Errorf("isDevBuildFrom(%q, modified=%v) = %v, want %v", c.mainVersion, c.vcsModified, got, c.wantDev)
			}
		})
	}
}

// TestIsDevBuildOverride pins the test seam: SetDevBuild wins over the ambient
// build info, and ClearDevBuild restores the real read.
func TestIsDevBuildOverride(t *testing.T) {
	t.Cleanup(ClearDevBuild)

	SetDevBuild(false)
	if IsDevBuild() {
		t.Error("SetDevBuild(false): IsDevBuild() = true, want false")
	}
	SetDevBuild(true)
	if !IsDevBuild() {
		t.Error("SetDevBuild(true): IsDevBuild() = false, want true")
	}
	ClearDevBuild()
	// After clearing, IsDevBuild reads the ambient test binary, which is
	// always "(devel)" under `go test` → dev.
	if !IsDevBuild() {
		t.Error("after ClearDevBuild: IsDevBuild() = false for a (devel) test binary, want true")
	}
}

// TestInstallableVersion pins the contract that InstallableVersion()
// only ever returns a ref a module proxy can serve: a release tag or a clean
// pseudo-version, never a `+dirty` build (which fails every CI run —
// fr-8c8a24ea97).
//
// It has TWO consumers, and the second is why this table is load-bearing:
//  1. the CI template's `go install ...@<ref>` step, which falls back to
//     pinning by git SHA on "";
//  2. since forge became one module, the version a scaffolded go.mod
//     REQUIRES (generator.resolveForgeVersion). A value that slips through
//     here becomes an unresolvable require in a user's project, and "" is
//     what routes an unreleasable build to the go.work source bridge
//     instead of a version it cannot honour.
func TestInstallableVersion(t *testing.T) {
	t.Cleanup(func() { Set("dev", "unknown", "unknown") })

	cases := []struct {
		name string
		set  string
		want string
	}{
		{"release tag", "v1.2.3", "v1.2.3"},
		{"prerelease tag", "v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"clean pseudo-version", "v0.0.0-20260612070344-a3e3b883c97c", "v0.0.0-20260612070344-a3e3b883c97c"},
		{"dirty pseudo-version rejected", "v0.0.0-20260612070344-a3e3b883c97c+dirty", ""},
		{"dirty release rejected", "v1.2.3+dirty", ""},
		{"dev sentinel rejected", "dev", ""},
		{"missing v prefix rejected", "1.2.3", ""},
		{"garbage rejected", "latest", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Set a non-"dev" value so Version() returns it verbatim
			// (Version falls through to ReadBuildInfo only for ""/"dev").
			Set(c.set, "unknown", "deadbeef")
			if got := InstallableVersion(); got != c.want {
				t.Errorf("Set(%q): InstallableVersion() = %q, want %q", c.set, got, c.want)
			}
		})
	}
}

// TestIsDevVersion pins the identity half of forge versioning: given a
// version STRING (a forge.yaml pin, a report line), is this a release or a
// build somebody made locally? Ordering is a separate question answered by
// SemVer comparison — conflating the two is what made a locally-built forge's
// pseudo-version indistinguishable from an ancient project.
func TestIsDevVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		// Released tags.
		{"v0.0.3", false},
		{"v0.1.0", false},
		{"v1.4.2", false},
		{"0.0.3", false}, // leading "v" optional
		{"v1.0.0-rc.1", false},
		// Sentinels.
		{"", true},
		{"dev", true},
		{"(devel)", true},
		// Go pseudo-versions: an untagged commit, i.e. a build nobody
		// published.
		{"v0.0.0-20260430002332-8f05b089372c", true},
		{"v0.0.4-0.20260724212501-dfb85daf8474", true},
		// Build metadata is only ever stamped from a modified tree.
		{"v0.0.4-0.20260724212501-dfb85daf8474+dirty", true},
		{"v1.4.2+dirty", true},
		// Not a version at all.
		{"main", true},
		{"latest", true},
	}
	for _, tt := range tests {
		if got := IsDevVersion(tt.version); got != tt.want {
			t.Errorf("IsDevVersion(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

// TestInstallableVersion_RefusesALocalCheckoutBuild is the regression for a
// failure that hit EVERY pull request.
//
// A pseudo-version can be perfectly formed and name a commit no proxy will
// serve. Since Go began stamping VCS info, that is exactly what a build from a
// working tree reports — and on a pull_request event the checked-out commit is
// GitHub's ephemeral refs/pull/N/merge, which exists on no branch. forge built
// there reported e.g. v0.0.0-20260916183946-6fbaa5b262be, the scaffolder wrote
// it into the test project's go.mod, and tidy failed with "invalid version:
// unknown revision". Pushes to main passed, because there the commit is real —
// which is why the two scaffold jobs failed on #207, #208, #210 and #213 alike
// while main stayed green.
//
// Under `go test` the ambient build info IS a local-checkout build, so this
// asserts the real condition rather than a simulation of it.
func TestInstallableVersion_RefusesALocalCheckoutBuild(t *testing.T) {
	if !builtFromLocalCheckout() {
		t.Skip("test binary carries no vcs.* settings (e.g. -buildvcs=false); nothing to assert")
	}
	t.Cleanup(func() { Set("dev", "unknown", "unknown") })

	// A clean, well-formed pseudo-version — the shape that slipped through.
	// Set() makes it look stamped, so clear that first: an ldflags stamp is a
	// release build asserting its own tag and is trusted on purpose.
	Set("", "unknown", "unknown")
	if got := InstallableVersion(); got != "" {
		t.Errorf("InstallableVersion() = %q for a build compiled from a working tree. "+
			"Nothing in that tree can prove the commit was pushed, and pinning it puts an "+
			"unresolvable require into a scaffold's go.mod.", got)
	}

	// An ldflags-stamped release is still installable: the build is asserting
	// a tag it was cut from, and refusing that would make releases unpinnable.
	Set("v1.2.3", "unknown", "deadbeef")
	if got := InstallableVersion(); got != "v1.2.3" {
		t.Errorf("InstallableVersion() = %q for an ldflags-stamped release, want v1.2.3", got)
	}
}

// TestHasVCSStamps is the runnable half of the pull-request regression above:
// the ambient build info under `go test` carries no vcs.* settings, so only an
// injected one can exercise both branches.
func TestHasVCSStamps(t *testing.T) {
	proxyInstalled := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"},
		{Key: "GOARCH", Value: "arm64"},
	}}
	if hasVCSStamps(proxyInstalled) {
		t.Error("a module served by the proxy carries no vcs.* settings; its version IS resolvable")
	}

	localBuild := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"},
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "6fbaa5b262be00000000000000000000000000"},
		{Key: "vcs.modified", Value: "false"},
	}}
	if !hasVCSStamps(localBuild) {
		t.Error("a build from a working tree stamps vcs.*; nothing there proves the commit was pushed")
	}

	// vcs.modified=false is the CI case exactly: a CLEAN checkout of an
	// ephemeral merge commit. Clean is not the same as published, which is the
	// distinction the old shape-only check could not make.
	if !hasVCSStamps(localBuild) {
		t.Error("a clean working tree is still a working tree")
	}
}
