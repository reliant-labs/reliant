// Copyright (c) 2025 Reliant Labs

package accountseal

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Credential is u's write of identity, with its secrets sealed for account accountID.
func Credential(ctx context.Context, s Sealer, owner core.AccountOwner, accountID string, u core.AccountUpsert, identity string, now time.Time) (store.ProviderAccountCredential, error) {
	secret, refresh, idToken, err := SealAll(ctx, s, owner, accountID, u.Secrets)
	if err != nil {
		return store.ProviderAccountCredential{}, err
	}
	return store.ProviderAccountCredential{
		AuthKind: u.AuthKind, IdentityKey: identity,
		Email: u.Email, ExternalAccountID: u.ExternalAccountID, OrganizationID: u.OrganizationID,
		OrganizationName: u.OrganizationName, Plan: u.Plan, PlanRaw: u.PlanRaw, KeyHint: u.KeyHint, Scope: u.Scope,
		Secret: secret, Refresh: refresh, IDToken: idToken, ExpiresAt: u.ExpiresAt, UpdatedAt: now,
	}, nil
}
