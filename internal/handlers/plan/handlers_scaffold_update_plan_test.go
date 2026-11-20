package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
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

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.UpdatePlanRequest, pb.UpdatePlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     &pb.UpdatePlanRequest{PlanId: missingID, Title: ptr.Of("Renamed")},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     &pb.UpdatePlanRequest{PlanId: otherPlanID, Title: ptr.Of("Renamed")},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "only the fields set change",
			Req:  &pb.UpdatePlanRequest{PlanId: ownPlanID, Status: ptr.Of(pb.PlanStatus_PLAN_STATUS_COMPLETED)},
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.UpdatePlanResponse) {
				require.NotNil(t, updated)
				require.Equal(t, "Own plan", updated.Title)
				require.Equal(t, pb.PlanStatus_PLAN_STATUS_COMPLETED, resp.GetPlan().GetStatus())
				require.Equal(t, ownChatID, resp.GetPlan().GetChatId())
			},
		},
	}, svc.UpdatePlan)
}
