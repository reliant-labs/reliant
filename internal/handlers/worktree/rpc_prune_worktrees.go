package worktree

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
)

// PruneWorktrees removes git's records of the project's worktrees whose
// directories no longer exist. Only an explicit request does this.
func (s *Service) PruneWorktrees(
	ctx context.Context,
	req *connect.Request[reliantv1.PruneWorktreesRequest],
) (*connect.Response[reliantv1.PruneWorktreesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	project, err := s.getProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	placement, err := s.placeNewWorktree(ctx, userID, project.ID, nil, nil, req.Msg.DaemonId, "prune")
	if err != nil {
		return nil, err
	}
	target, err := s.withCheckout(ctx, userID, project, placement, "prune its git worktrees")
	if err != nil {
		return nil, err
	}
	placement = target.machine
	refs, byID, err := s.projectRepoRefs(ctx, project, target.checkout)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Pruned []discoveredEntry `json:"pruned"`
		Errors []string          `json:"errors"`
	}
	if err := s.sendToMachine(ctx, userID, placement, "worktree.prune", map[string]any{"repos": refs}, &resp, worktreeDaemonCommandTimeoutMs); err != nil {
		if machinewake.IsWaking(err) {
			return nil, err
		}
		return nil, daemonerr.Error("prune git worktrees", err)
	}
	for _, msg := range resp.Errors {
		logging.WarnContext(ctx, "worktree prune error", "projectID", project.ID, "error", msg)
	}
	if len(resp.Pruned) == 0 && len(resp.Errors) > 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("git error: %s", resp.Errors[0]))
	}
	out := &reliantv1.PruneWorktreesResponse{}
	for _, e := range resp.Pruned {
		out.Pruned = append(out.Pruned, staleToProto(e, byID))
	}
	if len(out.Pruned) == 0 {
		activity.Skip(ctx) // git had no stale records: nothing was pruned
	}
	return connect.NewResponse(out), nil
}
