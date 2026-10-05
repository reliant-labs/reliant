package services

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
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
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), []string{"anthropic"}, nil, nil))

	flagship, ok := tiers["flagship"]
	require.True(t, ok, "flagship must resolve on anthropic")
	assert.Equal(t, "claude-5.5-opus@anthropic", flagship.ModelId)
	assert.Equal(t, "xhigh", flagship.ThinkingLevel)

	moderate, ok := tiers["moderate"]
	require.True(t, ok, "moderate must resolve on anthropic")
	assert.Equal(t, "claude-5.5-sonnet@anthropic", moderate.ModelId)
	assert.Equal(t, "medium", moderate.ThinkingLevel)
}

func TestTierResolutions_SkipsNonTextAndUnresolvableTags(t *testing.T) {
	// openai serves image models; image-gen must still never appear as a tier
	// because the chat picker can only run text models.
	tiers := tiersByTag(tierResolutions(models.MustGetRegistry(), []string{"openai"}, nil, nil))
	assert.NotContains(t, tiers, "image-gen")

	assert.Empty(t, tierResolutions(models.MustGetRegistry(), nil, nil, nil),
		"no configured providers means no tag resolves")
}
