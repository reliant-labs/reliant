package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// RestoreProject cancels a project's scheduled deletion (project_delete.go):
// it shows again, and the triggers the delete paused are turned back on.
// Whoever may delete the project may restore it.
func (s *Service) RestoreProject(
	ctx context.Context,
	req *connect.Request[pb.RestoreProjectRequest],
) (*connect.Response[pb.RestoreProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, authority, err := s.deletableProject(ctx, userID, req.Msg.GetProjectId(), "restore")
	if err != nil {
		return nil, err
	}
	// Re-checked under the owner's org lock: a project that moved org since
	// the authority above was decided is refused.
	resumed, err := s.deps.Repo.RestoreProject(ctx, project.ID, project.Decision())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to restore project"))
	}
	s.convergeTriggerSchedules(ctx, resumed)
	if project.DeleteAfter != nil {
		logging.InfoContext(ctx, "[project] restored a project scheduled for deletion", "project_id", project.ID,
			"restored_by", userID, "triggers_resumed", len(resumed))
	}
	restored, err := s.deps.Repo.GetProject(ctx, project.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the restored project"))
	}
	return connect.NewResponse(&pb.RestoreProjectResponse{
		Project:         deletedProjectToProto(restored, authority),
		TriggersResumed: int32(len(resumed)),
	}), nil
}
