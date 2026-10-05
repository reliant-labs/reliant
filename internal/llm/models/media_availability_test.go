// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaUnavailableMessage_ProviderAddedToCatalogAppearsAutomatically(t *testing.T) {
	registry := MustGetRegistry().Clone()
	before := MediaUnavailableMessage(registry, ModalityVideo)
	assert.Equal(t, "Video generation needs a Gemini API key or Reliant-managed credits — add one in Settings → AI providers", before)

	require.NoError(t, registry.addCustomModels([]ModelDefinition{{
		ID:           "acme-video-1",
		Capabilities: ModelCapabilities{OutputModalities: []Modality{ModalityVideo}},
		Providers:    []ProviderMapping{{Driver: "acmevid", APIModel: "acme-video-1"}},
	}}))
	assert.Equal(t, "Video generation needs a Gemini API key, Reliant-managed credits or a acmevid provider — add one in Settings → AI providers",
		MediaUnavailableMessage(registry, ModalityVideo), "no code change needed for a new provider")
}

func TestMediaProviders_FollowsCatalogOrderAndModality(t *testing.T) {
	registry := MustGetRegistry()
	assert.Equal(t, []string{"gemini", "reliant"}, MediaProviders(registry, ModalityVideo))
	image := MediaProviders(registry, ModalityImage)
	assert.Contains(t, image, "openai")
	assert.Contains(t, image, "gemini")
	assert.Contains(t, image, "codex")
	assert.NotContains(t, MediaProviders(registry, ModalityText), "")
}
