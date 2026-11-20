package model_endpoint

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func (s *Service) DeleteModelEndpoint(ctx context.Context, req *reliantv1.DeleteModelEndpointRequest) (*reliantv1.DeleteModelEndpointResponse, error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.manager.Delete(ctx, userID, req.GetId()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return &reliantv1.DeleteModelEndpointResponse{}, nil
}
