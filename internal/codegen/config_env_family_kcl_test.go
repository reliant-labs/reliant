package codegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
)

// envFamilyKCLMessages is reliant's IntegrationsConfig shape: a composed
// block carrying the OAuth client pair as env families — the ids plain, the
// secrets a sensitive map<string, string> — beside a sensitive scalar, so the
// golden shows a family and a scalar credential projecting side by side.
func envFamilyKCLMessages() []ConfigMessage {
	secrets := familyField("oauth_client_secrets", "RELIANT_OAUTH_", "_CLIENT_SECRET", "string")
	secrets.Sensitive = true
	secrets.Description = "Integration OAuth client secrets, RELIANT_OAUTH_<ID>_CLIENT_SECRET"
	ids := familyField("oauth_client_ids", "RELIANT_OAUTH_", "_CLIENT_ID", "string")
	ids.Description = "Integration OAuth client ids, RELIANT_OAUTH_<ID>_CLIENT_ID"
	return []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "integrations", GoName: "Integrations", ProtoType: "message", MessageType: "IntegrationsConfig"},
		}},
		{Name: "IntegrationsConfig", Fields: []ConfigField{
			{Name: "webhook_secret", GoName: "WebhookSecret", GoType: "string", ProtoType: "string", EnvVar: "RELIANT_GITHUB_WEBHOOK_SECRET", Sensitive: true, Optional: true},
			ids,
			secrets,
			familyField("features", "RELIANT_FEATURE_", "", "bool"),
		}},
	}
}

// envFamilyKCLModule is the config_gen.k a single-AppConfig graph project
// with envFamilyKCLMessages generates: the module plus ROOTS for a `server`
// root reaching every field.
func envFamilyKCLModule(t *testing.T) string {
	t.Helper()
	fields := FlattenBlockLeaves(envFamilyKCLMessages(), "AppConfig")
	module, err := GenerateConfigKCL(fields, "reliant")
	if err != nil {
		t.Fatalf("GenerateConfigKCL: %v", err)
	}
	return module + RootsKCL([]GraphRoot{DefaultServerRoot(fields)})
}

const envFamilyGolden = "testdata/config_env_family.golden.k"

// A sensitive map<string, string> env family projects into config_gen.k as
// `{str: ConfigSecretRef}` and expands to one forge.SecretRef per KEY, each
// with its own store_key; a plain family as `{str: str}`, expanded inline.
// The golden pins the whole module. FORGE_UPDATE_GOLDEN=1 rewrites it.
func TestEnvFamilyKCL_Golden(t *testing.T) {
	got := envFamilyKCLModule(t)
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(envFamilyGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envFamilyGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(envFamilyGolden)
	if err != nil {
		t.Fatalf("read golden (FORGE_UPDATE_GOLDEN=1 writes it): %v", err)
	}
	if got != string(want) {
		t.Errorf("config_gen.k differs from %s (FORGE_UPDATE_GOLDEN=1 rewrites it):\n%s", envFamilyGolden, got)
	}

	// The load-bearing lines, spelled out so a regenerated golden cannot
	// quietly drop them.
	for _, line := range []string{
		"import regex",
		"    oauth_client_secrets: {str: ConfigSecretRef} = {}",
		"    oauth_client_ids: {str: str} = {}",
		"    features: {str: bool} = {}",
		`        "RELIANT_OAUTH_<KEY>_CLIENT_SECRET" = {"RELIANT_OAUTH_" + _k + "_CLIENT_SECRET": forge.SecretRef {name = _v.name, key = _v.key, store_key = "RELIANT_OAUTH_" + _k + "_CLIENT_SECRET"} for _k, _v in c.integrations.oauth_client_secrets}`,
		`{"RELIANT_OAUTH_" + _k + "_CLIENT_ID": _v for _k, _v in c.integrations.oauth_client_ids}`,
		`RootFamily {prefix = "RELIANT_OAUTH_", suffix = "_CLIENT_SECRET", sensitive = True}`,
	} {
		if !strings.Contains(got, line) {
			t.Errorf("module missing %q", line)
		}
	}
	if vals := kclListConstant(t, got, "APP_CONFIG_SENSITIVE_ENV"); !slices.Equal(vals, []string{"RELIANT_GITHUB_WEBHOOK_SECRET", "RELIANT_OAUTH_<KEY>_CLIENT_SECRET"}) {
		t.Errorf("APP_CONFIG_SENSITIVE_ENV = %q", vals)
	}
}

// A project with no env family renders exactly as before: no regex import,
// no _families, no RootFamily.
func TestEnvFamilyKCL_AbsentFamilyChangesNothing(t *testing.T) {
	fields := FlattenBlockLeaves(nestedBlockMessages(), "AppConfig")
	module, err := GenerateConfigKCL(fields, "hounders")
	if err != nil {
		t.Fatal(err)
	}
	module += RootsKCL([]GraphRoot{DefaultServerRoot(fields)})
	for _, absent := range []string{"import regex", "_families", "RootFamily", "families"} {
		if strings.Contains(module, absent) {
			t.Errorf("a project with no env family renders %q:\n%s", absent, module)
		}
	}
}

// Evaluating the golden module with real kcl: each KEY of a sensitive family
// becomes its own SecretRef only when config_secrets names the family's
// pattern, plain entries render inline for every caller, ROOTS passes the
// expanded names through its reach filter, and a KEY that cannot be part of a
// variable name fails the render.
func TestEnvFamilyKCL_Expands(t *testing.T) {
	if testing.Short() {
		t.Skip("execs kcl twice; runs in task test")
	}
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH")
	}
	module := envFamilyKCLModule(t)
	run := func(t *testing.T, main string) ([]byte, error) {
		t.Helper()
		dir := t.TempDir()
		for name, body := range map[string]string{
			"kcl.mod":                 "[package]\nname = \"family_proof\"\n",
			ConfigSchemaModule + ".k": module,
			"forge/kcl.mod":           "[package]\nname = \"forge\"\n",
			"forge/core.k":            "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n",
			"main.k":                  main,
		} {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	}

	out, err := run(t, `import `+ConfigSchemaModule+` as config_gen

_c = config_gen.AppConfig {
    integrations.oauth_client_ids = {SLACK = "slack-id"}
    integrations.oauth_client_secrets = {
        SLACK = config_gen.ConfigSecretRef {name = "reliant-slack", key = "client_secret"}
        GMAIL = config_gen.ConfigSecretRef {name = "reliant-gmail-oauth", key = "client_secret"}
    }
    integrations.features = {BETA = True}
}
unselected = config_gen.appConfigEnvMap(_c, [])
selected = config_gen.appConfigEnvMap(_c, ["RELIANT_OAUTH_<KEY>_CLIENT_SECRET"])
root_opted = config_gen.workloadEnvMap(_c, {name = "api", root = "server", args = ["server"], config_secrets = ["RELIANT_OAUTH_<KEY>_CLIENT_SECRET"]})
root_default = config_gen.workloadEnvMap(_c, {name = "api", root = "server", args = ["server"]})
`)
	if err != nil {
		t.Fatalf("kcl run: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}

	secret := func(name, key, storeKey string) map[string]any {
		return map[string]any{"name": name, "key": key, "store_key": storeKey}
	}
	inline := map[string]any{
		"PORT":                          "8080",
		"RELIANT_OAUTH_SLACK_CLIENT_ID": "slack-id",
		"RELIANT_FEATURE_BETA":          "true",
	}
	withSecrets := map[string]any{
		"RELIANT_OAUTH_SLACK_CLIENT_SECRET": secret("reliant-slack", "client_secret", "RELIANT_OAUTH_SLACK_CLIENT_SECRET"),
		"RELIANT_OAUTH_GMAIL_CLIENT_SECRET": secret("reliant-gmail-oauth", "client_secret", "RELIANT_OAUTH_GMAIL_CLIENT_SECRET"),
	}
	for k, v := range inline {
		withSecrets[k] = v
	}
	for name, want := range map[string]map[string]any{
		"unselected":   inline,
		"selected":     withSecrets,
		"root_opted":   withSecrets,
		"root_default": inline,
	} {
		gotJSON, _ := json.Marshal(got[name])
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, gotJSON, wantJSON)
		}
	}

	out, err = run(t, `import `+ConfigSchemaModule+` as config_gen

_c = config_gen.AppConfig {
    integrations.oauth_client_secrets = {"google-drive" = config_gen.ConfigSecretRef {name = "x", key = "y"}}
}
`)
	if err == nil || !strings.Contains(err.Error(), "google-drive") {
		t.Errorf("a KEY with a '-' rendered (err %v):\n%s", err, out)
	}
}
