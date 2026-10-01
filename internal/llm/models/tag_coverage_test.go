// Copyright (c) 2025 Reliant Labs
package models

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// coreTags are the tiers any feature may ask for by name. They are the product's
// vocabulary, so every provider has to answer all of them — a tier that cannot
// resolve is not a degraded experience, it is a hard failure at request time
// ("failed to resolve model: no available provider for models with tags: ...").
var coreTags = []string{
	TagPowerful, TagFlagship, TagModerate, TagFast, TagCheap, TagReasoning, TagMeta,
}

// textCatalogDrivers is every driver the embedded catalog maps to at least one
// shippable TEXT-producing model, derived rather than listed: a provider added
// to models.yaml is covered by the invariant below the moment its first text
// model lands, with no test edit.
//
// Two kinds of driver are absent by construction rather than by exception.
// Drivers with no catalog models at all (ollama, whose models are configured by
// the user at runtime) never appear. Neither does a driver whose only models are
// VisibilityDev test fixtures — `mock` serves exactly the three fixtures used by
// the test harness, and no user can configure it as a provider, so tagging them
// would mean putting fake models in the tiers real requests resolve through.
func textCatalogDrivers(reg *ModelRegistry) []string {
	seen := map[string]bool{}
	for _, def := range reg.ListAll() {
		if !def.Capabilities.CanOutput(ModalityText) || def.Visibility == VisibilityDev {
			continue
		}
		for _, p := range def.Providers {
			seen[p.Driver] = true
		}
	}
	out := make([]string, 0, len(seen))
	for driver := range seen {
		out = append(out, driver)
	}
	sort.Strings(out)
	return out
}

// EVERY provider must implement the full default tag set.
//
// The failure this pins: a user whose only provider was antigravity could not
// get a chat title, because the titling selector's tags resolved to nothing
// servable by that driver. Tag resolution filters to the providers a user
// actually configured, so a tag list that looks complete globally can be empty
// for a single-provider user — and nothing else in the suite notices, because
// every other test runs with the full provider set.
func TestEveryProviderImplementsEveryCoreTag(t *testing.T) {
	reg := MustGetRegistry()

	for _, driver := range textCatalogDrivers(reg) {
		t.Run(driver, func(t *testing.T) {
			for _, tag := range coreTags {
				_, err := reg.Resolve(ModelSelector{
					Tags:                  []string{tag},
					RequireOutputModality: ModalityText,
				}, []string{driver})
				assert.NoErrorf(t, err,
					"provider %q cannot resolve [%s]: a user whose only provider is %q gets a hard "+
						"resolution failure from any feature asking for that tier. Fix by APPENDING an "+
						"entry for a %s-served model to the END of the %q list in "+
						"definitions/models.yaml — appending cannot change any existing resolution, "+
						"because a trailing entry is reached only when nothing earlier is servable.",
					driver, tag, driver, driver, tag)
			}
		})
	}
}
