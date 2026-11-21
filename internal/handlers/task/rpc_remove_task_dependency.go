package task

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// RemoveTaskDependency was never implemented; it answers exactly as before.
func (s *Service) RemoveTaskDependency(ctx context.Context, req *connect.Request[pb.RemoveTaskDependencyRequest]) (*connect.Response[pb.RemoveTaskDependencyResponse], error) {
	return s.UnimplementedTaskServiceHandler.RemoveTaskDependency(ctx, req)
}
