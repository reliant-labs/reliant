package daemon_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon "github.com/reliant-labs/reliant/internal/handlers/daemon"
)

// What the daemon does with the command, and how its failures map, is in
// daemon_proxy_machine_state_test.go; these rows pin the checks that run
// before the command is sent.
func TestCloseOAuthHelper_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness leaves the optional Router nil.
	svc := daemon.NewTestDaemon(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CloseOAuthHelperRequest, pb.CloseOAuthHelperResponse]{
		{
			Name:    "a caller is required",
			Req:     connect.NewRequest(&pb.CloseOAuthHelperRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "no daemon router is unavailable",
			Req:     connect.NewRequest(&pb.CloseOAuthHelperRequest{}),
			Ctx:     daemon.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, svc.CloseOAuthHelper)
}
