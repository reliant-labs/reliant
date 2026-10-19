// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

func applyOpts(opts []llm.DriverOption) llm.DriverOptions {
	var o llm.DriverOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

func TestPreferenceDriverOptions(t *testing.T) {
	temp := 0.4
	budget := int64(900)

	got := applyOpts(PreferenceDriverOptions(models.Model{DefaultMaxTokens: 128000}, models.Preference{}))
	assert.Equal(t, int64(128000), got.MaxTokens, "the model's output cap is the default")
	assert.Nil(t, got.Temperature)

	got = applyOpts(PreferenceDriverOptions(models.Model{DefaultMaxTokens: 128000},
		models.Preference{TokenBudget: &budget, Temperature: &temp, ThinkingMode: "high", DisableCache: true}))
	assert.Equal(t, budget, got.MaxTokens)
	assert.Equal(t, &temp, got.Temperature)
	assert.Equal(t, "high", got.ReasoningEffort)
	assert.True(t, got.DisableCache)

	got = applyOpts(PreferenceDriverOptions(models.Model{DefaultMaxTokens: 10, TemperatureMode: models.TemperatureModeOmit},
		models.Preference{Temperature: &temp}))
	assert.Nil(t, got.Temperature, "a model that takes only its default temperature never receives one")
}
