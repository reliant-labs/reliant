package task_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

func TestGetTask_Generated(t *testing.T) {
	t.Parallel()

	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: newTaskRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetTaskRequest, pb.GetTaskResponse]{
		{
			Name:    "unknown task is not found",
			Req:     connect.NewRequest(&pb.GetTaskRequest{TaskId: missingID}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's task is denied",
			Req:     connect.NewRequest(&pb.GetTaskRequest{TaskId: otherTaskID}),
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's task",
			Req:  connect.NewRequest(&pb.GetTaskRequest{TaskId: ownTaskID}),
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetTaskResponse]) {
				require.Equal(t, ownTaskID, resp.Msg.GetTask().GetId())
				require.Equal(t, ownPlanID, resp.Msg.GetTask().GetPlanId())
				require.Equal(t, "Own task", resp.Msg.GetTask().GetTitle())
			},
		},
	}, svc.GetTask)
}
