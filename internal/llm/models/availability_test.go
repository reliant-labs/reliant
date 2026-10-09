// Copyright (c) 2025 Reliant Labs
package models

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotDisables reports the given catalog ids as policy=disabled on copilot.
func copilotDisables(ids ...string) AvailabilityFunc {
	return func(driver, modelID string) ModelAvailability {
		if driver == "copilot" && slices.Contains(ids, modelID) {
			return ModelAvailability{Disabled: true, Reason: "not enabled on your Copilot plan — enable it in GitHub Copilot settings"}
		}
		return ModelAvailability{}
	}
}

// An explicit id the account has disabled must fail with the actionable reason,
// not resolve and then 400 upstream.
func TestResolve_DisabledModelByIDFailsWithCopilotSettingsHint(t *testing.T) {
	reg := MustGetRegistry().WithAvailability(copilotDisables("claude-5-sonnet"))

	_, err := reg.Resolve(ModelSelector{ID: "claude-5-sonnet"}, []string{"copilot"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled on your Copilot plan")
	assert.Contains(t, err.Error(), "enable it in GitHub Copilot settings")

	_, err = reg.Resolve(ModelSelector{ID: "claude-5-sonnet@copilot"}, []string{"copilot"})
	require.Error(t, err, "an @copilot suffix is a hard constraint and must not bypass the policy")
	assert.Contains(t, err.Error(), "enable it in GitHub Copilot settings")
}

// A disabled model on one provider must not hide the same model on another.
func TestResolve_DisabledOnCopilotStillServedByOtherProvider(t *testing.T) {
	reg := MustGetRegistry().WithAvailability(copilotDisables("claude-5-sonnet"))

	got, err := reg.Resolve(ModelSelector{ID: "claude-5-sonnet"}, []string{"copilot", "anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", got.Provider.Driver)
}

// Tag resolution skips a disabled candidate and falls through to the next
// servable entry.
func TestResolve_TagFallsThroughPastDisabledModel(t *testing.T) {
	providers := []string{"copilot"}

	open := MustGetRegistry()
	baseline, err := open.Resolve(ModelSelector{Tags: []string{TagPowerful}}, providers)
	require.NoError(t, err)
	require.Equal(t, "gemini-3.8-flash", baseline.Definition.ID, "precondition: copilot-only [powerful] resolves to gemini-3.8-flash")

	// Disabling that winner moves [powerful] to the next copilot-served entry.
	next, err := open.WithAvailability(copilotDisables("gemini-3.8-flash")).Resolve(ModelSelector{Tags: []string{TagPowerful}}, providers)
	require.NoError(t, err)
	assert.Equal(t, "claude-5-sonnet", next.Definition.ID)

	// [flagship] lists copilot-served models in order; disabling the winner must
	// move resolution to a lower entry, not fail.
	flagshipWinner, err := open.Resolve(ModelSelector{Tags: []string{TagFlagship}}, providers)
	require.NoError(t, err)

	gated := open.WithAvailability(copilotDisables(flagshipWinner.Definition.ID))
	got, err := gated.Resolve(ModelSelector{Tags: []string{TagFlagship}}, providers)
	require.NoError(t, err, "tag resolution must fall through, not fail")
	assert.NotEqual(t, flagshipWinner.Definition.ID, got.Definition.ID)
}

// Every copilot model disabled: tag resolution fails with the usual error.
func TestResolve_TagFailsWhenEveryCandidateDisabled(t *testing.T) {
	all := func(driver, _ string) ModelAvailability {
		return ModelAvailability{Disabled: driver == "copilot"}
	}
	reg := MustGetRegistry().WithAvailability(all)
	_, err := reg.Resolve(ModelSelector{Tags: []string{TagFast}}, []string{"copilot"})
	require.Error(t, err)
}

// The account's advertised limit and usable levels land on the resolved model,
// so ProviderPromptCeiling and clamping derive from what the account gets. A
// limit below the catalog-derived ceiling (922,000 for gpt-5.6-sol) is the
// ceiling; one above it never raises it.
func TestResolve_AvailabilityCapsPromptCeilingAndLevels(t *testing.T) {
	resolveWith := func(limit int) *ResolvedModel {
		t.Helper()
		avail := func(driver, modelID string) ModelAvailability {
			if driver == "codex" && modelID == "gpt-5.6-sol" {
				return ModelAvailability{ContextWindow: limit, ThinkingLevels: []string{"low", "medium"}}
			}
			return ModelAvailability{}
		}
		got, err := MustGetRegistry().WithAvailability(avail).Resolve(ModelSelector{ID: "gpt-5.6-sol@codex"}, []string{"codex"})
		require.NoError(t, err)
		return got
	}

	got := resolveWith(123456)
	assert.Equal(t, 123456, ProviderPromptCeiling(&got.Definition, "codex"))
	assert.Equal(t, 1_050_000, EffectiveContextWindow(&got.Definition, "codex"), "the catalog window is not rewritten")
	assert.Equal(t, []string{"low", "medium"}, got.Definition.Capabilities.ThinkingLevels)

	got = resolveWith(2_000_000)
	assert.Equal(t, 922_000, ProviderPromptCeiling(&got.Definition, "codex"), "a larger advertised limit does not raise the ceiling")

	// The shared registry is untouched.
	orig, _ := MustGetRegistry().GetDefinition("gpt-5.6-sol")
	assert.NotEqual(t, []string{"low", "medium"}, orig.Capabilities.ThinkingLevels)
	assert.Equal(t, 922_000, ProviderPromptCeiling(orig, "codex"))
}

// ServedDefinition applies the same advertised limit Resolve does, so the
// picker's thresholds match the request path.
func TestServedDefinition_MatchesResolve(t *testing.T) {
	reg := MustGetRegistry().WithAvailability(func(driver, _ string) ModelAvailability {
		if driver == "codex" {
			return ModelAvailability{ContextWindow: 872_000}
		}
		return ModelAvailability{}
	})
	def, ok := reg.GetDefinition("gpt-5.6-terra")
	require.True(t, ok)

	served := reg.ServedDefinition(def, "codex")
	assert.Equal(t, 872_000, ProviderPromptCeiling(&served, "codex"))
	resolved, err := reg.Resolve(ModelSelector{ID: "gpt-5.6-terra@codex"}, []string{"codex"})
	require.NoError(t, err)
	assert.Equal(t, ProviderPromptCeiling(&resolved.Definition, "codex"), ProviderPromptCeiling(&served, "codex"))

	// copilot reports nothing here: the catalog ceiling applies.
	copilot := reg.ServedDefinition(def, "copilot")
	assert.Equal(t, 922_000, ProviderPromptCeiling(&copilot, "copilot"))
}

func TestWithAvailability_NilIsIdentity(t *testing.T) {
	reg := MustGetRegistry()
	assert.Same(t, reg, reg.WithAvailability(nil))
}

func TestSelectBestDriver_SkipsDisabledProvider(t *testing.T) {
	drivers := AvailableDrivers{
		Drivers: map[DriverID]DriverConfig{
			"copilot":    {DriverID: "copilot", APIKey: "k", Enabled: true},
			"openrouter": {DriverID: "openrouter", APIKey: "k", Enabled: true},
		},
		Availability: copilotDisables("gpt-5.6-terra"),
	}
	// gpt-5.6-terra is mapped on codex and copilot only; with copilot disabled and
	// no codex credential nothing can serve it.
	_, found := SelectBestDriver("gpt-5.6-terra", drivers)
	assert.False(t, found)
}

// Advertised levels are intersected with what the API accepts: `ultra` is
// advertised by /codex/models and 400s.
func TestIntersectCodexLevels(t *testing.T) {
	assert.Equal(t,
		[]string{"low", "medium", "high", "xhigh", "max"},
		IntersectCodexLevels([]string{"low", "medium", "high", "xhigh", "max", "ultra"}))
	assert.Equal(t, []string{"low", "high"}, IntersectCodexLevels([]string{"LOW", " high ", "ultra", "low"}))
	assert.Empty(t, IntersectCodexLevels([]string{"ultra"}))
}

// xAI is not a provider: no catalog model maps the xai driver, and no grok model
// is served by anything but Copilot.
func TestCatalogHasNoXAIProvider(t *testing.T) {
	reg := MustGetRegistry()
	for _, def := range reg.ListAll() {
		for _, p := range def.Providers {
			assert.NotEqual(t, "xai", p.Driver, "%s maps the retired xai driver", def.ID)
		}
		if strings.HasPrefix(def.ID, "grok") {
			require.Len(t, def.Providers, 1, def.ID)
			assert.Equal(t, "copilot", def.Providers[0].Driver, "%s: grok is served via Copilot only", def.ID)
		}
		assert.NotEqual(t, "grok-4", def.ID)
		assert.NotEqual(t, "grok-3-mini-beta", def.ID)
	}
	for _, tag := range reg.ListAllTags() {
		for _, e := range reg.TagEntries(tag) {
			assert.NotContains(t, []string{"grok-4", "grok-3-mini-beta"}, e.Model, "tag %s still names %s", tag, e.Model)
		}
	}
	assert.NotContains(t, textCatalogDrivers(reg), "xai")
	_, hasPriority := ProviderPriority["xai"]
	assert.False(t, hasPriority)
}

// The Copilot-served models Copilot's /models reports (2026-10-04), with the
// limits and reasoning levels it advertises. They are copilot-only and sit at
// the tail of tags so no existing resolution changes.
func TestCopilotServedModelsAreCatalogued(t *testing.T) {
	want := map[string]struct {
		apiModel   string
		context    int
		maxOutput  int
		levels     []string
		endpointRs bool
	}{
		"grok-4.5":           {"grok-4.5", 500000, 128000, []string{"low", "medium", "high"}, true},
		"grok-4.6":           {"grok-4.6", 500000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
		"grok-4.7":           {"grok-4.7", 500000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
		"kimi-k3":            {"kimi-k3", 1048576, 131072, []string{"low", "high", "max"}, false},
		"mai-code-1.1-flash": {"mai-code-1.1-flash", 256000, 128000, []string{"low", "medium", "high"}, true},
		"gpt-5.4-nano":       {"gpt-5.4-nano", 400000, 128000, []string{"low", "medium", "high", "xhigh"}, true},
	}
	reg := MustGetRegistry()
	for id, w := range want {
		def, ok := reg.GetDefinition(id)
		require.True(t, ok, "%s missing from catalog", id)
		require.Len(t, def.Providers, 1, id)
		assert.Equal(t, "copilot", def.Providers[0].Driver, id)
		assert.Equal(t, w.apiModel, def.Providers[0].APIModel, id)
		assert.Equal(t, w.context, def.Capabilities.MaxContextWindow, id)
		assert.Equal(t, w.maxOutput, def.Capabilities.MaxOutputTokens, id)
		assert.Equal(t, w.levels, def.Capabilities.ThinkingLevels, id)
		assert.True(t, def.Capabilities.SupportsTools, id)
		assert.Equal(t, w.endpointRs, def.DriverSettings != nil && def.DriverSettings.PreferredEndpoint == "responses", id)
	}
}

// Adding the copilot-only models must not change what any existing provider
// set resolves to for a core tag: they only ever trail.
func TestCopilotOnlyModelsNeverLeadATag(t *testing.T) {
	newIDs := []string{"grok-4.5", "grok-4.6", "grok-4.7", "kimi-k3", "mai-code-1.1-flash", "gpt-5.4-nano"}
	reg := MustGetRegistry()
	for _, tag := range reg.ListAllTags() {
		entries := reg.TagEntries(tag)
		seenNew := false
		for _, e := range entries {
			if slices.Contains(newIDs, e.Model) {
				seenNew = true
				continue
			}
			assert.False(t, seenNew, "tag %s: %s follows a new copilot-only model, so the new model is not at the tail", tag, e.Model)
		}
	}
}
