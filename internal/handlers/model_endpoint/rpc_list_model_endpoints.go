package model_endpoint

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) ListModelEndpoints(ctx context.Context, _ *reliantv1.ListModelEndpointsRequest) (*reliantv1.ListModelEndpointsResponse, error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := s.manager.List(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return &reliantv1.ListModelEndpointsResponse{
		Endpoints:            eps,
		CredentialsAvailable: s.manager.CredentialsAvailable(),
	}, nil
}
