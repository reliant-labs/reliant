// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"fmt"
	"net/http"
	"os"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/vault"
)

// wireConnections builds the connection service and the OAuth broker's HTTP
// routes. A provider whose client credentials are unset is listed as
// unavailable; it never fails boot.
func wireConnections(repo *db.Repo, keys *vault.Vault, jwtPublicKey, jwksURL, publicURL string) (*connections.Service, *connections.OAuthHTTP, error) {
	providers, err := connections.ProvidersFromEnv(os.Getenv)
	if err != nil {
		return nil, nil, fmt.Errorf("connections: %w", err)
	}
	for _, p := range providers.List() {
		if !p.Available() {
			logging.Info("connection integration unavailable", "integration", p.ID, "reason", p.UnavailableReason())
		}
	}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, keys, providers, nil)
	broker := connections.NewBroker(store, keys, providers, nil, publicURL)
	svc := connections.NewService(store, keys, providers, tokens, broker, nil)

	authn, err := auth.NewMiddleware(jwtPublicKey, jwksURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connections: http auth: %w", err)
	}
	routes := connections.NewOAuthHTTP(broker, func(next http.Handler) http.Handler { return authn.RequireAuth(next) }, publicURL)
	return svc, routes, nil
}
