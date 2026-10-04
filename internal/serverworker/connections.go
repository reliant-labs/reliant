// Copyright (c) 2025 Reliant Labs

package serverworker

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/vault"
)

// newConnectionResolver builds the worker-side resolver. It shares the vault
// key with the api-server: the worker is the only process that reads secrets,
// and the only one besides the api-server that writes them (on refresh).
func newConnectionResolver(repo *db.Repo, keys *vault.Vault) (*connections.Resolver, error) {
	providers, err := connections.ProvidersFromEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, keys, providers, nil)
	return connections.NewResolver(repo, store, tokens), nil
}

// newIntegrationCredentials is the credential source integration actions
// authenticate with: saved connections, plus — when a control plane is
// configured — GitHub tokens delegated by control-plane, the GitHub token
// authority. Self-hosted, `github` stays a saved connection through reliant's
// own provider.
//
// A control-plane URL without INTERNAL_SERVICE_SECRET is a boot error, never a
// silent fall-back to the self-hosted provider: hosted users' GitHub tokens
// live at control-plane, so falling back would answer "not connected" for
// every one of them.
func newIntegrationCredentials(resolver *connections.Resolver, getenv func(string) string) (httpaction.CredentialSource, error) {
	saved := connauth.New(resolver)
	cpURL := controlPlaneURLFrom(getenv)
	if cpURL == "" {
		return saved, nil
	}
	secret := strings.TrimSpace(getenv("INTERNAL_SERVICE_SECRET"))
	if secret == "" {
		return nil, errors.New("integrations: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not; " +
			"hosted GitHub tokens come from control-plane and cannot be fetched unauthenticated")
	}
	client := gitcredentialclient.New(gitcredentialclient.Deps{
		BaseURL: cpURL,
		Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	broker := ghdelegated.NewBroker(client, slog.Default().With("component", "ghdelegated"))
	return ghdelegated.NewSource(resolver, broker, saved), nil
}

// controlPlaneURLFrom reads the control-plane URL from the same variables, in
// the same order, as tokenauthority.ControlPlaneURL — so the worker's notion
// of "hosted" is the one BootVault and the token authority already use —
// from an injectable getenv.
func controlPlaneURLFrom(getenv func(string) string) string {
	for _, key := range []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
