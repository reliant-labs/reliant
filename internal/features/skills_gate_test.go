package features

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func TestIsSkillsEnabledForContext_PreferenceOrder(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "test-user"

	// Default fallback (no env, no setting) should be disabled.
	t.Setenv(SkillsEnabledEnvVar, "")
	require.False(t, IsSkillsEnabledForContext(ctx, repo, userID))

	// Settings enablement should activate the feature.
	require.NoError(t, repo.SetString(ctx, userID, nil, SkillsEnabledSetting, "true"))
	require.True(t, IsSkillsEnabledForContext(ctx, repo, userID))

	// Environment override should take precedence.
	t.Setenv(SkillsEnabledEnvVar, "false")
	require.False(t, IsSkillsEnabledForContext(ctx, repo, userID))
	t.Setenv(SkillsEnabledEnvVar, "true")
	require.True(t, IsSkillsEnabledForContext(ctx, repo, userID))
}
