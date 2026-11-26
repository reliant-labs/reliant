package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListIdeas lists the org's ideas, newest first.
func (s *Service) ListIdeas(
	ctx context.Context,
	req *connect.Request[pb.ListIdeasRequest],
) (*connect.Response[pb.ListIdeasResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListIdeas(ctx, a, gtmsvc.ListIdeasInput{
		Kind:       enumIn[gtmsvc.IdeaKind](m.GetKind()),
		Status:     enumIn[gtmsvc.IdeaStatus](m.GetStatus()),
		CampaignID: m.CampaignId,
		PageSize:   int(m.GetPageSize()),
		PageToken:  m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListIdeasResponse{
		Ideas:         ideasOut(res.Ideas),
		NextPageToken: res.NextPageToken,
	}), nil
}
