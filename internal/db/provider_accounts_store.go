// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/credentials/accountseal"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// accountStore is what is left of the facade's provider-account store: the org administration
// (org_provider_accounts.go) that has not moved to internal/credentials/orgaccounts yet. Personal
// and owner accounts, limits and thread assignments are internal/credentials/provideraccounts.
type accountStore struct {
	db     orm.Context
	sealer accountseal.Sealer
	now    func() time.Time
}

// newAccountStore creates the store. Without SetSealer every secret write fails.
func newAccountStore(db orm.Context) *accountStore {
	return &accountStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// SetSealer enables the credential vault.
func (s *accountStore) SetSealer(sealer accountseal.Sealer) { s.sealer = sealer }

// SetClock replaces the clock used for timestamps the store stamps itself. For tests.
func (s *accountStore) SetClock(now func() time.Time) { s.now = now }

// CredentialSealer is the slice of the credential vault the facade's account store seals with.
type CredentialSealer = accountseal.Sealer

// inTx runs f in a READ COMMITTED transaction, or in the one ctx already carries. Writers of one
// (user, provider) serialize on an advisory lock rather than relying on serialization retries,
// and a transaction is never retried: f seals with fresh randomness and reads under its locks,
// so it is only correct as the one attempt.
func (s *accountStore) inTx(ctx context.Context, f func(ctx context.Context) error) error {
	return s.db.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, f)
}

// credential is u's write of identity, with its secrets sealed for account accountID.
func (s *accountStore) credential(ctx context.Context, owner core.AccountOwner, accountID string, u core.AccountUpsert, identity string) (store.ProviderAccountCredential, error) {
	return accountseal.Credential(ctx, s.sealer, owner, accountID, u, identity, s.now())
}
