package task

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The task-dependency rpcs were never implemented. Each answers exactly as
// before, through the embedded Unimplemented handler rather than forge's
// scaffold stub.

// AddTaskDependency was never implemented; it answers exactly as before.
func (s *Service) AddTaskDependency(ctx context.Context, req *connect.Request[pb.AddTaskDependencyRequest]) (*connect.Response[pb.AddTaskDependencyResponse], error) {
	return s.UnimplementedTaskServiceHandler.AddTaskDependency(ctx, req)
}
