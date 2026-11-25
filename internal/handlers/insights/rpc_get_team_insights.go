package insights

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// GetTeamInsights is the org's totals by one dimension, for any live member.
// It reads only TeamStore, so nothing it answers names a person, and it
// merges every group fewer than MinContributors members contributed to into
// "other" (floorKeys), counting contributors per group of the dimension
// asked for: per project when grouping by project, since a project belongs
// to one member and its bucket would otherwise be that member's numbers.
// Cost figures are for billing:read holders only (canSeeOrgCost).
func (s *Service) GetTeamInsights(
	ctx context.Context,
	req *connect.Request[pb.GetTeamInsightsRequest],
) (*connect.Response[pb.GetTeamInsightsResponse], error) {
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
	seeCost, err := canSeeOrgCost(ctx, userID, q.orgID)
	if err != nil {
		return nil, err
	}
	byDay := q.dim == db.InsightsDimDay

	resp := &pb.GetTeamInsightsResponse{
		OrgId:           q.orgID,
		GroupBy:         q.groupBy,
		ModelCalls:      modelSection(nil, nil, byDay),
		ToolCalls:       toolSection(nil, nil, byDay),
		MinContributors: MinContributors,
		AsOf:            s.now().Format(time.RFC3339),
		Note:            TeamNote,
	}
	if db.InsightsDimApplies(db.InsightsModelCalls, q.dim) {
		rows, err := s.deps.Team.ListInsightsTeamModelCalls(ctx, q.orgID, q.start, q.end, q.dim)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		keys, calls := modelKeys(rows)
		shown, err := s.floor(ctx, db.InsightsModelCalls, q, keys, calls)
		if err != nil {
			return nil, err
		}
		resp.ModelCalls = modelSection(rows, shown, byDay)
	}
	if db.InsightsDimApplies(db.InsightsToolCalls, q.dim) {
		rows, err := s.deps.Team.ListInsightsTeamToolCalls(ctx, q.orgID, q.start, q.end, q.dim)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		keys, calls := toolKeys(rows)
		shown, err := s.floor(ctx, db.InsightsToolCalls, q, keys, calls)
		if err != nil {
			return nil, err
		}
		resp.ToolCalls = toolSection(rows, shown, byDay)
	}
	if !seeCost {
		hideModelCost(resp.ModelCalls)
		resp.CostHidden = true
		resp.Note += " " + CostHiddenNote
	}
	return connect.NewResponse(resp), nil
}

// floor is the set of group keys the team view shows on their own, from the
// contributors of each group of src's calls.
func (s *Service) floor(ctx context.Context, src db.InsightsSource, q query, keys []string, calls map[string]int64) (map[string]bool, error) {
	if len(keys) == 0 {
		return map[string]bool{}, nil
	}
	contributors, err := s.deps.Team.ListInsightsContributors(ctx, src, q.orgID, q.start, q.end, q.dim)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return floorKeys(keys, calls, contributorSets(contributors), MinContributors), nil
}

func modelKeys(rows []db.InsightsModelRow) ([]string, map[string]int64) {
	keys := make([]string, 0, len(rows))
	calls := make(map[string]int64, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
		calls[r.Key] += r.Calls
	}
	return keys, calls
}

func toolKeys(rows []db.InsightsToolRow) ([]string, map[string]int64) {
	keys := make([]string, 0, len(rows))
	calls := make(map[string]int64, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
		calls[r.Key] += r.Calls
	}
	return keys, calls
}
