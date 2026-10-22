package task_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

// RemoveTaskDependency was never implemented. It answers exactly as the
// embedded Unimplemented handler always did, not with forge's scaffold stub.
func TestRemoveTaskDependency_AnswersUnimplemented(t *testing.T) {
	t.Parallel()

	svc := task.NewTestTask(t)
	_, err := svc.RemoveTaskDependency(task.AuthedContext(t), connect.NewRequest(&pb.RemoveTaskDependencyRequest{}))
	require.Equal(t, connect.CodeUnimplemented, svcerr.Code(err))
	require.ErrorContains(t, err, "reliant.v1.TaskService.RemoveTaskDependency is not implemented")
}
