package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"

	"github.com/reliant-labs/reliant/pkg/config"
)

// hostedDefaults are the endpoint defaults this source carries: CLIConfig's
// server and gateway, and the API server's OAuth provider. A release build
// stamps the same keys into BuildDefaults (.github/workflows/release.yml).
func hostedDefaults(t *testing.T) map[string]string {
	t.Helper()
	cli, err := forgeconfig.DefaultsTyped[*config.CLIConfig]()
	if err != nil {
		t.Fatalf("CLIConfig defaults: %v", err)
	}
	api, err := forgeconfig.DefaultsTyped[*config.ApiServerConfig]()
	if err != nil {
		t.Fatalf("ApiServerConfig defaults: %v", err)
	}
	return map[string]string{
		"RELIANT_SERVER_URL":  cli.GetConnection().GetServerUrl(),
		"RELIANT_GATEWAY_URL": cli.GetConnection().GetGatewayUrl(),
		"RELIANT_AUTH_URL":    api.GetAuthProviderUrl(),
		"RELIANT_AUTH_KEY":    api.GetAuthProviderKey(),
	}
}

// The proto defaults ARE the hosted endpoints, so `go install` works out of
// the box for the overwhelmingly common case: a user pointing at Reliant's
// hosted platform. An unstamped build that resolved to localhost failed
// against nothing, with an error that never mentioned the hosted option. A
// self-hoster opts out with RELIANT_SERVER_URL / --server.
func TestProtoDefaultsAreTheHostedEndpoints(t *testing.T) {
	got := hostedDefaults(t)
	for key, want := range map[string]string{
		"RELIANT_SERVER_URL":  "https://api.reliantapi.com",
		"RELIANT_GATEWAY_URL": "https://gateway.reliantapi.com",
		"RELIANT_AUTH_URL":    "https://dash.reliantlabs.io",
	} {
		if got[key] != want {
			t.Errorf("%s default = %q, want %q", key, got[key], want)
		}
	}
	if got["RELIANT_AUTH_KEY"] == "" {
		t.Error("RELIANT_AUTH_KEY has no default — a `go install` build cannot complete an OAuth sign-in without it")
	}
}

// The gateway must NOT be derivable-by-accident from the server.
//
// prod's server host is `api.reliantapi.com`, and the generic derivation
// prefixes the leading label — inventing `gateway-api.reliantapi.com`, which
// does not exist. That shipped once already (see deriveGatewayURL in
// cmd/reliant/cmd/connection.go). Declaring the gateway default
// explicitly is what keeps a bare `go install` build off that path.
func TestGatewayDefaultIsExplicitNotDerived(t *testing.T) {
	gateway := hostedDefaults(t)["RELIANT_GATEWAY_URL"]
	if gateway == "" {
		t.Fatal("RELIANT_GATEWAY_URL has no default — the daemon would derive gateway-api.reliantapi.com, which does not resolve")
	}
	if strings.Contains(gateway, "gateway-api.") {
		t.Fatalf("RELIANT_GATEWAY_URL default = %q — that host does not exist", gateway)
	}
}

// The defaults are a PROJECTION of control-plane's KCL, the single
// declaration every other surface (hosted SPA, packaged desktop renderer, that
// app's main process, the release workflow's BuildDefaults stamp) is also
// built from.
//
// A proto cannot import KCL, so this is the seam where the two can drift.
// electron/release.config.json is generated from that KCL and committed here,
// and control-plane CI fails if it drifts — so pinning against it transitively
// pins these to the KCL without this test needing to run `kcl`.
//
// If this fails: do NOT edit the proto to match. Fix the value in
// control-plane deploy/kcl/lib/env.k (reliant_endpoints), regenerate with
// `node .github/scripts/sync-release-config.mjs --env prod`, and then update
// the default_value in proto/reliant/config/v1 (cli_config.proto,
// api_server_config.proto) to match the regenerated file.
func TestProtoDefaultsMatchGeneratedReleaseConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "electron", "release.config.json"))
	if err != nil {
		t.Skipf("release.config.json unavailable (%v) — skipping the KCL drift pin", err)
	}

	var cfg struct {
		Env  string            `json:"env"`
		Main map[string]string `json:"main"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parsing release.config.json: %v", err)
	}
	if cfg.Env != "prod" {
		t.Skipf("release.config.json is rendered for %q, not prod — nothing to compare", cfg.Env)
	}

	got := hostedDefaults(t)
	for envVar, releaseKey := range map[string]string{
		"RELIANT_SERVER_URL":  "RELIANT_SERVER_URL",
		"RELIANT_GATEWAY_URL": "RELIANT_GATEWAY_URL",
		"RELIANT_AUTH_URL":    "SUPABASE_URL",
		"RELIANT_AUTH_KEY":    "SUPABASE_ANON_KEY",
	} {
		want := cfg.Main[releaseKey]
		if want == "" {
			t.Errorf("release.config.json main.%s is empty", releaseKey)
			continue
		}
		if got[envVar] != want {
			t.Errorf("%s default = %q, but control-plane's KCL declares %q — "+
				"fix it in deploy/kcl/lib/env.k and regenerate, do not hand-edit the proto", envVar, got[envVar], want)
		}
	}
}
