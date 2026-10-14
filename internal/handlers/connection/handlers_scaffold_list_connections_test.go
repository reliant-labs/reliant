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
	"github.com/reliant-labs/reliant/internal/ptr"
)

// connection_test.go covers listing against a real database; these rows need
// none.
func TestListConnections_Generated(t *testing.T) {
	t.Parallel()

	// The integration filter names no integration.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		ListFunc: func(context.Context, string, string) ([]*core.Connection, error) {
			return nil, &connections.Error{Code: connections.CodeInvalidArgument, Message: "unknown integration"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListConnectionsRequest, pb.ListConnectionsResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.ListConnectionsRequest{IntegrationId: ptr.Of("nope")}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an unknown integration is invalid",
			Req:     connect.NewRequest(&pb.ListConnectionsRequest{IntegrationId: ptr.Of("nope")}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ListConnections)
}
