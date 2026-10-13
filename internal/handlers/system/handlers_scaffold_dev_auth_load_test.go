package system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthLoad_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DevAuthLoadRequest, pb.DevAuthLoadResponse]{
		{
			Name:    "not supported",
			Req:     connect.NewRequest(&pb.DevAuthLoadRequest{}),
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthLoad)
}
