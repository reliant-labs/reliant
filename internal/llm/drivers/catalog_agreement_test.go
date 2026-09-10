// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"sort"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Every driver that participates in resolution must be imported here, so
	// its init() has registered before the agreement is checked. A driver
	// missing from this list is invisible to the test AND to the resolver.
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/gemini"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/vertexai"
)

// curatedDrivers are the drivers whose model list is deliberately NARROWER
// than their models.yaml provider mappings. For these, a mapping means "this
// model could be routed here", while the driver's list means "we have chosen
// to serve it" — so a catalog entry with no registration is expected.
//
// Every other driver must serve exactly what the catalog says it serves.
var curatedDrivers = map[string]string{
	"reliant": "gateway roster is curated in pkg/llmcatalog; see reliant/models_test.go",
	"copilot": "gated on per-account GitHub Copilot model policy; see copilot/models.go",
	"codex":   "a ChatGPT account serves a subset of the GPT catalog; see codex/models.go",
	"mock":    "test fixtures; no driver package participates in resolution",
	"xai":     "package is not wired into the resolver (no blank import in resolver.go)",
}

// TestEveryCatalogProviderMappingIsRegistered is the drift gate that this
// package previously lacked.
//
// models.yaml declaring `- driver: anthropic` for a model is a promise that
// asking for that model on that driver works. Resolution checks a SECOND
// structure — the DriverMapping populated by each driver's init() — and when
// the two disagree the resolver reports the model as unservable:
//
//	model "claude-5.1-fable": no configured driver can serve it
//	(explicitDriver="anthropic")
//
// That is indistinguishable from a disconnected provider, which is what made
// the original report ("even though I have claude code driver set") so hard to
// act on: the driver WAS connected, and the catalog DID list the model. Only
// the registration was missing.
func TestEveryCatalogProviderMappingIsRegistered(t *testing.T) {
	for driver, declared := range catalogModelsByDriver(t) {
		if reason, curated := curatedDrivers[driver]; curated {
			t.Logf("skipping curated driver %q: %s", driver, reason)
			continue
		}

		for _, modelID := range declared {
			assert.True(t,
				models.CanDriverUseModel(models.Family(driver), models.ModelID(modelID)),
				"models.yaml maps model %q to driver %q, but the driver never registered it — "+
					"selecting this model resolves to \"no configured driver can serve it\". "+
					"Registration lives in internal/llm/drivers/%s.",
				modelID, driver, driver)
		}
	}
}

// TestNoDriverRegistersAnUnmappedModel checks the other direction. A driver
// that claims a model the catalog does not map to it passes CanDriverUseModel
// and then fails later, at request time, with a provider-specific error —
// because ToModel takes APIModel from the FIRST provider mapping, so the
// request goes out addressed to the wrong provider's spelling.
func TestNoDriverRegistersAnUnmappedModel(t *testing.T) {
	declaredBy := catalogModelsByDriver(t)

	for family, registered := range models.DriverMapping {
		driver := string(family)

		// Local models are discovered at runtime from the user's endpoint and
		// are legitimately absent from the shipped catalog.
		if driver == "local" {
			continue
		}

		declared := make(map[string]bool, len(declaredBy[driver]))
		for _, id := range declaredBy[driver] {
			declared[id] = true
		}

		for _, modelID := range registered {
			assert.True(t, declared[string(modelID)],
				"driver %q registered model %q, but models.yaml has no `- driver: %s` "+
					"mapping for it — requests resolve, then go out with another "+
					"provider's api_model spelling.",
				driver, modelID, driver)
		}
	}
}

// TestFable51ResolvesOnAConnectedAnthropicDriver is the end-to-end reproduction
// of the reported failure, exercised through the real resolution path rather
// than the mapping table directly.
func TestFable51ResolvesOnAConnectedAnthropicDriver(t *testing.T) {
	// A connected Claude Code subscription: BuildAvailableDrivers registers
	// the sk-ant-oat token under the "anthropic" driver id, and the anthropic
	// factory routes oat keys to the Claude Code client.
	connected := models.AvailableDrivers{
		Drivers: map[models.DriverID]models.DriverConfig{
			models.DriverID("anthropic"): {
				DriverID: models.DriverID("anthropic"),
				APIKey:   "sk-ant-oat01-test",
				Enabled:  true,
			},
		},
	}

	config, found := models.SelectBestDriver(models.Claude51Fable, connected)
	require.True(t, found,
		"claude-5.1-fable must resolve when Anthropic is connected; "+
			"this is the failure the user reported")
	assert.Equal(t, models.DriverID("anthropic"), config.DriverID)
}

// catalogModelsByDriver inverts the catalog into driver -> declared model ids.
func catalogModelsByDriver(t *testing.T) map[string][]string {
	t.Helper()

	registry, err := models.GetRegistry()
	require.NoError(t, err)

	byDriver := map[string][]string{}
	for _, definition := range registry.ListAll() {
		for _, provider := range definition.Providers {
			byDriver[provider.Driver] = append(byDriver[provider.Driver], definition.ID)
		}
	}
	for _, ids := range byDriver {
		sort.Strings(ids)
	}
	return byDriver
}
