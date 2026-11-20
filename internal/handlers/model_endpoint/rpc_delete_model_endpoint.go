package model_endpoint

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) DeleteModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.DeleteModelEndpointRequest]) (*connect.Response[reliantv1.DeleteModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.manager.Delete(ctx, userID, req.Msg.GetId()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.DeleteModelEndpointResponse{}), nil
}
