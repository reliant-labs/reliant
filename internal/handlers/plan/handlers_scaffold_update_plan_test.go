package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdatePlan_Generated(t *testing.T) {
	t.Parallel()

	var updated *db.Plan
	repo := newPlanRepo()
	repo.UpdatePlanFunc = func(_ context.Context, p *db.Plan) error {
		updated = p
		return nil
	}
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdatePlanRequest, pb.UpdatePlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     connect.NewRequest(&pb.UpdatePlanRequest{PlanId: missingID, Title: ptr.Of("Renamed")}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     connect.NewRequest(&pb.UpdatePlanRequest{PlanId: otherPlanID, Title: ptr.Of("Renamed")}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "only the fields set change",
			Req:  connect.NewRequest(&pb.UpdatePlanRequest{PlanId: ownPlanID, Status: ptr.Of(pb.PlanStatus_PLAN_STATUS_COMPLETED)}),
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdatePlanResponse]) {
				require.NotNil(t, updated)
				require.Equal(t, "Own plan", updated.Title)
				require.Equal(t, pb.PlanStatus_PLAN_STATUS_COMPLETED, resp.Msg.GetPlan().GetStatus())
				require.Equal(t, ownChatID, resp.Msg.GetPlan().GetChatId())
			},
		},
	}, svc.UpdatePlan)
}
