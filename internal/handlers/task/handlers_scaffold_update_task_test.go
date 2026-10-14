package task_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdateTask_Generated(t *testing.T) {
	t.Parallel()

	var updated *db.Task
	repo := newTaskRepo()
	repo.UpdateTaskFunc = func(_ context.Context, tk *db.Task) error {
		updated = tk
		return nil
	}
	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateTaskRequest, pb.UpdateTaskResponse]{
		{
			Name:    "unknown task is not found",
			Req:     connect.NewRequest(&pb.UpdateTaskRequest{TaskId: missingID, Title: ptr.Of("Renamed")}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's task is denied",
			Req:     connect.NewRequest(&pb.UpdateTaskRequest{TaskId: otherTaskID, Title: ptr.Of("Renamed")}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "only the fields set change",
			Req:  connect.NewRequest(&pb.UpdateTaskRequest{TaskId: ownTaskID, Status: ptr.Of(pb.TaskStatus_TASK_STATUS_COMPLETED)}),
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateTaskResponse]) {
				require.NotNil(t, updated)
				require.Equal(t, "Own task", updated.Title)
				require.Equal(t, 2, updated.Position)
				require.Equal(t, pb.TaskStatus_TASK_STATUS_COMPLETED, resp.Msg.GetTask().GetStatus())
			},
		},
	}, svc.UpdateTask)
}
