// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestAuthProviderReachesTheWebRuntimeConfig covers the deploy boundary that
// browser tests bypass by injecting AUTH_PROVIDER directly. Both omission (the
// production-compatible default) and explicit opt-in must reach /config.js.
func TestAuthProviderReachesTheWebRuntimeConfig(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**", "frontends/web/src/lib/config_schema_gen.json"))

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontends", "web", "src", "lib", "config_schema_gen.json"))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"name": "AUTH_PROVIDER"`)

	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		want     string
	}{
		{"provider_default", "supabase"},
		{"provider_forge", "forge"},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/web_auth_provider.k", tc.selector, &doc, project))
			require.Equal(t, "cloud", doc["AUTH_MODE"])
			require.Equal(t, tc.want, doc["AUTH_PROVIDER"], "AUTH_PROVIDER must survive the typed runtime projection")
		})
	}
}
