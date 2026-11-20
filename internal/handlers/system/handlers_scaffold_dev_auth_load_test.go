package system_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthLoad_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DevAuthLoadRequest, pb.DevAuthLoadResponse]{
		{
			Name:    "not supported",
			Req:     &pb.DevAuthLoadRequest{},
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthLoad)
}
