package project

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// projectsWithAppsLimit bounds each half of the list: the caller's own
// projects in the org, and the ones shared with them in it. The Projects
// page does not page; a person with more than this many of either sees the
// most recently active.
const projectsWithAppsLimit = 500

// ListProjectsWithApps is the Projects list: every project the caller may
// read in one org, each with the forge apps its repos hold linked to the
// org's hosted apps (project_app_link.go), and the org's apps no listed
// project contains.
//
// Authorization is two halves that never trust each other: the projects are
// exactly what ListProjects and ListSharedProjects show the caller (owner and
// viewer rules, org membership), and the apps are what control-plane shows
// the caller's own bearer (deploy:read). The link is computed here, between
// the two, by forge name in the org; no project id from control-plane or the
// request takes part. A control plane that refuses or fails never fails the
// list: its projects still list, with hosted_apps_state saying why their
// apps are unlinked.
func (s *Service) ListProjectsWithApps(
	ctx context.Context,
	req *connect.Request[pb.ListProjectsWithAppsRequest],
) (*connect.Response[pb.ListProjectsWithAppsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	scope, orgID, err := s.projectsWithAppsOrg(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// The org's apps are read while the database is: the two are
	// independent until the link.
	type appsResult struct {
		apps  []controlplane.HostedApp
		state pb.HostedAppsState
	}
	appsDone := make(chan appsResult, 1)
	bearer := auth.BearerToken(req.Header().Get("Authorization"))
	go func() {
		apps, state := s.hostedApps(ctx, bearer, orgID)
		appsDone <- appsResult{apps: apps, state: state}
	}()

	listed, err := s.projectsWithAppsListed(ctx, userID, scope, orgID)
	if err != nil {
		<-appsDone
		return nil, err
	}
	ids := make([]string, len(listed))
	var ownedIDs []string
	for i, l := range listed {
		ids[i] = l.project.ID
		if !l.viewer {
			ownedIDs = append(ownedIDs, l.project.ID)
		}
	}
	repos, err := s.deps.Repo.ListReposByProjects(ctx, ids)
	if err != nil {
		<-appsDone
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the projects' repos"))
	}
	forgeApps, err := s.deps.Repo.ListRepoForgeAppsByProjects(ctx, ids)
	if err != nil {
		<-appsDone
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the projects' forge apps"))
	}
	// Where a project is checked out is its owner's to see, never a viewer's.
	checkouts, err := s.deps.Repo.ListProjectDaemonsForProjects(ctx, ownedIDs)
	if err != nil {
		<-appsDone
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the projects' checkouts"))
	}

	reposByProject := make(map[string][]*core.Repo, len(listed))
	for _, r := range repos {
		reposByProject[r.ProjectID] = append(reposByProject[r.ProjectID], r)
	}
	appsByRepo := make(map[string][]core.RepoForgeApp, len(repos))
	for _, a := range forgeApps {
		appsByRepo[a.RepoID] = append(appsByRepo[a.RepoID], a)
	}
	checkoutsByProject := make(map[string][]*core.ProjectDaemon, len(ownedIDs))
	for _, c := range checkouts {
		checkoutsByProject[c.ProjectID] = append(checkoutsByProject[c.ProjectID], c)
	}
	refs := make([][]projectAppRef, len(listed))
	for i, l := range listed {
		refs[i] = projectAppRefs(l.project, reposByProject[l.project.ID], appsByRepo)
	}

	hosted := <-appsDone
	// Only an OK read takes part in the link. On NO_ACCESS or UNAVAILABLE
	// the apps are never read (hostedApps drops them too): every ref is the
	// same NOT_HOSTED whatever the org deploys, no ProjectApp carries hosted
	// metadata, and no app-only row is listed, so a caller without
	// deploy:read learns nothing about the org's apps.
	links, unlinked := linkProjectApps(refs, hosted.apps, hosted.state == pb.HostedAppsState_HOSTED_APPS_STATE_OK)

	out := &pb.ListProjectsWithAppsResponse{
		OrgId:           orgID,
		Projects:        make([]*pb.ProjectWithApps, 0, len(listed)),
		UnlinkedApps:    make([]*pb.HostedApp, 0, len(unlinked)),
		HostedAppsState: hosted.state,
	}
	names := s.newMachineNames()
	for i, l := range listed {
		entry := &pb.ProjectWithApps{
			Repos:     []*pb.ProjectRepoSummary{},
			Apps:      make([]*pb.ProjectApp, 0, len(refs[i])),
			Checkouts: []*pb.ProjectCheckout{},
		}
		if l.viewer {
			entry.Project = projectproto.ForViewer(projectproto.Project(l.project))
		} else {
			entry.Project = projectToProto(l.project)
		}
		for _, r := range reposByProject[l.project.ID] {
			summary := &pb.ProjectRepoSummary{Id: r.ID, Name: r.Name, RelativePath: r.RelativePath}
			if !l.viewer {
				summary.Remote = repoRemote(l.project, r)
			}
			entry.Repos = append(entry.Repos, summary)
		}
		for j, ref := range refs[i] {
			app := &pb.ProjectApp{Name: ref.Name, RepoId: ref.RepoID, Subpath: ref.Subpath, Link: links[i][j].Link}
			if links[i][j].Hosted != nil {
				app.Hosted = hostedAppToProto(links[i][j].Hosted)
			}
			entry.Apps = append(entry.Apps, app)
		}
		if !l.viewer {
			for _, c := range checkoutsByProject[l.project.ID] {
				entry.Checkouts = append(entry.Checkouts, &pb.ProjectCheckout{
					DaemonId:     c.DaemonID,
					DaemonName:   names.name(ctx, c.DaemonID),
					InstallState: installStateToProto(c.InstallState),
				})
			}
		}
		out.Projects = append(out.Projects, entry)
	}
	for i := range unlinked {
		out.UnlinkedApps = append(out.UnlinkedApps, hostedAppToProto(&unlinked[i]))
	}
	return connect.NewResponse(out), nil
}

// listedProject is one project of the list, and whether the caller reads it
// as a viewer of a project shared with them rather than as its owner.
type listedProject struct {
	project *core.Project
	viewer  bool
}

// projectsWithAppsOrg is the org the list is of, and its filter: the named
// org, which the caller must be a live member of (PermissionDenied
// otherwise), else their default org. A deployment with no orgs, or a
// caller with none, is no filter and org "": every project they own, and no
// apps (an app is an org's).
func (s *Service) projectsWithAppsOrg(ctx context.Context, userID, requested string) (*core.OrgScope, string, error) {
	if s.deps.Orgs == nil {
		return nil, "", nil
	}
	requested = strings.TrimSpace(requested)
	// A machine credential's list is its own org's, named or not
	// (orgs.ListScope); a person's unnamed list is their default org's.
	if p, machine := auth.MachineTokenFromContext(ctx); requested == "" && (!machine || p.OrgID == "") {
		def, err := s.deps.Orgs.DefaultOrg(ctx, userID)
		if err != nil {
			return nil, "", orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
		}
		requested = def
	}
	scope, err := s.listScope(ctx, userID, requested)
	if err != nil || scope == nil {
		return nil, "", err
	}
	return scope, scope.OrgID, nil
}

// projectsWithAppsListed is every project the caller may read in the org:
// their own (ListProjects' rows), then the ones others shared with the org
// (ListSharedProjects' rows), most recently active first. Each is decided
// ONCE, with ReadProjectAccess, and that Access alone says whether it is
// listed in the owner's shape or a viewer's.
func (s *Service) projectsWithAppsListed(ctx context.Context, userID string, scope *core.OrgScope, orgID string) ([]listedProject, error) {
	candidates, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Org: scope, Limit: projectsWithAppsLimit})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	// Sharing is between people in an org: a machine credential reads its
	// own projects only, as ListSharedProjects refuses it.
	if orgID != "" && auth.RequirePerson(ctx, "read what others shared with your organization") == nil {
		shared, err := s.deps.Repo.ListSharedProjects(ctx, core.SharedProjectFilters{
			OrgID: orgID, ViewerUserID: userID, Limit: projectsWithAppsLimit,
		})
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list shared projects"))
		}
		candidates = append(candidates, shared...)
	}
	viewing := authz.WithViewerReads(ctx)
	listed := make([]listedProject, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		if seen[p.ID] {
			continue
		}
		// A project of an org the caller has left, or one not shared with
		// them, is not listed: the list shows what GetProject would.
		access, err := authz.ReadProjectAccess(viewing, userID, p)
		if err != nil {
			continue
		}
		seen[p.ID] = true
		listed = append(listed, listedProject{project: p, viewer: access.Viewer()})
	}
	sort.SliceStable(listed, func(i, j int) bool {
		a, b := listed[i].project, listed[j].project
		if !a.LastActive.Equal(b.LastActive) {
			return a.LastActive.After(b.LastActive)
		}
		return a.ID < b.ID
	})
	return listed, nil
}

// hostedApps reads the org's apps as the caller. It never fails the list:
// a refusal is NO_ACCESS, anything else UNAVAILABLE (logged).
func (s *Service) hostedApps(ctx context.Context, bearer, orgID string) ([]controlplane.HostedApp, pb.HostedAppsState) {
	if s.deps.Apps == nil || orgID == "" {
		return nil, pb.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE
	}
	apps, err := s.deps.Apps.ListHostedApps(ctx, bearer, orgID)
	switch {
	case err == nil:
		return apps, pb.HostedAppsState_HOSTED_APPS_STATE_OK
	case errors.Is(err, controlplane.ErrHostedAppsDenied):
		return nil, pb.HostedAppsState_HOSTED_APPS_STATE_NO_ACCESS
	default:
		logging.WarnContext(ctx, "could not read the org's hosted apps; listing projects without them",
			"error", err, "org_id", orgID)
		return nil, pb.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE
	}
}

// projectAppRefs is every forge.yaml in the project's repos, by repo and
// then subpath: what the daemon's scan recorded (repo_forge_apps). The root
// repo of a project no daemon has scanned yet falls back to the project's
// legacy forge_project_name, read from the root forge.yaml, at subpath "";
// so does a project that predates its repos being recorded at all.
func projectAppRefs(p *core.Project, repos []*core.Repo, appsByRepo map[string][]core.RepoForgeApp) []projectAppRef {
	legacy := ""
	if p.ForgeProjectName != nil {
		legacy = strings.TrimSpace(*p.ForgeProjectName)
	}
	var refs []projectAppRef
	hasRoot := false
	for _, r := range repos {
		remote := repoRemote(p, r)
		root := normalizeSubpath(r.RelativePath) == ""
		hasRoot = hasRoot || root
		recorded := appsByRepo[r.ID]
		atRoot := false
		for _, a := range recorded {
			atRoot = atRoot || normalizeSubpath(a.Subpath) == ""
		}
		if root && r.ForgeAppsScannedAt == nil && !atRoot && legacy != "" {
			refs = append(refs, projectAppRef{Name: legacy, RepoID: r.ID, Subpath: "", Remote: remote})
		}
		for _, a := range recorded {
			refs = append(refs, projectAppRef{Name: a.Name, RepoID: r.ID, Subpath: normalizeSubpath(a.Subpath), Remote: remote})
		}
	}
	if !hasRoot && legacy != "" {
		remote := ""
		if p.RemoteURL != nil {
			remote = forgeRemote(*p.RemoteURL)
		}
		refs = append([]projectAppRef{{Name: legacy, Subpath: "", Remote: remote}}, refs...)
	}
	return refs
}

// repoRemote is the repo's canonical remote: its own, or, for the root repo
// of a project that recorded its remote only on the project, the project's.
func repoRemote(p *core.Project, r *core.Repo) string {
	if r.RemoteURL != nil {
		if c := forgeRemote(*r.RemoteURL); c != "" {
			return c
		}
	}
	if normalizeSubpath(r.RelativePath) == "" && p.RemoteURL != nil {
		return forgeRemote(*p.RemoteURL)
	}
	return ""
}

// hostedAppToProto is an org's app as the wire carries it.
func hostedAppToProto(a *controlplane.HostedApp) *pb.HostedApp {
	out := &pb.HostedApp{
		Name:          a.Name,
		CreatedAt:     rfc3339(a.CreatedAt),
		Environments:  append([]string{}, a.Environments...),
		HasStaticSite: a.HasStaticSite,
		Sources:       make([]*pb.HostedAppSource, 0, len(a.Sources)),
		Warning:       a.Warning,
	}
	if a.LastDeployedAt != nil {
		at := rfc3339(*a.LastDeployedAt)
		out.LastDeployedAt = &at
	}
	for _, src := range a.Sources {
		out.Sources = append(out.Sources, &pb.HostedAppSource{
			Remote:     src.Remote,
			Subpath:    src.Subpath,
			Commit:     src.Commit,
			LastSeenAt: rfc3339(src.LastSeenAt),
		})
	}
	return out
}

// rfc3339 is t on the wire; "" for the zero time.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
