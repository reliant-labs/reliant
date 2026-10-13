package terminal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	terminal "github.com/reliant-labs/reliant/internal/handlers/terminal"
)

// The daemon's answers to terminal.close are in terminal_session_close_test.go
// and terminal_proxy_machine_state_test.go; these rows pin the checks that run
// before the command is sent.
func TestCloseSession_Generated(t *testing.T) {
	t.Parallel()

	svc := terminal.NewTestTerminal(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CloseTerminalSessionRequest, pb.CloseTerminalSessionResponse]{
		{
			Name:    "a caller is required",
			Req:     connect.NewRequest(&pb.CloseTerminalSessionRequest{SessionId: "s1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "session_id is required",
			Req:     connect.NewRequest(&pb.CloseTerminalSessionRequest{}),
			Ctx:     terminal.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CloseSession)
}
