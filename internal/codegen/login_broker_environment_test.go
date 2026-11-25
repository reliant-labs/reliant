package codegen

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A process that loads the broker's settings from its OWN command config
// (`app server` with a ServeConfig) keeps its tier on the root config. The
// broker scaffold once required `environment` on the config that declares the
// broker's settings, and the composition graph refuses that same field there
// ("env var ENVIRONMENT in both the root config and ServeConfig"), so such a
// project could have no broker at all. The broker is scaffolded without the
// field now, runs as deployed by default, and takes the tier from the caller
// (WithDevelopment).
func TestGenerateLoginBroker_BrokerConfigWithoutEnvironment(t *testing.T) {
	brokerFields := []ConfigField{
		{Name: "idp_base", GoName: "IdpBase", GoType: "string", ProtoType: "string", EnvVar: "IDP_BASE"},
		{Name: "idp_host", GoName: "IdpHost", GoType: "string", ProtoType: "string", EnvVar: "IDP_HOST"},
		{Name: "idp_broker_token", GoName: "IdpBrokerToken", GoType: "string", ProtoType: "string", EnvVar: "IDP_BROKER_TOKEN", Sensitive: true},
		{Name: "idp_broker_token_file", GoName: "IdpBrokerTokenFile", GoType: "string", ProtoType: "string", EnvVar: "IDP_BROKER_TOKEN_FILE"},
		{Name: "oidc_client_id", GoName: "OidcClientId", GoType: "string", ProtoType: "string", EnvVar: "OIDC_CLIENT_ID"},
		{Name: "oidc_redirect_uri", GoName: "OidcRedirectUri", GoType: "string", ProtoType: "string", EnvVar: "OIDC_REDIRECT_URI"},
		{Name: "idp_login_uri", GoName: "IdpLoginUri", GoType: "string", ProtoType: "string", EnvVar: "IDP_LOGIN_URI"},
		{Name: "auth_self_signup", GoName: "AuthSelfSignup", GoType: "bool", ProtoType: "bool", EnvVar: "AUTH_SELF_SIGNUP"},
	}
	environment := ConfigField{Name: "environment", GoName: "Environment", GoType: "string", ProtoType: "string", EnvVar: "ENVIRONMENT", Role: "CONFIG_FIELD_ROLE_MODE"}
	port := ConfigField{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT"}

	for _, tc := range []struct {
		name     string
		configs  []ConfigMessage
		cfgType  string
		wantEnv  bool
		mustHave []string
	}{
		{
			// reliant's shape: the tier on the root config, the broker's
			// settings on `shop server`'s own config.
			name: "command config, tier on the root",
			configs: []ConfigMessage{
				{Name: "AppConfig", Fields: []ConfigField{port, environment}},
				{Name: "ServeConfig", Binary: "shop", Command: []string{"server"}, Fields: brokerFields},
			},
			cfgType: "ServeConfig",
			mustHave: []string{
				"func WithDevelopment(dev bool) BrokerOption",
				"broker.secureCookie = !broker.devMode",
				"broker.legacySelfSignup = broker.devMode || cfg.AuthSelfSignup",
			},
		},
		{
			// Every scaffold since the broker exists: unchanged.
			name:    "root config with environment",
			configs: []ConfigMessage{{Name: "AppConfig", Fields: append([]ConfigField{port, environment}, brokerFields...)}},
			wantEnv: true,
			mustHave: []string{
				`devMode: cfg.Environment == "development",`,
				`legacySelfSignup: cfg.Environment == "development" || cfg.AuthSelfSignup,`,
				`secureCookie: cfg.Environment != "development",`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBrokerFixture(t, dir, tc.configs)
			if _, declared := configDeclaring(dir, append(append([]string{}, brokerConfigFields...), "Environment")); declared == !tc.wantEnv {
				t.Fatalf("fixture is wrong: a config declaring the broker settings and environment exists=%v, want %v", declared, tc.wantEnv)
			}

			wrote, err := GenerateLoginBroker("example.com/shop", "shop", dir)
			if err != nil {
				t.Fatalf("GenerateLoginBroker: %v", err)
			}
			if !wrote {
				t.Fatal("login_broker.go was not scaffolded for a config that declares every broker setting")
			}
			b, err := os.ReadFile(filepath.Join(dir, "internal", "app", "login_broker.go"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			if _, err := parser.ParseFile(token.NewFileSet(), "login_broker.go", b, 0); err != nil {
				t.Fatalf("the scaffolded broker does not parse: %v", err)
			}
			wantSig := "cfg *config." + firstNonEmptyString(tc.cfgType, "Config") + ", logger *slog.Logger"
			if !strings.Contains(src, wantSig) {
				t.Errorf("RegisterLoginBroker does not read the config that declares the broker settings (%s)", wantSig)
			}
			if got := strings.Contains(src, "cfg.Environment"); got != tc.wantEnv {
				t.Errorf("broker reads cfg.Environment = %v, want %v (its config declares environment: %v)", got, tc.wantEnv, tc.wantEnv)
			}
			if tc.wantEnv && strings.Contains(src, "WithDevelopment") {
				t.Error("a broker whose config declares environment also offers WithDevelopment: two sources for one decision")
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(src, want) {
					t.Errorf("broker is missing %q", want)
				}
			}
		})
	}
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// writeBrokerFixture lays out a project with cmd/shop's command tree and an
// UNSTAMPED forge descriptor holding configs (no proto tree to hash, so the
// staleness guard does not apply).
func writeBrokerFixture(t *testing.T, dir string, configs []ConfigMessage) {
	t.Helper()
	files := map[string]string{
		"go.mod":                            "module example.com/shop\n\ngo 1.22\n",
		"cmd/shop/cmd/root.go":              "package cmd\n",
		"gen/forge_descriptor/fixture.json": "",
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(ForgeDescriptor{Configs: configs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor", "fixture.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
