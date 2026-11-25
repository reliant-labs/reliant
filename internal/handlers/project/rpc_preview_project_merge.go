package project

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// PreviewProjectMerge says what MergeProjects would move from the source onto
// the target, and what refuses it now, without writing. The caller is
// admitted as for MergeProjects (mergeableProjects).
func (s *Service) PreviewProjectMerge(
	ctx context.Context,
	req *connect.Request[pb.PreviewProjectMergeRequest],
) (*connect.Response[pb.PreviewProjectMergeResponse], error) {
	userID := auth.MustGetUserID(ctx)
	source, target, err := s.mergeableProjects(ctx, userID, req.Msg.GetSourceProjectId(), req.Msg.GetTargetProjectId())
	if err != nil {
		return nil, err
	}
	preview, err := s.deps.Merges.PreviewProjectMerge(ctx, source.ID, target.ID)
	if err != nil {
		return nil, projectMergeError(err, nil, "failed to read what the merge would move")
	}
	return connect.NewResponse(&pb.PreviewProjectMergeResponse{
		Counts:   s.projectMergeCountsProto(ctx, preview.Counts),
		Blockers: projectMergeBlockersProto(preview.Blockers),
	}), nil
}
