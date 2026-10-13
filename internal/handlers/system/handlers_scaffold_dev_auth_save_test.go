package system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

func TestDevAuthSave_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DevAuthSaveRequest, pb.DevAuthSaveResponse]{
		{
			Name:    "not supported",
			Req:     connect.NewRequest(&pb.DevAuthSaveRequest{}),
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.DevAuthSave)
}
