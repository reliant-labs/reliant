package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

// forgeDep / embedderMain model the two shapes debug.ReadBuildInfo reports
// for the two ways forge runs on one machine (the defect under test):
//
//   - standalone: `go install ./cmd/forge` — forge IS the main module.
//   - embedded:   reliant imports github.com/reliant-labs/forge/cli, so the
//     main module is reliant and forge is a DEP, usually behind a
//     local `replace` to a sibling checkout.
//
// The two can be different builds with nothing on the CLI to tell them
// apart. buildFrom is the discriminator.

func standaloneInfo() *debug.BuildInfo {
	return &debug.BuildInfo{
		Path: "github.com/reliant-labs/forge/cmd/forge",
		Main: debug.Module{
			Path:    "github.com/reliant-labs/forge",
			Version: "v0.0.4-0.20260811031231-912a7b2c850e+dirty",
		},
		Deps: []*debug.Module{
			{Path: "github.com/reliant-labs/forge/pkg", Version: "(devel)"},
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "912a7b2c850ea203cc7827d69ed004a59827aa31"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
}

func embeddedInfo() *debug.BuildInfo {
	return &debug.BuildInfo{
		Path: "github.com/reliant-labs/reliant/cmd/reliant",
		Main: debug.Module{
			Path:    "github.com/reliant-labs/reliant",
			Version: "v1.5.1-0.20260731051956-0359a80a8f23+dirty",
		},
		Deps: []*debug.Module{
			{
				Path:    "github.com/reliant-labs/forge",
				Version: "v0.0.3",
				Replace: &debug.Module{Path: "../forge", Version: "(devel)"},
			},
			{
				Path:    "github.com/reliant-labs/forge/pkg",
				Version: "v0.0.3",
				Replace: &debug.Module{Path: "../forge/pkg", Version: "(devel)"},
			},
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0359a80a8f2311522b41550859e9c6e827efefc5"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
}

// TestBuildFrom_EmbeddedDoesNotReportEmbedderVersion is the core defect.
//
// When forge is compiled INTO reliant, the old Version() fell through to
// info.Main.Version — which is RELIANT's version. That is how the affected
// project came to record `forge_version: v1.5.1-...` (a reliant version) in
// its forge.yaml, and why no forge command could tell the two builds apart.
func TestBuildFrom_EmbeddedDoesNotReportEmbedderVersion(t *testing.T) {
	b := buildFrom(embeddedInfo(), "", "")

	if strings.HasPrefix(b.Version, "v1.5.1") {
		t.Fatalf("embedded build reported the EMBEDDER's version %q as forge's own version", b.Version)
	}
	if !b.Embedded {
		t.Error("embedded build should be flagged Embedded")
	}
	if b.Embedder != "github.com/reliant-labs/reliant" {
		t.Errorf("Embedder = %q, want the host module path", b.Embedder)
	}
	if b.EmbedderVersion != "v1.5.1-0.20260731051956-0359a80a8f23+dirty" {
		t.Errorf("EmbedderVersion = %q, want the host module version", b.EmbedderVersion)
	}
	// forge's own identity must come from the forge DEP, following the
	// local replace that makes this a different build from the standalone.
	if b.Version != "v0.0.3" {
		t.Errorf("Version = %q, want the forge dep version v0.0.3", b.Version)
	}
	if b.ReplacedBy != "../forge" {
		t.Errorf("ReplacedBy = %q, want the local replace target", b.ReplacedBy)
	}
}

// TestBuildFrom_StandaloneUsesMainModule is the mirror: when forge IS the
// main module, its identity comes from Main.
func TestBuildFrom_StandaloneUsesMainModule(t *testing.T) {
	b := buildFrom(standaloneInfo(), "", "")

	if b.Embedded {
		t.Error("standalone build should not be flagged Embedded")
	}
	if b.Version != "v0.0.4-0.20260811031231-912a7b2c850e+dirty" {
		t.Errorf("Version = %q, want the main module version", b.Version)
	}
	if b.Commit != "912a7b2c850ea203cc7827d69ed004a59827aa31" {
		t.Errorf("Commit = %q, want the vcs.revision", b.Commit)
	}
}

// TestBuildFrom_NeverEmptyIdentity: an empty version string is a defect on
// its own — it is what `reliant forge --version` printed
// ("forge version  (built , commit )") and it is unusable for telling two
// builds apart. Every shape must yield SOMETHING identifying.
func TestBuildFrom_NeverEmptyIdentity(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
	}{
		{"standalone", standaloneInfo()},
		{"embedded", embeddedInfo()},
		{"no build info", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := buildFrom(tc.info, "", "")
			if strings.TrimSpace(b.Version) == "" {
				t.Error("Version is empty — cannot identify this build")
			}
			s := b.String()
			if strings.TrimSpace(s) == "" {
				t.Fatal("String() is empty — cannot identify this build")
			}
			// The rendered identity must not contain the empty-field
			// artifacts the defect produced.
			for _, bad := range []string{"version  ", "commit )", "built )"} {
				if strings.Contains(s, bad) {
					t.Errorf("String() = %q contains empty-field artifact %q", s, bad)
				}
			}
		})
	}
}

// TestBuildFrom_TwoBuildsAreDistinguishable is the whole point: the
// standalone and embedded identities must not render identically, or a user
// with both on PATH still cannot tell which one ran.
func TestBuildFrom_TwoBuildsAreDistinguishable(t *testing.T) {
	standalone := buildFrom(standaloneInfo(), "", "").String()
	embedded := buildFrom(embeddedInfo(), "", "").String()

	if standalone == embedded {
		t.Fatalf("standalone and embedded builds render identically (%q) — indistinguishable", standalone)
	}
	if !strings.Contains(embedded, "reliant") {
		t.Errorf("embedded identity %q should name the host binary", embedded)
	}
}

// TestBuildFrom_LdflagsWin: when a release build stamps version/commit via
// ldflags, those take precedence over the inferred build info.
func TestBuildFrom_LdflagsWin(t *testing.T) {
	b := buildFrom(standaloneInfo(), "v1.2.3", "deadbeef")
	if b.Version != "v1.2.3" {
		t.Errorf("Version = %q, want the ldflags stamp v1.2.3", b.Version)
	}
	if b.Commit != "deadbeef" {
		t.Errorf("Commit = %q, want the ldflags stamp deadbeef", b.Commit)
	}
}

// TestVersionFromInfo_EmbeddedNeverReportsEmbedderVersion covers the
// downstream consequence of the same root cause.
//
// Version() is what the scaffolder stamps into a new project's forge.yaml as
// `forge_version`. When forge runs embedded, falling through to
// info.Main.Version records the HOST's version — which is exactly what the
// affected project shows:
//
//	forge_version: v1.5.1-0.20260731051956-0359a80a8f23+dirty
//
// v1.5.1 is a RELIANT version; forge has never had one. A project that
// records its toolchain wrongly cannot detect skew against it later.
func TestVersionFromInfo_EmbeddedNeverReportsEmbedderVersion(t *testing.T) {
	got := versionFromInfo(embeddedInfo(), "dev")

	if got == "v1.5.1-0.20260731051956-0359a80a8f23+dirty" {
		t.Fatalf("Version() reported the EMBEDDER's version %q as forge's own", got)
	}
	if got != "v0.0.3" {
		t.Errorf("Version() = %q, want forge's own dep version v0.0.3", got)
	}
}

// TestVersionFromInfo_StandaloneUsesMainModule is the mirror: standalone
// forge legitimately reads its own main module version.
func TestVersionFromInfo_StandaloneUsesMainModule(t *testing.T) {
	got := versionFromInfo(standaloneInfo(), "dev")
	if got != "v0.0.4-0.20260811031231-912a7b2c850e+dirty" {
		t.Errorf("Version() = %q, want the main module version", got)
	}
}
