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

// connection_test.go covers deletion against a real database; these rows need
// none.
func TestDeleteConnection_Generated(t *testing.T) {
	t.Parallel()

	// The caller owns no connection.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		DeleteFunc: func(context.Context, string, string) error {
			return &connections.Error{Code: connections.CodeNotFound, Message: "connection not found"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteConnectionRequest, pb.DeleteConnectionResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.DeleteConnectionRequest{Id: "c-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "a connection the caller does not own is not found",
			Req:     connect.NewRequest(&pb.DeleteConnectionRequest{Id: "c-1"}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DeleteConnection)
}
