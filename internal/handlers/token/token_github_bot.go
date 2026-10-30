// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

// Reason codes GetGitHubBotToken puts in x-forge-error-reason, beside
// GetGitToken's. They are control-plane's names, so a client sees one
// vocabulary end to end.
const (
	reasonAppNotConfigured = "github_app_not_configured"
	reasonAppNotInstalled  = "github_app_not_installed"
	reasonAppPermissions   = "github_app_permissions"
	reasonOwnerCannotPush  = "github_owner_cannot_push"
)

// githubName is what GitHub allows in an owner or repository name. A name
// outside it is refused here rather than sent on to be refused there.
var githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// botPermissionNames are the permissions control-plane mints an installation
// token with; a request names a subset.
var botPermissionNames = map[string]bool{"issues": true, "pull_requests": true, "contents": true}

// GetGitHubBotToken mints a GitHub App installation token for one repository,
// for the calling daemon's acting user. It authenticates its own bearer,
// uncached, and admits only a daemon credential, exactly as GetGitToken.
// control-plane refuses the mint unless that user can push to the repository.
// The token is passed through: never stored, cached or logged here.
func (s *Service) GetGitHubBotToken(
	ctx context.Context, req *connect.Request[reliantv1.GetGitHubBotTokenRequest],
) (*connect.Response[reliantv1.GetGitHubBotTokenResponse], error) {
	subject, err := s.daemonCredential(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	userID := subject.ActingUserID
	owner, repo := strings.TrimSpace(req.Msg.GetOwner()), strings.TrimSpace(req.Msg.GetRepo())
	if !githubName.MatchString(owner) || !githubName.MatchString(repo) || repo == "." || repo == ".." {
		return nil, svcerr.InvalidArgument("owner and repo must name one GitHub repository, e.g. owner=acme repo=api")
	}
	perms := map[string]string{}
	for name, level := range req.Msg.GetPermissions() {
		name, level = strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(level))
		if !botPermissionNames[name] {
			return nil, svcerr.InvalidArgument("permission " + name + " is not available to the bot (allowed: issues, pull_requests, contents)")
		}
		if level != "read" && level != "write" {
			return nil, svcerr.InvalidArgument("permission " + name + " must be read or write")
		}
		perms[name] = level
	}
	if s.deps.GitBotTokens == nil {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"this server has no control plane, so it has no GitHub App to act as"), reasonGitNoControlPlane)
	}

	repository := owner + "/" + repo
	tok, err := s.deps.GitBotTokens.MintInstallationToken(ctx, gitcredentialclient.InstallationRequest{
		ExternalUserID: userID, Owner: owner, Repo: repo, Permissions: perms,
	})
	if err != nil {
		return nil, botMintError(ctx, userID, repository, err)
	}
	resp := &reliantv1.GetGitHubBotTokenResponse{
		BotLogin:       tok.BotLogin,
		ActingForLogin: tok.ActingForLogin,
		Repository:     tok.Repository,
	}
	if err := tok.Token.Use(func(access string) error {
		resp.Token = access
		return nil
	}); err != nil || resp.Token == "" {
		slog.ErrorContext(ctx, "github bot token empty", "user_id", userID, "repository", repository, "outcome", "unavailable")
		return nil, svcerr.WithCause(svcerr.Unavailable("GitHub App token service unavailable; retry"), err)
	}
	if !tok.ExpiresAt.IsZero() {
		resp.ExpiresAt = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	slog.InfoContext(ctx, "github bot token issued", "user_id", userID, "repository", repository, "outcome", "ok")
	return connect.NewResponse(resp), nil
}

// botMintError maps a refused mint onto the wire. Each refusal a person acts on
// says what to do; the cause never reaches the caller (it may quote GitHub).
func botMintError(ctx context.Context, userID, repository string, err error) error {
	refuse := func(e error, reason string) error {
		slog.InfoContext(ctx, "github bot token refused", "user_id", userID, "repository", repository, "outcome", reason)
		return svcerr.WithReason(e, reason)
	}
	switch {
	case errors.Is(err, gitcredentialclient.ErrOwnerCannotPush):
		return refuse(svcerr.PermissionDenied(
			"you cannot push to "+repository+", so the Reliant bot will not write there for you"), reasonOwnerCannotPush)
	case errors.Is(err, gitcredentialclient.ErrAppNotInstalled):
		return refuse(svcerr.FailedPrecondition(
			"the Reliant GitHub App is not installed on "+repository), reasonAppNotInstalled)
	case errors.Is(err, gitcredentialclient.ErrAppPermissions):
		return refuse(svcerr.FailedPrecondition(
			"the Reliant GitHub App has not been granted a permission this token needs: accept its updated permissions in the installation settings"), reasonAppPermissions)
	case errors.Is(err, gitcredentialclient.ErrAppNotConfigured):
		slog.ErrorContext(ctx, "github bot token refused", "user_id", userID, "repository", repository, "outcome", reasonAppNotConfigured)
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"this deployment's control plane has no GitHub App key, so it cannot mint a bot token"), reasonAppNotConfigured)
	case errors.Is(err, gitcredentialclient.ErrNotConnected):
		return refuse(svcerr.FailedPrecondition(
			"connect your GitHub account in Reliant first: the bot writes only where you can push, which Reliant checks with your connection"), reasonGitNotConnected)
	case errors.Is(err, gitcredentialclient.ErrNeedsReconnect):
		return refuse(svcerr.FailedPrecondition(
			"your GitHub connection expired; reconnect it in Reliant"), reasonGitNeedsReconnect)
	}
	slog.ErrorContext(ctx, "github bot token unavailable", "user_id", userID, "repository", repository, "outcome", "unavailable")
	return svcerr.WithCause(svcerr.Unavailable("GitHub App token service unavailable; retry"), err)
}
