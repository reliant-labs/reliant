package task_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestListTasks_Generated(t *testing.T) {
	t.Parallel()

	repo := newTaskMocks()
	repo.Tasks.ListTasksByPlanFunc = func(_ context.Context, planID string) ([]*core.Task, error) {
		return []*core.Task{
			{ID: "task-1", PlanID: planID, Title: "First"},
			{ID: "task-2", PlanID: planID, Title: "Second", Position: 1},
		}, nil
	}
	svc := task.NewTestTask(t, task.WithTaskDeps(repo.Deps()))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListTasksRequest, pb.ListTasksResponse]{
		{
			Name:    "plan_id is required",
			Req:     connect.NewRequest(&pb.ListTasksRequest{}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown plan is not found",
			Req:     connect.NewRequest(&pb.ListTasksRequest{PlanId: ptr.Of(missingID)}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     connect.NewRequest(&pb.ListTasksRequest{PlanId: ptr.Of(otherPlanID)}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the plan's tasks",
			Req:  connect.NewRequest(&pb.ListTasksRequest{PlanId: ptr.Of(ownPlanID)}),
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListTasksResponse]) {
				require.EqualValues(t, 2, resp.Msg.GetTotal())
				require.Equal(t, "task-2", resp.Msg.GetTasks()[1].GetId())
				require.EqualValues(t, 1, resp.Msg.GetTasks()[1].GetPosition())
			},
		},
	}, svc.ListTasks)
}
