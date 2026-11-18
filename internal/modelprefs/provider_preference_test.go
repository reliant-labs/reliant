// Copyright (c) 2025 Reliant Labs
package modelprefs

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeProviderPreference(t *testing.T) {
	for value, want := range map[string][]string{
		``:                                       nil,
		`null`:                                   nil,
		`{}`:                                     nil,
		`{"providers":[]}`:                       nil,
		`{"providers":["codex"]}`:                {"codex"},
		`{"providers":["codex","claude"]}`:       {"codex", "claude"},
		`{"providers":[" codex ","","codex"]}`:   {"codex"},
		`{"providers":["local","local:d1","x"]}`: {"x"},
	} {
		got, err := DecodeProviderPreference(value)
		require.NoError(t, err, value)
		assert.Equal(t, want, got.Providers, value)
	}

	_, err := DecodeProviderPreference(`{oops`)
	assert.Error(t, err)
}

func TestLoadProviderPreference(t *testing.T) {
	ctx := context.Background()
	project := "p1"

	got, err := LoadProviderPreference(ctx, rows{
		// ListSettingsByKey is a LIKE and `_` matches any character, so a
		// near-miss key can come back; it must not be read.
		{Key: "model.providerXpreference", Value: `{"providers":["openai"]}`},
		{Key: ProviderPreferenceKey, ProjectID: &project, Value: `{"providers":["claude"]}`},
		{Key: ProviderPreferenceKey, Value: `{"providers":["codex"]}`},
	}, "u")
	require.NoError(t, err)
	assert.Equal(t, []string{"codex"}, got.Providers, "the user-level row wins")

	got, err = LoadProviderPreference(ctx, rows{{Key: ProviderPreferenceKey, Value: `{oops`}}, "u")
	assert.Error(t, err)
	assert.True(t, got.IsZero(), "an unreadable row resolves as Auto")

	got, err = LoadProviderPreference(ctx, nil, "u")
	assert.NoError(t, err)
	assert.True(t, got.IsZero())

	got, err = LoadProviderPreference(ctx, rows{}, "u")
	assert.NoError(t, err)
	assert.True(t, got.IsZero(), "no row is Auto")
}

func TestProviderPreferenceApply(t *testing.T) {
	pref := ProviderPreference{Providers: []string{"codex", "claude"}}
	tier := models.ModelSelector{Tags: []string{models.TagFlagship}}

	assert.Equal(t, []string{"codex", "claude"}, pref.Apply(tier).Providers)
	assert.Nil(t, tier.Providers, "Apply never mutates its argument's providers")
	assert.Equal(t, []string{"codex", "claude"}, pref.Apply(models.ModelSelector{ID: "gpt-6.2-sol"}).Providers,
		"an unpinned model id takes the preferred provider when it serves it")

	own := models.ModelSelector{Tags: []string{models.TagFast}, Providers: []string{"openai"}}
	assert.Equal(t, own, pref.Apply(own), "a selector naming providers keeps them")
	pinned := models.ModelSelector{ID: "claude-5.5-opus@claude"}
	assert.Equal(t, pinned, pref.Apply(pinned), "an @driver pin keeps its driver")
	assert.Equal(t, tier, ProviderPreference{}.Apply(tier), "Auto changes nothing")
}

// The registry turns the preference into the model choice, not only the
// driver: a tier another provider leads resolves to the preferred provider's
// best entry, and falls back to the tier's own order when it serves none.
func TestProviderPreferenceDecidesTheTier(t *testing.T) {
	reg := models.MustGetRegistry()
	connected := []string{"claude", "codex"}
	codexFirst := ProviderPreference{Providers: []string{"codex"}}

	auto, err := reg.Resolve(models.ModelSelector{Tags: []string{models.TagFlagship}}, connected)
	require.NoError(t, err)
	assert.Equal(t, "claude", auto.Provider.Driver)

	chosen, err := reg.Resolve(codexFirst.Apply(models.ModelSelector{Tags: []string{models.TagFlagship}}), connected)
	require.NoError(t, err)
	assert.Equal(t, "gpt-6.2-sol", chosen.Definition.ID)
	assert.Equal(t, "codex", chosen.Provider.Driver)

	// Not connected: a preference, never a restriction.
	fallback, err := reg.Resolve(codexFirst.Apply(models.ModelSelector{Tags: []string{models.TagFlagship}}), []string{"claude"})
	require.NoError(t, err)
	assert.Equal(t, "claude", fallback.Provider.Driver)
}
