package task_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestListTasks_Generated(t *testing.T) {
	t.Parallel()

	repo := newTaskRepo()
	repo.ListTasksByPlanFunc = func(_ context.Context, planID string) ([]*db.Task, error) {
		return []*db.Task{
			{ID: "task-1", PlanID: planID, Title: "First"},
			{ID: "task-2", PlanID: planID, Title: "Second", Position: 1},
		}, nil
	}
	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListTasksRequest, pb.ListTasksResponse]{
		{
			Name:    "plan_id is required",
			Req:     &pb.ListTasksRequest{},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown plan is not found",
			Req:     &pb.ListTasksRequest{PlanId: ptr.Of(missingID)},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     &pb.ListTasksRequest{PlanId: ptr.Of(otherPlanID)},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the plan's tasks",
			Req:  &pb.ListTasksRequest{PlanId: ptr.Of(ownPlanID)},
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ListTasksResponse) {
				require.EqualValues(t, 2, resp.GetTotal())
				require.Equal(t, "task-2", resp.GetTasks()[1].GetId())
				require.EqualValues(t, 1, resp.GetTasks()[1].GetPosition())
			},
		},
	}, svc.ListTasks)
}
