// Copyright (c) 2025 Reliant Labs
package models

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The driver rank is a tie-break among the drivers serving the model
// resolution already chose (control-plane docs/design/org-provider-accounts.md
// §4.3): it decides who pays, never which model.

// rankFunc ranks drivers the same for every model.
func rankFunc(rank map[DriverID]AccountTier) func(DriverID, string) AccountTier {
	return func(d DriverID, _ string) AccountTier { return rank[d] }
}

// Over every tag tier, every subset of providers and random ranks, a ranked
// resolution resolves to the same model as an unranked one, and to a provider
// of the best rank among those serving it.
func TestDriverRank_NeverChangesTheModel(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()
	drivers := []string{"claude", "anthropic", "codex", "openai", "openrouter", "reliant", "gemini", "copilot"}
	rng := rand.New(rand.NewPCG(7, 11))
	tags := reg.ListAllTags()
	require.NotEmpty(t, tags)

	for i := 0; i < 600; i++ {
		var avail []string
		for _, d := range drivers {
			if rng.IntN(2) == 0 {
				avail = append(avail, d)
			}
		}
		rank := map[DriverID]AccountTier{}
		for _, d := range avail {
			if rng.IntN(2) == 0 {
				rank[DriverID(d)] = TierFirst
			}
		}
		selector := ModelSelector{Tags: []string{tags[rng.IntN(len(tags))]}}
		if rng.IntN(3) == 0 {
			selector.Tags = append(selector.Tags, tags[rng.IntN(len(tags))])
		}
		plain, plainErr := reg.Resolve(selector, avail)
		ranked, rankedErr := reg.WithDriverRank(rankFunc(rank)).Resolve(selector, avail)
		if plainErr != nil {
			require.Error(t, rankedErr, "a rank never makes an unresolvable selector resolve: %v %v", selector, avail)
			continue
		}
		require.NoError(t, rankedErr)
		require.Equal(t, plain.Definition.ID, ranked.Definition.ID, "selector %v over %v with rank %v", selector.Tags, avail, rank)

		// The ranked provider is of the best rank among the model's servable ones.
		best := TierSecond
		for _, p := range ranked.Definition.Providers {
			if slices.Contains(avail, p.Driver) && rank[DriverID(p.Driver)] < best {
				best = rank[DriverID(p.Driver)]
			}
		}
		assert.Equal(t, best, rank[DriverID(ranked.Provider.Driver)], "selector %v: picked %s", selector.Tags, ranked.Provider.Driver)
	}
}

// The design's example: a model both the person's Claude subscription and the
// org's Anthropic key serve runs on the org's key; with no rank the
// subscription wins, as before; a pin decides above any rank.
func TestDriverRank_OrgKeyServesAModelTheSubscriptionAlsoServes(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()
	var model *ModelDefinition
	for _, def := range reg.ListAll() {
		hasClaude, hasAnthropic := false, false
		for _, p := range def.Providers {
			hasClaude = hasClaude || p.Driver == "claude"
			hasAnthropic = hasAnthropic || p.Driver == "anthropic"
		}
		if hasClaude && hasAnthropic {
			model = &def
			break
		}
	}
	require.NotNil(t, model, "the catalog has a model both claude and anthropic serve")
	avail := []string{"claude", "anthropic"}

	plain, err := reg.Resolve(ModelSelector{ID: model.ID}, avail)
	require.NoError(t, err)
	assert.Equal(t, "claude", plain.Provider.Driver)

	orgFirst := map[DriverID]AccountTier{"anthropic": TierFirst}
	ranked, err := reg.WithDriverRank(rankFunc(orgFirst)).Resolve(ModelSelector{ID: model.ID}, avail)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", ranked.Provider.Driver)

	pinned, err := reg.WithDriverRank(rankFunc(orgFirst)).Resolve(ModelSelector{ID: model.ID + "@claude"}, avail)
	require.NoError(t, err)
	assert.Equal(t, "claude", pinned.Provider.Driver, "an explicit pin outranks the tier")

	preferred, err := reg.WithDriverRank(rankFunc(orgFirst)).Resolve(ModelSelector{ID: model.ID, Providers: []string{"claude"}}, avail)
	require.NoError(t, err)
	assert.Equal(t, "claude", preferred.Provider.Driver, "a selector's providers outrank the tier")

	assert.Same(t, reg, reg.WithDriverRank(nil), "no rank, no view")
}

// TierFor ranks a driver for one model by the accounts that serve it: a seat
// that cannot serve the model does not lift its driver for that model (review
// B1 of reliant#2689).
func TestTierFor_OnlyAccountsServingTheModel(t *testing.T) {
	t.Parallel()
	notOpus := func(m string) bool { return m != "claude-5.5-opus" }
	avail := AvailableDrivers{Accounts: map[DriverID][]AccountConfig{
		"claude": {
			{DriverConfig: DriverConfig{AccountID: "seat"}, Tier: TierFirst, Serves: notOpus},
			{DriverConfig: DriverConfig{AccountID: "mine"}, Tier: TierSecond},
		},
		"anthropic": {{DriverConfig: DriverConfig{AccountID: "org-key"}, Tier: TierFirst}},
	}}
	assert.Equal(t, TierSecond, avail.TierFor("claude", "claude-5.5-opus"))
	assert.Equal(t, TierFirst, avail.TierFor("claude", "claude-5.5-sonnet"))
	assert.Equal(t, TierFirst, avail.TierFor("anthropic", "claude-5.5-opus"))
	assert.Equal(t, TierSecond, avail.TierFor("codex", "claude-5.5-opus"))

	got, err := MustGetRegistry().WithDriverRank(avail.TierFor).Resolve(ModelSelector{ID: "claude-5.5-opus"}, []string{"claude", "anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", got.Provider.Driver, "the org's key serves it, not the person's own sign-in")
}
