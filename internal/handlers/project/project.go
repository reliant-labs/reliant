// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

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
func (s *Service) verifyProjectDirectoryExists(ctx context.Context, userID, projectID, path string) error {
	// Local struct rather than daemon.FileStat: the API tier does not import
	// the daemon package, and only one field is needed.
	var stat struct {
		Exists bool `json:"exists"`
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "fs.stat", map[string]string{"path": path}, &stat); err != nil {
		logging.WarnContext(ctx, "Could not verify project directory via daemon; proceeding",
			"error", err, "path", path, "projectID", projectID)
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

// resolveProjectRepoPath resolves a per-RPC repo_id selector against the
// project's repo set and returns the absolute git checkout path. Rules
// match WorktreeService.resolveRepoPath:
//
//   - empty repoID + project has 0 or 1 repos -> use project.Path as-is
//     (legacy single-repo behavior).
//   - empty repoID + project has 2+ repos -> InvalidArgument; multi-repo
//     callers must specify which repo they're acting on.
//   - non-empty repoID -> look up the repo, ensure it belongs to this
//     project, and return <project.Path>/<repo.relative_path>.
//   - repoID not in the project's repo set -> NotFound.
func (s *Service) resolveProjectRepoPath(ctx context.Context, project *db.Project, repoID string) (string, *core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return "", nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}

	if repoID == "" {
		if len(repos) > 1 {
			return "", nil, svcerr.InvalidArgument("repo_id required in multi-repo projects")
		}
		if len(repos) == 1 {
			return filepath.Join(project.Path, repos[0].RelativePath), repos[0], nil
		}
		return project.Path, nil, nil
	}

	for _, r := range repos {
		if r.ID == repoID {
			return filepath.Join(project.Path, r.RelativePath), r, nil
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

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		// A deadline or a cancellation is the client's to see as such, not a
		// server fault: only an unclassified store failure is Internal.
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}

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
	var checkGitResp struct {
		IsGitRepo bool `json:"is_git_repo"`
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.check_git", map[string]string{"path": project.Path}, &checkGitResp); err != nil {
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
		} else if project != nil && strings.TrimSpace(project.Path) != "" {
			projectID := project.ID
			projectPath := project.Path
			go func() {
				warmupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()

				// Send mcp.ensure_loaded daemon command to warmup MCP servers on the user's machine
				payload, _ := json.Marshal(map[string]string{"project_path": projectPath})
				if _, err := s.deps.Router.SendDaemonCommand(warmupCtx, userID, "mcp.ensure_loaded", payload, 90000); err != nil {
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
