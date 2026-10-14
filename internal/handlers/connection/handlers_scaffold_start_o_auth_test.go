package connection_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/connections"
	connection "github.com/reliant-labs/reliant/internal/handlers/connection"
)

// connection_test.go covers the authorization URL and its relay origin; these
// rows need none.
func TestStartOAuth_Generated(t *testing.T) {
	t.Parallel()

	// This deployment has no OAuth client for the integration.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		StartOAuthFunc: func(context.Context, connections.StartParams) (string, error) {
			return "", &connections.Error{Code: connections.CodeFailedPrecondition, Message: "oauth is not configured"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.StartOAuthRequest, pb.StartOAuthResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.StartOAuthRequest{IntegrationId: "github"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an unconfigured OAuth client is a failed precondition",
			Req:     connect.NewRequest(&pb.StartOAuthRequest{IntegrationId: "github"}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.StartOAuth)
}
