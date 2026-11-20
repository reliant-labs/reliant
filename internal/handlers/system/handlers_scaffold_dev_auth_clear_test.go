package system_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthClear_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DevAuthClearRequest, pb.DevAuthClearResponse]{
		{
			Name:    "not supported",
			Req:     &pb.DevAuthClearRequest{},
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthClear)
}
