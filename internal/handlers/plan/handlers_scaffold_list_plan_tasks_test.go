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

func TestListPlanTasks_Generated(t *testing.T) {
	t.Parallel()

	repo := newPlanRepo()
	repo.ListTasksByPlanFunc = func(_ context.Context, planID string) ([]*db.Task, error) {
		return []*db.Task{
			{ID: "task-1", PlanID: planID, Title: "First", Position: 0},
			{ID: "task-2", PlanID: planID, Title: "Second", Position: 1, ParentTaskID: ptr.Of("task-1")},
		}, nil
	}
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListPlanTasksRequest, pb.ListPlanTasksResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     connect.NewRequest(&pb.ListPlanTasksRequest{PlanId: missingID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     connect.NewRequest(&pb.ListPlanTasksRequest{PlanId: otherPlanID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the plan's tasks, in order",
			Req:  connect.NewRequest(&pb.ListPlanTasksRequest{PlanId: ownPlanID}),
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListPlanTasksResponse]) {
				require.EqualValues(t, 2, resp.Msg.GetTotal())
				require.Equal(t, "task-1", resp.Msg.GetTasks()[0].GetId())
				require.Equal(t, "task-1", resp.Msg.GetTasks()[1].GetParentTaskId())
			},
		},
	}, svc.ListPlanTasks)
}
