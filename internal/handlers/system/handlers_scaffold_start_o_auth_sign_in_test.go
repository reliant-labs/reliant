package system_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
)

// The sign-in flow itself opens a browser and waits for a localhost
// callback; only the request validation is unit-testable here.
func TestStartOAuthSignIn_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.StartOAuthSignInRequest, pb.StartOAuthSignInResponse]{
		{
			Name:    "provider is required",
			Req:     &pb.StartOAuthSignInRequest{},
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.StartOAuthSignIn)
}
