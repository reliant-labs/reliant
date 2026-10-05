// Copyright (c) 2025 Reliant Labs
package registry

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAvailabilityFunc_DisabledModelIsNotResolvableByTagOrID(t *testing.T) {
	reg := models.MustGetRegistry()
	reports := map[string]ProviderAvailability{"copilot": {Models: map[string]models.ModelAvailability{
		"claude-sonnet-5": {Disabled: true, Reason: "not enabled on your Copilot plan — enable it in GitHub Copilot settings"},
	}}}
	view := reg.WithAvailability(BuildAvailabilityFunc(reg, reports))

	_, err := view.Resolve(models.ModelSelector{ID: "claude-5-sonnet"}, []string{"copilot"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enable it in GitHub Copilot settings")

	got, err := view.Resolve(models.ModelSelector{Tags: []string{models.TagPowerful}}, []string{"copilot"})
	require.NoError(t, err)
	assert.NotEqual(t, "claude-5-sonnet", got.Definition.ID)
}

func TestBuildAvailabilityFunc_NoReportsMeansNoFilter(t *testing.T) {
	assert.Nil(t, BuildAvailabilityFunc(models.MustGetRegistry(), nil))
}

func TestProviderAvailability_AuthoritativeDisablesUnlisted(t *testing.T) {
	p := ProviderAvailability{Authoritative: true, UnlistedReason: "nope", Models: map[string]models.ModelAvailability{"a": {}}}
	assert.False(t, p.For("a").Disabled)
	assert.True(t, p.For("b").Disabled)
	assert.False(t, ProviderAvailability{}.For("b").Disabled)
}

func TestApplyAvailability_StampsEnabledWindowAndLevels(t *testing.T) {
	infos := []models.ModelInfo{{ID: "m", APIModel: "m", Capabilities: models.ModelCapabilities{MaxContextWindow: 500, ThinkingLevels: []string{"low", "ultra"}}, Enabled: true}}
	out := ApplyAvailability(infos, ProviderAvailability{Models: map[string]models.ModelAvailability{
		"m": {Disabled: true, ContextWindow: 100, ThinkingLevels: []string{"low"}},
	}})
	assert.False(t, out[0].Enabled)
	assert.Equal(t, 100, out[0].Capabilities.MaxContextWindow)
	assert.Equal(t, []string{"low"}, out[0].Capabilities.ThinkingLevels)
}
