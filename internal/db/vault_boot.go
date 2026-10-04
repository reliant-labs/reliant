// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
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
	if err := sealAPIKeys(ctx, repo, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Connections returns the store for user connections (the credential vault's
// metadata and ciphertext tables).
func (r *Repo) Connections() core.ConnectionStore {
	return postgresstore.NewConnectionStore(r.DB.SQLDB())
}

// sealAPIKeys enables sealing, backfills legacy rows, and finalizes the
// api_keys contract. Readers are sealed-only, so a row left unsealed would be
// unreadable; refuse to start rather than serve with it.
func sealAPIKeys(ctx context.Context, repo *Repo, sealer postgresstore.APIKeySealer) error {
	if err := repo.EnableAPIKeySealing(sealer); err != nil {
		return err
	}
	sealed, err := repo.BackfillAPIKeys(ctx, 200)
	if err != nil {
		return fmt.Errorf("vault: backfilling api_keys: %w", err)
	}
	remaining, err := repo.CountUnsealedAPIKeys(ctx)
	if err != nil {
		return fmt.Errorf("vault: counting unsealed api_keys: %w", err)
	}
	if remaining > 0 {
		return fmt.Errorf("vault: %d api_keys row(s) still have no sealed value after backfill; refusing to start because readers no longer fall back to plaintext (a concurrent writer likely raced the backfill: restart, and if it persists re-save those keys)", remaining)
	}
	if err := repo.ValidateAPIKeysSealedConstraint(ctx); err != nil {
		return fmt.Errorf("vault: validating api_keys sealed constraint: %w", err)
	}
	slog.Info("vault ready", "api_keys_sealed_now", sealed)
	return nil
}
