package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// oidcFrontendConfig is a frontend whose config message declares the three
// OIDC fields, i.e. one scaffolded by a forge that ships the browser
// sign-in surface.
func oidcFrontendConfig() FrontendConfig {
	fc := webFrontendConfig()
	fc.Fields = append(fc.Fields,
		ConfigField{Name: "oidc_issuer", ProtoType: "string", EnvVar: "OIDC_ISSUER"},
		ConfigField{Name: "oidc_client_id", ProtoType: "string", EnvVar: "OIDC_CLIENT_ID"},
		ConfigField{Name: "oidc_redirect_uri", ProtoType: "string", EnvVar: "OIDC_REDIRECT_URI"},
	)
	return fc
}

// A dev env's identity is declared on the Bundle (forge.ManagedIdentity) and
// filled by `forge env up`, so the scaffolded config.k carries no identity
// block and imports no published identity file.
func TestFrontendConfigInstance_CarriesNoIdentityBlock(t *testing.T) {
	kclDir := filepath.Join(t.TempDir(), "deploy", "kcl")
	path := writeConfigK(t, kclDir, "dev", backendOnlyConfigK)
	if _, err := EnsureFrontendConfigInstances([]FrontendConfig{oidcFrontendConfig()}, kclDir, "dev", "acme"); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	body := readFile(t, path)
	for _, unwanted := range []string{"idp_identity", "idp_identity_gen", "import .idp"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("config.k still reads a published identity file (%q):\n%s", unwanted, body)
		}
	}
}
