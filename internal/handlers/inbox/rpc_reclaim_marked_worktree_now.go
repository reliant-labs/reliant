package inbox

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReclaimMarkedWorktreeNow skips the wait of the mark the person saw
// (Sweeper.ReclaimMarkedNow).
func (s *Service) ReclaimMarkedWorktreeNow(
	ctx context.Context,
	req *connect.Request[pb.ReclaimMarkedWorktreeNowRequest],
) (*connect.Response[pb.ReclaimMarkedWorktreeNowResponse], error) {
	userID, err := s.markDecision(ctx, req.Msg.WorktreeId, req.Msg.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.ReclaimMarkedNow(ctx, s.deps.Marks, userID, req.Msg.WorktreeId, req.Msg.MarkId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ReclaimMarkedWorktreeNowResponse{}), nil
}
