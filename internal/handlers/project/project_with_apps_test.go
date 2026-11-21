package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// fakeHostedApps is control-plane's ListApps: it answers apps or err, and
// records the bearer and org each call presented.
type fakeHostedApps struct {
	apps  []controlplane.HostedApp
	err   error
	calls []string
}

func (f *fakeHostedApps) ListHostedApps(_ context.Context, jwt, orgID string) ([]controlplane.HostedApp, error) {
	f.calls = append(f.calls, jwt+"@"+orgID)
	return f.apps, f.err
}

// withAppsFixture is viewertest's org (owner, viewer, outsider) plus the
// projects the Projects list joins with the org's apps.
type withAppsFixture struct {
	*viewertest.Fixture
	// SecretID is the owner's PRIVATE project in the org; its forge app
	// "secret-app" is hosted and its source remote points at it.
	SecretID string
	// ForkID is the viewer's own project: a fork of the shared one.
	ForkID string
	// ElsewhereID is the viewer's project in another org.
	ElsewhereID string
	// LegacyID is the viewer's project no daemon has scanned, known only by
	// its root forge.yaml name.
	LegacyID string
	// Apps is the org's apps, as control-plane lists them.
	Apps []controlplane.HostedApp
}

func newWithAppsFixture(t *testing.T) (*withAppsFixture, *db.Repo) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := &withAppsFixture{Fixture: viewertest.New(t, repo)}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	suffix := strings.TrimPrefix(f.ProjectID, "shared-project-")
	f.SecretID, f.ForkID, f.ElsewhereID, f.LegacyID = "secret-"+suffix, "fork-"+suffix, "elsewhere-"+suffix, "legacy-"+suffix
	str := func(s string) *string { return &s }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	project := func(id, user, org, name string, lastActive time.Time, forgeName *string) {
		t.Helper()
		must(repo.CreateProject(ctx, &db.Project{
			ID: id, UserID: user, OrgID: &org, Name: name, Path: "/home/" + user + "/" + id,
			ForgeProjectName: forgeName, IsForge: forgeName != nil,
			CreatedAt: now, UpdatedAt: now, LastActive: lastActive,
		}))
	}
	repoWithApps := func(projectID, remote string, apps ...core.RepoForgeApp) {
		t.Helper()
		id := "repo-" + projectID
		must(repo.CreateRepo(ctx, &core.Repo{ID: id, ProjectID: projectID, Name: projectID, RelativePath: "",
			RemoteURL: str(remote), CreatedAt: now, UpdatedAt: now}))
		must(repo.SetRepoForgeApps(ctx, id, apps, true))
	}

	// The shared project (viewertest's, the owner's): three forge.yamls.
	repoWithApps(f.ProjectID, "https://github.com/Acme/Shop.git",
		core.RepoForgeApp{Subpath: "", Name: "shop"},       // deployed from here: SOURCE
		core.RepoForgeApp{Subpath: "web", Name: "site"},    // deployed from elsewhere: NAME
		core.RepoForgeApp{Subpath: "tools", Name: "draft"}) // never deployed: NOT_HOSTED
	must(repo.UpsertProjectDaemon(ctx, f.ProjectID, "owner-laptop", "/home/owner/shared", nil))

	project(f.SecretID, f.OwnerID, viewertest.OrgID, "Secret project", now.Add(-time.Hour), nil)
	repoWithApps(f.SecretID, "git@github.com:acme/secret-repo.git", core.RepoForgeApp{Subpath: "", Name: "secret-app"})

	project(f.ForkID, f.ViewerID, viewertest.OrgID, "My fork", now.Add(time.Minute), nil)
	repoWithApps(f.ForkID, "git@github.com:viewer/shop.git", core.RepoForgeApp{Subpath: "", Name: "shop"})

	project(f.ElsewhereID, f.ViewerID, viewertest.OtherOrgID, "Globex thing", now.Add(time.Hour), nil)
	repoWithApps(f.ElsewhereID, "https://github.com/globex/thing.git", core.RepoForgeApp{Subpath: "", Name: "thing"})

	project(f.LegacyID, f.ViewerID, viewertest.OrgID, "Old one", now.Add(-2*time.Hour), str("legacy-app"))

	f.Apps = []controlplane.HostedApp{
		{Name: "shop", CreatedAt: now, Environments: []string{"prod"}, Sources: []controlplane.HostedAppSource{
			{Remote: "github.com/acme/shop", Subpath: "", Commit: "abc", LastSeenAt: now},
		}},
		{Name: "site", CreatedAt: now, HasStaticSite: true, Sources: []controlplane.HostedAppSource{
			{Remote: "github.com/acme/marketing", Subpath: "", LastSeenAt: now},
		}},
		{Name: "secret-app", CreatedAt: now, Sources: []controlplane.HostedAppSource{
			{Remote: "github.com/acme/secret-repo", Subpath: "", LastSeenAt: now},
		}},
		{Name: "legacy-app", CreatedAt: now},
	}
	return f, repo
}

// listWithApps calls ListProjectsWithApps as ctx's caller with bearer.
func listWithApps(t *testing.T, svc *Service, ctx context.Context, bearer string, org *string) (*reliantv1.ListProjectsWithAppsResponse, error) {
	t.Helper()
	req := connect.NewRequest(&reliantv1.ListProjectsWithAppsRequest{OrgId: org})
	req.Header().Set("Authorization", "Bearer "+bearer)
	resp, err := svc.ListProjectsWithApps(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func byProjectID(resp *reliantv1.ListProjectsWithAppsResponse) map[string]*reliantv1.ProjectWithApps {
	out := map[string]*reliantv1.ProjectWithApps{}
	for _, p := range resp.GetProjects() {
		out[p.GetProject().GetId()] = p
	}
	return out
}

func appLinks(p *reliantv1.ProjectWithApps) string {
	var parts []string
	for _, a := range p.GetApps() {
		parts = append(parts, fmt.Sprintf("%s@%s:%s", a.GetName(), a.GetSubpath(),
			strings.TrimPrefix(a.GetLink().String(), "PROJECT_APP_LINK_")))
	}
	return strings.Join(parts, ",")
}

// A viewer's list: their own projects and the one shared with them, in the
// org only; another member's private project never appears and never links
// an app; the shared project comes in the viewer's shape.
func TestListProjectsWithApps_Viewer(t *testing.T) {
	f, repo := newWithAppsFixture(t)
	apps := &fakeHostedApps{apps: f.Apps}
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership, Apps: apps}))
	org := viewertest.OrgID

	resp, err := listWithApps(t, svc, f.Viewer, "viewer-token", &org)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps.calls) != 1 || apps.calls[0] != "viewer-token@"+viewertest.OrgID {
		t.Fatalf("control-plane calls %v, want the viewer's own bearer in the org", apps.calls)
	}
	if resp.GetOrgId() != viewertest.OrgID || resp.GetHostedAppsState() != reliantv1.HostedAppsState_HOSTED_APPS_STATE_OK {
		t.Fatalf("org %q state %v", resp.GetOrgId(), resp.GetHostedAppsState())
	}

	var order []string
	for _, p := range resp.GetProjects() {
		order = append(order, p.GetProject().GetId())
	}
	if want := []string{f.ForkID, f.ProjectID, f.LegacyID}; strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("projects %v, want %v (most recently active first; not the private one, not another org's)", order, want)
	}
	got := byProjectID(resp)

	shared := got[f.ProjectID]
	if !shared.GetProject().GetViewerAccess() || shared.GetProject().GetPath() != "" {
		t.Fatalf("shared project for a viewer: viewer_access %v path %q", shared.GetProject().GetViewerAccess(), shared.GetProject().GetPath())
	}
	if len(shared.GetCheckouts()) != 0 {
		t.Fatalf("a viewer sees the owner's checkouts: %v", shared.GetCheckouts())
	}
	for _, r := range shared.GetRepos() {
		if r.GetRemote() != "" || r.GetName() == "" {
			t.Fatalf("viewer's repo summary %+v: want name and path, no remote", r)
		}
	}
	if links := appLinks(shared); links != "shop@:SOURCE,draft@tools:NOT_HOSTED,site@web:NAME" {
		t.Fatalf("shared project's apps %s", links)
	}
	if links := appLinks(got[f.ForkID]); links != "shop@:NAME" {
		t.Fatalf("a fork's apps %s, want a name match only", links)
	}
	if links := appLinks(got[f.LegacyID]); links != "legacy-app@:NAME" {
		t.Fatalf("an unscanned project's apps %s, want its legacy forge name", links)
	}

	var unlinked []string
	for _, a := range resp.GetUnlinkedApps() {
		unlinked = append(unlinked, a.GetName())
	}
	if strings.Join(unlinked, ",") != "secret-app" {
		t.Fatalf("unlinked %v, want the private project's app alone", unlinked)
	}
	// Nothing about the private project leaks: not its id, name or path.
	body, err := protojson.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{f.SecretID, "Secret project", "/home/" + f.OwnerID, f.ElsewhereID, "Globex thing"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("the viewer's list carries %q:\n%s", leak, body)
		}
	}
}

// The owner's list, with no org named, is their default org's: their own
// projects with remotes and checkouts.
func TestListProjectsWithApps_OwnerDefaultOrg(t *testing.T) {
	f, repo := newWithAppsFixture(t)
	apps := &fakeHostedApps{apps: f.Apps}
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership, Apps: apps}))

	resp, err := listWithApps(t, svc, f.Owner, "owner-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOrgId() != viewertest.OrgID || len(apps.calls) != 1 || apps.calls[0] != "owner-token@"+viewertest.OrgID {
		t.Fatalf("org %q calls %v, want the owner's default org", resp.GetOrgId(), apps.calls)
	}
	got := byProjectID(resp)
	if len(got) != 2 || got[f.ProjectID] == nil || got[f.SecretID] == nil {
		t.Fatalf("owner's projects %v", got)
	}
	shared := got[f.ProjectID]
	if shared.GetProject().GetViewerAccess() || shared.GetProject().GetPath() == "" {
		t.Fatalf("owner reads their own project as a viewer: %+v", shared.GetProject())
	}
	if len(shared.GetRepos()) != 1 || shared.GetRepos()[0].GetRemote() != "github.com/Acme/Shop" {
		t.Fatalf("owner's repos %+v, want the canonical remote", shared.GetRepos())
	}
	if len(shared.GetCheckouts()) != 1 || shared.GetCheckouts()[0].GetDaemonId() != "owner-laptop" ||
		shared.GetCheckouts()[0].GetInstallState() != reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED {
		t.Fatalf("owner's checkouts %+v", shared.GetCheckouts())
	}
	if links := appLinks(got[f.SecretID]); links != "secret-app@:SOURCE" {
		t.Fatalf("owner's private project's apps %s", links)
	}
	if h := got[f.SecretID].GetApps()[0].GetHosted(); h.GetName() != "secret-app" || len(h.GetSources()) != 1 {
		t.Fatalf("hosted %+v", h)
	}
	var unlinked []string
	for _, a := range resp.GetUnlinkedApps() {
		unlinked = append(unlinked, a.GetName())
	}
	if strings.Join(unlinked, ",") != "legacy-app" {
		t.Fatalf("owner's unlinked %v, want the app only the viewer's project holds", unlinked)
	}
}

// An org the caller is not a member of is refused, named or forged, before
// anything is read from control-plane.
func TestListProjectsWithApps_NotAMember(t *testing.T) {
	f, repo := newWithAppsFixture(t)
	apps := &fakeHostedApps{apps: f.Apps}
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership, Apps: apps}))
	acme, globex, forged := viewertest.OrgID, viewertest.OtherOrgID, "org-that-does-not-exist"

	for name, call := range map[string]func() error{
		"the outsider names the org":   func() error { _, err := listWithApps(t, svc, f.Outsider, "x", &acme); return err },
		"the viewer names another org": func() error { _, err := listWithApps(t, svc, f.Viewer, "x", &globex); return err },
		"a forged org id":              func() error { _, err := listWithApps(t, svc, f.Viewer, "x", &forged); return err },
	} {
		if err := call(); svcerr.Code(err) != connect.CodePermissionDenied {
			t.Errorf("%s: %v, want PermissionDenied", name, err)
		}
	}
	if len(apps.calls) != 0 {
		t.Fatalf("control-plane was asked %v for a refused list", apps.calls)
	}
}

// Control-plane refusing or failing never fails the list: projects still
// list, their apps unlinked, and the state says why.
func TestListProjectsWithApps_HostedAppsUnreadable(t *testing.T) {
	f, repo := newWithAppsFixture(t)
	org := viewertest.OrgID
	for _, tc := range []struct {
		name string
		apps HostedApps
		want reliantv1.HostedAppsState
	}{
		{"no deploy:read", &fakeHostedApps{err: fmt.Errorf("%w: permission_denied", controlplane.ErrHostedAppsDenied)}, reliantv1.HostedAppsState_HOSTED_APPS_STATE_NO_ACCESS},
		// A refusal that came with apps anyway (a misbehaving adapter or
		// control plane) still discloses none of them: the denied result
		// is never read, so no hosted metadata, no app-only rows, and a
		// link that is the same NOT_HOSTED whether or not the app exists.
		{"no deploy:read, apps returned anyway", &fakeHostedApps{apps: f.Apps, err: fmt.Errorf("%w: permission_denied", controlplane.ErrHostedAppsDenied)}, reliantv1.HostedAppsState_HOSTED_APPS_STATE_NO_ACCESS},
		{"unreachable, apps returned anyway", &fakeHostedApps{apps: f.Apps, err: errors.New("502 bad gateway")}, reliantv1.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE},
		{"unreachable", &fakeHostedApps{err: errors.New("dial tcp: connection refused")}, reliantv1.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE},
		{"no control plane", nil, reliantv1.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership, Apps: tc.apps}))
			resp, err := listWithApps(t, svc, f.Viewer, "viewer-token", &org)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetHostedAppsState() != tc.want || len(resp.GetProjects()) != 3 || len(resp.GetUnlinkedApps()) != 0 {
				t.Fatalf("state %v, %d projects, %d unlinked", resp.GetHostedAppsState(), len(resp.GetProjects()), len(resp.GetUnlinkedApps()))
			}
			if links := appLinks(byProjectID(resp)[f.ProjectID]); links != "shop@:NOT_HOSTED,draft@tools:NOT_HOSTED,site@web:NOT_HOSTED" {
				t.Fatalf("apps %s, want every one NOT_HOSTED", links)
			}
			for _, p := range resp.GetProjects() {
				for _, a := range p.GetApps() {
					if a.GetHosted() != nil || a.GetLink() != reliantv1.ProjectAppLink_PROJECT_APP_LINK_NOT_HOSTED {
						t.Fatalf("project %s app %s: link %v hosted %v, want NOT_HOSTED and no hosted metadata", p.GetProject().GetId(), a.GetName(), a.GetLink(), a.GetHosted())
					}
				}
			}
		})
	}
}
