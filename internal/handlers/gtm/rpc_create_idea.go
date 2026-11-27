package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateIdea adds a PROPOSED idea. Its source and source chat are the
// caller's (the Actor), so the request's source_chat_id is not read: only a
// GTM tool, acting in a chat, records one.
func (s *Service) CreateIdea(
	ctx context.Context,
	req *connect.Request[pb.CreateIdeaRequest],
) (*connect.Response[pb.CreateIdeaResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	evidence, err := evidenceIn(m.GetEvidence())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	idea, err := s.deps.Service.CreateIdea(ctx, a, gtmsvc.CreateIdeaInput{
		Kind:       enumIn[gtmsvc.IdeaKind](m.GetKind()),
		Title:      m.GetTitle(),
		BodyMD:     m.GetBodyMd(),
		Evidence:   evidence,
		CampaignID: m.GetCampaignId(),

		LibraryKey:          m.GetLibraryKey(),
		TailorAnswers:       tailorAnswersIn(m.GetTailorAnswers()),
		SuggestedWorkflowMD: m.GetSuggestedWorkflowMd(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateIdeaResponse{Idea: ideaOut(idea)}), nil
}
