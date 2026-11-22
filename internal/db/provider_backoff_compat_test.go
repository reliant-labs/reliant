package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The deprecated Repo forwarders still read and write what the
// providerbackoff package does: a wait recorded through one is seen through
// the other, and cleared through either.
func TestRepoProviderBackoffForwarders(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)
	svc, err := repo.providerBackoff()
	require.NoError(t, err)

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, "thread-1", "anthropic", false, now.Add(time.Hour), now))

	viaRepo, err := repo.ProviderBackoffByChat(ctx, chatID)
	require.NoError(t, err)
	viaService, err := svc.ProviderBackoffByChat(ctx, chatID)
	require.NoError(t, err)
	require.Equal(t, viaService, viaRepo)
	require.True(t, viaRepo["thread-1"].Waiting())
	require.Equal(t, ReasonUsageLimit, viaRepo["thread-1"].Reason)

	held, err := repo.ChatsHeldOnUsageLimit(ctx, []string{chatID}, now.Add(-time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{chatID}, held)

	require.NoError(t, repo.ClearUsageLimitWait(ctx, chatID, "", now.Add(time.Minute)))
	after, err := repo.ProviderBackoffByChat(ctx, chatID)
	require.NoError(t, err)
	require.False(t, after["thread-1"].Waiting())
}
