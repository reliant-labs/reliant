// Copyright (c) 2025 Reliant Labs

package serverworker

import (
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
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
