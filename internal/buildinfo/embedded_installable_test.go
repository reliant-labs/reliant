package buildinfo

import (
	"runtime/debug"
	"testing"
)

// TestForgeBytesFromCheckout_EmbeddedJudgesForgeNotTheHost is the regression
// for released `reliant forge project new` scaffolding a go.mod that requires
// the RETIRED module github.com/reliant-labs/forge/pkg v0.1.15.
//
// The chain: a released reliant is built from a checkout (with -trimpath), so
// its build info carries the HOST's vcs.* stamps. InstallableVersion used to
// read those stamps as "forge was compiled from a working tree" and return ""
// — although forge itself arrived from the module proxy at a real version.
// With "" the scaffold pins no forge at all; -trimpath leaves no source tree to
// bridge to; and `go mod tidy`, asked to resolve
// github.com/reliant-labs/forge/pkg/forgepb with nothing requiring forge,
// picks the longest module path that provides it: the retired forge/pkg
// v0.1.15. The next `forge generate` then refuses the project forge just made.
//
// Reproduced end to end with a scratch host module requiring forge from the
// proxy, committed and built with -trimpath.
//
// The rule is the one IsDevBuild already follows for the embedded case: ask
// how FORGE resolved (its dep entry), never what the host's tree looked like.
func TestForgeBytesFromCheckout_EmbeddedJudgesForgeNotTheHost(t *testing.T) {
	const host = "github.com/reliant-labs/reliant"
	hostVCS := []debug.BuildSetting{
		{Key: "-trimpath", Value: "true"},
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "943f092f00000000000000000000000000000000"},
		{Key: "vcs.modified", Value: "false"},
	}

	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		want bool
		why  string
	}{
		{
			name: "standalone forge built from a checkout",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: forgeModulePath, Version: "v0.1.18-0.20260925050659-5925770089f4"},
				Settings: hostVCS,
			},
			want: true,
			why:  "a PR's ephemeral merge commit stamps a clean pseudo-version no proxy can serve",
		},
		{
			name: "standalone forge installed from the proxy",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: forgeModulePath, Version: "v0.1.18-0.20260925050659-5925770089f4", Sum: "h1:x"},
			},
			want: false,
			why:  "no vcs.* settings: the proxy served this module, so it demonstrably has it",
		},
		{
			name: "embedded: host from a checkout, forge from the proxy",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.7.15"},
				Deps: []*debug.Module{{
					Path: forgeModulePath, Version: "v0.1.18-0.20260925050659-5925770089f4", Sum: "h1:jKQY",
				}},
				Settings: hostVCS,
			},
			want: false,
			why: "forge's bytes came from the module cache at a version with a go.sum hash — the host's " +
				"vcs stamps describe the HOST's tree, not forge's",
		},
		{
			name: "embedded: forge replaced by a local directory",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.7.15"},
				Deps: []*debug.Module{{
					Path: forgeModulePath, Version: "v0.1.18-0.20260925050659-5925770089f4",
					Replace: &debug.Module{Path: "../forge"},
				}},
				Settings: hostVCS,
			},
			want: true,
			why:  "a directory replace compiles a local checkout; nothing proves it was pushed",
		},
		{
			name: "embedded: forge bridged by go.work",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: host, Version: "v1.7.15"},
				Deps:     []*debug.Module{{Path: forgeModulePath, Version: "(devel)"}},
				Settings: hostVCS,
			},
			want: true,
			why:  "(devel) is a workspace module: local bytes",
		},
		{
			name: "embedded: forge dep with no module sum",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.7.15"},
				Deps: []*debug.Module{{
					Path: forgeModulePath, Version: "v0.1.18-0.20260925050659-5925770089f4",
				}},
				Settings: hostVCS,
			},
			want: true,
			why:  "without a go.sum hash there is no evidence the module came from a proxy — refuse rather than guess",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := forgeBytesFromCheckout(tc.info); got != tc.want {
				t.Errorf("forgeBytesFromCheckout = %v, want %v: %s", got, tc.want, tc.why)
			}
		})
	}
}
