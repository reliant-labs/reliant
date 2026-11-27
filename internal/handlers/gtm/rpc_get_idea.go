package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetIdea returns one idea of the org.
func (s *Service) GetIdea(
	ctx context.Context,
	req *connect.Request[pb.GetIdeaRequest],
) (*connect.Response[pb.GetIdeaResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	idea, err := s.deps.Service.GetIdea(ctx, a, gtmsvc.GetIdeaInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetIdeaResponse{Idea: ideaOut(idea)}), nil
}
