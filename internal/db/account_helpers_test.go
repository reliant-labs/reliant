// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/credentials/accountseal"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The org-administration tests that remain here (until they move with internal/credentials/orgaccounts)
// seed accounts through the provider-account store.

const (
	accountColSecret = accountseal.ColSecret
)

func ProviderAccountAAD(accountID, column string) []byte { return accountseal.AAD(accountID, column) }

func orgKeyUpsert(org, creator, label, key string) core.AccountUpsert {
	return core.AccountUpsert{
		Owner: core.OrgAccountOwner(org), OrgAccess: core.OrgAccessMembers, CreatedByUserID: creator,
		Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:" + label,
		KeyHint: key[len(key)-4:], Secrets: core.AccountSecrets{Secret: key},
	}
}

func apiKeyUpsert(user, label, key string) core.AccountUpsert {
	return core.AccountUpsert{
		UserID: user, Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:" + label,
		KeyHint: key[len(key)-4:], Secrets: core.AccountSecrets{Secret: key},
	}
}

func claudeUpsert(user, acct, org, secret, refresh string) core.AccountUpsert {
	return core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey(acct, org), Email: acct + "@example.com",
		ExternalAccountID: acct, OrganizationID: org, OrganizationName: "Org " + org,
		Secrets:   core.AccountSecrets{Secret: secret, Refresh: refresh},
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
}

func mustUpsert(t *testing.T, repo *Repo, u core.AccountUpsert) core.ProviderAccount {
	t.Helper()
	a, _, err := TestAccounts(t, repo).UpsertProviderAccount(context.Background(), u)
	require.NoError(t, err)
	return a
}
