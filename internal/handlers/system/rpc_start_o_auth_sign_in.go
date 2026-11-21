package system

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// StartOAuthSignIn completes the Electron-local OAuth sign-in flow for any provider.
func (s *Service) StartOAuthSignIn(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthSignInRequest],
) (*connect.Response[reliantv1.StartOAuthSignInResponse], error) {
	provider := req.Msg.Provider
	if provider == "" {
		return nil, svcerr.InvalidArgument("provider is required")
	}

	result, err := auth.LoginWithOAuthProvider(ctx, s.deps.OAuthProvider, provider)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("failed to complete %s OAuth sign-in", provider)))
	}

	return connect.NewResponse(&reliantv1.StartOAuthSignInResponse{
		AccessToken:   result.AccessToken,
		RefreshToken:  result.RefreshToken,
		UserId:        result.UserID,
		Email:         result.Email,
		ProviderToken: result.ProviderToken,
	}), nil
}
