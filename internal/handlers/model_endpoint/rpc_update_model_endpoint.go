package model_endpoint

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) UpdateModelEndpoint(ctx context.Context, req *reliantv1.UpdateModelEndpointRequest) (*reliantv1.UpdateModelEndpointResponse, error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Update(ctx, userID, req.GetId(), req.GetEndpoint())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return &reliantv1.UpdateModelEndpointResponse{Endpoint: ep}, nil
}
