package model_endpoint_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	model_endpoint "github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
)

// Probing a saved endpoint runs against a real database in
// internal/handlers/catalog/model_endpoint_test.go; these rows pin the checks
// before a probe is attempted.
func TestTestModelEndpoint_Generated(t *testing.T) {
	t.Parallel()

	svc := model_endpoint.NewTestModelEndpoint(t, model_endpoint.WithModelEndpointDeps(model_endpoint.Deps{Repo: unknownEndpointStore{}}), model_endpoint.WithPlaceholderDeps())
	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.TestModelEndpointRequest, pb.TestModelEndpointResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     &pb.TestModelEndpointRequest{Target: &pb.TestModelEndpointRequest_Id{Id: "ep-1"}},
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an endpoint the caller does not have is not found",
			Req:     &pb.TestModelEndpointRequest{Target: &pb.TestModelEndpointRequest_Id{Id: "ep-1"}},
			Ctx:     model_endpoint.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a draft needs a name",
			Req:     &pb.TestModelEndpointRequest{Target: &pb.TestModelEndpointRequest_Draft{Draft: &pb.ModelEndpointInput{}}},
			Ctx:     model_endpoint.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.TestModelEndpoint)
}
