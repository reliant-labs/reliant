package connector_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	connector "github.com/reliant-labs/reliant/internal/handlers/connector"
)

// The listing itself, scoped to its caller, runs in connector_test.go
// (TestClientConsentRoundTrip).
func TestListAuthorizedClients_Generated(t *testing.T) {
	t.Parallel()

	svc := connector.NewTestConnector(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListAuthorizedClientsRequest, pb.ListAuthorizedClientsResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.ListAuthorizedClientsRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
	}, svc.ListAuthorizedClients)
}
