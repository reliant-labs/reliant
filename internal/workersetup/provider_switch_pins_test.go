// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Clear incompatible pins immediately before assigning on the new provider,
// without clearing account limits or touching another user's thread. The
// router can keep a compatible account for the new provider (its prompt cache
// is still useful), but no old-provider assignment survives the switch.
func TestProviderSwitchClearsOnlyIncompatibleThreadAccountPins(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	user, chatID := uuid.NewString(), uuid.NewString()
	projectID := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "switch", Path: t.TempDir()}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: user}))
	thread := uuid.NewString()
	_, err := repo.CreateThread(ctx, &db.Thread{ID: thread, ChatID: chatID})
	require.NoError(t, err)
	add := func(provider string) core.ProviderAccount {
		a, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			UserID: user, Provider: provider, AuthKind: core.AuthKindOAuth,
			IdentityKey: "switch-" + provider, Secrets: core.AccountSecrets{Secret: "test-key"},
		})
		require.NoError(t, err)
		_, err = repo.SetThreadAssignment(ctx, core.Assignment{
			ThreadID: thread, Provider: provider, AccountID: a.ID, AssignedAt: time.Now(), Reason: "initial",
		}, "")
		require.NoError(t, err)
		return a
	}
	old, chosen := add("claude"), add("codex")
	until := time.Now().Add(time.Hour)
	require.NoError(t, repo.BenchAccount(ctx, user, old.ID, until, "five_hour", false))
	routing := accountRouting(repo, nil)
	require.NotNil(t, routing.ClearOtherAssignments)
	require.NoError(t, routing.ClearOtherAssignments(ctx, "someone-else", thread, "codex"))
	pins, err := repo.ListThreadAssignments(ctx, thread)
	require.NoError(t, err)
	require.Len(t, pins, 2, "foreign users cannot clear pins")
	require.NoError(t, routing.ClearOtherAssignments(ctx, user, thread, "codex"))
	pins, err = repo.ListThreadAssignments(ctx, thread)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	require.Equal(t, "codex", pins[0].Provider)
	assigned, err := routing.Router.Assign(ctx, accountrouter.Request{
		UserID: user, ThreadID: thread, Driver: "codex", ModelID: "gpt-6.1-sol",
		Accounts: []models.AccountConfig{{DriverConfig: models.DriverConfig{AccountID: chosen.ID}}},
	})
	require.NoError(t, err)
	require.Equal(t, chosen.ID, assigned.Account.AccountID)
	limits, err := repo.ListAccountLimits(ctx, user)
	require.NoError(t, err)
	require.NotNil(t, limits[old.ID].LimitedUntil, "other chats still respect the old account's limit")
}
