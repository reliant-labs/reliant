package settings

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CompleteCodexOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Codex account: labelled by the ChatGPT email, planned from the id_token, deduped by the
// ChatGPT account and user.
func (s *Service) CompleteCodexOAuth(ctx context.Context, req *reliantv1.CompleteCodexOAuthRequest) (*reliantv1.CompleteCodexOAuthResponse, error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Code)
	codeVerifier := strings.TrimSpace(req.CodeVerifier)
	redirectURI := strings.TrimSpace(req.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeCodexCode(code, codeVerifier, redirectURI)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Codex OAuth"))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "codex", targetID: req.GetAccountId(),
		upsert: codexIdentity(tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, tokens.AccountID),
	})
	if err != nil {
		return nil, err
	}
	return &reliantv1.CompleteCodexOAuthResponse{
		Success: true, Message: connectedMessage("codex", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}, nil
}
