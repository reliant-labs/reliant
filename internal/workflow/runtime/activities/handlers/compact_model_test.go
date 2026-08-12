// Copyright (c) 2025 Reliant Labs
// Regression tests for compaction model selection.
//
// Compaction used to summarize with a hardcoded tier list headed by
// claude-4.6-sonnet (a 200k-window model), regardless of the model the agent
// loop was running. That is unsound by construction: compaction only fires
// because the thread outgrew the AGENT model's window, so an 850k-token
// claude-5-opus thread was handed to a 200k summarizer, which the provider
// rejected outright ("Usage credits are required for long context requests",
// HTTP 429). These tests pin the fix: the agent's own model wins.
package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveCompactionModelWith mirrors resolveCompactionModel's selection logic
// against an explicit provider list, so the test does not depend on the global
// API-key provider that GetAvailableDrivers reads.
func resolveCompactionModelWith(t *testing.T, availableProviders []string, preferred *reliantv1.ModelSelector) (models.ModelSelector, string) {
	t.Helper()
	registry := models.MustGetRegistry()

	if preferred != nil && (preferred.GetId() != "" || len(preferred.GetTags()) > 0) {
		selector := models.ModelSelector{
			ID:        preferred.GetId(),
			Tags:      preferred.GetTags(),
			Providers: preferred.GetProviders(),
		}
		if _, err := registry.Resolve(selector, availableProviders); err == nil {
			return selector, "agent model (summarizes in the window that filled)"
		}
	}
	for _, modelID := range compactionModelTier {
		selector := models.ModelSelector{ID: string(modelID)}
		if _, err := registry.Resolve(selector, availableProviders); err == nil {
			return selector, "best available from tier list"
		}
	}
	return models.ModelSelector{}, ""
}

func TestResolveCompactionModel_PrefersAgentModel(t *testing.T) {
	providers := []string{"anthropic"}

	// The exact failing shape: a large-window agent model must summarize itself
	// rather than falling to the 200k-window head of the tier list.
	selector, reason := resolveCompactionModelWith(t, providers, &reliantv1.ModelSelector{Id: "claude-5-opus"})
	assert.Equal(t, "claude-5-opus", selector.ID)
	assert.Contains(t, reason, "agent model")

	// The summarization window must be at least the window that triggered
	// compaction — this is the property the 429 violated.
	def, ok := models.MustGetRegistry().GetDefinition(selector.ID)
	require.True(t, ok, "agent model must resolve in the registry")
	agentWindow := models.EffectiveContextWindow(def, "anthropic")

	tierDef, ok := models.MustGetRegistry().GetDefinition(string(compactionModelTier[0]))
	require.True(t, ok)
	tierWindow := models.EffectiveContextWindow(tierDef, "anthropic")

	assert.Greater(t, agentWindow, tierWindow,
		"this test is only meaningful while the tier head is smaller-windowed than the agent model")
}

func TestResolveCompactionModel_FallsBackToTierList(t *testing.T) {
	providers := []string{"anthropic"}

	// No model declared on the node (older workflows) => tier list.
	selector, reason := resolveCompactionModelWith(t, providers, nil)
	assert.Equal(t, string(compactionModelTier[0]), selector.ID)
	assert.Contains(t, reason, "tier list")

	// A model that does not resolve against the user's providers => tier list,
	// rather than failing the compaction outright.
	selector, reason = resolveCompactionModelWith(t, providers, &reliantv1.ModelSelector{Id: "not-a-real-model"})
	assert.Equal(t, string(compactionModelTier[0]), selector.ID)
	assert.Contains(t, reason, "tier list")
}
