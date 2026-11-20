package model_endpoint

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) TestModelEndpoint(ctx context.Context, req *reliantv1.TestModelEndpointRequest) (*reliantv1.TestModelEndpointResponse, error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	probe, latency, err := s.manager.Test(ctx, userID, req.GetId(), req.GetDraft())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return &reliantv1.TestModelEndpointResponse{Probe: probe, LatencyMs: latency.Milliseconds()}, nil
}
