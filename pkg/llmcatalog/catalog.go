// Package llmcatalog exposes the set of models reachable through the managed
// Reliant LLM gateway, as a public, cross-module surface.
//
// It exists so that the deployment artifacts describing the gateway — the
// LiteLLM proxy's model_list, the gateway key allowlist, and the billing
// pricing seeds — can be GENERATED from the same catalog the app resolves
// against, instead of being hand-maintained alongside it. Those lists having
// drifted apart is what produced "Invalid model name passed in
// model=claude-opus-5" in production.
//
// The surface here is deliberately narrow. It projects models.yaml down to the
// four facts a downstream generator needs — catalog id, gateway api_model,
// billing aliases, and per-1M costs — and nothing else. Capabilities, driver
// settings and thinking policy are Reliant's own business: exposing them would
// turn every internal refactor into a downstream break.
//
// The types below are local struct copies rather than aliases of the internal
// ones, for the same reason. Callers depend on this projection, not on the
// shape of the catalog behind it.
package llmcatalog

import (
	"fmt"
	"math"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// gatewayDriver is the models.yaml provider driver name for the managed
// Reliant gateway. A model is servable through the gateway only if it carries
// a provider mapping for this driver.
const gatewayDriver = "reliant"

// Model is one model served through the Reliant gateway.
type Model struct {
	// ID is the catalog identifier — the spelling models.yaml declares and
	// the app selects by (for example "claude-5-opus").
	ID string

	// Name is the human-readable display name.
	Name string

	// APIModel is the identifier the gateway itself speaks (for example
	// "claude-opus-5"). This is the spelling that belongs in a LiteLLM
	// model_list entry and in a gateway key allowlist; using ID there is the
	// mistake that takes models offline.
	APIModel string

	// Aliases are every spelling this model may be billed under: the catalog
	// ID, the gateway APIModel, and each other provider's api_model. Usage
	// records arrive labelled with whichever spelling the upstream provider
	// used, so a pricing table keyed by a partial alias set silently bills
	// the missing spellings at zero.
	Aliases []string

	// Cost is the model's list price.
	Cost Cost
}

// Cost is a model's price, in US dollars per one million tokens.
type Cost struct {
	InputPer1MUSD       float64
	OutputPer1MUSD      float64
	CachedInputPer1MUSD float64
}

// InputPer1MUSDNanos returns the input price in integer USD nanos.
//
// Billing stores prices as nanos to keep arithmetic exact, and converting by
// hand is a 1e9-sized mistake waiting to happen — so the conversion lives here
// once rather than in each consumer.
func (c Cost) InputPer1MUSDNanos() int64 { return usdNanos(c.InputPer1MUSD) }

// OutputPer1MUSDNanos returns the output price in integer USD nanos.
func (c Cost) OutputPer1MUSDNanos() int64 { return usdNanos(c.OutputPer1MUSD) }

// CachedInputPer1MUSDNanos returns the cached-input price in integer USD nanos.
func (c Cost) CachedInputPer1MUSDNanos() int64 { return usdNanos(c.CachedInputPer1MUSD) }

func usdNanos(usd float64) int64 {
	return int64(math.Round(usd * 1e9))
}

// gatewayRoster is the curated set of catalog IDs the managed gateway serves,
// in the order they should appear in generated artifacts.
//
// This is a SUBSET of the models that carry a `reliant` provider mapping in
// models.yaml, and the difference is intentional: a mapping records that a
// model COULD be routed through the gateway, while this roster records that we
// have chosen to sell it. Several mapped models are deliberately withheld —
// see the exclusion assertions in internal/llm/drivers/reliant/models_test.go.
//
// Adding an entry here is what puts a model on the gateway. The roster is
// validated against models.yaml by TestGatewayModelsMatchCatalogRoster, so an
// entry that is absent from the catalog, or that lacks a `reliant` provider
// mapping, fails the build rather than emitting a broken allowlist.
var gatewayRoster = []string{
	"claude-5.1-fable",
	"claude-5-opus",
	"claude-4.6-opus",
	"claude-4.5-opus",
	"claude-4.6-sonnet",
	"claude-4.5-sonnet",
	"claude-4.5-haiku",
	"gemini-3.1-pro-preview",
	"gemini-3.8-flash",
	"gemini-3.7-flash",
	"gemini-3-flash-preview",
	"gemini-2.5-pro",
}

// GatewayModelIDs returns the catalog IDs of every model the gateway serves,
// in generated-artifact order.
//
// This reads no files and cannot fail, which is what makes it safe to call
// from a package-level variable initializer — see
// internal/llm/drivers/reliant, which derives its driver allowlist from it.
func GatewayModelIDs() []string {
	ids := make([]string, len(gatewayRoster))
	copy(ids, gatewayRoster)
	return ids
}

// GatewayModels returns the full projection of every model the gateway serves,
// in generated-artifact order.
//
// It fails rather than skipping when a rostered model is missing from the
// catalog or lacks a gateway provider mapping. A generator that quietly
// dropped a model would emit an allowlist that rejects it at runtime, which is
// exactly the failure this package exists to prevent.
func GatewayModels() ([]Model, error) {
	registry, err := models.ParseRegistry()
	if err != nil {
		return nil, fmt.Errorf("parse model catalog: %w", err)
	}

	result := make([]Model, 0, len(gatewayRoster))
	for _, id := range gatewayRoster {
		definition, ok := registry.GetDefinition(id)
		if !ok {
			return nil, fmt.Errorf("gateway model %q is not in the model catalog", id)
		}

		model, err := project(*definition)
		if err != nil {
			return nil, err
		}
		result = append(result, model)
	}
	return result, nil
}

// GatewayAPIModels returns just the gateway api_model strings, in
// generated-artifact order.
//
// This is the gateway key allowlist. It speaks api_model spellings and never
// catalog IDs — an allowlist populated with catalog IDs rejects every real
// request while looking correct.
func GatewayAPIModels() ([]string, error) {
	gateway, err := GatewayModels()
	if err != nil {
		return nil, err
	}

	apiModels := make([]string, 0, len(gateway))
	for _, model := range gateway {
		apiModels = append(apiModels, model.APIModel)
	}
	return apiModels, nil
}

// project converts one catalog definition into this package's local shape.
func project(definition models.ModelDefinition) (Model, error) {
	var apiModel string
	for _, provider := range definition.Providers {
		if provider.Driver == gatewayDriver {
			apiModel = provider.APIModel
			break
		}
	}
	if apiModel == "" {
		return Model{}, fmt.Errorf(
			"gateway model %q has no %q provider mapping in the model catalog",
			definition.ID, gatewayDriver)
	}

	// The catalog ID leads, then every provider's api_model in catalog order.
	aliases := []string{definition.ID}
	seen := map[string]bool{definition.ID: true}
	for _, provider := range definition.Providers {
		if provider.APIModel == "" || seen[provider.APIModel] {
			continue
		}
		seen[provider.APIModel] = true
		aliases = append(aliases, provider.APIModel)
	}

	return Model{
		ID:       definition.ID,
		Name:     definition.Name,
		APIModel: apiModel,
		Aliases:  aliases,
		Cost: Cost{
			InputPer1MUSD:       definition.Cost.InputPer1M,
			OutputPer1MUSD:      definition.Cost.OutputPer1M,
			CachedInputPer1MUSD: definition.Cost.CachedInputPer1M,
		},
	}, nil
}
