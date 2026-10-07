// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/ghusers"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/vault"
)

// gitHubAccess adapts *ghaccess.Refresher to services.IntegrationAccess.
type gitHubAccess struct{ *ghaccess.Refresher }

func (gitHubAccess) IsPermanent(err error) bool { return ghaccess.IsPermanent(err) }

// gitHubSenders adapts *ghusers.Directory to services.SenderDirectory. It
// asks GitHub with the same token source the access refresher uses: the
// caller's own GitHub token, delegated by control-plane when hosted.
type gitHubSenders struct{ dir *ghusers.Directory }

func (g gitHubSenders) Resolve(ctx context.Context, userID string, handles, ids []string) ([]services.ResolvedSender, error) {
	found, err := g.dir.Resolve(ctx, userID, handles, ids)
	if err != nil {
		return nil, err
	}
	out := make([]services.ResolvedSender, 0, len(found))
	for _, f := range found {
		out = append(out, services.ResolvedSender{Query: f.Query, ID: f.User.ID, DisplayName: f.User.Login})
	}
	return out, nil
}

func (gitHubSenders) IsPermanent(err error) bool {
	return ghaccess.IsPermanent(err) || errors.Is(err, ghusers.ErrTokenRejected)
}

func (gitHubSenders) IsInvalid(err error) bool { return errors.Is(err, ghusers.ErrTooManyQueries) }

// wireGitHubSenders builds the GitHub people lookup behind "Only from", or
// nil under the same condition as wireGitHubAccess: a deployment that
// receives no GitHub webhooks has no GitHub triggers to restrict.
func wireGitHubSenders(repo *db.Repo, keys *vault.Vault, getenv func(string) string) (map[string]services.SenderDirectory, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv(github.SecretEnv)) == "" {
		return nil, nil
	}
	tokens, err := gitHubTokenSource(repo, keys, getenv)
	if err != nil {
		return nil, err
	}
	return map[string]services.SenderDirectory{
		ghusers.IntegrationID: gitHubSenders{dir: ghusers.New(tokens, ghusers.Options{})},
	}, nil
}

// wireGitHubAccess builds the refresher that keeps GitHub trigger owners'
// repository access fresh, or nil when this deployment receives no GitHub
// webhooks (RELIANT_GITHUB_WEBHOOK_SECRET unset), since nothing routes then.
//
// The user's token comes from control-plane when one is configured (hosted:
// the one GitHub App's user tokens live there), and from the user's saved
// GitHub connection otherwise (self-hosted, reliant's own App). A
// control-plane URL without INTERNAL_SERVICE_SECRET is a boot error, never a
// silent fall-back: hosted users have no saved connection, so falling back
// would refuse every GitHub trigger as "not connected".
func wireGitHubAccess(repo *db.Repo, keys *vault.Vault, getenv func(string) string) (*ghaccess.Refresher, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv(github.SecretEnv)) == "" {
		return nil, nil
	}
	tokens, err := gitHubTokenSource(repo, keys, getenv)
	if err != nil {
		return nil, err
	}
	return ghaccess.New(repo, tokens, ghaccess.Options{
		Logger: slog.Default().With("component", "ghaccess"),
	}), nil
}

func gitHubTokenSource(repo *db.Repo, keys *vault.Vault, getenv func(string) string) (ghaccess.TokenSource, error) {
	if cpURL := controlPlaneURLFrom(getenv); cpURL != "" {
		secret := strings.TrimSpace(getenv("INTERNAL_SERVICE_SECRET"))
		if secret == "" {
			return nil, errors.New("github triggers: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not; " +
				"hosted GitHub tokens come from control-plane and cannot be fetched unauthenticated")
		}
		return ghaccess.NewDelegatedTokens(gitcredentialclient.New(gitcredentialclient.Deps{
			BaseURL: cpURL,
			Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
		})), nil
	}
	if repo == nil {
		return nil, errors.New("github triggers: no database for saved GitHub connections")
	}
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), getenv)
	if err != nil {
		return nil, fmt.Errorf("github triggers: %w", err)
	}
	store := repo.Connections()
	return ghaccess.NewSavedTokens(store, connections.NewTokenSource(store, keys, providers, nil)), nil
}

// controlPlaneURLFrom reads the control-plane URL from the same variables, in
// the same order, as tokenauthority.ControlPlaneURL (and the worker's
// controlPlaneURLFrom), from an injectable getenv.
func controlPlaneURLFrom(getenv func(string) string) string {
	for _, key := range []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
