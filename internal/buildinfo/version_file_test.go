package buildinfo

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

// workspaceEmbeddedInfo reproduces the exact shape from the briefing: a host
// binary (reliant) built with go.work `use ../forge`, so the forge
// dependency resolves from source and reports "(devel)" instead of a real
// module-cache version.
func workspaceEmbeddedInfo() *debug.BuildInfo {
	return &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/reliant-labs/reliant", Version: "v1.7.1+dirty"},
		Deps: []*debug.Module{{Path: forgeModulePath, Version: "(devel)"}},
	}
}

// TestVersionFromInfo_WorkspaceEmbeddedPrefersTheGitDerivedVersion: in the
// go.work embedded shape (forge is a dep at "(devel)"), the answer comes from
// the forge checkout this binary was compiled from — a real pseudo-version
// naming a real commit.
//
// The ordering assertion is the point. The VERSION file states the last
// RELEASE; source is arbitrarily far ahead of it, so a truthful version must
// sort strictly AFTER that release. The floor this replaced sorted EQUAL to
// it, which is what let a released binary overwrite a newer build's vendored
// KCL (see versionFloor's comment).
func TestVersionFromInfo_WorkspaceEmbeddedPrefersTheGitDerivedVersion(t *testing.T) {
	got := versionFromInfo(workspaceEmbeddedInfo(), "dev")

	if got == "dev" || got == "(devel)" {
		t.Fatalf("versionFromInfo = %q, want a derived version, not a sentinel", got)
	}
	release := versionFromFile(embeddedVersionFile)
	if release != "" && semver.Compare(got, release) <= 0 {
		t.Errorf("versionFromInfo = %q, which does not sort after the last release %q — "+
			"a build of source ahead of a release must not compare equal or older to it",
			got, release)
	}
	if !IsDevVersion(got) {
		t.Errorf("versionFromInfo = %q must still read as a dev version", got)
	}
}

// TestVersionFromInfo_WorkspaceEmbeddedFallsBackToFloorWithoutGit: when the
// checkout cannot be reached, the last-resort floor still has to order
// correctly rather than claim the release itself.
func TestVersionFromInfo_WorkspaceEmbeddedFallsBackToFloorWithoutGit(t *testing.T) {
	SetGitVersion("")
	t.Cleanup(ClearGitVersion)

	got := versionFromInfo(workspaceEmbeddedInfo(), "dev")

	if got == "dev" {
		t.Fatalf("versionFromInfo = %q, want the VERSION file floor, not the bare dev sentinel", got)
	}
	want := unknownDevAfter(versionFromFile(embeddedVersionFile))
	if got != want {
		t.Errorf("versionFromInfo = %q, want the embedded VERSION file marked as a dev build: %q", got, want)
	}
}

// TestVersionFromInfo_LdflagsStillWinsOverVersionFile pins tier 1: a release
// build's explicit stamp must never be shadowed by the new file-based floor.
func TestVersionFromInfo_LdflagsStillWinsOverVersionFile(t *testing.T) {
	got := versionFromInfo(workspaceEmbeddedInfo(), "v9.9.9")
	if got != "v9.9.9" {
		t.Errorf("versionFromInfo = %q, want the ldflags stamp v9.9.9 to win over the VERSION file floor", got)
	}
}

// TestVersionFromInfo_RealDepVersionStillWinsOverVersionFile pins tier 2: a
// genuinely resolvable forge dep version (the ordinary `go get` case, no
// go.work involved) must never be shadowed by the file-based floor.
func TestVersionFromInfo_RealDepVersionStillWinsOverVersionFile(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/reliant-labs/reliant", Version: "v1.7.1"},
		Deps: []*debug.Module{{Path: forgeModulePath, Version: "v0.0.3"}},
	}
	got := versionFromInfo(info, "dev")
	if got != "v0.0.3" {
		t.Errorf("versionFromInfo = %q, want the real forge dep version v0.0.3 to win over the VERSION file floor", got)
	}
}

// TestIsDevBuild_WorkspaceEmbeddedStaysDevAfterVersionFloor pins that the
// new floor is a REPORTING fallback only — it must never leak into the
// dev/release classification. A workspace build must still be a dev build:
// the scaffolder gates writing a local-forge go.work bridge on this.
func TestIsDevBuild_WorkspaceEmbeddedStaysDevAfterVersionFloor(t *testing.T) {
	info := workspaceEmbeddedInfo()
	dep, embedded := forgeModuleDep(info)
	if !embedded {
		t.Fatal("forge must be detected as embedded")
	}
	if !isDevBuildFrom(dep.Version, false) {
		t.Error("a go.work-embedded forge ((devel) dep) must still classify as a dev build")
	}
}

// TestInstallableVersion_WorkspaceFloorStaysUninstallable pins that the
// VERSION-file floor, being marked with "+dev" build metadata, is never
// mistaken for an installable ref: InstallableVersion must keep returning ""
// so generated CI falls back to pinning by git SHA (a "+dev" ref cannot be
// resolved by `go install ...@<ref>` from any module proxy).
func TestInstallableVersion_WorkspaceFloorStaysUninstallable(t *testing.T) {
	got := versionFromInfo(workspaceEmbeddedInfo(), "dev")
	if installableVersionRE.MatchString(got) {
		t.Errorf("VERSION-file floor %q must not match the installable-ref pattern", got)
	}
}

// TestBuildFrom_WorkspaceEmbeddedUsesVersionFloorAndStaysHonest covers the
// Build.String()/Describe() path (identity.go's buildFrom), the other
// consumer of the same "(devel)" dep shape.
func TestBuildFrom_WorkspaceEmbeddedUsesVersionFloorAndStaysHonest(t *testing.T) {
	SetGitVersion("")
	t.Cleanup(ClearGitVersion)

	b := buildFrom(workspaceEmbeddedInfo(), "", "")

	if b.Version == "(devel)" {
		t.Fatalf("Build.Version = %q, want the VERSION file floor, not the raw (devel) marker", b.Version)
	}
	want := unknownDevAfter(versionFromFile(embeddedVersionFile))
	if b.Version != want {
		t.Errorf("Build.Version = %q, want %q", b.Version, want)
	}
	if s := b.String(); strings.Contains(s, "(devel)") {
		t.Errorf("String() = %q must not surface the raw (devel) marker to a user", s)
	}
}

// TestVersionFromFile pins the parsing/validation rules for the embedded
// VERSION file content: trims whitespace, accepts a clean release tag,
// rejects anything else (missing file, malformed content) by degrading to
// "" rather than trusting garbage into a reported version.
func TestVersionFromFile(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"clean tag", "v0.1.1", "v0.1.1"},
		{"trailing newline", "v0.1.1\n", "v0.1.1"},
		{"surrounding whitespace", "  v0.1.1  \n", "v0.1.1"},
		{"prerelease tag", "v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"empty file", "", ""},
		{"missing v prefix", "0.1.1", ""},
		{"garbage", "not-a-version", ""},
		{"devel marker", "(devel)", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := versionFromFile(c.raw); got != c.want {
				t.Errorf("versionFromFile(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// TestUnknownDevAfter pins the last-resort floor's THREE required properties,
// each of which a previous spelling got wrong:
//
//  1. it sorts strictly AFTER the release it is derived from. `v0.1.1+dev`
//     did not — semver ignores build metadata, so it compared EQUAL, and an
//     older released binary was therefore allowed to overwrite a newer
//     workspace build's vendored KCL.
//  2. it sorts strictly BEFORE the next release, since that is all that is
//     known: "some commit after v0.1.1".
//  3. it is NOT installable. `v0.1.16-0.dev` alone is a valid semver
//     prerelease that installableVersionRE matches, which would put an
//     unresolvable version into a scaffold's go.mod — hence `+unknown`.
func TestUnknownDevAfter(t *testing.T) {
	got := unknownDevAfter("v0.1.1")
	if got != "v0.1.2-0.dev+unknown" {
		t.Fatalf("unknownDevAfter(v0.1.1) = %q, want v0.1.2-0.dev+unknown", got)
	}
	if semver.Compare(got, "v0.1.1") <= 0 {
		t.Errorf("%q must sort AFTER v0.1.1", got)
	}
	if semver.Compare(got, "v0.1.2") >= 0 {
		t.Errorf("%q must sort BEFORE v0.1.2", got)
	}
	if installableVersionRE.MatchString(got) {
		t.Errorf("%q must not match the installable-ref pattern", got)
	}
	if !IsDevVersion(got) {
		t.Errorf("%q must read as a dev version", got)
	}

	// Idempotent / defensive: already-marked or non-plain inputs pass through.
	cases := []struct{ in, want string }{
		{"", ""},
		{"v0.1.1+dirty", "v0.1.1+dirty"},
		{"v0.1.2-0.dev+unknown", "v0.1.2-0.dev+unknown"},
	}
	for _, c := range cases {
		if got := unknownDevAfter(c.in); got != c.want {
			t.Errorf("unknownDevAfter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEmbeddedVersionFileMatchesSource mirrors internal/assets'
// TestEmbeddedForgeProtoMatchesSource: this package's embedded copy of
// VERSION must stay byte-identical to the repo-root source of truth, because
// an embed directive cannot reach outside its own package directory to read
// it directly. Sync with:
//
//	cp VERSION internal/buildinfo/VERSION
func TestEmbeddedVersionFileMatchesSource(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/buildinfo -> ../../ -> repo root
	sourcePath := filepath.Join(cwd, "..", "..", "VERSION")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source VERSION at %s: %v", sourcePath, err)
	}
	if !bytes.Equal(source, []byte(embeddedVersionFile)) {
		t.Fatalf("embedded VERSION (%q) is out of sync with source VERSION (%q) — "+
			"sync with: cp VERSION internal/buildinfo/VERSION", embeddedVersionFile, string(source))
	}
}
