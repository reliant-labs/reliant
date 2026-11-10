// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// gitCheckBudget bounds GetProject's live check of the project's git status
// on the daemon. The daemon answers it from a short filesystem walk
// (repo.ContainsRepo), well under this; past it, the stored flag is served.
// A var so a test can shrink it.
var gitCheckBudget = 1500 * time.Millisecond

// errNoDaemonRouter is what the daemon-backed paths report in a composition
// without a daemon router (Deps.Router is optional).
var errNoDaemonRouter = errors.New("no daemon router")

// resolveDaemonID returns the daemon SendDaemonCommand would route the
// user's commands to.
func (s *Service) resolveDaemonID(ctx context.Context, userID string) (string, error) {
	if s.deps.Router == nil {
		return "", errNoDaemonRouter
	}
	return s.deps.Router.ResolveDaemonID(ctx, userID)
}

// enqueueDaemonCommand queues a command for delivery when one of the user's
// daemons next connects, and returns how many daemons it was queued for.
func (s *Service) enqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte) (int, error) {
	if s.deps.Router == nil {
		return 0, errNoDaemonRouter
	}
	return s.deps.Router.EnqueueDaemonCommand(ctx, userID, commandType, payload, 30_000)
}

// sendProjectDaemonCommand sends a command to the user's daemon and unmarshals the response.
// If the daemon is offline, it returns connect.CodeUnavailable.
func (s *Service) sendProjectDaemonCommand(ctx context.Context, userID, commandType string, payload interface{}, resp interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if s.deps.Router == nil {
		return fmt.Errorf("daemon command %s: %w", commandType, errNoDaemonRouter)
	}
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// sendToDaemon is sendProjectDaemonCommand pinned to one machine: for a
// command about a checkout that machine holds, whose path means nothing on
// any other.
func (s *Service) sendToDaemon(ctx context.Context, userID, daemonID, commandType string, payload interface{}, resp interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if s.deps.Router == nil {
		return fmt.Errorf("daemon command %s: %w", commandType, errNoDaemonRouter)
	}
	respBytes, err := s.deps.Router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// checkout resolves the machine a command about project runs on and the
// project's path there (projectcheckout.Resolve). projects.path is the
// creating machine's path and is never sent: purpose finishes the error's
// "clone it onto a machine to …" when no machine holds a checkout.
func (s *Service) checkout(ctx context.Context, userID string, project *db.Project, purpose string) (projectcheckout.Target, error) {
	if s.deps.Router == nil {
		return projectcheckout.Target{}, svcerr.WithCause(svcerr.Unavailable("no daemon available"), errNoDaemonRouter)
	}
	return projectcheckout.Resolve(ctx, s.deps.Repo, s.deps.Router, userID, project, purpose)
}

// verifyProjectDirectoryExists refuses to hand back a project whose directory
// the daemon says is gone.
//
// CreateProject's create branch mkdirs the project directory; nothing did that
// for the find branch, so a projects row could outlive its directory and still
// resolve. A run bound to that phantom path starts, executes nothing, and
// reports success — the worst failure shape there is. Recreating the directory
// silently would be worse than refusing: it produces an EMPTY directory that
// looks like the project, so a workflow asked to change existing code finds
// nothing to change and still reports success. Creating a missing directory is
// the right answer for a project that does not exist yet (that is what
// find-or-create means, and the create branch below still does it); for a row
// that already exists it is a stale registry, and the user's intent was "run
// against my project", not "make me a fresh empty one".
//
// Only a daemon that ANSWERED "does not exist" is evidence. An unreachable
// daemon is a different failure — onboarding races daemon provisioning by
// ~10s — so a failed probe falls through rather than inventing a refusal.
//
// The path is the one the caller named, so it is asked of the machine the
// request runs on (requestMachine), never of default resolution: a
// --project-path run on machine A, whose user's default is machine B, was
// told its directory did not exist because B was asked about A's path. When
// no machine can be named, nothing is asked and nothing is refused.
func (s *Service) verifyProjectDirectoryExists(ctx context.Context, userID, projectID, path, daemonID string) error {
	if daemonID == "" {
		logging.InfoContext(ctx, "No machine records this project's checkout at the requested path; not verifying its directory",
			"path", path, "projectID", projectID)
		return nil
	}
	// Local struct rather than daemon.FileStat: the API tier does not import
	// the daemon package, and only one field is needed.
	var stat struct {
		Exists bool `json:"exists"`
	}
	if err := s.sendToDaemon(ctx, userID, daemonID, "fs.stat", map[string]string{"path": path}, &stat); err != nil {
		logging.WarnContext(ctx, "Could not verify project directory via daemon; proceeding",
			"error", err, "path", path, "projectID", projectID, "daemonID", daemonID)
		return nil
	}
	if stat.Exists {
		return nil
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"project %s is registered at %s but that directory does not exist; "+
			"restore the directory or delete the project, then retry",
		projectID, path))
}

// requestMachine is the machine a request about path runs on: the one its
// credentials name (a daemon's own token), else the machine of the user's
// (one that exists and is theirs, connected or not) whose checkout of the
// project is recorded at exactly that path. "" when neither says: the API
// never guesses which disk a path is on.
func (s *Service) requestMachine(ctx context.Context, userID, projectID, path string) string {
	if id := auth.GetDaemonIDFromContext(ctx); id != "" {
		return id
	}
	rows, err := daemonplacement.UserCheckouts(ctx, s.deps.Repo, userID, projectID)
	if err != nil {
		return ""
	}
	want := ospath.Clean(path)
	for _, row := range rows {
		if ospath.Clean(row.Path) == want {
			return row.DaemonID
		}
	}
	return ""
}

// resolveProjectRepo resolves a per-RPC repo_id selector against the
// project's repo set and returns the repo's path RELATIVE to the checkout
// (join it onto a projectcheckout.Target: the checkout's path depends on the
// machine). Rules match WorktreeService.resolveRepoPath:
//
//   - empty repoID + project has 0 or 1 repos -> the checkout root (or its
//     one repo).
//   - empty repoID + project has 2+ repos -> InvalidArgument; multi-repo
//     callers must specify which repo they're acting on.
//   - non-empty repoID -> look up the repo, ensure it belongs to this
//     project, and return its relative_path.
//   - repoID not in the project's repo set -> NotFound.
func (s *Service) resolveProjectRepo(ctx context.Context, project *db.Project, repoID string) (string, *core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return "", nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}

	if repoID == "" {
		if len(repos) > 1 {
			return "", nil, svcerr.InvalidArgument("repo_id required in multi-repo projects")
		}
		if len(repos) == 1 {
			return repos[0].RelativePath, repos[0], nil
		}
		return "", nil, nil
	}

	for _, r := range repos {
		if r.ID == repoID {
			return r.RelativePath, r, nil
		}
	}
	return "", nil, svcerr.WithMessage(svcerr.ErrNotFound, "repo not in project")
}

// projectBelongsToUser checks if a project belongs to a user. A project that
// is missing and one owned by someone else are the same NotFound (the store's
// core.ErrProjectNotFound), so the answer never reveals that a project exists.
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("database error"))
	}
	return nil
}

// projectToProto converts a db.Project to proto Project
func projectToProto(p *db.Project) *reliantv1.Project {
	proto := &reliantv1.Project{
		Id:         p.ID,
		UserId:     p.UserID,
		Name:       p.Name,
		Path:       p.Path,
		IsGitRepo:  p.IsGitRepo,
		IsForge:    p.IsForge,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
		LastActive: p.LastActive.Format(time.RFC3339),
	}
	if p.Description != nil {
		proto.Description = p.Description
	}
	if p.DefaultBranch != nil {
		proto.DefaultBranch = p.DefaultBranch
	}
	if p.RemoteURL != nil {
		proto.RemoteUrl = p.RemoteURL
	}
	proto.OrgId = p.OrgID
	if p.ForgeProjectName != nil {
		proto.ForgeProjectName = p.ForgeProjectName
	}
	return proto
}

// ListProjects lists all projects for the current user
func (s *Service) ListProjects(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectsRequest],
) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 100
	}
	offset := int(req.Msg.Offset)
	if offset < 0 {
		offset = 0
	}

	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{
		UserID: userID,
		Org:    scope,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		// A deadline or a cancellation is the client's to see as such, not a
		// server fault: only an unclassified store failure is Internal.
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	// A project of an org the caller has left is not listed, named org or
	// not: the list shows what GetProject would.
	projects = authz.Visible(ctx, userID, projects, authz.CanReadProject)

	protoProjects := make([]*reliantv1.Project, len(projects))
	for i, p := range projects {
		protoProjects[i] = projectToProto(p)
	}

	return connect.NewResponse(&reliantv1.ListProjectsResponse{
		Projects: protoProjects,
		Total:    int32(len(projects)),
	}), nil
}

// GetProject retrieves a project by ID
func (s *Service) GetProject(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectRequest],
) (*connect.Response[reliantv1.GetProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Live-check git status via daemon and reconcile in BOTH directions so the
	// cached flag can never diverge from what the daemon actually observes on
	// disk — a project that gained a .git (manual `git init`) flips true, and
	// one whose .git was removed flips false. is_git_repo is a cache of the
	// daemon's filesystem observation, nothing more; it is deliberately not
	// derived from repo-count, because a git project the user never registers
	// as a worktree repo still has zero repo rows. When the daemon is
	// unreachable we keep the last-known DB value.
	//
	// The check is bounded by gitCheckBudget: it refreshes a cached flag, so a
	// slow or unreachable daemon must cost the read that budget at most, never
	// the request's whole deadline. Without the bound, GetProject in prod sat
	// at exactly 10.0s (the client's deadline) whenever check_git was slow,
	// and the client then failed the read it had waited for (2026-10-31/11-01:
	// 16 of 31 calls).
	var checkGitResp struct {
		IsGitRepo bool `json:"is_git_repo"`
	}
	// Asked of the machine holding a checkout, about ITS path; with no
	// checkout anywhere the cached flag stands.
	if target, err := s.checkout(ctx, userID, project, "open it"); err != nil {
		logging.WarnContext(ctx, "GetProject: no checkout to live-check git status, using DB value", "error", err, "projectID", project.ID)
	} else if err := s.checkGit(ctx, userID, target, &checkGitResp); err != nil {
		logging.WarnContext(ctx, "GetProject: failed to live-check git status, using DB value", "error", err, "projectID", project.ID)
	} else if err := projectfacts.ReconcileGitRepo(ctx, s.deps.Repo, project, checkGitResp.IsGitRepo); err != nil {
		logging.WarnContext(ctx, "GetProject: failed to persist updated git status", "error", err, "projectID", project.ID)
	}

	analyticsClient := analytics.ForUser(ctx, userID)
	analyticsClient.TrackProjectOpened(analytics.ProjectOpenedMetrics{
		ProjectID: project.ID,
		IsGitRepo: project.IsGitRepo,
	})

	return connect.NewResponse(&reliantv1.GetProjectResponse{
		Project: projectToProto(project),
	}), nil
}

// checkGit asks target's machine whether its checkout is a git repository,
// within gitCheckBudget.
func (s *Service) checkGit(ctx context.Context, userID string, target projectcheckout.Target, out any) error {
	checkCtx, cancel := context.WithTimeout(ctx, gitCheckBudget)
	defer cancel()
	return s.sendToDaemon(checkCtx, userID, target.DaemonID, "project.check_git", map[string]string{"path": target.Path}, out)
}

// UpdateProject updates a project
func (s *Service) UpdateProject(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectRequest],
) (*connect.Response[reliantv1.UpdateProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Update fields
	if req.Msg.Name != nil {
		project.Name = *req.Msg.Name
	}
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}
	if req.Msg.DefaultBranch != nil {
		project.DefaultBranch = req.Msg.DefaultBranch
	}

	project.UpdatedAt = time.Now().UTC()
	project.LastActive = time.Now().UTC()

	if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectResponse{
		Project: projectToProto(project),
	}), nil
}

// DeleteProject deletes a project
func (s *Service) DeleteProject(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteProjectRequest],
) (*connect.Response[reliantv1.DeleteProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	if err := s.deps.Repo.DeleteProject(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete project"))
	}

	return connect.NewResponse(&reliantv1.DeleteProjectResponse{
		Success: true,
		Message: "Project deleted successfully",
	}), nil
}

// TouchProject updates the project's last_active timestamp
func (s *Service) TouchProject(
	ctx context.Context,
	req *connect.Request[reliantv1.TouchProjectRequest],
) (*connect.Response[reliantv1.TouchProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	if err := s.deps.Repo.TouchProject(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project activity"))
	}

	if s.deps.Router != nil {
		project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			logging.WarnContext(ctx, "TouchProject: failed to load project for MCP warmup", "projectID", req.Msg.ProjectId, "error", err)
		} else if project != nil {
			// Warm up on the machine holding the checkout, at its path there.
			// A project no machine holds has nothing to warm up.
			target, err := s.checkout(ctx, userID, project, "load its MCP servers")
			if err != nil {
				logging.InfoContext(ctx, "TouchProject: no checkout to warm MCP servers on", "projectID", project.ID, "error", err)
				target = projectcheckout.Target{}
			}
			projectID := project.ID
			go func() {
				if target.DaemonID == "" {
					return
				}
				warmupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()

				// Send mcp.ensure_loaded daemon command to warmup MCP servers on the user's machine
				payload, _ := json.Marshal(map[string]string{"project_path": target.Path})
				if _, err := target.Send(warmupCtx, s.deps.Router, userID, "mcp.ensure_loaded", payload, 90000); err != nil {
					logging.WarnContext(ctx, "Project MCP warmup via daemon failed", "projectID", projectID, "error", err)
				}
			}()
		}
	}

	return connect.NewResponse(&reliantv1.TouchProjectResponse{
		Success: true,
		Message: "Project activity updated",
	}), nil
}

// GetProjectMetadata returns detailed project metadata
func (s *Service) GetProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectMetadataRequest],
) (*connect.Response[reliantv1.GetProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	resp := &reliantv1.GetProjectMetadataResponse{
		ProjectId:  project.ID,
		Name:       project.Name,
		Path:       project.Path,
		IsGitRepo:  project.IsGitRepo,
		CreatedAt:  project.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  project.UpdatedAt.Format(time.RFC3339),
		LastActive: project.LastActive.Format(time.RFC3339),
	}
	if project.Description != nil {
		resp.Description = project.Description
	}
	if project.DefaultBranch != nil {
		resp.DefaultBranch = project.DefaultBranch
	}

	return connect.NewResponse(resp), nil
}

// UpdateProjectMetadata updates project metadata
func (s *Service) UpdateProjectMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateProjectMetadataRequest],
) (*connect.Response[reliantv1.UpdateProjectMetadataResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Update fields
	if req.Msg.Description != nil {
		project.Description = req.Msg.Description
	}

	if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update project"))
	}

	return connect.NewResponse(&reliantv1.UpdateProjectMetadataResponse{
		Project: projectToProto(project),
	}), nil
}
