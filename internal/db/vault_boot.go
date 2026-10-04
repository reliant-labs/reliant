// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/vault"
)

// BootVault resolves the vault key per the boot policy (hosted: required;
// self-hosted: generated and persisted), enables api_keys sealing, and seals
// any legacy plaintext rows. Both composition roots call it right after the
// repo is built, so a missing or malformed key stops startup.
func BootVault(ctx context.Context, repo *Repo, hosted bool, dataDir string) (*vault.Vault, error) {
	v, err := vault.Boot(ctx, vault.BootOptions{
		Hosted:  hosted,
		DataDir: dataDir,
		DB:      repo.DB.SQLDB(),
	})
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if err := repo.EnableAPIKeySealing(v); err != nil {
		return nil, err
	}
	sealed, err := repo.BackfillAPIKeys(ctx, 200)
	if err != nil {
		return nil, fmt.Errorf("vault: backfilling api_keys: %w", err)
	}
	remaining, err := repo.CountUnsealedAPIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("vault: counting unsealed api_keys: %w", err)
	}
	slog.Info("vault ready", "api_keys_sealed_now", sealed, "api_keys_unsealed", remaining)
	return v, nil
}
