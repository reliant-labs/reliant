// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Antigravity serves two models, both over Google's v1internal cloudcode
// endpoint rather than the public Gemini API: gemini-3.8-flash for chat, and
// gemini-3.1-flash-image for image generation. They are reached by DIFFERENT
// clients on different paths (:streamGenerateContent?alt=sse versus
// :generateContent) and only the chat one takes an effort suffix.
//
// The catalog half of that claim is pinned here. The driver half — that
// something registered the `antigravity` family — is pinned by
// TestEveryCatalogProviderMappingIsRegistered in internal/llm/drivers, which
// is the only place both structures are visible at once. These tests exist
// because a provider mapping with no matching registration resolves to "no
// configured driver can serve it", which is indistinguishable from a
// disconnected account.
func TestAntigravityCatalogMapsGemini38Flash(t *testing.T) {
	reg := MustGetRegistry()

	definition, found := reg.GetDefinition(string(Gemini38Flash))
	require.True(t, found, "gemini-3.8-flash must exist in the catalog")

	var antigravity *ProviderMapping
	for i := range definition.Providers {
		if definition.Providers[i].Driver == "antigravity" {
			antigravity = &definition.Providers[i]
			break
		}
	}
	require.NotNil(t, antigravity,
		"gemini-3.8-flash must carry an `antigravity` provider mapping")

	// Antigravity addresses effort as a model-id suffix on the top-level
	// `model` field (gemini-3.8-flash-high). The suffix is composed by the
	// driver from the request's thinking level; baking it into api_model here
	// would pin every request to one effort and make the catalog disagree with
	// the thinking_levels declared right above it.
	assert.Equal(t, "gemini-3.8-flash", antigravity.APIModel,
		"api_model must be the BASE id — the driver appends the effort suffix")
}

// The driver's effort-suffix mapping reads these levels, so a catalog that
// dropped one would silently narrow what Antigravity can be asked for.
func TestAntigravityModelDeclaresAllThreeEffortLevels(t *testing.T) {
	reg := MustGetRegistry()

	definition, found := reg.GetDefinition(string(Gemini38Flash))
	require.True(t, found)

	assert.ElementsMatch(t,
		[]string{"low", "medium", "high"},
		definition.Capabilities.ThinkingLevels,
		"antigravity exposes low/medium/high as model-id suffixes")
}

// Resolution must reach antigravity when it is the only connected provider,
// and must NOT steal the model from the native Gemini driver when both are
// connected — they share priority 1, so this is decided by catalog order and
// is exactly the kind of thing that flips silently on an edit.
func TestAntigravityResolution(t *testing.T) {
	reg := MustGetRegistry()

	t.Run("resolves when antigravity is the only provider", func(t *testing.T) {
		resolved, err := reg.Resolve(ModelSelector{ID: string(Gemini38Flash)}, []string{"antigravity"})
		require.NoError(t, err)
		assert.Equal(t, "antigravity", resolved.Provider.Driver)
		assert.Equal(t, "gemini-3.8-flash", resolved.Provider.APIModel)
	})

	t.Run("native gemini still wins when both are connected", func(t *testing.T) {
		resolved, err := reg.Resolve(ModelSelector{ID: string(Gemini38Flash)}, []string{"antigravity", "gemini"})
		require.NoError(t, err)
		assert.Equal(t, "gemini", resolved.Provider.Driver)
	})
}

// The two systems that decide what a driver can serve — CanDriverUseModel
// (the mapping table) and Resolve (the YAML providers list) — must reach the
// same answer, or a model shows up in the picker and then fails to resolve.
// Registering the family here rather than importing the driver keeps this
// package free of a dependency on internal/llm/drivers; the driver's own
// init() performs exactly this call, and the drivers package pins that it did.
func TestAntigravityMappingAndResolutionAgree(t *testing.T) {
	RegisterCatalogDriver(Family("antigravity"))

	assert.True(t, CanDriverUseModel(Family("antigravity"), Gemini38Flash),
		"CanDriverUseModel must see the catalog mapping")

	// The served set is pinned EXACTLY rather than by count. Every model
	// mapped to antigravity needs a client that can call it, and the two here
	// are reached by different ones — the chat driver and
	// imagegen.AntigravityClient. An unrecognized third mapping would resolve
	// cleanly and then fail at call time, which is the failure this guards.
	//
	// It also fixes which models take an effort suffix. The suffix is a
	// thinking concern: gemini-3.8-flash is addressed as "…-high", while the
	// captured image request sends "gemini-3.1-flash-image" bare. The driver's
	// effortVariants map encodes that, and the drivers package pins the bare
	// api_model in TestAntigravityImageModel_IsMappedForImageOutput.
	served := GetModelsForDriver(Family("antigravity"))
	assert.ElementsMatch(t,
		[]ModelID{Gemini38Flash, ModelID("gemini-3.1-flash-image")},
		served,
		"a catalog mapping with no client that can call it resolves cleanly and then fails at call time")

	assert.Contains(t, GetDriversForModel(Gemini38Flash), Family("antigravity"))
}
