package project

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// THE LINK RULE as a table: what one forge.yaml in a listed project links to.
func TestLinkProjectApps(t *testing.T) {
	t.Parallel()
	shop := controlplane.HostedApp{Name: "shop", Sources: []controlplane.HostedAppSource{
		{Remote: "github.com/acme/shop", Subpath: ""},
		{Remote: "github.com/acme/shop", Subpath: "web"},
	}}
	local := controlplane.HostedApp{Name: "notes", Sources: []controlplane.HostedAppSource{{Remote: "", Subpath: ""}}}
	apps := []controlplane.HostedApp{shop, local}

	for _, tc := range []struct {
		name string
		ref  projectAppRef
		want reliantv1.ProjectAppLink
	}{
		{"the checkout it deploys from", projectAppRef{Name: "shop", Remote: "github.com/acme/shop"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_SOURCE},
		{"remotes compare case-insensitively", projectAppRef{Name: "shop", Remote: "github.com/Acme/Shop"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_SOURCE},
		{"a recorded subpath", projectAppRef{Name: "shop", Remote: "github.com/acme/shop", Subpath: "web"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_SOURCE},
		{"another subpath of the same repo", projectAppRef{Name: "shop", Remote: "github.com/acme/shop", Subpath: "api"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME},
		{"a fork", projectAppRef{Name: "shop", Remote: "github.com/someone/shop"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME},
		{"a folder with no remote", projectAppRef{Name: "shop"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME},
		{"no remote never matches a local source", projectAppRef{Name: "notes"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME},
		{"never deployed", projectAppRef{Name: "draft", Remote: "github.com/acme/shop"}, reliantv1.ProjectAppLink_PROJECT_APP_LINK_NOT_HOSTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links, _ := linkProjectApps([][]projectAppRef{{tc.ref}}, apps, true)
			got := links[0][0]
			if got.Link != tc.want {
				t.Fatalf("link %v, want %v", got.Link, tc.want)
			}
			if (got.Hosted == nil) != (tc.want == reliantv1.ProjectAppLink_PROJECT_APP_LINK_NOT_HOSTED) {
				t.Fatalf("hosted %+v with link %v: set exactly when hosted", got.Hosted, got.Link)
			}
			if got.Hosted != nil && got.Hosted.Name != tc.ref.Name {
				t.Fatalf("hosted %q for %q", got.Hosted.Name, tc.ref.Name)
			}
		})
	}
}

// An app no listed project names is an app-only row, in control-plane's
// order; a name match of any strength (NAME too) contains it.
func TestLinkProjectApps_Unlinked(t *testing.T) {
	t.Parallel()
	apps := []controlplane.HostedApp{{Name: "zeta"}, {Name: "shop"}, {Name: "alpha"}}
	refs := [][]projectAppRef{{{Name: "shop", Remote: "github.com/fork/shop"}}, {}}
	links, unlinked := linkProjectApps(refs, apps, true)
	if links[0][0].Link != reliantv1.ProjectAppLink_PROJECT_APP_LINK_NAME || len(links[1]) != 0 {
		t.Fatalf("links %+v", links)
	}
	if len(unlinked) != 2 || unlinked[0].Name != "zeta" || unlinked[1].Name != "alpha" {
		t.Fatalf("unlinked %+v, want zeta then alpha", unlinked)
	}
}

// When the org's apps could not be read, nothing is hosted and nothing is
// unlinked: the list cannot tell an app-only row from one it failed to read.
func TestLinkProjectApps_AppsUnknown(t *testing.T) {
	t.Parallel()
	apps := []controlplane.HostedApp{{Name: "shop", Sources: []controlplane.HostedAppSource{{Remote: "github.com/acme/shop"}}}}
	links, unlinked := linkProjectApps([][]projectAppRef{{{Name: "shop", Remote: "github.com/acme/shop"}}}, apps, false)
	if links[0][0].Link != reliantv1.ProjectAppLink_PROJECT_APP_LINK_NOT_HOSTED || links[0][0].Hosted != nil || len(unlinked) != 0 {
		t.Fatalf("links %+v unlinked %+v", links, unlinked)
	}
}

func TestForgeRemote(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://github.com/Acme/Shop.git":          "github.com/Acme/Shop",
		"git@github.com:acme/shop.git":              "github.com/acme/shop",
		"https://user:tok@GitHub.com:443/acme/shop": "github.com/acme/shop",
		"":                     "",
		"/home/me/src/shop":    "",
		"file:///home/me/shop": "",
		"../shop":              "",
		"shop":                 "",
	} {
		if got := forgeRemote(in); got != want {
			t.Errorf("forgeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// The forge.yamls of a project: what the scan recorded, and the legacy root
// name only where no scan says otherwise.
func TestProjectAppRefs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	str := func(s string) *string { return &s }
	legacy := &core.Project{ID: "p", ForgeProjectName: str("legacy"), RemoteURL: str("https://github.com/acme/shop.git")}

	for _, tc := range []struct {
		name    string
		project *core.Project
		repos   []*core.Repo
		apps    map[string][]core.RepoForgeApp
		want    []projectAppRef
	}{
		{
			name:    "an unscanned root repo falls back to the legacy name",
			project: legacy,
			repos:   []*core.Repo{{ID: "r", RelativePath: ""}},
			want:    []projectAppRef{{Name: "legacy", RepoID: "r", Remote: "github.com/acme/shop"}},
		},
		{
			name:    "a scanned root repo is what the scan found",
			project: legacy,
			repos:   []*core.Repo{{ID: "r", RelativePath: "", ForgeAppsScannedAt: &now, RemoteURL: str("git@github.com:acme/other.git")}},
			apps:    map[string][]core.RepoForgeApp{"r": {{RepoID: "r", Subpath: "web", Name: "site"}}},
			want:    []projectAppRef{{Name: "site", RepoID: "r", Subpath: "web", Remote: "github.com/acme/other"}},
		},
		{
			name:    "a scanned repo with no forge.yaml has none",
			project: legacy,
			repos:   []*core.Repo{{ID: "r", ForgeAppsScannedAt: &now}},
		},
		{
			name:    "a partial walk that found the root is not overridden",
			project: legacy,
			repos:   []*core.Repo{{ID: "r"}},
			apps:    map[string][]core.RepoForgeApp{"r": {{RepoID: "r", Subpath: "", Name: "api"}}},
			want:    []projectAppRef{{Name: "api", RepoID: "r", Remote: "github.com/acme/shop"}},
		},
		{
			name:    "a project with no recorded repos keeps its legacy name",
			project: legacy,
			want:    []projectAppRef{{Name: "legacy", Remote: "github.com/acme/shop"}},
		},
		{
			name:    "a nested repo is never the legacy root",
			project: legacy,
			repos:   []*core.Repo{{ID: "root", ForgeAppsScannedAt: &now}, {ID: "sub", RelativePath: "libs/x"}},
			apps:    map[string][]core.RepoForgeApp{"sub": {{RepoID: "sub", Subpath: "", Name: "x"}}},
			want:    []projectAppRef{{Name: "x", RepoID: "sub"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := projectAppRefs(tc.project, tc.repos, tc.apps)
			if len(got) != len(tc.want) {
				t.Fatalf("refs %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("refs %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}
