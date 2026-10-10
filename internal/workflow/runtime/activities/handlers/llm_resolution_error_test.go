// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Chat dfd85515's composer stored gpt-5.6-sol@codex; the user later
// disconnected Codex. The error said "none of required providers [codex]
// available for model gpt-5.6-sol. Please check your API key configuration in
// Settings" — true, and no help. It must name the provider and both ways out.
func TestResolutionError_PinnedModelNamesProviderAndFix(t *testing.T) {
	t.Parallel()
	_, resolveErr := models.MustGetRegistry().Resolve(models.ModelSelector{ID: "gpt-5.6-sol@codex"}, []string{"anthropic"})
	require.Error(t, resolveErr)

	err := resolutionError(resolveErr, models.AvailableDrivers{})
	assert.Equal(t,
		"failed to resolve model: gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. "+
			"Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker",
		err.Error())
	assert.ErrorIs(t, fmt.Errorf("failed to stream LLM response: %w", err), drivererrors.ErrNoServableProvider,
		"the sentinel must survive the activity's wrapping so the runtime fails once")
}

func TestResolutionError_RejectedProviderGivesItsReason(t *testing.T) {
	t.Parallel()
	_, resolveErr := models.MustGetRegistry().Resolve(models.ModelSelector{ID: "claude-5.5-sonnet@copilot"}, []string{"codex"})
	require.Error(t, resolveErr)

	err := resolutionError(resolveErr, models.AvailableDrivers{Unavailable: map[models.DriverID]string{
		"copilot": "GitHub Copilot rejected the saved credential (HTTP 400: bad request: Authorization header is badly formatted)",
	}})
	assert.Contains(t, err.Error(), "claude-5.5-sonnet runs only on GitHub Copilot")
	assert.Contains(t, err.Error(), "Authorization header is badly formatted")
	assert.Contains(t, err.Error(), "Reconnect GitHub Copilot in Settings → Providers")
}

func TestResolutionError_EmptyTierNamesRejectedProviders(t *testing.T) {
	t.Parallel()
	_, resolveErr := models.MustGetRegistry().Resolve(models.ModelSelector{Tags: []string{"flagship"}}, nil)
	require.Error(t, resolveErr)

	err := resolutionError(resolveErr, models.AvailableDrivers{Unavailable: map[models.DriverID]string{
		"codex": "Codex (ChatGPT) rejected the saved credential (HTTP 401: revoked)",
	}})
	assert.Contains(t, err.Error(), "Codex (ChatGPT) rejected the saved credential (HTTP 401: revoked)")
	assert.True(t, errors.Is(err, drivererrors.ErrNoServableProvider))
}
