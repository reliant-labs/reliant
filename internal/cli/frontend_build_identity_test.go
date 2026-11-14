package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestFrontendBuildEnv_CarriesBuildIdentity pins what a frontend build is told
// about itself: the GitSource it was fetched from and the release it ships in.
//
// The regression it guards: reliant-web, a GitSource frontend forge builds for
// Firebase, shipped "version unknown" to prod. The shallow, tagless fetch left
// the build only a detached sha, and the release name never reached it at all.
func TestFrontendBuildEnv_CarriesBuildIdentity(t *testing.T) {
	const sha = "10c676b6f2fa66a4b13e68f2f995289a7245b8f0"
	pinned := FrontendEntity{
		Name: "reliant-web",
		Type: "vite",
		Source: &config.GitSource{
			Repo:   "github.com/reliant-labs/reliant",
			Ref:    "v1.8.1",
			Subdir: "web",
		},
		SourceCommit: sha,
		Release:      "20261011.184732-0b1d2c3e4f5a",
	}

	cases := []struct {
		name string
		fe   FrontendEntity
		want map[string]string
	}{
		{
			name: "pinned GitSource in a bound release",
			fe:   pinned,
			want: map[string]string{
				"FORGE_SOURCE_REPO":   "github.com/reliant-labs/reliant",
				"FORGE_SOURCE_REF":    "v1.8.1",
				"FORGE_SOURCE_COMMIT": sha,
				"FORGE_RELEASE":       "20261011.184732-0b1d2c3e4f5a",
			},
		},
		{
			// The developer's checkout stood in for the pin, so naming the pin
			// would misreport what was built. Empty: the build reads its own git.
			name: "local override reports no source",
			fe: func() FrontendEntity {
				fe := pinned
				fe.SourceOverridden = true
				fe.SourceCommit = ""
				return fe
			}(),
			want: map[string]string{
				"FORGE_SOURCE_REPO":   "",
				"FORGE_SOURCE_REF":    "",
				"FORGE_SOURCE_COMMIT": "",
				"FORGE_RELEASE":       "20261011.184732-0b1d2c3e4f5a",
			},
		},
		{
			// An in-repo frontend outside any release still gets every key,
			// empty, so a stale value in the caller's shell cannot leak in.
			name: "in-repo frontend, unbound deploy",
			fe:   FrontendEntity{Name: "web", Type: "nextjs", Path: "frontends/web"},
			want: map[string]string{
				"FORGE_SOURCE_REPO":   "",
				"FORGE_SOURCE_REF":    "",
				"FORGE_SOURCE_COMMIT": "",
				"FORGE_RELEASE":       "",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := frontendBuildEnv(tc.fe)
			for name, want := range tc.want {
				v, ok := got[name]
				if !ok {
					t.Errorf("%s missing from the build env; it must always be present (empty when unknown)", name)
					continue
				}
				if v != want {
					t.Errorf("%s = %q, want %q", name, v, want)
				}
			}
		})
	}
}

// TestFrontendBuildEnv_IdentityBeatsDeclaredEnv: the identity is what forge
// fetched, so an env_vars entry claiming otherwise must not win — a bundle
// reporting a commit it was not built from is worse than one reporting none.
func TestFrontendBuildEnv_IdentityBeatsDeclaredEnv(t *testing.T) {
	be := frontendBuildEnv(FrontendEntity{
		Name:         "reliant-web",
		Type:         "vite",
		Source:       &config.GitSource{Repo: "github.com/reliant-labs/reliant", Ref: "main"},
		SourceCommit: "abc123",
		EnvVars:      []KCLEnvVar{{Name: "FORGE_SOURCE_COMMIT", Value: "deadbeef"}},
	})
	if be["FORGE_SOURCE_COMMIT"] != "abc123" {
		t.Errorf("FORGE_SOURCE_COMMIT = %q, want the fetched commit abc123", be["FORGE_SOURCE_COMMIT"])
	}
}
