package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
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

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DeletePlanRequest, pb.DeletePlanResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     &pb.DeletePlanRequest{PlanId: missingID},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     &pb.DeletePlanRequest{PlanId: otherPlanID},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's plan is deleted",
			Req:  &pb.DeletePlanRequest{PlanId: ownPlanID},
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.DeletePlanResponse) {
				require.True(t, resp.GetSuccess())
				require.Equal(t, ownPlanID, deleted)
			},
		},
	}, svc.DeletePlan)
}
