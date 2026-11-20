package plan_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

func TestGetPlan_Generated(t *testing.T) {
	t.Parallel()

	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: newPlanRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetPlanRequest, pb.GetPlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     &pb.GetPlanRequest{PlanId: missingID},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     &pb.GetPlanRequest{PlanId: otherPlanID},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the plan carries its chat",
			Req:  &pb.GetPlanRequest{PlanId: ownPlanID},
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetPlanResponse) {
				require.Equal(t, ownPlanID, resp.GetPlan().GetId())
				require.Equal(t, ownChatID, resp.GetPlan().GetChatId())
				require.Equal(t, "Own plan", resp.GetPlan().GetTitle())
			},
		},
	}, svc.GetPlan)
}
