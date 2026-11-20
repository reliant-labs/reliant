package system_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthSave_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DevAuthSaveRequest, pb.DevAuthSaveResponse]{
		{
			Name:    "not supported",
			Req:     &pb.DevAuthSaveRequest{},
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthSave)
}
