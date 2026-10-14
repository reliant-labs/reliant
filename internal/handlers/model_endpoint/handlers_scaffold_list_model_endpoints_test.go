package model_endpoint_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	model_endpoint "github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
)

// The listing itself, scoped to its caller, runs against a real database in
// internal/handlers/catalog/model_endpoint_test.go
// (TestModelEndpointServiceCRUDOverTheHandler).
func TestListModelEndpoints_Generated(t *testing.T) {
	t.Parallel()

	// The caller has no endpoints. The harness's auto-stub refuses an
	// unprogrammed call, so the one listing this rpc makes is programmed.
	svc := model_endpoint.NewTestModelEndpoint(t, model_endpoint.WithModelEndpointDeps(model_endpoint.Deps{
		Repo: &model_endpoint.StubRepo{
			ListModelEndpointsFunc: func(context.Context, string) ([]*db.ModelEndpoint, error) { return nil, nil },
		},
	}), model_endpoint.WithPlaceholderDeps())
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListModelEndpointsRequest, pb.ListModelEndpointsResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ListModelEndpointsRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "a caller with no endpoints gets an empty list",
			Req:  connect.NewRequest(&pb.ListModelEndpointsRequest{}),
			Ctx:  model_endpoint.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListModelEndpointsResponse]) {
				if n := len(resp.Msg.GetEndpoints()); n != 0 {
					t.Fatalf("ListModelEndpoints returned %d endpoints, want 0", n)
				}
			},
		},
	}, svc.ListModelEndpoints)
}
