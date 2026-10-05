// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogMappingsAgreeWithDriverRegistration is the agreement guard for
// EVERY registered chat driver, curated ones included (reliant, copilot, codex),
// which TestEveryCatalogProviderMappingIsRegistered skips.
//
// A models.yaml `- driver: D` mapping on a text model promises that D serves
// it. When D's registered list is narrower, the picker offers the model and
// resolution picks it, then the request is re-routed to another provider or
// fails — a silent lie. Both directions are checked:
//
//   - catalog -> driver: every text-model mapping satisfies CanDriverUseModel;
//   - driver -> catalog: every model a driver claims is mapped to it.
//
// Image-output models are exempt for codex and reliant: images go through
// imagegen, not the chat driver. Video-output models are exempt for reliant
// too: they go through videogen.
func TestCatalogMappingsAgreeWithDriverRegistration(t *testing.T) {
	reg, err := models.GetRegistry()
	require.NoError(t, err)

	imageViaImagegen := map[string]bool{"codex": true, "reliant": true}
	mapped := map[string]map[string]bool{}
	// mappedViaMedia records non-text mappings on drivers whose media goes
	// through imagegen/videogen: the roster registers them, the chat check skips them.
	mappedViaMedia := map[string]map[string]bool{}

	for _, def := range reg.ListAll() {
		if def.Visibility == models.VisibilityDev {
			continue
		}
		textModel := def.Capabilities.CanOutput(models.ModalityText)
		for _, p := range def.Providers {
			if mapped[p.Driver] == nil {
				mapped[p.Driver] = map[string]bool{}
			}
			if !textModel && imageViaImagegen[p.Driver] {
				if mappedViaMedia[p.Driver] == nil {
					mappedViaMedia[p.Driver] = map[string]bool{}
				}
				mappedViaMedia[p.Driver][def.ID] = true
				continue
			}
			mapped[p.Driver][def.ID] = true

			if _, isChatDriver := registry.GetDriverFactory(models.Family(p.Driver)); !isChatDriver {
				continue
			}
			assert.True(t, models.CanDriverUseModel(models.Family(p.Driver), models.ModelID(def.ID)),
				"models.yaml maps %q to driver %q, but %q does not serve it (CanDriverUseModel=false): "+
					"the picker offers it and resolution picks it. Remove the mapping, or register the model.",
				def.ID, p.Driver, p.Driver)
		}
	}

	for family, registered := range models.DriverMapping {
		driver := string(family)
		if driver == "local" || driver == "mock" {
			continue
		}
		for _, id := range registered {
			assert.True(t, mapped[driver][string(id)] || mappedViaMedia[driver][string(id)],
				"driver %q registers %q but models.yaml has no (text) `- driver: %s` mapping for it", driver, id, driver)
		}
	}
}
