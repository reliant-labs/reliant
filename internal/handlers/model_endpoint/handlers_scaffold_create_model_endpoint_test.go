package model_endpoint_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	model_endpoint "github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
)

// Creation (probe, limits, the hosted network policy) runs against a real
// database in internal/handlers/catalog/model_endpoint_test.go; these rows pin
// the checks before it.
func TestCreateModelEndpoint_Generated(t *testing.T) {
	t.Parallel()

	svc := model_endpoint.NewTestModelEndpoint(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateModelEndpointRequest, pb.CreateModelEndpointResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.CreateModelEndpointRequest{Endpoint: &pb.ModelEndpointInput{Name: "gpu box"}}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "endpoint details are required",
			Req:     connect.NewRequest(&pb.CreateModelEndpointRequest{}),
			Ctx:     model_endpoint.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a name is required",
			Req:     connect.NewRequest(&pb.CreateModelEndpointRequest{Endpoint: &pb.ModelEndpointInput{Name: "  "}}),
			Ctx:     model_endpoint.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CreateModelEndpoint)
}
