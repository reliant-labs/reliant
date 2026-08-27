package builddefaults

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The source defaults ARE the hosted endpoints, so `go install` works out of
// the box for the overwhelmingly common case: a user pointing at Reliant's
// hosted platform. Before this, an un-injected build resolved to
// http://localhost:8080 and failed against nothing, with an error that never
// mentioned the hosted option.
//
// A self-hoster opts OUT with RELIANT_SERVER_URL / --server, which still
// outrank these (TestValuePrefersEnvironment above pins that ordering).
func TestSourceDefaultsAreTheHostedEndpoints(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"ServerURL", ServerURL, "https://api.reliantapi.com"},
		{"GatewayURL", GatewayURL, "https://gateway.reliantapi.com"},
		{"AuthURL", AuthURL, "https://dash.reliantlabs.io"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if AuthKey == "" {
		t.Error("AuthKey is empty — `go install` cannot complete an OAuth login without it")
	}
}

// The gateway must NOT be derivable-by-accident from the server.
//
// prod's server host is `api.reliantapi.com`, and the generic derivation
// prefixes the leading label — inventing `gateway-api.reliantapi.com`, which
// does not exist. That shipped once already (see deriveGatewayURL in
// cmd/reliant/commands/connection.go). Declaring GatewayURL explicitly is what
// keeps a bare `go install` build off that path, so it must never be blank.
func TestGatewayDefaultIsExplicitNotDerived(t *testing.T) {
	if GatewayURL == "" {
		t.Fatal("GatewayURL is empty — the daemon would derive gateway-api.reliantapi.com, which does not resolve")
	}
	if strings.Contains(GatewayURL, "gateway-api.") {
		t.Fatalf("GatewayURL = %q — that host does not exist", GatewayURL)
	}
}

// These constants are a PROJECTION of control-plane's KCL, which is the single
// declaration every other surface (hosted SPA, packaged desktop renderer, that
// app's main process, the release workflow's -X flags) is also built from.
//
// Go source cannot import KCL, so this is the seam where the two can drift.
// electron/release.config.json is generated from that KCL and committed here,
// and control-plane CI fails if it drifts — so pinning against it transitively
// pins these to the KCL without this test needing to run `kcl`.
//
// If this fails: do NOT edit defaults.go to match. Fix the value in
// control-plane deploy/kcl/lib/env.k (reliant_endpoints), regenerate with
// `node .github/scripts/sync-release-config.mjs --env prod`, and then update
// defaults.go to match the regenerated file.
func TestSourceDefaultsMatchGeneratedReleaseConfig(t *testing.T) {
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

	for _, c := range []struct{ key, got string }{
		{"RELIANT_SERVER_URL", ServerURL},
		{"RELIANT_GATEWAY_URL", GatewayURL},
		{"SUPABASE_URL", AuthURL},
		{"SUPABASE_ANON_KEY", AuthKey},
	} {
		want := cfg.Main[c.key]
		if want == "" {
			t.Errorf("release.config.json main.%s is empty", c.key)
			continue
		}
		if c.got != want {
			t.Errorf("builddefaults value for %s = %q, but control-plane's KCL declares %q — "+
				"fix it in deploy/kcl/lib/env.k and regenerate, do not hand-edit defaults.go", c.key, c.got, want)
		}
	}
}

func TestValuePrefersEnvironment(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "from-env")

	got := Value("RELIANT_TEST_DEFAULT", "from-build", "fallback")
	if got != "from-env" {
		t.Fatalf("Value() = %q, want %q", got, "from-env")
	}
}

func TestValueUsesCompiledDefaultWhenEnvironmentUnset(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "")

	got := Value("RELIANT_TEST_DEFAULT", "from-build", "fallback")
	if got != "from-build" {
		t.Fatalf("Value() = %q, want %q", got, "from-build")
	}
}

func TestValueUsesFallbackWhenEnvironmentAndCompiledDefaultUnset(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "")

	got := Value("RELIANT_TEST_DEFAULT", "", "fallback")
	if got != "fallback" {
		t.Fatalf("Value() = %q, want %q", got, "fallback")
	}
}
