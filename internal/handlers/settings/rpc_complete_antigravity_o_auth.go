package settings

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
)

// CompleteAntigravityOAuth exchanges a Google OAuth authorization code + PKCE verifier for
// Antigravity tokens and stores them as an account keyed by the Google subject.
//
// Unlike CompleteClaudeOAuth there is no state parameter: Google's token
// endpoint does not require it echoed back. The frontend still generates and
// verifies state — it just never reaches the backend.
func (s *Service) CompleteAntigravityOAuth(ctx context.Context, req *reliantv1.CompleteAntigravityOAuthRequest) (*reliantv1.CompleteAntigravityOAuthResponse, error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Code)
	codeVerifier := strings.TrimSpace(req.CodeVerifier)
	redirectURI := strings.TrimSpace(req.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeAntigravityCode(code, codeVerifier, redirectURI)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Antigravity OAuth"))
	}

	u := core.AccountUpsert{
		AuthKind: core.AuthKindOAuth, Scope: tokens.Scope, ExpiresAt: tokens.ExpiresAt,
		Secrets: core.AccountSecrets{Secret: tokens.AccessToken, Refresh: tokens.RefreshToken, IDToken: tokens.IDToken},
	}
	if id, ok := core.GoogleIdentityFromIDToken(tokens.IDToken); ok {
		u.IdentityKey, u.Email, u.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
	}
	res, err := s.connectAccount(ctx, signIn{userID: userID, provider: antigravity.DriverID, targetID: req.GetAccountId(), upsert: u})
	if err != nil {
		return nil, err
	}
	return &reliantv1.CompleteAntigravityOAuthResponse{
		Success: true, Message: connectedMessage(antigravity.DriverID, res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}, nil
}
