// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/vault"
)

// wiredConnections is what the api-server serves connections with.
type wiredConnections struct {
	service *connections.Service
	oauth   *connections.OAuthHTTP
	// tokens opens a connection's stored credential for its owner. The
	// inbound receiver verifies Twilio deliveries with it (each account
	// signs with its own Auth Token).
	tokens *connections.TokenSource
}

// wireConnections builds the connection service, the token source and the
// OAuth broker's HTTP callback. A provider whose client credentials are unset
// is listed as unavailable; it never fails boot.
//
// appOrigins is the deployment's CORS allow-list: the origins the web app is
// served from, which are the only ones (besides PUBLIC_URL's and loopback) a
// browser OAuth flow is relayed back to.
func wireConnections(repo *db.Repo, keys *vault.Vault, publicURL string, appOrigins []string) (*wiredConnections, error) {
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, keys, providers, nil)
	broker := connections.NewBroker(store, keys, providers, nil, publicURL).WithAppOrigins(appOrigins)
	svc := connections.NewService(store, keys, providers, tokens, broker, nil).
		WithDelegated(delegatedAvailable(tokenauthority.ControlPlaneURL() != ""))
	for _, in := range svc.ListIntegrations() {
		for _, m := range in.Methods {
			if !m.Available {
				logging.Info("connection method unavailable", "integration", in.ID, "method", m.Kind, "reason", m.Reason)
			}
		}
	}
	return &wiredConnections{service: svc, oauth: connections.NewOAuthHTTP(broker), tokens: tokens}, nil
}

// wireCatalogSearch builds the integration catalog search: the embedded
// catalog's index (built once, shared) plus each caller's connections, with
// delegated brokers judged by the same rule ListIntegrations uses.
func wireCatalogSearch(repo *db.Repo) (*catalogindex.Service, error) {
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil, fmt.Errorf("integration catalog index: %w", err)
	}
	delegated := delegatedAvailable(tokenauthority.ControlPlaneURL() != "")
	return catalogindex.NewService(idx, repo.Connections(), catalogindex.DelegatedAvailable(delegated)), nil
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
