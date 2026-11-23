// Copyright (c) 2025 Reliant Labs

package orgaccounts_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/credentials/accountseal"
	"github.com/reliant-labs/reliant/internal/credentials/orgaccounts"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// env is one test's database, its vault, the person/owner account store the tests seed through,
// and the org administration under test.
type env struct {
	orgaccounts.Service
	accounts provideraccounts.Service
	repo     *db.Repo
	vault    vault.Service
}

func newEnv(t *testing.T) (*env, *sql.DB) {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	return newEnvOver(t, repo, db.NewTestVault(t, repo)), raw
}

func newEnvOver(t *testing.T, repo *db.Repo, v vault.Service) *env {
	t.Helper()
	org, err := orgaccounts.New(orgaccounts.Deps{DB: repo.ORM(), Sealer: v})
	require.NoError(t, err)
	acc, err := provideraccounts.New(provideraccounts.Deps{DB: repo.ORM(), Sealer: v})
	require.NoError(t, err)
	return &env{Service: org, accounts: acc, repo: repo, vault: v}
}

func ProviderAccountAAD(accountID, column string) []byte { return accountseal.AAD(accountID, column) }

const accountColSecret = accountseal.ColSecret

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

func mustUpsert(t *testing.T, e *env, u core.AccountUpsert) core.ProviderAccount {
	t.Helper()
	a, _, err := e.accounts.UpsertProviderAccount(context.Background(), u)
	require.NoError(t, err)
	return a
}

func createTestChat(t *testing.T, e *env) string {
	t.Helper()
	chatID := uuid.New().String()
	now := time.Now()
	require.NoError(t, e.repo.CreateChat(context.Background(), &db.Chat{
		ID: chatID, Title: "Test Chat", ProjectID: "test-project", UserID: "test-user",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return chatID
}

func insertTestThread(t *testing.T, e *env, id, chatID, workflowID string) {
	t.Helper()
	wf := workflowID
	_, err := e.repo.CreateThread(context.Background(), &db.Thread{
		ID: id, ChatID: chatID, WorkflowID: &wf, CreatedAt: time.Now(), Status: db.ThreadStatusRunning,
	})
	require.NoError(t, err)
}

func orgTenant(id string) vault.Tenant { return vault.OrgTenant(id) }
