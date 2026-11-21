package settings

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CompleteClaudeOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Claude subscription account: labelled by the account email, planned from the profile
// endpoint, deduped by account and organization.
func (s *Service) CompleteClaudeOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteClaudeOAuthRequest]) (*connect.Response[reliantv1.CompleteClaudeOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	state := strings.TrimSpace(req.Msg.State)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeClaudeCode(code, codeVerifier, redirectURI, state)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Claude OAuth"))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "claude", targetID: req.Msg.GetAccountId(),
		upsert: s.claudeIdentity(ctx, tokens.AccessToken, tokens.AccountUUID, tokens.OrganizationUUID,
			tokens.AccountEmail, tokens.OrganizationName, tokens.Scope, tokens.ExpiresAt, tokens.RefreshToken),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteClaudeOAuthResponse{
		Success: true, Message: connectedMessage("claude", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}
