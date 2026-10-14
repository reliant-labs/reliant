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

// connection_test.go covers the catalog a deployment's providers build; these
// rows need none.
func TestListIntegrations_Generated(t *testing.T) {
	t.Parallel()

	// One integration, no auth methods.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		ListIntegrationsFunc: func() []connections.Integration {
			return []connections.Integration{{ID: "github", DisplayName: "GitHub"}}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListIntegrationsRequest, pb.ListIntegrationsResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.ListIntegrationsRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "the integrations are listed",
			Req:  connect.NewRequest(&pb.ListIntegrationsRequest{}),
			Ctx:  connection.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListIntegrationsResponse]) {
				if got := resp.Msg.GetIntegrations(); len(got) != 1 || got[0].GetId() != "github" {
					t.Fatalf("integrations = %v, want github", got)
				}
			},
		},
	}, svc.ListIntegrations)
}
