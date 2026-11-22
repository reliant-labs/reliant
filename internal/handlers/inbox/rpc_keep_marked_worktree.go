package inbox

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// KeepMarkedWorktree cancels the mark the person saw (Sweeper.KeepMarked).
func (s *Service) KeepMarkedWorktree(
	ctx context.Context,
	req *connect.Request[pb.KeepMarkedWorktreeRequest],
) (*connect.Response[pb.KeepMarkedWorktreeResponse], error) {
	userID, err := s.markDecision(ctx, req.Msg.WorktreeId, req.Msg.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.KeepMarked(ctx, s.deps.Marks, userID, req.Msg.WorktreeId, req.Msg.MarkId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.KeepMarkedWorktreeResponse{}), nil
}
