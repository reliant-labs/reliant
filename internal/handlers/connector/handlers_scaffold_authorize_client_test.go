package connector_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"

	connector "github.com/reliant-labs/reliant/internal/handlers/connector"
)

// The consent round trip and an unknown connector run against a grant store
// in connector_test.go (TestClientConsentRoundTrip,
// TestAuthorizeClientUnknownConnectorIsNotFound); these rows pin the request
// checks that run before the store is read.
func TestAuthorizeClient_Generated(t *testing.T) {
	t.Parallel()

	svc := connector.NewTestConnector(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.AuthorizeClientRequest, pb.AuthorizeClientResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.AuthorizeClientRequest{ClientId: "client-1", ConnectorId: ptr.Of("c-1")}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "client_id is required",
			Req:     connect.NewRequest(&pb.AuthorizeClientRequest{ConnectorId: ptr.Of("c-1")}),
			Ctx:     connector.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an existing and a new connector together are refused",
			Req: connect.NewRequest(&pb.AuthorizeClientRequest{
				ClientId: "client-1", ConnectorId: ptr.Of("c-1"), NewConnector: &pb.CreateConnectorRequest{},
			}),
			Ctx:     connector.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a connector must be chosen",
			Req:     connect.NewRequest(&pb.AuthorizeClientRequest{ClientId: "client-1"}),
			Ctx:     connector.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an invalid new connector is refused",
			Req: connect.NewRequest(&pb.AuthorizeClientRequest{
				ClientId: "client-1", NewConnector: &pb.CreateConnectorRequest{Name: "no workspace"},
			}),
			Ctx:     connector.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.AuthorizeClient)
}
