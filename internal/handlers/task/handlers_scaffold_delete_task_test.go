package task_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

func TestDeleteTask_Generated(t *testing.T) {
	t.Parallel()

	var deleted string
	repo := newTaskRepo()
	repo.DeleteTaskFunc = func(_ context.Context, id string) error {
		deleted = id
		return nil
	}
	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DeleteTaskRequest, pb.DeleteTaskResponse]{
		{
			Name:    "unknown task is not found",
			Req:     &pb.DeleteTaskRequest{TaskId: missingID},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's task is denied",
			Req:     &pb.DeleteTaskRequest{TaskId: otherTaskID},
			Ctx:     task.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's task is deleted",
			Req:  &pb.DeleteTaskRequest{TaskId: ownTaskID},
			Ctx:  task.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.DeleteTaskResponse) {
				require.True(t, resp.GetSuccess())
				require.Equal(t, ownTaskID, deleted)
			},
		},
	}, svc.DeleteTask)
}
