package model_endpoint

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) ListModelEndpoints(ctx context.Context, _ *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := s.manager.List(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.ListModelEndpointsResponse{
		Endpoints:            eps,
		CredentialsAvailable: s.manager.CredentialsAvailable(),
	}), nil
}
