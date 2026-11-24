package insights

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetMyInsights is the caller's own numbers in the org ("My analytics"): the
// same read an admin's drill-down makes, of the caller's own rows, so a
// member can always see what an admin could see about them. Any live member
// may ask, with any of their credentials; nothing on the request names
// another person.
func (s *Service) GetMyInsights(
	ctx context.Context,
	req *connect.Request[pb.GetMyInsightsRequest],
) (*connect.Response[pb.GetMyInsightsResponse], error) {
	msg := req.Msg
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireMember(ctx, userID, msg.GetOrgId()); err != nil {
		return nil, err
	}
	q, err := parseQuery(msg.GetOrgId(), msg.GetStartDate(), msg.GetEndDate(), msg.GetGroupBy())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	model, tool, err := s.memberSections(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetMyInsightsResponse{
		OrgId:      q.orgID,
		GroupBy:    q.groupBy,
		ModelCalls: model,
		ToolCalls:  tool,
		AsOf:       s.now().Format(time.RFC3339),
		Note:       MemberNote,
	}), nil
}
