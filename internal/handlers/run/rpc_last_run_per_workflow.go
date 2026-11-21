package run

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// LastRunPerWorkflow returns the newest run of each workflow the caller has run.
func (s *Service) LastRunPerWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.LastRunPerWorkflowRequest],
) (*connect.Response[reliantv1.LastRunPerWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)

	items, err := s.deps.Repo.LastRunPerWorkflow(ctx, db.RunListFilters{
		UserID:    userID,
		ProjectID: req.Msg.ProjectId,
		Workflows: req.Msg.Workflow,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
	}
	resp := &reliantv1.LastRunPerWorkflowResponse{Runs: make([]*reliantv1.Run, 0, len(items))}
	for _, item := range items {
		resp.Runs = append(resp.Runs, runItemToProto(item))
	}
	return connect.NewResponse(resp), nil
}
