// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Driver selection must be deterministic and must prefer user-owned (BYO)
// credential drivers over the managed reliant driver on ProviderPriority
// ties. Historically SelectBestDriver broke priority-1 ties using the
// iteration order of the DriverMapping map, so each call was a coin flip
// between e.g. the user's own Claude subscription (anthropic) and the managed
// reliant driver.

func testDriverConfig(id DriverID) DriverConfig {
	return DriverConfig{
		DriverID: id,
		APIKey:   "key-" + string(id),
		Enabled:  true,
	}
}

// registerTestModel registers a unique model ID under the given drivers and
// returns it. Unique IDs keep the global DriverMapping free of cross-test
// interference.
func registerTestModel(t *testing.T, name string, driverIDs ...DriverID) ModelID {
	t.Helper()
	modelID := ModelID("driver-config-test-" + name)
	for _, id := range driverIDs {
		RegisterDriverModels(Family(id), []ModelID{modelID})
	}
	return modelID
}

func availableDriversFor(ids ...DriverID) AvailableDrivers {
	drivers := make(map[DriverID]DriverConfig, len(ids))
	for _, id := range ids {
		drivers[id] = testDriverConfig(id)
	}
	return AvailableDrivers{Drivers: drivers}
}

func TestSelectBestDriver_ByoBeatsManagedOnPriorityTie(t *testing.T) {
	// anthropic (user's own Claude subscription) and reliant (managed) are
	// both priority 1 — the user's own credentials must win the tie.
	modelID := registerTestModel(t, "byo-tie", "anthropic", "reliant")
	available := availableDriversFor("anthropic", "reliant")

	config, found := SelectBestDriver(modelID, available)
	require.True(t, found)
	assert.Equal(t, DriverID("anthropic"), config.DriverID,
		"BYO anthropic must beat managed reliant on a priority tie")
}

func TestSelectBestDriver_DeterministicAcrossCalls(t *testing.T) {
	// With several same-priority candidates registered, selection must return
	// the identical driver on every call — never map-iteration-order roulette.
	modelID := registerTestModel(t, "determinism", "anthropic", "reliant", "vertexai", "openrouter")
	available := availableDriversFor("anthropic", "reliant", "vertexai", "openrouter")

	first, found := SelectBestDriver(modelID, available)
	require.True(t, found)
	for i := 0; i < 500; i++ {
		config, ok := SelectBestDriver(modelID, available)
		require.True(t, ok)
		require.Equalf(t, first.DriverID, config.DriverID, "selection changed on iteration %d", i)
	}
	// And the deterministic winner is a BYO priority-1 driver, first by ID.
	assert.Equal(t, DriverID("anthropic"), first.DriverID)
}

func TestSelectBestDriver_ManagedWinsWhenOnlyManagedConfigured(t *testing.T) {
	modelID := registerTestModel(t, "managed-only", "anthropic", "reliant")
	available := availableDriversFor("reliant")

	config, found := SelectBestDriver(modelID, available)
	require.True(t, found)
	assert.Equal(t, ManagedDriverID, config.DriverID)
}

func TestSelectBestDriver_PriorityBeatsByoTieBreak(t *testing.T) {
	// The BYO-over-managed rule only applies on priority ties: managed
	// reliant (priority 1) still beats BYO openrouter (priority 10).
	modelID := registerTestModel(t, "priority-dominates", "reliant", "openrouter")
	available := availableDriversFor("reliant", "openrouter")

	config, found := SelectBestDriver(modelID, available)
	require.True(t, found)
	assert.Equal(t, ManagedDriverID, config.DriverID)
}

func TestSelectBestDriver_LexicographicAmongByoTies(t *testing.T) {
	// Two BYO drivers at the same priority tie-break by driver ID so the
	// result is stable.
	modelID := registerTestModel(t, "byo-lexicographic", "vertexai", "anthropic")
	available := availableDriversFor("vertexai", "anthropic")

	config, found := SelectBestDriver(modelID, available)
	require.True(t, found)
	assert.Equal(t, DriverID("anthropic"), config.DriverID)
}

func TestPreferDriver_Ordering(t *testing.T) {
	tests := []struct {
		a, b DriverID
		want bool
	}{
		// BYO beats managed on priority-1 tie, in both comparison directions.
		{"anthropic", "reliant", true},
		{"reliant", "anthropic", false},
		{"vertexai", "reliant", true},
		{"reliant", "vertexai", false},
		// Lower priority always dominates.
		{"reliant", "openrouter", true},
		{"openrouter", "reliant", false},
		{"anthropic", "local", true},
		// BYO ties break lexicographically.
		{"anthropic", "vertexai", true},
		{"vertexai", "anthropic", false},
		// Unknown drivers rank last.
		{"reliant", "totally-unknown", true},
		{"totally-unknown", "anthropic", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			assert.Equal(t, tt.want, preferDriver(tt.a, tt.b))
		})
	}
}

func TestFindBestProvider_ByoBeatsManagedOnTie(t *testing.T) {
	// The registry's provider choice (used by the normal CallLLM path and by
	// compaction) must also prefer BYO credentials over the managed reliant
	// driver on a priority tie, even when the managed provider is listed
	// first in the model definition.
	registry := &ModelRegistry{}
	model := &ModelDefinition{
		ID: "find-best-provider-tie",
		Providers: []ProviderMapping{
			{Driver: "reliant"},
			{Driver: "anthropic"},
		},
	}
	availableSet := map[string]bool{"reliant": true, "anthropic": true}

	provider, err := registry.findBestProvider(model, nil, availableSet, false)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", provider.Driver)

	// With only the managed driver available, it is still selected.
	provider, err = registry.findBestProvider(model, nil, map[string]bool{"reliant": true}, false)
	require.NoError(t, err)
	assert.Equal(t, "reliant", provider.Driver)
}

func TestFindBestProvider_KeepsYAMLOrderAmongByoTies(t *testing.T) {
	// Among same-priority BYO providers the earlier YAML entry still wins —
	// only the managed driver loses ties.
	registry := &ModelRegistry{}
	model := &ModelDefinition{
		ID: "find-best-provider-yaml-order",
		Providers: []ProviderMapping{
			{Driver: "vertexai"},
			{Driver: "anthropic"},
		},
	}
	availableSet := map[string]bool{"vertexai": true, "anthropic": true}

	provider, err := registry.findBestProvider(model, nil, availableSet, false)
	require.NoError(t, err)
	assert.Equal(t, "vertexai", provider.Driver)
}
