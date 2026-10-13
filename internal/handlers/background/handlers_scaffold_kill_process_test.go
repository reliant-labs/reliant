package background_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	background "github.com/reliant-labs/reliant/internal/handlers/background"
)

// The daemon round trip is in background_proxy_*_test.go; these rows pin the
// checks that run before it.
func TestKillProcess_Generated(t *testing.T) {
	t.Parallel()

	svc := background.NewTestBackground(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.KillProcessRequest, pb.KillProcessResponse]{
		{
			Name:    "a caller is required",
			Req:     connect.NewRequest(&pb.KillProcessRequest{ProcessId: "proc-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "process_id is required",
			Req:     connect.NewRequest(&pb.KillProcessRequest{}),
			Ctx:     background.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.KillProcess)
}
