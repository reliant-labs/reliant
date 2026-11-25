package run

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// LastRunPerWorkflow returns the newest run of each workflow the caller has
// run, scoped like the run list (ListRuns): the org the request names, and
// never a run of an org the caller has left.
func (s *Service) LastRunPerWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.LastRunPerWorkflowRequest],
) (*connect.Response[reliantv1.LastRunPerWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// The newest run per workflow AND org, so the orgs the caller may no
	// longer see are dropped before each workflow's newest is picked: a newer
	// run of an org they left must not hide an older one they may see.
	items, err := s.deps.RunLists.LastRunPerWorkflowAndOrg(ctx, db.RunListFilters{
		UserID:     userID,
		Org:        scope,
		ProjectID:  req.Msg.ProjectId,
		ProjectIDs: req.Msg.ProjectIds,
		Workflows:  req.Msg.Workflow,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
	}
	// As ListRuns: the store reads what the caller owns, and a run of an org
	// they have left is not theirs to see, named org or not.
	items = core.NewestRunPerWorkflow(authz.Visible(ctx, userID, items, func(ctx context.Context, userID string, item *db.RunListItem) bool {
		return authz.CanReadOwn(ctx, userID, item.OrgID)
	}))
	resp := &reliantv1.LastRunPerWorkflowResponse{Runs: make([]*reliantv1.Run, 0, len(items))}
	for _, item := range items {
		resp.Runs = append(resp.Runs, runItemToProto(item))
	}
	return connect.NewResponse(resp), nil
}
