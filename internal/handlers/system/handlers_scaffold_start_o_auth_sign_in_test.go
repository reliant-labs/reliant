package system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

// The sign-in flow itself opens a browser and waits for a localhost
// callback; only the request validation is unit-testable here.
func TestStartOAuthSignIn_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.StartOAuthSignInRequest, pb.StartOAuthSignInResponse]{
		{
			Name:    "provider is required",
			Req:     connect.NewRequest(&pb.StartOAuthSignInRequest{}),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.StartOAuthSignIn)
}
