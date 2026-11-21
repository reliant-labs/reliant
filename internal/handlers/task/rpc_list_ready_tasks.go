package task

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListReadyTasks was never implemented; it answers exactly as before.
func (s *Service) ListReadyTasks(ctx context.Context, req *connect.Request[pb.ListReadyTasksRequest]) (*connect.Response[pb.ListReadyTasksResponse], error) {
	return s.UnimplementedTaskServiceHandler.ListReadyTasks(ctx, req)
}
