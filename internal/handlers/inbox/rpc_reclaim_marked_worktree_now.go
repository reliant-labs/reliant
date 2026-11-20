package inbox

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReclaimMarkedWorktreeNow skips the wait of the mark the person saw
// (Sweeper.ReclaimMarkedNow).
func (s *Service) ReclaimMarkedWorktreeNow(
	ctx context.Context,
	req *pb.ReclaimMarkedWorktreeNowRequest,
) (*pb.ReclaimMarkedWorktreeNowResponse, error) {
	userID, err := s.markDecision(ctx, req.WorktreeId, req.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.ReclaimMarkedNow(ctx, s.deps.Repo, userID, req.WorktreeId, req.MarkId)); err != nil {
		return nil, err
	}
	return &pb.ReclaimMarkedWorktreeNowResponse{}, nil
}
