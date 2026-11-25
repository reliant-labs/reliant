package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectproto"
)

// MergeProjects folds one of the caller's projects (source) into another
// (target) in one transaction (store.MergeProjects): every row of the source
// moves onto the target, and the source, left empty, is scheduled for
// deletion as DeleteProject schedules it, so RestoreProject brings it back,
// empty, within the window. A repeat after it succeeded is NotFound: the
// source is scheduled for deletion.
func (s *Service) MergeProjects(
	ctx context.Context,
	req *connect.Request[pb.MergeProjectsRequest],
) (*connect.Response[pb.MergeProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	source, target, err := s.mergeableProjects(ctx, userID, req.Msg.GetSourceProjectId(), req.Msg.GetTargetProjectId())
	if err != nil {
		return nil, err
	}
	// The store re-checks, under the owner's org lock, the owner and org both
	// were admitted on, and what refuses the merge, before it writes.
	result, err := s.deps.Merges.MergeProjects(ctx, core.ProjectMerge{
		SourceID:    source.ID,
		TargetID:    target.ID,
		ActorUserID: userID,
		Decided:     source.Decision(),
		DeleteAfter: s.now().Add(core.ProjectDeleteWindow),
	})
	if err != nil {
		var blockers []core.ProjectMergeBlocker
		if result != nil {
			blockers = result.Blockers
		}
		return nil, projectMergeError(err, blockers, "failed to merge the projects")
	}
	s.convergeTriggerSchedules(ctx, result.MovedTriggerIDs)
	logging.InfoContext(ctx, "[project] merged a project into another", "source_project_id", source.ID,
		"target_project_id", target.ID, "chats", result.Counts.Chats, "worktrees", result.Counts.Worktrees,
		"checkouts", len(result.Counts.Checkouts), "triggers", result.Counts.Triggers, "repos", result.Counts.Repos,
		"other_rows", result.Counts.OtherRows, "source_delete_after", result.DeleteAfter)

	merged, err := s.deps.ProjectRows.GetProject(ctx, target.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("the projects were merged, but the merged project could not be read"))
	}
	return connect.NewResponse(&pb.MergeProjectsResponse{
		Project: projectproto.Project(merged),
		Counts:  s.projectMergeCountsProto(ctx, result.Counts),
	}), nil
}
