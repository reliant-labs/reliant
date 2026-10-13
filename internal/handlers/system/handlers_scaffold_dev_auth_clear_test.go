package system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthClear_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DevAuthClearRequest, pb.DevAuthClearResponse]{
		{
			Name:    "not supported",
			Req:     connect.NewRequest(&pb.DevAuthClearRequest{}),
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthClear)
}
