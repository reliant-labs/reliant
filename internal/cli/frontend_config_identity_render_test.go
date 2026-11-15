package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// identityFrontendConfig is a frontend config message that declares the
// three OIDC fields, i.e. the surface the scaffolded frontend_config.proto
// ships.
func identityFrontendConfig() codegen.FrontendConfig {
	return codegen.FrontendConfig{
		Frontend:    "web",
		MessageName: "WebConfig",
		Fields: []codegen.ConfigField{
			{Name: "api_url", ProtoType: "string", EnvVar: "API_URL", DefaultValue: "http://localhost:8080"},
			{Name: "oidc_issuer", ProtoType: "string", EnvVar: "OIDC_ISSUER"},
			{Name: "oidc_client_id", ProtoType: "string", EnvVar: "OIDC_CLIENT_ID"},
			{Name: "oidc_redirect_uri", ProtoType: "string", EnvVar: "OIDC_REDIRECT_URI"},
		},
	}
}

// identityProject scaffolds the on-disk shape a dev env has once
// `forge generate` has run: the forge-owned frontend config module, a
// dev config.k whose frontend instance forge SCAFFOLDED (reading the
// published identity file), and the identity stub itself.
func identityProject(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	if err := os.MkdirAll(filepath.Join(kclDir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrontendCfgFile(t, filepath.Join(kclDir, "kcl.mod"),
		"[package]\nname = \"deploy\"\nversion = \"0.0.1\"\n")

	fc := identityFrontendConfig()
	module, err := codegen.GenerateFrontendConfigKCL([]codegen.FrontendConfig{fc}, "acme")
	if err != nil {
		t.Fatalf("GenerateFrontendConfigKCL: %v", err)
	}
	writeFrontendCfgFile(t, filepath.Join(kclDir, codegen.FrontendConfigModule+".k"), module)

	// The env's config.k starts with only the backend half, exactly as the
	// write-if-absent scaffolder leaves it, and forge appends the frontend
	// instance — the code path under test.
	writeFrontendCfgFile(t, filepath.Join(kclDir, "dev", "config.k"),
		"app_config = {\n    log_level = \"debug\"\n}\n")
	if _, err := codegen.EnsureFrontendConfigInstances(
		[]codegen.FrontendConfig{fc}, kclDir, "dev", "acme"); err != nil {
		t.Fatalf("EnsureFrontendConfigInstances: %v", err)
	}
	return projectDir
}

// writeConvergedIdentity stands in for an identity a running stack produced:
// it sets the frontend instance's OIDC values in the dev config.k, which is
// the only way a differing value can reach a render now that nothing
// publishes a file.
func writeConvergedIdentity(t *testing.T, projectDir, clientID string) {
	t.Helper()
	writeConvergedIdentityForEnv(t, projectDir, "dev", clientID)
}

// writeConvergedIdentityForEnv is writeConvergedIdentity for any env.
func writeConvergedIdentityForEnv(t *testing.T, projectDir, env, clientID string) {
	t.Helper()
	path := filepath.Join(projectDir, "deploy", "kcl", env, "config.k")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	open := regexp.MustCompile(`(?m)^web_config: [A-Za-z_.]+ = \{\n`)
	loc := open.FindIndex(raw)
	if loc == nil {
		t.Fatalf("no frontend instance in %s:\n%s", path, raw)
	}
	ins := "    oidc_issuer = \"http://localhost:8080\"\n    oidc_client_id = \"" + clientID + "\"\n"
	body := string(raw[:loc[1]]) + ins + string(raw[loc[1]:])
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// THE END-TO-END PROPERTY. What forge scaffolds into config.k must be valid
// KCL that renders through the real projection, reading the identity
// values the env declares — the file is forge-owned at
// the point it is written, so a shape that does not evaluate would break
// `forge generate` for every project that declares a frontend, not just
// fail a unit assertion.
func TestScaffoldedIdentityConfigKRenders(t *testing.T) {
	projectDir := identityProject(t)
	writeConvergedIdentity(t, projectDir, "resolved-client-id")

	values, err := loadFrontendRuntimeConfig(projectDir, "dev",
		[]codegen.FrontendConfig{identityFrontendConfig()})
	if err != nil {
		t.Fatalf("render the scaffolded config.k: %v", err)
	}

	web := values["web"]
	if web == nil {
		t.Fatalf("no runtime config projected for the frontend: %v", values)
	}
	if got := web["OIDC_CLIENT_ID"]; got != "resolved-client-id" {
		t.Errorf("OIDC_CLIENT_ID = %v, want the published id", got)
	}
	// A render that read the id but dropped the issuer would still leave
	// sign-in broken.
	if got := web["OIDC_ISSUER"]; got != "http://localhost:8080" {
		t.Errorf("OIDC_ISSUER = %v", got)
	}
	// The redirect URI projects through EMPTY, and that is the un-pinning:
	// the value is only ever a registered constant the server echoes on the
	// auth request it makes — nothing navigates to it under native sign-in —
	// so a literal here would re-pin the frontend's port for no gain.
	if got := web["OIDC_REDIRECT_URI"]; got != "" {
		t.Errorf("OIDC_REDIRECT_URI = %v, want empty so the port stays unpinned", got)
	}
}
