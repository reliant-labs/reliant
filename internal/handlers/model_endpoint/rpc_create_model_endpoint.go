package model_endpoint

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) CreateModelEndpoint(ctx context.Context, req *reliantv1.CreateModelEndpointRequest) (*reliantv1.CreateModelEndpointResponse, error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Create(ctx, userID, req.GetEndpoint())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return &reliantv1.CreateModelEndpointResponse{Endpoint: ep}, nil
}
