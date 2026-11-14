// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Tenant isolation through the real loader: whatever org or attendance a
// scope names, its candidates are its person's own accounts and nobody
// else's. Naming an org (yours, another person's, or one you are not in)
// never widens the set, and a scope with no person reads nothing.
func TestScopedLoader_CandidatesAreOnlyTheScopesPersonsAccounts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	previous := AccountAvailabilityClient
	AccountAvailabilityClient = noReportClients
	t.Cleanup(func() { AccountAvailabilityClient = previous })

	const alice, bob = "scope-alice", "scope-bob"
	own := map[string]map[string]bool{alice: {}, bob: {}}
	for _, user := range []string{alice, bob} {
		own[user][addClaude(t, repo, user, user+"-claude").ID] = true
		db.SeedProviderAccount(t, repo, user, "anthropic", "sk-ant-api03-"+user)
	}
	for _, user := range []string{alice, bob} {
		for _, acct := range availableFor(t, repo, user).Accounts["anthropic"] {
			own[user][acct.AccountID] = true
		}
		require.Len(t, own[user], 2, "each person has a Claude sign-in and an Anthropic key")
	}

	ctx := context.Background()
	for _, user := range []string{alice, bob} {
		personal := accountIDs(availableFor(t, repo, user))
		for name, scope := range map[string]Scope{
			"personal":                   PersonalScope(user),
			"in org A":                   {UserID: user, OrgID: "org-a", Attended: true},
			"in org A, unattended (any)": {UserID: user, OrgID: "org-a", Attended: false, UnattendedAnySource: true},
			"in the other person's org":  {UserID: user, OrgID: "org-of-" + otherOf(user, alice, bob), Attended: true},
			"in an org they are not in":  {UserID: user, OrgID: "org-nobody", Attended: false, UnattendedAnySource: true},
		} {
			t.Run(user+" "+name, func(t *testing.T) {
				for _, avail := range []models.AvailableDrivers{
					mustBuild(t, repo, scope),
					GetAvailableDrivers(ctx, scope),
					mustLookup(t, scope),
				} {
					require.NotEmpty(t, avail.Accounts, "the person's own accounts serve")
					for driver, accts := range avail.Accounts {
						for _, acct := range accts {
							assert.True(t, own[user][acct.AccountID], "%s account %s is not %s's", driver, acct.AccountID, user)
							assert.Equal(t, user, acct.UserID)
						}
					}
					for driver, cfg := range avail.Drivers {
						if cfg.AccountID == "" {
							continue
						}
						assert.True(t, own[user][cfg.AccountID], "%s default account %s is not %s's", driver, cfg.AccountID, user)
						assert.Equal(t, user, cfg.UserID)
					}
					assert.Equal(t, personal, accountIDs(avail), "naming an org that gave the person nothing changes nothing")
				}
			})
		}
	}

	t.Run("a scope with no person reads nothing", func(t *testing.T) {
		_, err := buildAvailableDrivers(ctx, repo, Scope{OrgID: "org-a", Attended: true}, noReportClients)
		require.Error(t, err)
		_, err = LookupAvailableDrivers(ctx, Scope{OrgID: "org-a"})
		require.True(t, errors.Is(err, ErrDriverLookupFailed), "got %v", err)
		assert.Empty(t, GetAvailableDrivers(ctx, Scope{OrgID: "org-a"}).Accounts)
	})
}

func otherOf(user, a, b string) string {
	if user == a {
		return b
	}
	return a
}

func mustBuild(t *testing.T, repo *db.Repo, scope Scope) models.AvailableDrivers {
	t.Helper()
	avail, err := buildAvailableDrivers(context.Background(), repo, scope, noReportClients)
	require.NoError(t, err)
	return avail
}

func mustLookup(t *testing.T, scope Scope) models.AvailableDrivers {
	t.Helper()
	avail, err := LookupAvailableDrivers(context.Background(), scope)
	require.NoError(t, err)
	return avail
}

func accountIDs(avail models.AvailableDrivers) []string {
	var ids []string
	for _, accts := range avail.Accounts {
		for _, acct := range accts {
			ids = append(ids, acct.AccountID)
		}
	}
	sort.Strings(ids)
	return ids
}
