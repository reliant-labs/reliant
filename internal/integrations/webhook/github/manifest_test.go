// Copyright (c) 2025 Reliant Labs
package github

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// declaredTriggers maps each event type the embedded github manifest's
// triggers name to the attributes declared for it.
func declaredTriggers(t *testing.T) map[string]map[string]bool {
	t.Helper()
	var found bool
	out := map[string]map[string]bool{}
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() != ProviderID {
			continue
		}
		found = true
		for _, tr := range m.GetTriggers() {
			for _, ev := range tr.GetEvents() {
				if out[ev] == nil {
					out[ev] = map[string]bool{}
				}
				for _, a := range tr.GetAttributes() {
					out[ev][a.GetName()] = true
				}
			}
		}
	}
	require.True(t, found, "no embedded github manifest")
	return out
}

// declaredDataKeys maps each event type to the top-level keys its trigger's
// `data` schema declares, and whether the schema is closed.
func declaredDataKeys(t *testing.T) (keys map[string]map[string]bool, closed map[string]bool) {
	t.Helper()
	keys, closed = map[string]map[string]bool{}, map[string]bool{}
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() != ProviderID {
			continue
		}
		for _, tr := range m.GetTriggers() {
			schema := tr.GetData().AsMap()
			props, _ := schema["properties"].(map[string]any)
			for _, ev := range tr.GetEvents() {
				keys[ev] = map[string]bool{}
				for k := range props {
					keys[ev][k] = true
				}
				closed[ev] = schema["additionalProperties"] == false
			}
		}
	}
	return keys, closed
}
