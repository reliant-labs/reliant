// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestAuthCookieDomainReachesTheWebRuntimeConfig pins the deploy half of the
// shared sign-in session: ReliantWebConfig.auth_cookie_domain must come out of
// the reliant-web runtime config document as AUTH_COOKIE_DOMAIN, empty by
// default (every host keeps its own session) and the configured domain when
// set, under the key the web bundle's config schema reads. The web tests
// inject AUTH_COOKIE_DOMAIN directly, so without this a projection that
// dropped or renamed the key would leave the setting documented and inert.
func TestAuthCookieDomainReachesTheWebRuntimeConfig(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**", "frontends/web/src/lib/config_schema_gen.json"))

	// The key the bundle reads at boot (lib/config_gen.ts, from this schema).
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontends", "web", "src", "lib", "config_schema_gen.json"))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"name": "AUTH_COOKIE_DOMAIN"`, "the web bundle's config schema no longer reads AUTH_COOKIE_DOMAIN")

	const fixture = "deploy/kcl/testdata/web_auth_cookie_domain.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		want     string
	}{
		{"cookie_default", ""},
		{"cookie_shared", ".reliantlabs.io"},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &doc, project))
			got, ok := doc["AUTH_COOKIE_DOMAIN"]
			require.True(t, ok, "the runtime config document has no AUTH_COOKIE_DOMAIN: %s", mustJSON(t, doc))
			require.Equal(t, tc.want, got)
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
