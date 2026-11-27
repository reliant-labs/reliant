package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// SetIdeaStatus moves an idea along its lifecycle; ACCEPTED and REJECTED are
// a person's decision.
func (s *Service) SetIdeaStatus(
	ctx context.Context,
	req *connect.Request[pb.SetIdeaStatusRequest],
) (*connect.Response[pb.SetIdeaStatusResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	idea, err := s.deps.Service.SetIdeaStatus(ctx, a, gtmsvc.SetIdeaStatusInput{
		ID:               m.GetId(),
		ExpectedRevision: m.GetExpectedRevision(),
		Status:           enumIn[gtmsvc.IdeaStatus](m.GetStatus()),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.SetIdeaStatusResponse{Idea: ideaOut(idea)}), nil
}
