package catalog

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tiersByTag(tiers []*reliantv1.TierResolution) map[string]*reliantv1.TierResolution {
	byTag := make(map[string]*reliantv1.TierResolution, len(tiers))
	for _, tier := range tiers {
		byTag[tier.Tag] = tier
	}
	return byTag
}

func TestTierResolutions_ReportModelAndEffortPerTag(t *testing.T) {
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), []string{"anthropic"}, nil, modelprefs.ProviderPreference{}, nil))

	flagship, ok := tiers["flagship"]
	require.True(t, ok, "flagship must resolve on anthropic")
	assert.Equal(t, "claude-5.5-opus@anthropic", flagship.ModelId)
	assert.Equal(t, "high", flagship.ThinkingLevel)

	moderate, ok := tiers["moderate"]
	require.True(t, ok, "moderate must resolve on anthropic")
	assert.Equal(t, "claude-5.5-sonnet@anthropic", moderate.ModelId)
	assert.Equal(t, "high", moderate.ThinkingLevel)
}

// A user with Claude AND ChatGPT connected got Claude in every tier Claude
// leads, with no say. The Settings default provider decides instead, and the
// labels the UI shows (ListModels.tiers) follow it, every tier included.
func TestTierResolutions_DefaultProviderChoosesBetweenConnectedProviders(t *testing.T) {
	providers := []string{"claude", "codex"}

	auto := tiersByTag(tierResolutions(models.MustGetRegistry(), providers, nil, modelprefs.ProviderPreference{}, nil))
	assert.Equal(t, "claude-5.5-opus@claude", auto["flagship"].ModelId, "Auto keeps the built-in order")

	codexFirst := modelprefs.ProviderPreference{Providers: []string{"codex"}}
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), providers, nil, codexFirst, nil))
	for tag, want := range map[string]string{
		"powerful": "gpt-6.2-astra@codex",
		"flagship": "gpt-6.2-sol@codex",
		"moderate": "gpt-6.1-terra@codex",
		"fast":     "gpt-6-luna@codex",
		"cheap":    "gpt-6-luna@codex",
	} {
		tier, ok := tiers[tag]
		require.True(t, ok, "%s must resolve", tag)
		assert.Equal(t, want, tier.ModelId, tag)
	}
	assert.Equal(t, "xhigh", tiers["powerful"].ThinkingLevel, "the effort is the winning entry's, not the leader's")

	// A tier pinned to a model keeps it: the pin is the more specific choice.
	pinned := map[string]modelprefs.TagPrefs{"flagship": {ModelID: "claude-5.5-opus"}}
	tiers = tiersByTag(tierResolutions(models.MustGetRegistry(), providers, pinned, codexFirst, nil))
	assert.Equal(t, "claude-5.5-opus@claude", tiers["flagship"].ModelId)
	assert.Equal(t, "gpt-6.2-astra@codex", tiers["powerful"].ModelId)
}

// What the composer's tier labels show a codex-only user (ListModels.tiers):
// Codex's current models, not gpt-5.5 / gpt-5.6-terra.
func TestTierResolutions_CodexUserGetsCurrentCodexModels(t *testing.T) {
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), []string{"codex"}, nil, modelprefs.ProviderPreference{}, nil))
	for tag, want := range map[string]string{
		"powerful": "gpt-6.2-astra@codex",
		"flagship": "gpt-6.2-sol@codex",
		"moderate": "gpt-6.1-terra@codex",
		"fast":     "gpt-6-luna@codex",
		"cheap":    "gpt-6-luna@codex",
	} {
		tier, ok := tiers[tag]
		require.True(t, ok, "%s must resolve on codex", tag)
		assert.Equal(t, want, tier.ModelId, tag)
	}
}

func TestTierResolutions_SkipsNonTextAndUnresolvableTags(t *testing.T) {
	// openai serves image models; image-gen must still never appear as a tier
	// because the chat picker can only run text models.
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), []string{"openai"}, nil, modelprefs.ProviderPreference{}, nil))
	assert.NotContains(t, tiers, "image-gen")

	assert.Empty(t, tierResolutions(models.MustGetRegistry(), nil, nil, modelprefs.ProviderPreference{}, nil),
		"no configured providers means no tag resolves")
}
