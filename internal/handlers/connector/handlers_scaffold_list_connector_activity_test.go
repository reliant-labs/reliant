package connector_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	connector "github.com/reliant-labs/reliant/internal/handlers/connector"
)

// The audit log is written by the MCP endpoint and read back through the
// store's user-scoped queries; this row pins that it has an owner to scope by.
func TestListConnectorActivity_Generated(t *testing.T) {
	t.Parallel()

	svc := connector.NewTestConnector(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListConnectorActivityRequest, pb.ListConnectorActivityResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.ListConnectorActivityRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
	}, svc.ListConnectorActivity)
}
