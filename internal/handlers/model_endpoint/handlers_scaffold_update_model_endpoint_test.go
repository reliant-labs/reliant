package model_endpoint_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	model_endpoint "github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
)

// An update of the caller's own endpoint runs against a real database in
// internal/handlers/catalog/model_endpoint_test.go; these rows pin the checks
// before it.
func TestUpdateModelEndpoint_Generated(t *testing.T) {
	t.Parallel()

	svc := model_endpoint.NewTestModelEndpoint(t, model_endpoint.WithModelEndpointDeps(model_endpoint.Deps{Repo: unknownEndpointStore{}}), model_endpoint.WithPlaceholderDeps())
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateModelEndpointRequest, pb.UpdateModelEndpointResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.UpdateModelEndpointRequest{Id: "ep-1", Endpoint: &pb.ModelEndpointInput{Name: "gpu box"}}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an endpoint the caller does not have is not found",
			Req:     connect.NewRequest(&pb.UpdateModelEndpointRequest{Id: "ep-1", Endpoint: &pb.ModelEndpointInput{Name: "gpu box"}}),
			Ctx:     model_endpoint.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.UpdateModelEndpoint)
}
