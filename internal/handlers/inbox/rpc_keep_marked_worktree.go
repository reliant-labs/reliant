package inbox

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// KeepMarkedWorktree cancels the mark the person saw (Sweeper.KeepMarked).
func (s *Service) KeepMarkedWorktree(
	ctx context.Context,
	req *pb.KeepMarkedWorktreeRequest,
) (*pb.KeepMarkedWorktreeResponse, error) {
	userID, err := s.markDecision(ctx, req.WorktreeId, req.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.KeepMarked(ctx, s.deps.Repo, userID, req.WorktreeId, req.MarkId)); err != nil {
		return nil, err
	}
	return &pb.KeepMarkedWorktreeResponse{}, nil
}
