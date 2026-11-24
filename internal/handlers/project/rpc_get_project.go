package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/projectfacts"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// GetProject retrieves a project by ID
func (s *Service) GetProject(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectRequest],
) (*connect.Response[reliantv1.GetProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		// A viewer of a shared project reads its card, in the viewer's shape
		// (no path) and without the git check, which would reach the owner's
		// machine.
		viewing := authz.WithViewerReads(ctx)
		if shared, getErr := s.deps.ProjectRows.GetProject(viewing, req.Msg.ProjectId); getErr == nil {
			// Not the owner here, so a readable answer is a viewer's: it is
			// shaped for one whatever the decision says.
			if _, denied := authz.ReadProjectAccess(viewing, userID, shared); denied == nil {
				return connect.NewResponse(&reliantv1.GetProjectResponse{Project: projectproto.ForViewer(projectToProto(shared))}), nil
			}
		}
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
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
