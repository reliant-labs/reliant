package features

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func TestIsReliantManagedAccessEnabledForContext_PreferenceOrder(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "test-user"

	t.Setenv(ReliantManagedAccessEnabledEnvVar, "")
	require.False(t, IsReliantManagedAccessEnabledForContext(ctx, repo, userID))

	require.NoError(t, repo.SetString(ctx, userID, nil, ReliantManagedAccessEnabledSetting, "true"))
	require.True(t, IsReliantManagedAccessEnabledForContext(ctx, repo, userID))

	t.Setenv(ReliantManagedAccessEnabledEnvVar, "false")
	require.False(t, IsReliantManagedAccessEnabledForContext(ctx, repo, userID))
	t.Setenv(ReliantManagedAccessEnabledEnvVar, "true")
	require.True(t, IsReliantManagedAccessEnabledForContext(ctx, repo, userID))
}
