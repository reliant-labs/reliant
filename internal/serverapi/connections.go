// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"fmt"
	"net/http"
	"os"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/vault"
)

// wireConnections builds the connection service and the OAuth broker's HTTP
// routes. A provider whose client credentials are unset is listed as
// unavailable; it never fails boot.
func wireConnections(repo *db.Repo, keys *vault.Vault, jwtPublicKey, jwksURL, publicURL string) (*connections.Service, *connections.OAuthHTTP, error) {
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), os.Getenv)
	if err != nil {
		return nil, nil, fmt.Errorf("connections: %w", err)
	}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, keys, providers, nil)
	broker := connections.NewBroker(store, keys, providers, nil, publicURL)
	svc := connections.NewService(store, keys, providers, tokens, broker, nil).
		WithDelegated(delegatedAvailable(tokenauthority.ControlPlaneURL() != ""))
	for _, in := range svc.ListIntegrations() {
		for _, m := range in.Methods {
			if !m.Available {
				logging.Info("connection method unavailable", "integration", in.ID, "method", m.Kind, "reason", m.Reason)
			}
		}
	}

	authn, err := auth.NewMiddleware(jwtPublicKey, jwksURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connections: http auth: %w", err)
	}
	routes := connections.NewOAuthHTTP(broker, func(next http.Handler) http.Handler { return authn.RequireAuth(next) }, publicURL)
	return svc, routes, nil
}

// delegatedAvailable reports which delegated brokers the worker registers.
// The api-server holds no brokers (credentials resolve on the worker), so it
// mirrors the worker's rule: the control-plane GitHub broker exists exactly
// when a control plane is configured (serverworker.newIntegrationCredentials).
func delegatedAvailable(hosted bool) connections.DelegatedAvailable {
	return func(brokerID string) (bool, string) {
		if brokerID == ghdelegated.BrokerID {
			if hosted {
				return true, ""
			}
			return false, "no control plane is configured (RELIANT_CONTROL_PLANE_URL), so GitHub cannot be delegated to it"
		}
		return false, "delegated broker " + brokerID + " is not registered on this deployment"
	}
}
