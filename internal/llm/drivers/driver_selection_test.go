// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Import drivers to trigger their init() functions which register models
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
)

func TestSelectBestDriver_OnlyOpenRouterConfigured_ShouldUseOpenRouter(t *testing.T) {
	// Setup: Create available drivers with only OpenRouter configured
	// When a user configures OpenRouter, BuildAvailableDrivers does NOT add the fallback
	availableDrivers := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{
			models.DriverID("openrouter"): {
				DriverID: models.DriverID("openrouter"),
				APIKey:   "sk-or-v1-user-configured-key",
				BaseURL:  "https://openrouter.ai/api/v1",
				Enabled:  true,
			},
		},
	}

	// Test with Claude Sonnet 4.5 - a model supported by OpenRouter
	modelID := models.Claude45Sonnet
	driverConfig, found := models.SelectBestDriver(modelID, availableDrivers)

	require.True(t, found, "Should find a driver for Claude Sonnet 4.5")

	// Should use the user's configured OpenRouter driver
	assert.Equal(t, models.DriverID("openrouter"), driverConfig.DriverID,
		"Should use OpenRouter when it's the only configured driver")
	assert.Equal(t, "sk-or-v1-user-configured-key", driverConfig.APIKey,
		"Should use the user's configured OpenRouter key")
}

func TestSelectBestDriver_BothConfigured_PrefersNative(t *testing.T) {
	// Setup: Create available drivers with both OpenRouter and Anthropic configured
	availableDrivers := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{
			models.DriverID("openrouter"): {
				DriverID: models.DriverID("openrouter"),
				APIKey:   "sk-or-v1-user-key",
				BaseURL:  "https://openrouter.ai/api/v1",
				Enabled:  true,
			},
			models.DriverID("anthropic"): {
				DriverID: models.DriverID("anthropic"),
				APIKey:   "sk-ant-api-user-key",
				Enabled:  true,
			},
		},
	}

	// Test with Claude Sonnet 4.5 - a model supported by both drivers
	modelID := models.Claude45Sonnet
	driverConfig, found := models.SelectBestDriver(modelID, availableDrivers)

	require.True(t, found, "Should find a driver for Claude Sonnet 4.5")

	// Should prefer native provider (Anthropic) over aggregator (OpenRouter)
	assert.Equal(t, models.DriverID("anthropic"), driverConfig.DriverID,
		"Should prefer Anthropic (native) over OpenRouter (aggregator)")
	assert.Equal(t, "sk-ant-api-user-key", driverConfig.APIKey,
		"Should use the Anthropic API key")
}

func TestSelectBestDriver_NoDriversConfigured_ShouldReturnFalse(t *testing.T) {
	// Setup: Empty available drivers
	availableDrivers := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{},
	}

	// Test with Claude Sonnet 4.5
	modelID := models.Claude45Sonnet
	_, found := models.SelectBestDriver(modelID, availableDrivers)

	assert.False(t, found, "Should not find a driver when none are configured")
}

func TestGetAvailableDriversForModel_FiltersDisabledDrivers(t *testing.T) {
	// Setup: Create available drivers with one disabled
	availableDrivers := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{
			models.DriverID("openrouter"): {
				DriverID: models.DriverID("openrouter"),
				APIKey:   "sk-or-key",
				Enabled:  false, // Disabled
			},
			models.DriverID("anthropic"): {
				DriverID: models.DriverID("anthropic"),
				APIKey:   "sk-ant-key",
				Enabled:  true,
			},
		},
	}

	// Get available drivers for Claude Sonnet 4.5
	configs := models.GetAvailableDriversForModel(models.Claude45Sonnet, availableDrivers)

	// Should only have the enabled driver
	assert.Len(t, configs, 1, "Should only return enabled drivers")
	assert.Equal(t, models.DriverID("anthropic"), configs[0].DriverID)
}

func TestGetAvailableDriversForModel_FiltersEmptyAPIKeys(t *testing.T) {
	// Setup: Create available drivers with one having empty API key
	availableDrivers := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{
			models.DriverID("openrouter"): {
				DriverID: models.DriverID("openrouter"),
				APIKey:   "", // Empty API key
				Enabled:  true,
			},
			models.DriverID("anthropic"): {
				DriverID: models.DriverID("anthropic"),
				APIKey:   "sk-ant-key",
				Enabled:  true,
			},
		},
	}

	// Get available drivers for Claude Sonnet 4.5
	configs := models.GetAvailableDriversForModel(models.Claude45Sonnet, availableDrivers)

	// Should only have the driver with a valid API key
	assert.Len(t, configs, 1, "Should only return drivers with API keys")
	assert.Equal(t, models.DriverID("anthropic"), configs[0].DriverID)
}

func TestDriverConfig_IsConfigured_LocalDriver(t *testing.T) {
	// Test that local drivers are properly configured with BaseURL instead of API key

	t.Run("Local driver with BaseURL is configured", func(t *testing.T) {
		config := models.DriverConfig{
			DriverID: models.DriverID("local"),
			BaseURL:  "http://localhost:11434/v1",
			Enabled:  true,
		}
		assert.True(t, config.IsConfigured(), "Local driver with BaseURL should be configured")
	})

	t.Run("Local driver without BaseURL is not configured", func(t *testing.T) {
		config := models.DriverConfig{
			DriverID: models.DriverID("local"),
			Enabled:  true,
		}
		assert.False(t, config.IsConfigured(), "Local driver without BaseURL should not be configured")
	})

	t.Run("Local driver disabled is not configured", func(t *testing.T) {
		config := models.DriverConfig{
			DriverID: models.DriverID("local"),
			BaseURL:  "http://localhost:11434/v1",
			Enabled:  false,
		}
		assert.False(t, config.IsConfigured(), "Disabled local driver should not be configured")
	})

	t.Run("Regular driver with API key is configured", func(t *testing.T) {
		config := models.DriverConfig{
			DriverID: models.DriverID("openai"),
			APIKey:   "sk-test-key",
			Enabled:  true,
		}
		assert.True(t, config.IsConfigured(), "Regular driver with API key should be configured")
	})

	t.Run("Regular driver without API key is not configured", func(t *testing.T) {
		config := models.DriverConfig{
			DriverID: models.DriverID("openai"),
			Enabled:  true,
		}
		assert.False(t, config.IsConfigured(), "Regular driver without API key should not be configured")
	})
}

func TestSelectBestDriver_PriorityOrder(t *testing.T) {
	// Test that native providers are preferred over cloud providers, which are preferred over aggregators

	t.Run("Prefers Anthropic over Vertex AI over OpenRouter", func(t *testing.T) {
		availableDrivers := models.AvailableDrivers{
			Drivers: map[models.DriverID]models.DriverConfig{
				models.DriverID("openrouter"): {
					DriverID: models.DriverID("openrouter"),
					APIKey:   "sk-or-key",
					Enabled:  true,
				},
				models.DriverID("vertexai"): {
					DriverID: models.DriverID("vertexai"),
					APIKey:   "vertex-key",
					Enabled:  true,
				},
				models.DriverID("anthropic"): {
					DriverID: models.DriverID("anthropic"),
					APIKey:   "sk-ant-key",
					Enabled:  true,
				},
			},
		}

		driverConfig, found := models.SelectBestDriver(models.Claude45Sonnet, availableDrivers)
		require.True(t, found)
		assert.Equal(t, models.DriverID("anthropic"), driverConfig.DriverID,
			"Should prefer native Anthropic over Vertex AI and OpenRouter")
	})

	t.Run("Prefers Vertex AI over OpenRouter when no native provider", func(t *testing.T) {
		availableDrivers := models.AvailableDrivers{
			Drivers: map[models.DriverID]models.DriverConfig{
				models.DriverID("openrouter"): {
					DriverID: models.DriverID("openrouter"),
					APIKey:   "sk-or-key",
					Enabled:  true,
				},
				models.DriverID("vertexai"): {
					DriverID: models.DriverID("vertexai"),
					APIKey:   "vertex-key",
					Enabled:  true,
				},
			},
		}

		// Use vertex-claude model which is only available via Vertex AI and OpenRouter
		driverConfig, found := models.SelectBestDriver(models.VertexClaude45Sonnet, availableDrivers)
		require.True(t, found)
		assert.Equal(t, models.DriverID("vertexai"), driverConfig.DriverID,
			"Should prefer Vertex AI over OpenRouter when no native provider available")
	})
}
