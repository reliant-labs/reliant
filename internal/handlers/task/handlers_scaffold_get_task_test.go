package task_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

func TestGetTask_Generated(t *testing.T) {
	t.Parallel()

	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: newTaskRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetTaskRequest, pb.GetTaskResponse]{
		{
			Name:    "unknown task is not found",
			Req:     &pb.GetTaskRequest{TaskId: missingID},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's task is denied",
			Req:     &pb.GetTaskRequest{TaskId: otherTaskID},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's task",
			Req:  &pb.GetTaskRequest{TaskId: ownTaskID},
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetTaskResponse) {
				require.Equal(t, ownTaskID, resp.GetTask().GetId())
				require.Equal(t, ownPlanID, resp.GetTask().GetPlanId())
				require.Equal(t, "Own task", resp.GetTask().GetTitle())
			},
		},
	}, svc.GetTask)
}
