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
