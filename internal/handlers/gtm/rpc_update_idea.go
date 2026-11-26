package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdateIdea edits an idea's content and campaign link; unset fields are left
// alone, and a set evidence list (possibly empty) replaces the stored one.
func (s *Service) UpdateIdea(
	ctx context.Context,
	req *connect.Request[pb.UpdateIdeaRequest],
) (*connect.Response[pb.UpdateIdeaResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	in := gtmsvc.UpdateIdeaInput{
		ID:               m.GetId(),
		ExpectedRevision: m.GetExpectedRevision(),
		Kind:             optEnumIn[gtmsvc.IdeaKind](m.Kind),
		Title:            m.Title,
		BodyMD:           m.BodyMd,
		CampaignID:       m.CampaignId,
	}
	if m.GetEvidence() != nil {
		evidence, err := evidenceIn(m.GetEvidence().GetEvidence())
		if err != nil {
			return nil, wireErr(err)
		}
		in.Evidence = &evidence
	}
	idea, err := s.deps.Service.UpdateIdea(ctx, a, in)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdateIdeaResponse{Idea: ideaOut(idea)}), nil
}
