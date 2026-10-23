package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// A FIXED ES256 (P-256) private key, test-only (the same key
// control-plane's e2e jwks_test.go uses). The derived public JWK x/y are
// deterministic, so we can pin them.
const testES256PEM = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIB2fmGPM1CXk+kC+GBvg5pD/zwwK+XRy8+mKNfr0PABuoAoGCCqGSM49
AwEHoUQDQgAEGdqz6sZl229WS3ixXQmFory5kkkus2UT4cBGQuO3dpMN2FQ/8260
9YszSMpty7qF7I3/9elHmcVvzBglAF7CrQ==
-----END EC PRIVATE KEY-----`

// TestTestJWKSRendersThroughPlugin proves the forge.TestJWKS path end to
// end through kclrender (which registers the forge.derive_jwk plugin):
// the builder emits ConfigMap + Deployment + Service, the ConfigMap holds
// a JWKS document whose single key is the PUBLIC JWK derived from the
// ES256 private PEM, and the private key NEVER appears in the render.
func TestTestJWKSRendersThroughPlugin(t *testing.T) {
	dir := t.TempDir()

	kclMod := "[package]\nname = \"jwkstest\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	if err := os.WriteFile(filepath.Join(dir, "kcl.mod"), []byte(kclMod), 0o644); err != nil {
		t.Fatal(err)
	}

	// Inline the PEM as a KCL multiline string (the value the CLI would
	// otherwise resolve from .env.<env> and inject). Tests the builder +
	// plugin without the brittle multiline `-D` plumbing.
	main := "import forge\n" +
		"import kcl_plugin.forge as plugin\n\n" +
		"_pem = \"\"\"\n" + testES256PEM + "\n\"\"\"\n\n" +
		"_jwks = forge.TestJWKS {\n" +
		"    name = \"test-jwks\"\n" +
		"    kid = \"e2e-test-es256\"\n" +
		"    private_key = forge.StoreRef { key = \"TEST_JWKS_PEM\" }\n" +
		"}\n\n" +
		"_jwk = plugin.derive_jwk(_pem, _jwks.kid, _jwks.alg)\n" +
		"manifests = forge.test_jwks_manifests(_jwks, \"auth\", _jwk)\n"
	if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	// Ensure the plugin is registered (kclrender.Run also calls it).
	kclplugin.Register()

	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render TestJWKS: %v", err)
	}

	// The private PEM must NEVER appear in the rendered output.
	if strings.Contains(string(out), "BEGIN EC PRIVATE KEY") {
		t.Fatalf("private key leaked into rendered output:\n%s", out)
	}

	var m struct {
		Manifests []map[string]any `json:"manifests"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal render: %v\n%s", err, out)
	}
	kinds := map[string]map[string]any{}
	for _, obj := range m.Manifests {
		kind, _ := obj["kind"].(string)
		kinds[kind] = obj
	}
	for _, want := range []string{"ConfigMap", "Deployment", "Service"} {
		if _, ok := kinds[want]; !ok {
			t.Errorf("missing %s in TestJWKS manifests; got kinds %v", want, jwksKindKeys(kinds))
		}
	}

	// The ConfigMap's jwks.json must be a JWKS doc whose single key carries
	// the kid + the derived x/y matching the known public JWK.
	cm := kinds["ConfigMap"]
	data, _ := cm["data"].(map[string]any)
	jwksJSON, _ := data["jwks.json"].(string)
	if jwksJSON == "" {
		t.Fatalf("ConfigMap has no jwks.json: %v", cm)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(jwksJSON), &doc); err != nil {
		t.Fatalf("jwks.json not valid JSON: %v\n%s", err, jwksJSON)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("jwks keys: got %d want 1", len(doc.Keys))
	}
	jwk := doc.Keys[0]

	// Cross-check x/y against the Go derivation of the SAME PEM — signer +
	// JWKS share the key, so the served JWK must equal the derived one.
	want, derr := kclplugin.DeriveJWK(testES256PEM, "e2e-test-es256", "ES256")
	if derr != nil {
		t.Fatalf("DeriveJWK: %v", derr)
	}
	for _, f := range []string{"kty", "crv", "x", "y", "kid", "alg", "use"} {
		if jwk[f] != want[f] {
			t.Errorf("jwk[%q] = %v, want %v", f, jwk[f], want[f])
		}
	}
}

func jwksKindKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
