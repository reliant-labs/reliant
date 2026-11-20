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

// CreateTask authorizes the caller through the plan before it validates the
// request, so a validation row names a plan the caller owns.
func TestCreateTask_Generated(t *testing.T) {
	t.Parallel()

	var created *db.Task
	repo := newTaskRepo()
	repo.CreateTaskFunc = func(_ context.Context, tk *db.Task) error {
		created = tk
		return nil
	}
	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.CreateTaskRequest, pb.CreateTaskResponse]{
		{
			Name:    "unknown plan is not found",
			Req:     &pb.CreateTaskRequest{PlanId: missingID, Title: "Task"},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's plan is denied",
			Req:     &pb.CreateTaskRequest{PlanId: otherPlanID, Title: "Task"},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "title is required",
			Req:     &pb.CreateTaskRequest{PlanId: ownPlanID},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "a new task is pending, under its parent",
			Req: &pb.CreateTaskRequest{
				PlanId:       ownPlanID,
				Title:        "Write tests",
				Position:     ptr.Of(int32(3)),
				ParentTaskId: ptr.Of(ownTaskID),
			},
			Ctx: task.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.CreateTaskResponse) {
				require.NotNil(t, created)
				require.Equal(t, created.ID, resp.GetTask().GetId())
				require.Equal(t, ownPlanID, resp.GetTask().GetPlanId())
				require.Equal(t, pb.TaskStatus_TASK_STATUS_PENDING, resp.GetTask().GetStatus())
				require.EqualValues(t, 3, resp.GetTask().GetPosition())
				require.Equal(t, ownTaskID, resp.GetTask().GetParentTaskId())
			},
		},
	}, svc.CreateTask)
}
