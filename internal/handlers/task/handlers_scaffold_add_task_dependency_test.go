package task_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

// AddTaskDependency was never implemented. It answers exactly as the
// embedded Unimplemented handler always did, not with forge's scaffold stub.
func TestAddTaskDependency_AnswersUnimplemented(t *testing.T) {
	t.Parallel()

	svc := task.NewTestTask(t)
	_, err := svc.AddTaskDependency(task.AuthedContext(t), connect.NewRequest(&pb.AddTaskDependencyRequest{}))
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	require.ErrorContains(t, err, "reliant.v1.TaskService.AddTaskDependency is not implemented")
}
