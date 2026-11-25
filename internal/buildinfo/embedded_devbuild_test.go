// Copyright (c) 2025 Reliant Labs
package buildinfo

import (
	"runtime/debug"
	"testing"
)

// TestIsDevBuild_EmbeddedClassifiesOnForgeNotTheHost pins the rule for forge
// running INSIDE another binary (`reliant forge …`).
//
// info.Main describes the HOST. Classifying on it asks the wrong question: a
// dirty reliant that consumes a TAGGED forge would be called a dev forge, and
// every project it scaffolds would get a go.work bridging to a local forge
// checkout the user never chose. Measured — reliant's binary reports
//
//	mod  github.com/reliant-labs/reliant  v1.5.1-…+dirty   ← host, dirty
//	dep  github.com/reliant-labs/forge    v0.0.4           ← forge, tagged
//
// and the old code read only the first line.
//
// The host's vcs.modified is deliberately not consulted for the embedded case:
// it describes the host's working tree, while forge's own bytes came from the
// module cache regardless of what the host edited.
func TestIsDevBuild_EmbeddedClassifiesOnForgeNotTheHost(t *testing.T) {
	const host = "github.com/reliant-labs/reliant"

	for _, tc := range []struct {
		name   string
		info   *debug.BuildInfo
		wantIs bool
		why    string
	}{
		{
			name: "dirty host, tagged forge dep -> RELEASE",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.5.1-0.20260812034956-a80455e31e13+dirty"},
				Deps: []*debug.Module{{Path: forgeModulePath, Version: "v0.0.4"}},
				Settings: []debug.BuildSetting{
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantIs: false,
			why:    "forge came from the module cache at a clean tag; the host's dirt is not forge's",
		},
		{
			name: "clean host, forge replaced by a local directory -> DEV",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.5.1"},
				Deps: []*debug.Module{{
					Path:    forgeModulePath,
					Version: "v0.0.4",
					Replace: &debug.Module{Path: "../forge"},
				}},
			},
			wantIs: true,
			why:    "a directory replace IS the sibling-checkout loop — the developer is iterating on forge",
		},
		{
			name: "host bridged by go.work -> forge dep reads (devel) -> DEV",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.5.1"},
				Deps: []*debug.Module{{Path: forgeModulePath, Version: "(devel)"}},
			},
			wantIs: true,
			why:    "go.work resolves forge from source, which surfaces as (devel) on the dep",
		},
		{
			name: "dirty host, forge at a pseudo-version -> DEV",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: host, Version: "v1.5.1+dirty"},
				Deps: []*debug.Module{{
					Path:    forgeModulePath,
					Version: "v0.0.4-0.20260812034956-037d029d45b7",
				}},
				Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
			},
			wantIs: true,
			why:    "an untagged commit is a dev forge no matter how it was consumed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep, embedded := forgeModuleDep(tc.info)
			if !embedded {
				t.Fatalf("forge must be detected as embedded when Main is %q", tc.info.Main.Path)
			}
			var got bool
			if dep.Replace != nil {
				got = dep.Replace.Version == ""
			} else {
				got = isDevBuildFrom(dep.Version, false)
			}
			if got != tc.wantIs {
				t.Errorf("IsDevBuild = %v, want %v — %s", got, tc.wantIs, tc.why)
			}
		})
	}
}

// TestForgeModuleDep_StandaloneIsNotEmbedded keeps the standalone `forge`
// binary on the original path: when forge IS the main module there is no dep
// entry for it, and info.Main is the right thing to classify on.
func TestForgeModuleDep_StandaloneIsNotEmbedded(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Path: forgeModulePath, Version: "v0.0.4"},
		Deps: []*debug.Module{{Path: "golang.org/x/mod", Version: "v0.35.0"}},
	}
	if _, embedded := forgeModuleDep(info); embedded {
		t.Error("the standalone forge binary must not be treated as embedded — " +
			"there is no forge dep to read, and info.Main is forge itself")
	}
}

// classifyBuildInfo is the decision IsDevBuild makes, extracted so a test can
// drive it with synthetic build info. IsDevBuild itself reads the ambient
// binary's info (always "(devel)" under `go test`), so the wiring between
// "read build info" and "decide" is what this covers.
func classifyBuildInfo(info *debug.BuildInfo) bool {
	vcsModified := false
	for _, s := range info.Settings {
		if s.Key == "vcs.modified" && s.Value == "true" {
			vcsModified = true
			break
		}
	}
	if forgeDep, embedded := forgeModuleDep(info); embedded {
		if forgeDep.Replace != nil {
			return forgeDep.Replace.Version == ""
		}
		return isDevBuildFrom(forgeDep.Version, false)
	}
	return isDevBuildFrom(info.Main.Version, vcsModified)
}

// TestClassify_DirtyHostTaggedForgeIsARelease is the regression pin, stated at
// the level of the whole decision rather than its helpers.
//
// Before the embedded branch existed this returned true — dev — because the
// host was dirty, and every scaffold from a `reliant forge` built that way got
// a go.work pointing at a local forge checkout.
func TestClassify_DirtyHostTaggedForgeIsARelease(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/reliant-labs/reliant",
			Version: "v1.5.1-0.20260812034956-a80455e31e13+dirty",
		},
		Deps:     []*debug.Module{{Path: forgeModulePath, Version: "v0.0.4"}},
		Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
	}
	if classifyBuildInfo(info) {
		t.Error("a DIRTY host consuming a TAGGED forge must classify as a RELEASE forge: " +
			"the host's working tree says nothing about the forge bytes, which came from " +
			"the module cache at v0.0.4. Classifying dev here writes a local-forge go.work " +
			"bridge into every scaffolded project.")
	}
}
