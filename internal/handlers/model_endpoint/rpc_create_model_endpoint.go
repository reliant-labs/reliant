package model_endpoint

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) CreateModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.CreateModelEndpointRequest]) (*connect.Response[reliantv1.CreateModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Create(ctx, userID, req.Msg.GetEndpoint())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.CreateModelEndpointResponse{Endpoint: ep}), nil
}
