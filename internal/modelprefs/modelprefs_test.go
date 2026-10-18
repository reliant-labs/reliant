// Copyright (c) 2025 Reliant Labs
package modelprefs

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rows []*db.Setting

func (r rows) ListSettingsByKey(context.Context, string, string) ([]*db.Setting, error) {
	return r, nil
}

func TestLoadAll_SkipsBadRowsButKeepsGoodOnes(t *testing.T) {
	loaded, err := LoadAll(context.Background(), rows{
		{Key: "model.tag_config.fast", Value: `{"thinking_level":"low","temperature":0}`},
		{Key: "model.tag_config.cheap", Value: `{oops`},
		{Key: "model.tag_config.powerful", Value: `{}`},
		{Key: "unrelated", Value: `{"model_id":"x"}`},
		nil,
	}, "u")
	require.Error(t, err, "the malformed row is reported")
	require.Contains(t, loaded, "fast")
	assert.Equal(t, "low", loaded["fast"].ThinkingLevel)
	require.NotNil(t, loaded["fast"].Temperature)
	assert.Equal(t, 0.0, *loaded["fast"].Temperature, "temperature 0 is a real value")
	assert.NotContains(t, loaded, "cheap")
	assert.NotContains(t, loaded, "powerful")
	assert.Len(t, loaded, 1)
}

func TestLoadAll_NilReaderOrUser(t *testing.T) {
	loaded, err := LoadAll(context.Background(), nil, "u")
	assert.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestTagFor(t *testing.T) {
	assert.Equal(t, "fast", TagFor(models.ModelSelector{Tags: []string{"fast", "cheap"}}))
	assert.Equal(t, "", TagFor(models.ModelSelector{ID: "m", Tags: []string{"fast"}}))
	assert.Equal(t, "", TagFor(models.ModelSelector{}))
}

func TestDecode_Providers(t *testing.T) {
	prefs, err := Decode(`{"model_id":"qwen3:latest@local","providers":["local:d1"]}`)
	require.NoError(t, err)
	assert.Equal(t, []string{"local:d1"}, prefs.Providers)
	assert.True(t, prefs.IsLocalModel())
	assert.False(t, prefs.IsZero())
	assert.Equal(t, models.ModelSelector{ID: "qwen3:latest@local", Providers: []string{"local:d1"}}, prefs.LocalSelector())

	remote, err := Decode(`{"model_id":"claude-5.5-opus"}`)
	require.NoError(t, err)
	assert.False(t, remote.IsLocalModel())
}

func TestDecode_EndpointPinIsALocalModel(t *testing.T) {
	prefs, err := Decode(`{"model_id":"llama-3@local","providers":["endpoint:ep-1"]}`)
	require.NoError(t, err)
	assert.True(t, prefs.IsLocalModel(), "an endpoint: pin never resolves through the registry")
	assert.Equal(t, models.ModelSelector{ID: "llama-3@local", Providers: []string{"endpoint:ep-1"}}, prefs.LocalSelector())
	assert.Nil(t, prefs.PreferredModel(models.MustGetRegistry(), models.ModelSelector{Tags: []string{models.TagModerate}}, []string{"anthropic"}))

	empty, err := Decode(`{"model_id":"llama-3","providers":["endpoint:"]}`)
	require.NoError(t, err)
	assert.False(t, empty.IsLocalModel(), "a bare prefix names nothing")
}

// The run's provider is more specific than the user's per-tag model: a
// preference pinning its tier to a model that provider cannot serve gives up
// its model, and one the provider does serve still applies.
func TestPreferredModelOn_RunProviderOutranksTheTagModel(t *testing.T) {
	registry := models.MustGetRegistry()
	fast := models.ModelSelector{Tags: []string{models.TagFast}}
	both := []string{"anthropic", "codex"}
	haiku := TagPrefs{ModelID: "claude-4.5-haiku"}

	require.NotNil(t, haiku.PreferredModelOn(registry, fast, both, nil))
	require.NotNil(t, haiku.PreferredModelOn(registry, fast, both, []string{"anthropic"}))
	assert.Nil(t, haiku.PreferredModelOn(registry, fast, both, []string{"codex"}),
		"codex does not serve the pinned model, so the tier resolves on codex instead")

	luna := TagPrefs{ModelID: "gpt-6-luna"}
	got := luna.PreferredModelOn(registry, fast, both, []string{"codex"})
	require.NotNil(t, got)
	assert.Equal(t, "codex", got.Provider.Driver)
}
