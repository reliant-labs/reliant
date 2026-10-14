package plan

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListPlansByProject was never implemented; it answers exactly as before,
// through the embedded Unimplemented handler rather than forge's scaffold
// stub.
func (s *Service) ListPlansByProject(ctx context.Context, req *connect.Request[pb.ListPlansByProjectRequest]) (*connect.Response[pb.ListPlansByProjectResponse], error) {
	return s.UnimplementedPlanServiceHandler.ListPlansByProject(ctx, req)
}
