package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// CreateProjectFromRepo adds a GitHub repository as a project on a daemon.
//
// # Why this is one RPC
//
// The frontend used to drive four calls — ListGitRepos, CloneRepo,
// CreateProject, MarkProjectInstalled — each able to fail on its own. A
// failure in the middle left real garbage behind: a project row with no
// checkout, or a checkout the product had no record of, and nothing
// server-side knew the sequence had ever begun. Making it one call makes the
// server responsible for the whole thing.
//
// # Why reliant owns it rather than the control plane
//
// The state being created is reliant's: `projects` and `project_daemons` live
// in this database, and the control plane has no tables for either (it only
// references chat/workspace ids as billing dimensions). The clone is the one
// step reliant cannot do itself, because the user's GitHub token is held by
// the control plane and deliberately never leaves it — so this calls out for
// that single step, forwarding the caller's own JWT, and keeps ownership of
// everything it persists.
//
// # Why the response says "queued"
//
// The control plane enqueues the clone durably and returns; it does not wait.
// That is what makes a machine which is asleep or still booting a usable
// target. It also means the checkout does not exist when this returns, so the
// project is created in its `installing` state and the response says
// `queued`. Reporting it as cloned is the false success this design exists to
// avoid.
func (s *ProjectService) CreateProjectFromRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateProjectFromRepoRequest],
) (*connect.Response[reliantv1.CreateProjectFromRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)

	cloneURL := strings.TrimSpace(req.Msg.GetCloneUrl())
	daemonID := strings.TrimSpace(req.Msg.GetDaemonId())
	if cloneURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("clone_url is required"))
	}
	if daemonID == "" {
		// Not defaulted here on purpose: with several machines the target is
		// the user's choice, and silently picking one puts their checkout
		// somewhere they did not ask for. The client defaults it visibly.
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}
	if s.controlPlane == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("cloning from GitHub requires a Reliant Cloud account"))
	}

	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		name = repoNameFromCloneURL(cloneURL)
	}
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("could not derive a project name from %q — pass name explicitly", cloneURL))
	}
	branch := strings.TrimSpace(req.Msg.GetBranch())
	path := strings.TrimSpace(req.Msg.GetPath())
	if path == "" {
		path = cloudProjectPath(name)
	}
	if !ospath.IsAbs(path) {
		// ospath, not filepath: the path names a directory on the user's
		// DAEMON, which may be Windows while this server is Linux.
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("project path must be absolute, got: %q", path))
	}

	// The project's id is settled BEFORE the clone is dispatched, because the
	// clone's request id is derived from it: the daemon echoes that id on its
	// failure announcement, and it is the only thing that can find this
	// project's install row again. Settling it here persists nothing — the
	// row is written only after the control plane accepts the clone.
	existing := s.existingProjectForRepo(ctx, userID, path, cloneURL)
	projectID := uuid.New().String()
	if existing != nil {
		projectID = existing.ID
	}
	requestID := cloneRequestID(projectID, daemonID)

	// Ask the control plane to start the clone BEFORE creating the project
	// row. If this is going to be refused — no credential, machine failed,
	// not the caller's daemon — it must fail with nothing persisted, rather
	// than leaving a project the user then has to clean up by hand.
	cloneResult, err := s.controlPlane.CloneRepoOntoDaemon(ctx, bearerToken(req.Header().Get("Authorization")),
		controlplane.CloneRepoRequest{
			DaemonID:  daemonID,
			CloneURL:  cloneURL,
			Branch:    branch,
			Path:      path,
			RequestID: requestID,
		})
	if err != nil {
		// The control plane's codes are already the right ones for the user
		// (FailedPrecondition for a missing GitHub credential, NotFound for
		// someone else's daemon), so pass them through rather than
		// flattening everything to Internal.
		logging.Error("CreateProjectFromRepo: clone dispatch failed",
			"error", err, "daemon_id", daemonID, "clone_url", remoteURLForLog(cloneURL))
		return nil, err
	}
	clonedPath := cloneResult.ClonedPath
	if clonedPath == "" {
		clonedPath = path
	}

	project := existing
	if project == nil {
		project, err = s.createProjectForRepo(ctx, projectID, userID, name, clonedPath, cloneURL, branch)
		if err != nil {
			return nil, err
		}
	}

	var branchPtr *string
	if branch != "" {
		branchPtr = &branch
	}

	// Record where the checkout will live, in the state it is actually in.
	installState := core.ProjectInstallInstalled
	if cloneResult.Queued {
		installState = core.ProjectInstallInstalling
		if err := s.database.UpsertQueuedProjectDaemon(
			ctx, project.ID, daemonID, clonedPath, branchPtr, requestID,
		); err != nil {
			logging.Error("CreateProjectFromRepo: failed to record queued install",
				"error", err, "project_id", project.ID, "daemon_id", daemonID)
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("failed to record project installation"))
		}
	} else if err := s.database.UpsertProjectDaemon(ctx, project.ID, daemonID, clonedPath, branchPtr); err != nil {
		logging.Error("CreateProjectFromRepo: failed to record install",
			"error", err, "project_id", project.ID, "daemon_id", daemonID)
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("failed to record project installation"))
	}

	pd := &core.ProjectDaemon{
		ProjectID:     project.ID,
		DaemonID:      daemonID,
		Path:          clonedPath,
		DefaultBranch: branchPtr,
		InstallState:  installState,
	}

	logging.Info("CreateProjectFromRepo: project added",
		"project_id", project.ID, "daemon_id", daemonID,
		"queued", cloneResult.Queued, "repo", remoteURLForLog(cloneURL))

	return connect.NewResponse(&reliantv1.CreateProjectFromRepoResponse{
		Project:       projectToProto(project),
		ProjectDaemon: projectDaemonToProto(pd),
		Queued:        cloneResult.Queued,
		DaemonName:    cloneResult.DaemonName,
	}), nil
}

// existingProjectForRepo returns the caller's existing project for this repo,
// or nil.
//
// Re-adding a repo the user already has is a normal thing to do — they may be
// putting it on a SECOND machine — so the add is find-or-create rather than an
// error. Matching on remote URL first and path second mirrors how the old
// client chain recovered from AlreadyExists.
func (s *ProjectService) existingProjectForRepo(ctx context.Context, userID, path, cloneURL string) *core.Project {
	if existing, err := s.database.GetProjectByRemoteURLAndUser(ctx, cloneURL, userID); err == nil && existing != nil {
		return existing
	}
	if existing, err := s.database.GetProjectByPathAndUser(ctx, path, userID); err == nil && existing != nil {
		return existing
	}
	return nil
}

// createProjectForRepo creates the project row for a repo being added, under
// the id the clone's request id was already derived from.
func (s *ProjectService) createProjectForRepo(
	ctx context.Context, projectID, userID, name, path, cloneURL, branch string,
) (*core.Project, error) {
	// The store writes these columns verbatim — they have no database
	// default — so a project created without them is dated 0001-01-01 and
	// sorts as the oldest thing the user owns, under a "last active" of
	// never. CreateProject (project.go) has always set them; this path did
	// not.
	now := time.Now().UTC()
	project := &core.Project{
		// The repository requires the caller to assign the id; it does not
		// mint one.
		ID:         projectID,
		Name:       name,
		Path:       path,
		UserID:     userID,
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	if cloneURL != "" {
		project.RemoteURL = &cloneURL
	}
	if branch != "" {
		project.DefaultBranch = &branch
	}
	if err := s.database.CreateProject(ctx, project); err != nil {
		logging.Error("CreateProjectFromRepo: failed to create project",
			"error", err, "name", name, "path", path)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create project"))
	}
	return project, nil
}

// cloneRequestID correlates the queued clone with its project_daemons row.
// The daemon's outcome notification carries no project id, so the row is
// found by this instead.
func cloneRequestID(projectID, daemonID string) string {
	return "clone:" + projectID + ":" + daemonID
}

// repoNameFromCloneURL pulls the bare repository name out of a clone URL —
// "https://github.com/acme/widgets.git" becomes "widgets".
func repoNameFromCloneURL(cloneURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(cloneURL), "/"), ".git")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.TrimSpace(trimmed)
}

// cloudProjectPath is where a cloned repo lands on a managed daemon when the
// caller does not name a path. Mirrors the frontend's cloudPathForRepo so a
// clone started from either side ends up in the same place.
func cloudProjectPath(name string) string {
	return "/home/workspace/projects/" + name
}
