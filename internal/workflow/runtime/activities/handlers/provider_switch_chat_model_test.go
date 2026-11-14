// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// A descendant whose own model the chat's bound provider cannot serve runs on
// the chat's model instead of holding, unless the user picked the descendant's
// model themselves.
//
// Prod, 2026-10-31..11-02: chats bound to claude (root claude-5.5-opus@claude)
// spawned the project's code_reviewer preset, which pins
// {id: gpt-6.1-sol, providers: [codex]}. switchSelector probed
// gpt-6.1-sol@claude, the registry refused it, and every review sub-agent was
// parked at its first model call with a non-retryable ProviderSwitchHold.

// offProviderPreset is that preset's model: an exact id pinned to a provider
// the chat is not bound to.
var offProviderPreset = models.ModelSelector{ID: "gpt-6.1-sol", Providers: []string{"codex"}}

func TestProviderSwitch_OffProviderPresetDescendantRunsOnTheChatModel(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)
	require.False(t, defServedBy(mustDef(t, "gpt-6.1-sol"), "anthropic"), "fixture: the bound provider must not serve the preset's model")

	t.Run("control: without the chat's model the old behavior holds, naming the thread's own selector", func(t *testing.T) {
		res, err := resolve(llmCallSpec{Selector: offProviderPreset, ChatProvider: "anthropic", Descendant: true})
		requireProviderSwitchHold(t, res, err)
		assert.Contains(t, err.Error(), `id="gpt-6.1-sol"`)
		assert.Contains(t, err.Error(), "providers=[codex]")
		assert.NotContains(t, err.Error(), "gpt-6.1-sol@anthropic",
			"the hold must not report a model composed onto a driver that cannot serve it")
	})

	t.Run("the chat's exact model", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:      offProviderPreset,
			ThinkingLevel: "high",
			ChatProvider:  "anthropic",
			Descendant:    true,
			ChatModel:     models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
		})
		requireOn(t, res, err, "anthropic")
		assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
	})

	t.Run("the chat's model as tags", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     offProviderPreset,
			ChatProvider: "anthropic",
			Descendant:   true,
			ChatModel:    models.ModelSelector{Tags: []string{models.TagFlagship}},
		})
		requireOn(t, res, err, "anthropic")
		assert.Equal(t, firstOn(t, models.TagFlagship, "anthropic"), res.ModelID)
	})

	t.Run("an unpinned chat model is pinned to the bound provider", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     offProviderPreset,
			ChatProvider: "anthropic",
			Descendant:   true,
			ChatModel:    models.ModelSelector{ID: "claude-4.5-haiku"},
		})
		requireOn(t, res, err, "anthropic")
	})

	t.Run("an unknown tag falls back too", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{Tags: []string{"no-such-tier"}},
			ChatProvider: "anthropic",
			Descendant:   true,
			ChatModel:    models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
		})
		requireOn(t, res, err, "anthropic")
		assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
	})

	t.Run("a descendant that can run on the bound provider keeps its own model", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "gpt-6.1-sol", Providers: []string{"openai"}},
			ChatProvider: "codex",
			Descendant:   true,
			ChatModel:    models.ModelSelector{ID: "gpt-6-luna@codex"},
		})
		requireOn(t, res, err, "codex")
		assert.Equal(t, "gpt-6.1-sol@codex", res.ModelID)
	})
}

// The hold keeps its legitimate use: the user's own model choice for a thread
// (a thread-scoped model edit) waits for the user, as does a root's pick.
func TestProviderSwitch_UserChoicesStillHold(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)
	chatModel := models.ModelSelector{ID: "claude-4.5-haiku@anthropic"}

	t.Run("a descendant model the user chose", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:          offProviderPreset,
			ChatProvider:      "anthropic",
			Descendant:        true,
			ChatModel:         chatModel,
			ThreadModelChosen: true,
		})
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("the root never substitutes", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "gpt-6.1-sol@codex"},
			ChatProvider: "anthropic",
			ChatModel:    chatModel,
		})
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("a chat model the bound provider cannot serve either holds on the thread's own selector", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     offProviderPreset,
			ChatProvider: "anthropic",
			Descendant:   true,
			ChatModel:    models.ModelSelector{ID: "gpt-6-luna@codex"},
		})
		requireProviderSwitchHold(t, res, err)
		assert.Contains(t, err.Error(), `id="gpt-6.1-sol"`)
	})
}

// Compaction summarizes on the model the agent turns ran on, so it falls back
// to the chat's model on the same terms.
func TestProviderSwitchCompaction_OffProviderPresetDescendantUsesTheChatModel(t *testing.T) {
	withAvailableDrivers(t, boundDrivers("", "anthropic", "codex", "openai"))
	ctx := context.Background()
	preset := &reliantv1.ModelSelector{Id: "gpt-6.1-sol", Providers: []string{"codex"}}

	spec := compactSpec("anthropic", true)
	spec.ChatModel = models.ModelSelector{ID: "claude-4.5-haiku@anthropic"}
	selector, reason, err := resolveCompactionModelForCall(ctx, preset, spec)
	require.NoError(t, err)
	assert.Equal(t, "claude-4.5-haiku@anthropic", selector.ID)
	assert.Equal(t, []string{"anthropic"}, selector.Providers)
	assert.Contains(t, reason, "chat's model")

	t.Run("a model the user chose still holds", func(t *testing.T) {
		chosen := spec
		chosen.ThreadModelChosen = true
		_, _, err := resolveCompactionModelForCall(ctx, preset, chosen)
		requireProviderSwitchHold(t, nil, err)
	})
}
