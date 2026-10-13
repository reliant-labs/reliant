package connector_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	connector "github.com/reliant-labs/reliant/internal/handlers/connector"
)

// Revocation, scoped to its caller, runs in connector_test.go
// (TestClientConsentRoundTrip); these rows pin the request checks.
func TestRevokeClientAuthorization_Generated(t *testing.T) {
	t.Parallel()

	svc := connector.NewTestConnector(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RevokeClientAuthorizationRequest, pb.RevokeClientAuthorizationResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.RevokeClientAuthorizationRequest{ClientId: "client-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "client_id is required",
			Req:     connect.NewRequest(&pb.RevokeClientAuthorizationRequest{}),
			Ctx:     connector.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.RevokeClientAuthorization)
}
