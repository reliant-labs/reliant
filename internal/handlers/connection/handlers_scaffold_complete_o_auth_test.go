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

// connection_test.go covers a completed flow against a real database; these
// rows need none.
func TestCompleteOAuth_Generated(t *testing.T) {
	t.Parallel()

	// No flow was started with this state.
	svc := connection.NewTestConnection(t, connection.WithConnectionDeps(connection.Deps{Connections: &connection.StubConnections{
		CompleteOAuthFunc: func(context.Context, string, string, string) (*connections.Completion, error) {
			return nil, &connections.Error{Code: connections.CodeNotFound, Message: "oauth flow not found"}
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CompleteOAuthRequest, pb.CompleteOAuthResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.CompleteOAuthRequest{State: "s-1", Code: "code"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an unknown state is not found",
			Req:     connect.NewRequest(&pb.CompleteOAuthRequest{State: "s-1", Code: "code"}),
			Ctx:     connection.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.CompleteOAuth)
}
