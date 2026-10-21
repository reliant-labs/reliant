// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/vault"
)

// BootVault resolves the vault key per the boot policy (hosted: required;
// self-hosted: generated and persisted), enables credential sealing (api_keys
// and the provider sign-in token tables), seals any legacy plaintext
// api_keys rows, and copies the legacy credential tables into provider_accounts once. Both composition roots call it right after the
// repo is built, so a missing or malformed key stops startup.
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
	if err := sealAPIKeys(ctx, repo, v); err != nil {
		return nil, err
	}
	// Copy the legacy credential tables into provider_accounts, once. A failure must never stop
	// the server, and the marker is only written on success, so the next boot retries.
	if _, _, err := repo.MigrateLegacyCredentials(ctx); err != nil {
		slog.WarnContext(ctx, "copying legacy credentials into provider accounts failed; will retry next boot", "error", err)
	}
	return v, nil
}

// sealAPIKeys enables sealing, backfills legacy rows, and finalizes the
// api_keys contract. Readers are sealed-only, so a row left unsealed would be
// unreadable; refuse to start rather than serve with it.
func sealAPIKeys(ctx context.Context, repo *Repo, sealer CredentialSealer) error {
	repo.EnableCredentialSealing(sealer)
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
	slog.InfoContext(ctx, "vault ready", "api_keys_sealed_now", sealed)
	return nil
}
