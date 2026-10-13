// Copyright (c) 2025 Reliant Labs

// Package vault seals secrets at rest. A single key-encryption key (KEK, from
// RELIANT_VAULT_KEY) wraps one random data-encryption key (DEK) per tenant;
// DEKs seal the values. Ciphertext and wrapped DEKs live in reliant Postgres.
// See research/CONNECTIONS_VAULT.md.
//
//forge:not-a-component: built by reliant's hand-wired servers (db.BootVault, from internal/serverapi and internal/serverworker), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package vault

import (
	"context"
	"database/sql"
)

// Service seals and opens values per tenant.
type Service interface {
	// Seal encrypts plaintext under the tenant's primary DEK, creating the DEK
	// on first use. aad is bound to the ciphertext together with the tenant,
	// so a blob copied to another row or another user's row does not open.
	Seal(ctx context.Context, tenant Tenant, plaintext, aad []byte) ([]byte, error)
	// Open decrypts a blob produced by Seal for the same tenant and aad.
	Open(ctx context.Context, tenant Tenant, ciphertext, aad []byte) ([]byte, error)
	// OpenSecret is Open, returning a value that cannot be printed.
	OpenSecret(ctx context.Context, tenant Tenant, ciphertext, aad []byte) (Secret, error)
}

// DBTX is the slice of *sql.DB the vault needs.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// KeyWrapper wraps and unwraps per-tenant DEKs. It is the seam a KMS or Bao
// Transit implementation would later fill without touching stored secrets.
type KeyWrapper interface {
	// Wrap seals dek under the current primary KEK and reports which KEK id did.
	Wrap(dek, aad []byte) (wrapped []byte, kekID string, err error)
	Unwrap(wrapped, aad []byte) ([]byte, error)
}

// Deps are the vault's collaborators.
type Deps struct {
	// DB holds the wrapped DEKs (vault_keys).
	DB DBTX
	// Keys wraps and unwraps DEKs under the KEK (NewEnvKeyWrapper).
	Keys KeyWrapper
}
