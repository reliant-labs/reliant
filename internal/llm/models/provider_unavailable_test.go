// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// Picking a model in the composer stores it pinned to its provider
// ("gpt-5.6-sol@codex"). When that provider is gone the failure must be
// typed (so the runtime fails once instead of retrying 5×) and must explain
// itself — chat dfd85515 got "none of required providers [codex] available
// for model gpt-5.6-sol. Please check your API key configuration in Settings".
func TestResolve_PinnedProviderUnavailableIsTypedAndExplained(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	_, err := reg.Resolve(ModelSelector{ID: "gpt-5.6-sol@codex"}, []string{"anthropic"})
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider)
	var pinned *ProviderUnavailableError
	require.ErrorAs(t, err, &pinned)
	assert.Equal(t, "gpt-5.6-sol", pinned.ModelID)
	assert.Equal(t, []string{"codex"}, pinned.Providers)

	notConnected := pinned.Explain(nil)
	assert.Equal(t,
		"gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. "+
			"Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker",
		notConnected)

	rejected := pinned.Explain(map[DriverID]string{"codex": "Codex (ChatGPT) rejected the saved credential (HTTP 401: token revoked)"})
	assert.Contains(t, rejected, "Codex (ChatGPT) rejected the saved credential (HTTP 401: token revoked)")
	assert.NotContains(t, rejected, "is not connected")
}

// Every other "nothing can serve this" resolution carries the same sentinel.
func TestResolve_NoProviderFailuresAreNoServableProvider(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	_, err := reg.Resolve(ModelSelector{Tags: []string{"flagship"}}, nil)
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider, "an empty tier")

	_, err = reg.Resolve(ModelSelector{ID: "gpt-5.6-sol"}, []string{"anthropic"})
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider, "an unpinned model with no connected provider")

	disabled := reg.WithAvailability(func(driver, _ string) ModelAvailability {
		return ModelAvailability{Disabled: driver == "codex", Reason: "not on your plan"}
	})
	_, err = disabled.Resolve(ModelSelector{ID: "gpt-5.6-sol"}, []string{"codex"})
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider, "a model the account's provider disables")
	assert.Contains(t, err.Error(), "not on your plan")
}

// A connected, working provider still resolves its pinned model.
func TestResolve_PinnedProviderAvailableResolves(t *testing.T) {
	t.Parallel()
	resolved, err := MustGetRegistry().Resolve(ModelSelector{ID: "gpt-5.6-sol@codex"}, []string{"codex", "anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "codex", resolved.Provider.Driver)
	assert.Equal(t, "gpt-5.6-sol", resolved.Definition.ID)
}
