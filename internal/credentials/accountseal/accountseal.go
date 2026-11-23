// Copyright (c) 2025 Reliant Labs

// Package accountseal seals and opens the secret columns of a provider account.
// It is the one place that decides which vault tenant and which AAD an account's
// ciphertext is bound to, shared by the credential packages that write or read
// provider_accounts (provideraccounts, orgaccounts).
//
// Each ciphertext is bound to its account id and column, and to the owner's tenant
// (the person's, or the org's), so a value copied to another account row, another
// user, another org or another column does not open. The tenant is chosen from the
// owner's kind, never from the stored user_id, so an org's account never opens under
// any person's tenant (UserTenant("org:<id>") included).
//
// Only functions are exported: it holds no state and offers no contract.
package accountseal

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Sealed account columns.
const (
	ColSecret  = "secret"
	ColRefresh = "refresh"
	ColIDToken = "id_token"

	table = "provider_accounts"
)

// ErrVaultDisabled is what sealing or opening returns without a vault.
var ErrVaultDisabled = errors.New("provider accounts require the credential vault; none is configured")

// Sealer is the slice of the credential vault the account columns are sealed with.
// vault.Service satisfies it.
type Sealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// AAD is the vault AAD of one sealed column of one account.
func AAD(accountID, column string) []byte {
	return []byte(table + "\x00" + accountID + "\x00" + column)
}

// Tenant is the vault tenant an owner's account secrets are sealed under.
func Tenant(owner core.AccountOwner) (vault.Tenant, error) {
	if !owner.Valid() {
		return vault.Tenant{}, fmt.Errorf("invalid provider account owner %q/%q", owner.Kind, owner.ID)
	}
	if owner.IsOrg() {
		return vault.OrgTenant(owner.ID), nil
	}
	return vault.UserTenant(owner.ID), nil
}

// Seal seals one column. An empty plaintext is a NULL column (nil).
func Seal(ctx context.Context, s Sealer, owner core.AccountOwner, accountID, column, plaintext string) ([]byte, error) {
	if s == nil {
		return nil, ErrVaultDisabled
	}
	if plaintext == "" {
		return nil, nil
	}
	tenant, err := Tenant(owner)
	if err != nil {
		return nil, err
	}
	sealed, err := s.Seal(ctx, tenant, []byte(plaintext), AAD(accountID, column))
	if err != nil {
		return nil, fmt.Errorf("sealing provider account %s.%s: %w", accountID, column, err)
	}
	return sealed, nil
}

// Open opens one column. A NULL column opens to "".
func Open(ctx context.Context, s Sealer, owner core.AccountOwner, accountID, column string, sealed []byte) (string, error) {
	if s == nil {
		return "", ErrVaultDisabled
	}
	if len(sealed) == 0 {
		return "", nil
	}
	tenant, err := Tenant(owner)
	if err != nil {
		return "", err
	}
	pt, err := s.Open(ctx, tenant, sealed, AAD(accountID, column))
	if err != nil {
		return "", fmt.Errorf("opening provider account %s.%s: %w", accountID, column, err)
	}
	defer clear(pt)
	return string(pt), nil
}

// SealAll seals an account's three secret columns.
func SealAll(ctx context.Context, s Sealer, owner core.AccountOwner, accountID string, sec core.AccountSecrets) (secret, refresh, idToken []byte, err error) {
	if secret, err = Seal(ctx, s, owner, accountID, ColSecret, sec.Secret); err != nil {
		return nil, nil, nil, err
	}
	if refresh, err = Seal(ctx, s, owner, accountID, ColRefresh, sec.Refresh); err != nil {
		return nil, nil, nil, err
	}
	if idToken, err = Seal(ctx, s, owner, accountID, ColIDToken, sec.IDToken); err != nil {
		return nil, nil, nil, err
	}
	return secret, refresh, idToken, nil
}

// OpenAll opens an account's three secret columns.
func OpenAll(ctx context.Context, s Sealer, owner core.AccountOwner, accountID string, secret, refresh, idToken []byte) (core.AccountSecrets, error) {
	var out core.AccountSecrets
	var err error
	if out.Secret, err = Open(ctx, s, owner, accountID, ColSecret, secret); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.Refresh, err = Open(ctx, s, owner, accountID, ColRefresh, refresh); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.IDToken, err = Open(ctx, s, owner, accountID, ColIDToken, idToken); err != nil {
		return core.AccountSecrets{}, err
	}
	return out, nil
}
