package llmcatalog_test

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/llmcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGatewayModelsMatchCatalogRoster is the table test: every model the
// package exposes must correspond to a real models.yaml entry that carries a
// `reliant` provider mapping, and the projected api_model and costs must be
// exactly what that entry declares. This is what stops the exposed surface
// from drifting into a fifth hand-maintained copy.
func TestGatewayModelsMatchCatalogRoster(t *testing.T) {
	registry, err := models.ParseRegistry()
	require.NoError(t, err)

	gateway, err := llmcatalog.GatewayModels()
	require.NoError(t, err)
	require.Len(t, gateway, len(llmcatalog.GatewayModelIDs()))

	for _, model := range gateway {
		t.Run(model.ID, func(t *testing.T) {
			definition, ok := registry.GetDefinition(model.ID)
			require.True(t, ok, "exposed model %q has no models.yaml entry", model.ID)

			var reliantAPIModel string
			for _, provider := range definition.Providers {
				if provider.Driver == "reliant" {
					reliantAPIModel = provider.APIModel
					break
				}
			}
			require.NotEmpty(t, reliantAPIModel,
				"exposed model %q has no reliant provider mapping in models.yaml", model.ID)

			assert.Equal(t, reliantAPIModel, model.APIModel)
			assert.Equal(t, definition.Cost.InputPer1M, model.Cost.InputPer1MUSD)
			assert.Equal(t, definition.Cost.OutputPer1M, model.Cost.OutputPer1MUSD)
			assert.Equal(t, definition.Cost.CachedInputPer1M, model.Cost.CachedInputPer1MUSD)

			// The catalog id and the gateway api_model are both billable
			// spellings, so both must be aliases — the billing seeds key off
			// whichever spelling the upstream usage record happened to use.
			assert.Contains(t, model.Aliases, model.ID)
			assert.Contains(t, model.Aliases, model.APIModel)
		})
	}
}

// TestGatewayAPIModelSpellings pins the four api_model strings that prod's
// LiteLLM proxy is missing today. These are the exact spellings that produced
// "Invalid model name passed in model=claude-opus-5", so a change to any of
// them is a change to what prod must serve.
func TestGatewayAPIModelSpellings(t *testing.T) {
	gateway, err := llmcatalog.GatewayModels()
	require.NoError(t, err)

	byID := make(map[string]llmcatalog.Model, len(gateway))
	for _, model := range gateway {
		byID[model.ID] = model
	}

	for catalogID, wantAPIModel := range map[string]string{
		"claude-5-opus":    "claude-opus-5",
		"claude-5.1-fable": "claude-fable-5-1",
		"gemini-3.8-flash": "gemini-3.8-flash",
		"gemini-3.7-flash": "gemini-3.7-flash",
	} {
		model, ok := byID[catalogID]
		require.True(t, ok, "gateway roster is missing %q", catalogID)
		assert.Equal(t, wantAPIModel, model.APIModel)
	}
}

// TestGatewayAPIModelsIsTheAllowlist checks the flat allowlist projection,
// which control-plane feeds to the gateway verbatim. It must speak api_model
// spellings, never catalog ids — mixing the two is the precise confusion that
// control-plane's allowed_models_test.go already guards against.
func TestGatewayAPIModelsIsTheAllowlist(t *testing.T) {
	apiModels, err := llmcatalog.GatewayAPIModels()
	require.NoError(t, err)

	gateway, err := llmcatalog.GatewayModels()
	require.NoError(t, err)
	require.Len(t, apiModels, len(gateway))

	assert.Contains(t, apiModels, "claude-opus-5")
	assert.Contains(t, apiModels, "claude-fable-5-1")
	assert.Contains(t, apiModels, "claude-haiku-4-5")

	// The catalog spellings of those same models must NOT appear.
	assert.NotContains(t, apiModels, "claude-5-opus")
	assert.NotContains(t, apiModels, "claude-5.1-fable")
	assert.NotContains(t, apiModels, "claude-4.5-haiku")
}

// TestGatewayAliasesCoverEveryProviderSpelling pins the alias set for one
// model end to end. Billing prices by alias lookup, and a spelling that is
// missing from the set bills at ZERO rather than failing loudly.
func TestGatewayAliasesCoverEveryProviderSpelling(t *testing.T) {
	gateway, err := llmcatalog.GatewayModels()
	require.NoError(t, err)

	for _, model := range gateway {
		if model.ID != "claude-5-opus" {
			continue
		}
		assert.ElementsMatch(t,
			[]string{"claude-5-opus", "claude-opus-5", "anthropic/claude-opus-5"},
			model.Aliases)
		return
	}
	t.Fatal("claude-5-opus is not on the gateway roster")
}

// TestCostNanosConversion pins the USD-to-nanos conversion. Billing seeds are
// integer nanos while the catalog is float dollars, so an error here is an
// error by a factor of a billion.
func TestCostNanosConversion(t *testing.T) {
	cost := llmcatalog.Cost{
		InputPer1MUSD:       5.0,
		OutputPer1MUSD:      25.0,
		CachedInputPer1MUSD: 0.5,
	}
	assert.Equal(t, int64(5_000_000_000), cost.InputPer1MUSDNanos())
	assert.Equal(t, int64(25_000_000_000), cost.OutputPer1MUSDNanos())
	assert.Equal(t, int64(500_000_000), cost.CachedInputPer1MUSDNanos())

	// 0.15 and 0.08 are not exactly representable in binary floating point;
	// they must still round to whole nanos rather than truncating downward.
	assert.Equal(t, int64(150_000_000), llmcatalog.Cost{InputPer1MUSD: 0.15}.InputPer1MUSDNanos())
	assert.Equal(t, int64(80_000_000), llmcatalog.Cost{InputPer1MUSD: 0.08}.InputPer1MUSDNanos())
	assert.Equal(t, int64(0), llmcatalog.Cost{}.CachedInputPer1MUSDNanos())
}

// TestGatewayModelIDsAreUnique guards the roster itself. It is the one
// hand-written list left, so a duplicated or empty entry must fail here
// rather than silently produce a short allowlist.
func TestGatewayModelIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, id := range llmcatalog.GatewayModelIDs() {
		assert.NotEmpty(t, id)
		assert.False(t, seen[id], "duplicate roster entry %q", id)
		seen[id] = true
	}
}
