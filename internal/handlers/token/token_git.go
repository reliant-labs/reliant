// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// Reason codes GetGitToken puts in x-forge-error-reason. The first two match
// control-plane's names so a client sees one vocabulary end to end.
const (
	reasonGitNoControlPlane = "git_credential_no_control_plane"
	reasonGitNotConnected   = "git_credential_not_connected"
	reasonGitNeedsReconnect = "git_credential_needs_reconnect"
)

const defaultGitProvider = "github"

// GetGitToken returns the calling daemon's acting user's current git provider
// token. It authenticates its own bearer, uncached, and admits only a daemon
// credential. The token is passed through: never stored, cached or logged here.
func (s *Service) GetGitToken(
	ctx context.Context, req *connect.Request[reliantv1.GetGitTokenRequest],
) (*connect.Response[reliantv1.GetGitTokenResponse], error) {
	subject, err := s.daemonCredential(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	userID := subject.ActingUserID
	provider := strings.ToLower(strings.TrimSpace(req.Msg.GetProvider()))
	if provider == "" {
		provider = defaultGitProvider
	}
	if s.deps.GitTokens == nil {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"this server has no control plane, so it holds no git provider token for you"), reasonGitNoControlPlane)
	}

	tok, err := s.deps.GitTokens.UserAccessToken(ctx, userID, provider)
	switch {
	case errors.Is(err, gitcredentialclient.ErrNotConnected):
		slog.InfoContext(ctx, "git token refused", "user_id", userID, "provider", provider, "outcome", reasonGitNotConnected)
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"connect your "+provider+" account in Reliant first"), reasonGitNotConnected)
	case errors.Is(err, gitcredentialclient.ErrNeedsReconnect):
		slog.InfoContext(ctx, "git token refused", "user_id", userID, "provider", provider, "outcome", reasonGitNeedsReconnect)
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"your "+provider+" connection expired; reconnect it in Reliant"), reasonGitNeedsReconnect)
	case err != nil:
		slog.ErrorContext(ctx, "git token unavailable", "user_id", userID, "provider", provider, "outcome", "unavailable")
		return nil, svcerr.WithCause(svcerr.Unavailable("git token service unavailable; retry"), err)
	}

	resp := &reliantv1.GetGitTokenResponse{}
	if err := tok.Use(func(access string) error {
		resp.AccessToken = access
		return nil
	}); err != nil || resp.AccessToken == "" {
		slog.ErrorContext(ctx, "git token empty", "user_id", userID, "provider", provider, "outcome", "unavailable")
		return nil, svcerr.WithCause(svcerr.Unavailable("git token service unavailable; retry"), err)
	}
	if tok.ExpiresAt != nil {
		resp.ExpiresAt = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	slog.InfoContext(ctx, "git token issued", "user_id", userID, "provider", provider, "outcome", "ok")
	return connect.NewResponse(resp), nil
}

// daemonCredential authenticates the bearer against the authority, uncached,
// and admits only a live daemon credential acting as a user.
func (s *Service) daemonCredential(ctx context.Context, header http.Header) (*fat.Principal, error) {
	bearer, ok := strings.CutPrefix(header.Get("Authorization"), "Bearer ")
	bearer = strings.TrimSpace(bearer)
	if !ok || !auth.IsAccessTokenFormat(bearer) {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("GetGitToken needs a Reliant daemon access token (rlat_) as its bearer"))
	}
	subject, err := s.deps.Authority.Introspect(ctx, bearer)
	if errors.Is(err, tokenauthority.ErrInactive) {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("this Reliant credential is no longer valid; sign in to Reliant again"))
	}
	if err != nil {
		return nil, svcerr.WithCause(svcerr.Unavailable("token service unavailable"), err)
	}
	if strings.TrimSpace(subject.ActingUserID) == "" {
		return nil, svcerr.ToConnect(svcerr.PermissionDenied("an organization token acts as no one, so it has no git account"))
	}
	if !subject.Scopes.Has(fat.ScopeDaemonConnect) {
		return nil, svcerr.ToConnect(svcerr.PermissionDenied("only a daemon credential can fetch a git token"))
	}
	return subject, nil
}
