package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

func TestDeletePlan_Generated(t *testing.T) {
	t.Parallel()

	var deleted string
	repo := newPlanRepo()
	repo.DeletePlanFunc = func(_ context.Context, id string) error {
		deleted = id
		return nil
	}
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeletePlanRequest, pb.DeletePlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     connect.NewRequest(&pb.DeletePlanRequest{PlanId: missingID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     connect.NewRequest(&pb.DeletePlanRequest{PlanId: otherPlanID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's plan is deleted",
			Req:  connect.NewRequest(&pb.DeletePlanRequest{PlanId: ownPlanID}),
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeletePlanResponse]) {
				require.True(t, resp.Msg.GetSuccess())
				require.Equal(t, ownPlanID, deleted)
			},
		},
	}, svc.DeletePlan)
}
