package drivers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider counts as available only if its driver is registered: xai/groq/
// azure/bedrock have catalog models but no driver, so a key for them must not
// make their models selectable.
func TestBuildAvailableDrivers_SkipsProvidersWithoutRegisteredDriver(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	userID := "test-user"

	for id, key := range map[string]string{
		"xai": "xai-key", "groq": "gsk-key", "openai": "sk-openai", "openrouter": "sk-or",
	} {
		require.NoError(t, repo.SetProviderAPIKey(ctx, userID, id, key))
	}

	avail, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	assert.NotContains(t, avail.Drivers, models.DriverID("xai"))
	assert.NotContains(t, avail.Drivers, models.DriverID("groq"))
	assert.Contains(t, avail.Drivers, models.DriverID("openai"))
	assert.Contains(t, avail.Drivers, models.DriverID("openrouter"))
}
