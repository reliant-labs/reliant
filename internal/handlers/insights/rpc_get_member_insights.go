package insights

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetMemberInsights is one member's numbers in the org, for its owners and
// admins: the role is read from control-plane now and only a person signed in
// to the app may ask (orgs.RequireAdmin: a CLI login, an access token or an
// agent is refused with reason human_only, a member with
// org_admin_required). The drill-down is recorded before anything is read; a
// drill-down that cannot be recorded is not answered.
func (s *Service) GetMemberInsights(
	ctx context.Context,
	req *connect.Request[pb.GetMemberInsightsRequest],
) (*connect.Response[pb.GetMemberInsightsResponse], error) {
	msg := req.Msg
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := orgs.RequireAdmin(ctx, userID, msg.GetOrgId(), "see one member's insights"); err != nil {
		return nil, svcerr.Wrap(err)
	}
	q, err := parseQuery(msg.GetOrgId(), msg.GetStartDate(), msg.GetEndDate(), msg.GetGroupBy())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	subject := msg.GetUserId()
	if subject == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("user_id is required"))
	}

	auditID, err := s.deps.Members.RecordInsightsDrilldown(ctx, q.orgID, userID, subject, q.start, q.end, s.now())
	if err != nil {
		return nil, svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("the drill-down could not be recorded, so it was not shown; try again"), err))
	}
	model, tool, err := s.memberSections(ctx, q, subject)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetMemberInsightsResponse{
		OrgId:      q.orgID,
		UserId:     subject,
		GroupBy:    q.groupBy,
		ModelCalls: model,
		ToolCalls:  tool,
		AsOf:       s.now().Format(time.RFC3339),
		Note:       MemberNote,
		AuditId:    auditID,
	}), nil
}

// memberSections is userID's own numbers in q's org, every group shown (the
// floor is for the team view; this is one member's view of themselves, or an
// admin's audited drill-down).
func (s *Service) memberSections(ctx context.Context, q query, userID string) (*pb.InsightsModelSection, *pb.InsightsToolSection, error) {
	byDay := q.dim == store.InsightsDimDay
	model, tool := modelSection(nil, nil, byDay), toolSection(nil, nil, byDay)
	if store.InsightsDimApplies(store.InsightsModelCalls, q.dim) {
		rows, err := s.deps.Members.ListInsightsMemberModelCalls(ctx, q.orgID, userID, q.start, q.end, q.dim)
		if err != nil {
			return nil, nil, svcerr.Wrap(err)
		}
		model = modelSection(rows, nil, byDay)
	}
	if store.InsightsDimApplies(store.InsightsToolCalls, q.dim) {
		rows, err := s.deps.Members.ListInsightsMemberToolCalls(ctx, q.orgID, userID, q.start, q.end, q.dim)
		if err != nil {
			return nil, nil, svcerr.Wrap(err)
		}
		tool = toolSection(rows, nil, byDay)
	}
	return model, tool, nil
}
