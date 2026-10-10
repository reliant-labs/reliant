// Copyright (c) 2025 Reliant Labs
package models

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A model only an unconnected provider serves is replaced by the pick of its
// most capable tier among the providers the user has: gpt-5.6-sol is codex-only
// and listed under powerful, so a Claude-only user gets powerful's Claude pick.
func TestSubstitute_UnservedModelTakesItsTiersPick(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()
	require.Contains(t, reg.TagsOf("gpt-5.6-sol"), TagPowerful, "fixture: gpt-5.6-sol is a powerful-tier model")

	resolved, ok := reg.Substitute("gpt-5.6-sol", []string{"anthropic"})
	require.True(t, ok)
	assert.Equal(t, "anthropic", resolved.Provider.Driver)

	want, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, want.Definition.ID, resolved.Definition.ID, "the substitute is what [powerful] resolves to for this user")
	assert.Equal(t, want.ThinkingLevel, resolved.ThinkingLevel, "at that tier's effort")
}

// When another connected provider serves the very same model, that is the
// substitute: nothing about the model changes but the provider.
func TestSubstitute_SameModelOnAnotherProviderWins(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()
	def, ok := reg.GetDefinition("claude-5.5-opus")
	require.True(t, ok)
	require.True(t, slices.ContainsFunc(def.Providers, func(p ProviderMapping) bool { return p.Driver == "anthropic" }),
		"fixture: anthropic serves claude-5.5-opus")

	resolved, ok := reg.Substitute("claude-5.5-opus", []string{"anthropic"})
	require.True(t, ok)
	assert.Equal(t, "claude-5.5-opus", resolved.Definition.ID)
	assert.Equal(t, "anthropic", resolved.Provider.Driver)
}

func TestSubstitute_NothingToSubstitute(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	_, ok := reg.Substitute("gpt-5.6-sol", nil)
	assert.False(t, ok, "no servable provider at all")

	_, ok = reg.Substitute("no-such-model", []string{"anthropic"})
	assert.False(t, ok, "an unknown model has no tier to stand in for it")
}

// The notice a send shows reuses Explain's first clause, so the two never
// disagree about why a pinned provider cannot serve.
func TestProviderUnavailableError_ReasonIsExplainsFirstClause(t *testing.T) {
	t.Parallel()
	pinned := &ProviderUnavailableError{ModelID: "gpt-5.6-sol", Providers: []string{"codex"}}
	assert.Equal(t, "gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected", pinned.Reason(nil))
	assert.Equal(t, pinned.Reason(nil)+". Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker",
		pinned.Explain(nil))
}
