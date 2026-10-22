package task_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

// The task reads (the task, and the plan and chat that authorize it) and
// writes pass a classified repository error through and redact an opaque
// one; an outage is no longer reported as "task not found".
func TestTaskRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run("GetTask: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTaskRepo()
			repo.GetTaskFunc = func(context.Context, string) (*db.Task, error) { return nil, tc.err }
			svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))
			_, err := svc.GetTask(task.AuthedContext(t), connect.NewRequest(&pb.GetTaskRequest{TaskId: ownTaskID}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ListTasks, reading the plan: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTaskRepo()
			repo.GetPlanFunc = func(context.Context, string) (*db.Plan, error) { return nil, tc.err }
			svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))
			planID := ownPlanID
			_, err := svc.ListTasks(task.AuthedContext(t), connect.NewRequest(&pb.ListTasksRequest{PlanId: &planID}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("DeleteTask, writing: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTaskRepo()
			repo.DeleteTaskFunc = func(context.Context, string) error { return tc.err }
			svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: repo}))
			_, err := svc.DeleteTask(task.AuthedContext(t), connect.NewRequest(&pb.DeleteTaskRequest{TaskId: ownTaskID}))
			requireClientReads(t, err, tc.want)
		})
	}

	// A missing task is still NotFound: the store says so.
	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: newTaskRepo()}))
	_, err := svc.GetTask(task.AuthedContext(t), connect.NewRequest(&pb.GetTaskRequest{TaskId: missingID}))
	requireClientReads(t, err, connect.CodeNotFound)
}

// An empty id is the caller's mistake, answered before any lookup.
func TestTaskRPCs_EmptyIDIsInvalidArgument(t *testing.T) {
	t.Parallel()

	svc := task.NewTestTask(t, task.WithTaskDeps(task.Deps{Repo: newTaskRepo()}))
	ctx := task.AuthedContext(t)

	_, err := svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
	_, err = svc.CreateTask(ctx, connect.NewRequest(&pb.CreateTaskRequest{Title: "Task"}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
	empty := ""
	_, err = svc.ListTasks(ctx, connect.NewRequest(&pb.ListTasksRequest{PlanId: &empty}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}
