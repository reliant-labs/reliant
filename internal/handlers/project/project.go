// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/projectproto"
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
	repos, err := s.deps.Repos.ListReposByProject(ctx, project.ID)
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
	if _, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("database error"))
	}
	return nil
}

// projectToProto is the project as its owner reads it (projectproto.Project).
func projectToProto(p *db.Project) *reliantv1.Project {
	return projectproto.Project(p)
}
