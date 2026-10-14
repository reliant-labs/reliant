package plan_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

func TestGetPlan_Generated(t *testing.T) {
	t.Parallel()

	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: newPlanRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetPlanRequest, pb.GetPlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     connect.NewRequest(&pb.GetPlanRequest{PlanId: missingID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     connect.NewRequest(&pb.GetPlanRequest{PlanId: otherPlanID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the plan carries its chat",
			Req:  connect.NewRequest(&pb.GetPlanRequest{PlanId: ownPlanID}),
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetPlanResponse]) {
				require.Equal(t, ownPlanID, resp.Msg.GetPlan().GetId())
				require.Equal(t, ownChatID, resp.Msg.GetPlan().GetChatId())
				require.Equal(t, "Own plan", resp.Msg.GetPlan().GetTitle())
			},
		},
	}, svc.GetPlan)
}
