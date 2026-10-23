package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestSourceFrom(t *testing.T) {
	const rev = "1b22a077ba9cd41ed53e5395d31cbba17f7845f6"
	tests := []struct {
		name    string
		info    *debug.BuildInfo
		stamped string
		want    Source
	}{
		{
			name: "standalone from a checkout: vcs settings are forge's own",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: forgeModulePath, Version: "v0.1.44-0.20261005230203-1b22a077ba9c"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: "true"}},
			},
			want: Source{Revision: rev, Modified: true},
		},
		{
			name: "standalone `go install @pseudo`: the version names the commit",
			info: &debug.BuildInfo{Main: debug.Module{Path: forgeModulePath, Version: "v0.1.44-0.20261005230203-1b22a077ba9c"}},
			want: Source{Revision: "1b22a077ba9c"},
		},
		{
			name: "standalone `go install @tag`: the tag is left for the caller to resolve",
			info: &debug.BuildInfo{Main: debug.Module{Path: forgeModulePath, Version: "v0.1.44"}},
			want: Source{Tag: "v0.1.44"},
		},
		{
			name:    "standalone with nothing but an ldflags stamp",
			info:    &debug.BuildInfo{Main: debug.Module{Path: forgeModulePath, Version: "(devel)"}},
			stamped: "abcdef1234567",
			want:    Source{Revision: "abcdef1234567"},
		},
		{
			// The roofers binary: reliant embeds forge through a workspace.
			// vcs.* is RELIANT's tree and must never be read as forge's.
			name: "embedded through a workspace: nothing names forge's commit",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: "github.com/reliant-labs/reliant", Version: "v1.7.17-0.20260930090302-898d6af5ef74+dirty"},
				Deps:     []*debug.Module{{Path: forgeModulePath, Version: "(devel)"}},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "898d6af5ef74944c46bbdfc467b9a2e315e5c6d5"}, {Key: "vcs.modified", Value: "true"}},
			},
			stamped: "898d6af5ef74",
			want:    Source{Embedded: true},
		},
		{
			name: "embedded behind a directory replace",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/reliant-labs/reliant"},
				Deps: []*debug.Module{{Path: forgeModulePath, Version: "v0.1.29", Replace: &debug.Module{Path: "../forge"}}},
			},
			want: Source{Embedded: true},
		},
		{
			name: "embedded at a published pseudo-version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/reliant-labs/control-plane"},
				Deps: []*debug.Module{{Path: forgeModulePath, Version: "v0.1.44-0.20261005230203-1b22a077ba9c", Sum: "h1:x"}},
			},
			want: Source{Revision: "1b22a077ba9c", Embedded: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceFrom(tc.info, tc.stamped); got != tc.want {
				t.Errorf("sourceFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCommitOfVersion(t *testing.T) {
	for _, tc := range []struct{ in, rev, tag string }{
		{"v0.1.44-0.20261005230203-1b22a077ba9c", "1b22a077ba9c", ""},
		{"v0.1.44", "", "v0.1.44"},
		{"v0.1.45-0.20261005230203-1b22a077ba9c+dirty", "1b22a077ba9c", ""},
		{"v0.1.45-0.dev+unknown", "", ""},
		{"(devel)", "", ""},
		{"", "", ""},
	} {
		if rev, tag := commitOfVersion(tc.in); rev != tc.rev || tag != tc.tag {
			t.Errorf("commitOfVersion(%q) = %q, %q; want %q, %q", tc.in, rev, tag, tc.rev, tc.tag)
		}
	}
}
