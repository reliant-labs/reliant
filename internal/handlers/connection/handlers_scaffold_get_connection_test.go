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

// connection_test.go covers reads against a real database; these rows need
// none.
func TestGetConnection_Generated(t *testing.T) {
	t.Parallel()

	// The caller owns no connection.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		GetFunc: func(context.Context, string, string) (*core.Connection, error) {
			return nil, &connections.Error{Code: connections.CodeNotFound, Message: "connection not found"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetConnectionRequest, pb.GetConnectionResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.GetConnectionRequest{Id: "c-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "a connection the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetConnectionRequest{Id: "c-1"}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetConnection)
}
