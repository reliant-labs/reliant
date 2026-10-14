package settings_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

// The code exchange itself reaches OpenAI's token endpoint; these rows pin
// the request checks that run before it.
func TestCompleteCodexOAuth_Generated(t *testing.T) {
	t.Parallel()

	svc := newSettingsService(t, memorySettings{}.repo())

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CompleteCodexOAuthRequest, pb.CompleteCodexOAuthResponse]{
		{
			Name:    "code is required",
			Req:     connect.NewRequest(&pb.CompleteCodexOAuthRequest{Code: "  ", CodeVerifier: "v", RedirectUri: "http://localhost/cb"}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "code_verifier is required",
			Req:     connect.NewRequest(&pb.CompleteCodexOAuthRequest{Code: "c", RedirectUri: "http://localhost/cb"}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "redirect_uri is required",
			Req:     connect.NewRequest(&pb.CompleteCodexOAuthRequest{Code: "c", CodeVerifier: "v"}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CompleteCodexOAuth)
}
