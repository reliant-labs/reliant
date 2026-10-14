package connection_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	connection "github.com/reliant-labs/reliant/internal/handlers/connection"
)

// connection_test.go covers creation against a real database and vault; these
// rows need none.
func TestCreateApiKeyConnection_Generated(t *testing.T) {
	t.Parallel()

	// The integration takes no API key.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		CreateAPIKeyFunc: func(context.Context, connections.CreateAPIKeyParams) (*core.Connection, error) {
			return nil, &connections.Error{Code: connections.CodeInvalidArgument, Message: "integration takes no API key"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateApiKeyConnectionRequest, pb.CreateApiKeyConnectionResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.CreateApiKeyConnectionRequest{IntegrationId: "github", Name: "work"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an integration without an API key method is invalid",
			Req:     connect.NewRequest(&pb.CreateApiKeyConnectionRequest{IntegrationId: "github", Name: "work"}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CreateApiKeyConnection)
}
