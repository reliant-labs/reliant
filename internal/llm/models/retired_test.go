// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Vertex AI retires gemini-2.5-pro and gemini-2.5-flash on 2026-10-20, and
// Google names gemini-3.8-flash first among the replacements for both
// (docs.cloud.google.com/vertex-ai/generative-ai/docs/learn/model-versions,
// "Replacement model ID"). The managed gateway serves Gemini FROM Vertex, so
// gemini-2.5-pro leaves `reliant` the same day. After it, a request for any of
// them on those providers 404s.
//
// Chats, presets and workflow inputs still name these ids, pinned to the
// provider the picker chose. Each must keep resolving — to the replacement, on
// the same provider — rather than fail with "model not found" or a 404.
func TestRetiredGemini25ResolvesToItsReplacement(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, tc := range []struct {
		selector   string
		providers  []string
		wantModel  string
		wantDriver string
	}{
		{"vertex-gemini-2.5-pro@vertexai", []string{"vertexai"}, "vertex-gemini-3.8-flash", "vertexai"},
		{"vertex-gemini-2.5-pro", []string{"vertexai"}, "vertex-gemini-3.8-flash", "vertexai"},
		{"vertex-gemini-2.5-flash@vertexai", []string{"vertexai"}, "vertex-gemini-3.8-flash", "vertexai"},
		{"vertex-gemini-2.5-flash", []string{"vertexai"}, "vertex-gemini-3.8-flash", "vertexai"},
		// Pinned to the gateway: stays on the gateway, on its replacement.
		{"gemini-2.5-pro@reliant", []string{"reliant", "gemini"}, "gemini-3.8-flash", "reliant"},
		// Unpinned, and the gateway is all the user has.
		{"gemini-2.5-pro", []string{"reliant"}, "gemini-3.8-flash", "reliant"},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			t.Parallel()
			resolved, err := reg.Resolve(ModelSelector{ID: tc.selector}, tc.providers)
			require.NoError(t, err)
			assert.Equal(t, tc.wantModel, resolved.Definition.ID)
			assert.Equal(t, tc.wantDriver, resolved.Provider.Driver)
			assert.Equal(t, "gemini-3.8-flash", resolved.Provider.APIModel)
		})
	}
}

// Unpinned, the replacement stays on the provider that retired the model even
// when a bring-your-own provider would otherwise win the tie: the move must
// not also change whose bill the chat runs on.
func TestRetiredUnpinnedStaysOnTheRetiringProvider(t *testing.T) {
	t.Parallel()
	resolved, err := MustGetRegistry().Resolve(ModelSelector{ID: "gemini-2.5-pro"}, []string{"reliant", "antigravity"})
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash", resolved.Definition.ID)
	assert.Equal(t, "reliant", resolved.Provider.Driver)
	require.NotNil(t, resolved.Retirement)
	assert.Equal(t, "gemini-2.5-pro", resolved.Retirement.Model)
}

// The Gemini API does not retire gemini-2.5-pro: it lists the model with no
// shutdown date and "not deprecated" (ai.google.dev/gemini-api/docs/deprecations,
// checked 2026-10-11). A user's own key keeps reaching it, unchanged.
func TestGemini25ProStaysOnTheGeminiAPI(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, selector := range []string{"gemini-2.5-pro@gemini", "gemini-2.5-pro"} {
		resolved, err := reg.Resolve(ModelSelector{ID: selector}, []string{"gemini", "reliant"})
		require.NoError(t, err, selector)
		assert.Equal(t, "gemini-2.5-pro", resolved.Definition.ID, selector)
		assert.Equal(t, "gemini", resolved.Provider.Driver, selector)
	}
}

// Nothing offers a retired model: it is gone from the picker, from every tier,
// and from the provider listing that the Vertex and gateway tests enumerate.
func TestRetiredGemini25IsNotOffered(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, id := range []string{"vertex-gemini-2.5-pro", "vertex-gemini-2.5-flash"} {
		_, defined := reg.GetDefinition(id)
		assert.False(t, defined, "%s is retired; it must have no definition", id)
		assert.Empty(t, reg.TagsOf(id), "%s is retired; no tier may list it", id)
	}
	for _, def := range reg.ListModelsByProvider("reliant") {
		assert.NotEqual(t, "gemini-2.5-pro", def.ID, "the gateway serves Gemini from Vertex, which retires gemini-2.5-pro")
	}
}

// When nothing the user has can serve the replacement, a send still has a
// substitute to offer: the replacement's own tier.
func TestSubstitute_RetiredModelStandsInThroughItsReplacement(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	resolved, ok := reg.Substitute("vertex-gemini-2.5-pro", []string{"vertexai"})
	require.True(t, ok)
	assert.Equal(t, "vertex-gemini-3.8-flash", resolved.Definition.ID)

	resolved, ok = reg.Substitute("vertex-gemini-2.5-pro", []string{"anthropic"})
	require.True(t, ok, "a retired model's replacement still has tiers to stand in from")
	assert.Equal(t, "anthropic", resolved.Provider.Driver)
}

// A user who defines a custom model under a retired id means their model, not
// the catalog's replacement for the one Google shut down.
func TestRetired_CustomModelOfTheSameIDWins(t *testing.T) {
	t.Parallel()
	reg, err := CreateRegistryWithUserConfig(&UserModelsConfig{Custom: []ModelDefinition{{
		ID:        "vertex-gemini-2.5-pro",
		Providers: []ProviderMapping{{Driver: "vertexai", APIModel: "my-tuned-gemini"}},
	}}})
	require.NoError(t, err)

	resolved, err := reg.Resolve(ModelSelector{ID: "vertex-gemini-2.5-pro@vertexai"}, []string{"vertexai"})
	require.NoError(t, err)
	assert.Equal(t, "vertex-gemini-2.5-pro", resolved.Definition.ID)
	assert.Equal(t, "my-tuned-gemini", resolved.Provider.APIModel)
	assert.Nil(t, resolved.Retirement)

	still, err := MustGetRegistry().Resolve(ModelSelector{ID: "vertex-gemini-2.5-pro@vertexai"}, []string{"vertexai"})
	require.NoError(t, err)
	assert.Equal(t, "vertex-gemini-3.8-flash", still.Definition.ID, "the user's config must not touch the shared catalog")
}

// A retirement is validated at parse time, like a tag entry: a typo in one does
// nothing at runtime and looks exactly like a working redirect.
func TestParseRegistryFromBytes_RetiredErrors(t *testing.T) {
	t.Parallel()
	const models = `
models:
  - id: old
    providers:
      - driver: gemini
        api_model: old
  - id: new
    providers:
      - driver: gemini
        api_model: new
      - driver: vertexai
        api_model: new
`
	for _, tc := range []struct {
		name, retired, wantErr string
	}{
		{"unknown replacement", `
retired:
  - {model: gone, drivers: [vertexai], replacement: nope, retired_on: "2026-10-20"}
`, `retired[0] (gone): unknown replacement "nope"`},
		{"replacement does not serve the retired driver", `
retired:
  - {model: gone, drivers: [reliant], replacement: new, retired_on: "2026-10-20"}
`, `retired[0] (gone): replacement new has no reliant mapping`},
		{"retired driver still mapped", `
retired:
  - {model: old, drivers: [gemini], replacement: new, retired_on: "2026-10-20"}
`, `retired[0] (old): still maps gemini`},
		{"no drivers", `
retired:
  - {model: gone, replacement: new, retired_on: "2026-10-20"}
`, `retired[0] (gone): no drivers`},
		{"bad date", `
retired:
  - {model: gone, drivers: [vertexai], replacement: new, retired_on: "Oct 20"}
`, `retired[0] (gone): retired_on "Oct 20" is not YYYY-MM-DD`},
		{"driver retired twice", `
retired:
  - {model: gone, drivers: [vertexai], replacement: new, retired_on: "2026-10-20"}
  - {model: gone, drivers: [vertexai], replacement: old, retired_on: "2026-10-21"}
`, `retired[1] (gone): vertexai is already retired`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRegistryFromBytes([]byte(tc.retired + models))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
