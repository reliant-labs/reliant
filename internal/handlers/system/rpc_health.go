package system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Health returns the health status of the service
func (s *Service) Health(
	ctx context.Context,
	req *connect.Request[reliantv1.HealthRequest],
) (*connect.Response[reliantv1.HealthResponse], error) {
	resp := &reliantv1.HealthResponse{
		Status:  "ok",
		Version: "v2",
	}
	return connect.NewResponse(resp), nil
}
