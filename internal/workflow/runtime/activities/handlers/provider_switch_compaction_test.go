// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Compaction on a chat bound to one provider follows the same rules as a
// call_llm turn: a root keeps its exact pick, a descendant keeps (or recovers)
// its semantic tags, and nothing — preferred model, tier list — ever resolves
// on another provider, because that would be held on the next call anyway.
//
// These tests inject the user's connected providers, so no database is needed.
// The old provider stays connected throughout: any fallthrough lands there.

// boundDrivers is every named provider configured with a key; disabled
// providers are connected but report every model as unavailable.
func boundDrivers(disabled string, ids ...string) models.AvailableDrivers {
	out := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{}}
	for _, id := range ids {
		out.Drivers[models.DriverID(id)] = models.DriverConfig{DriverID: models.DriverID(id), Enabled: true, APIKey: "k-" + id}
	}
	if disabled != "" {
		out.Availability = func(driver, _ string) models.ModelAvailability {
			return models.ModelAvailability{Disabled: driver == disabled, Reason: "disabled for this account (test)"}
		}
	}
	return out
}

func compactSpec(provider string, descendant bool, presetTags ...string) llmCallSpec {
	return llmCallSpec{ChatProvider: provider, Descendant: descendant, PresetModelTags: presetTags}
}

func TestProviderSwitchCompaction_DescendantKeepsRecoveredPresetTags(t *testing.T) {
	withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
	ctx := context.Background()

	// The preferred model was stamped as the old provider's exact id; the
	// preset's original tags are the only memory of what the thread asked for.
	stale := &reliantv1.ModelSelector{Id: "claude-4.5-haiku@anthropic"}
	selector, _, err := resolveCompactionModelForCall(ctx, "u", stale, compactSpec("codex", true, models.TagFast))
	require.NoError(t, err)
	assert.Equal(t, []string{models.TagFast}, selector.Tags, "a descendant's tags are recovered from its preset")
	assert.Empty(t, selector.ID, "the stale concrete id must not survive on a descendant")
	assert.Equal(t, []string{"codex"}, selector.Providers)

	resolved, err := models.MustGetRegistry().Resolve(selector, []string{"codex"})
	require.NoError(t, err)
	assert.Equal(t, "codex", resolved.Provider.Driver)

	t.Run("tags the descendant already has win over the preset's", func(t *testing.T) {
		own := &reliantv1.ModelSelector{Tags: []string{models.TagFlagship}}
		selector, _, err := resolveCompactionModelForCall(ctx, "u", own, compactSpec("codex", true, models.TagFast))
		require.NoError(t, err)
		assert.Equal(t, []string{models.TagFlagship}, selector.Tags)
	})
}

func TestProviderSwitchCompaction_RootIsARootNotADescendant(t *testing.T) {
	withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
	ctx := context.Background()

	t.Run("a root keeps its exact model and ignores preset tags", func(t *testing.T) {
		exact := &reliantv1.ModelSelector{Id: "gpt-6.1-sol@codex"}
		selector, _, err := resolveCompactionModelForCall(ctx, "u", exact, compactSpec("codex", false, models.TagFast))
		require.NoError(t, err)
		assert.Equal(t, "gpt-6.1-sol@codex", selector.ID)
		assert.Empty(t, selector.Tags, "preset tags must not rewrite the root's exact pick")
	})

	t.Run("a root whose exact model the bound provider cannot serve holds, never re-tagged", func(t *testing.T) {
		exact := &reliantv1.ModelSelector{Id: "claude-4.5-haiku@anthropic"}
		_, _, err := resolveCompactionModelForCall(ctx, "u", exact, compactSpec("codex", false, models.TagFast))
		requireProviderSwitchHold(t, nil, err)
	})
}

func TestProviderSwitchCompaction_NeverResolvesOnAnotherProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("control: unbound chat still falls back to the tier list", func(t *testing.T) {
		withAvailableDrivers(t, boundDrivers("", "anthropic"))
		selector, reason, err := resolveCompactionModelForCall(ctx, "u", &reliantv1.ModelSelector{Id: "not-a-real-model"}, llmCallSpec{})
		require.NoError(t, err)
		assert.Equal(t, string(compactionModelTier[0]), selector.ID)
		assert.Contains(t, reason, "tier list")
	})

	t.Run("preferred tag the bound provider cannot serve holds instead of using another provider", func(t *testing.T) {
		// codex is connected but serves nothing; anthropic and openai would serve.
		withAvailableDrivers(t, boundDrivers("codex", "anthropic", "codex", "openai"))
		_, _, err := resolveCompactionModelForCall(ctx, "u", &reliantv1.ModelSelector{Tags: []string{models.TagFast}}, compactSpec("codex", true))
		requireProviderSwitchHold(t, nil, err)
	})

	t.Run("no preferred model: the tier list cannot reach an unbound provider", func(t *testing.T) {
		withAvailableDrivers(t, boundDrivers("codex", "anthropic", "codex", "openai"))
		_, _, err := resolveCompactionModelForCall(ctx, "u", nil, compactSpec("codex", false))
		requireProviderSwitchHold(t, nil, err)
	})

	t.Run("bound provider not connected at all holds", func(t *testing.T) {
		withAvailableDrivers(t, boundDrivers("", "anthropic", "openai"))
		_, _, err := resolveCompactionModelForCall(ctx, "u", nil, compactSpec("codex", false))
		requireProviderSwitchHold(t, nil, err)
		_, _, err = resolveCompactionModelForCall(ctx, "u", &reliantv1.ModelSelector{Tags: []string{models.TagFast}}, compactSpec("codex", true))
		requireProviderSwitchHold(t, nil, err)
	})

	t.Run("unresolvable preferred id on a bound root holds rather than dropping to the tier list", func(t *testing.T) {
		withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
		_, _, err := resolveCompactionModelForCall(ctx, "u", &reliantv1.ModelSelector{Id: "not-a-real-model"}, compactSpec("codex", false))
		requireProviderSwitchHold(t, nil, err)
	})

	t.Run("no preferred model on a healthy bound provider resolves there", func(t *testing.T) {
		withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
		selector, _, err := resolveCompactionModelForCall(ctx, "u", nil, compactSpec("codex", false))
		require.NoError(t, err)
		resolved, err := models.MustGetRegistry().Resolve(selector, []string{"codex"})
		require.NoError(t, err)
		assert.Equal(t, "codex", resolved.Provider.Driver)
	})
}

// The summary request itself, end to end through the activity's own helper:
// the driver it asks for is the bound provider's, with the descendant's tags
// recovered from its preset.
func TestProviderSwitchCompaction_SummaryRequestLandsOnBoundProvider(t *testing.T) {
	withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
	captured := &capturedDriverOptions{}
	original := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(captured)
	t.Cleanup(func() { drivers.GetDriver = original })

	a := &CompactActivity{}
	chat := &db.Chat{ID: "chat-1", UserID: "u"}
	history := []message.Message{{Role: "user", Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	stale := &reliantv1.ModelSelector{Id: "claude-4.5-haiku@anthropic"}

	t.Run("descendant with recovered tags", func(t *testing.T) {
		rtx := types.RuntimeContext{ChatProvider: "codex", Descendant: true, PresetModelTags: []string{models.TagFast}}
		_, err := a.generateCompactionSummaryOnProvider(context.Background(), chat, "child", "", types.FailoverPolicy{}, history, stale, rtx)
		require.NoError(t, err)
		require.NotEmpty(t, captured.Preferences)
		assert.Equal(t, firstOn(t, models.TagFast, "codex"), string(captured.Preferences[0].ModelID))
	})

	t.Run("root with an exact pick on another provider is held, with no driver built", func(t *testing.T) {
		captured.Preferences = nil
		rtx := types.RuntimeContext{ChatProvider: "codex", Descendant: false, PresetModelTags: []string{models.TagFast}}
		_, err := a.generateCompactionSummaryOnProvider(context.Background(), chat, "root", "", types.FailoverPolicy{}, history, stale, rtx)
		requireProviderSwitchHold(t, nil, err)
		assert.Empty(t, captured.Preferences, "a held compaction must not have built any driver")
	})
}
