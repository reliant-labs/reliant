package settings_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

// The code exchange itself reaches Google's token endpoint; these rows pin
// the request checks that run before it.
func TestCompleteAntigravityOAuth_Generated(t *testing.T) {
	t.Parallel()

	svc := newSettingsService(t, memorySettings{}.repo())

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.CompleteAntigravityOAuthRequest, pb.CompleteAntigravityOAuthResponse]{
		{
			Name:    "code is required",
			Req:     &pb.CompleteAntigravityOAuthRequest{CodeVerifier: "v", RedirectUri: "http://localhost/cb"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "code_verifier is required",
			Req:     &pb.CompleteAntigravityOAuthRequest{Code: "c", CodeVerifier: " ", RedirectUri: "http://localhost/cb"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "redirect_uri is required",
			Req:     &pb.CompleteAntigravityOAuthRequest{Code: "c", CodeVerifier: "v"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CompleteAntigravityOAuth)
}
