// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/vault"
)

// BootVault resolves the vault key per the boot policy (hosted: required;
// self-hosted: generated and persisted) and enables credential sealing
// (api_keys and provider_accounts). Both composition roots call it right after
// the repo is built, so a missing or malformed key stops startup.
func BootVault(ctx context.Context, repo *Repo, hosted bool, dataDir, key string) (vault.Service, error) {
	v, err := vault.Boot(ctx, vault.BootOptions{
		Hosted:  hosted,
		Key:     key,
		DataDir: dataDir,
		DB:      repo.ORM(),
	})
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return v, nil
}
