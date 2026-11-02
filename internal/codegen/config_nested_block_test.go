package codegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
)

// nestedBlockMessages is the Bark Social shape: AppConfig composes a
// StripeConfig block (`StripeConfig stripe = 40;`) whose leaves include a
// sensitive key and a `site_url` — plus a second block that ALSO declares
// `site_url`. Flattened, the two site_urls were one field.
func nestedBlockMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "stripe", GoName: "Stripe", ProtoType: "message", MessageType: "StripeConfig"},
			{Name: "mailer", GoName: "Mailer", ProtoType: "message", MessageType: "MailerConfig"},
		}},
		{Name: "StripeConfig", Fields: []ConfigField{
			{Name: "secret_key", GoName: "SecretKey", GoType: "string", ProtoType: "string", EnvVar: "STRIPE_SECRET_KEY", Sensitive: true, Optional: true},
			{Name: "site_url", GoName: "SiteUrl", GoType: "string", ProtoType: "string", EnvVar: "STRIPE_SITE_URL", DefaultValue: "http://localhost:3000"},
		}},
		{Name: "MailerConfig", Fields: []ConfigField{
			{Name: "site_url", GoName: "SiteUrl", GoType: "string", ProtoType: "string", EnvVar: "MAILER_SITE_URL", DefaultValue: "http://mail"},
		}},
	}
}

// Nested config messages project as NESTED KCL schemas — not flattened into
// AppConfig ("nested schemas are not yet projected"), which made same-named
// leaves in two blocks collide. Proven by evaluating the module with real kcl:
// each block's leaf is independently settable by path and lands on its own
// env var, and the sensitive leaf still routes through the secret channel.
func TestNestedConfigBlocks_ProjectAsNestedSchemas(t *testing.T) {
	fields := FlattenBlockLeaves(nestedBlockMessages(), "AppConfig")
	module, err := GenerateConfigKCL(fields, "hounders")
	if err != nil {
		t.Fatalf("GenerateConfigKCL: %v", err)
	}
	for _, want := range []string{
		"schema AppConfigStripe:",
		"schema AppConfigMailer:",
		"    stripe: AppConfigStripe = AppConfigStripe {}",
		`forge.SecretRef {name = c.stripe.secret_key.name, key = c.stripe.secret_key.key, store_key = "STRIPE_SECRET_KEY", optional = True}`,
	} {
		if !strings.Contains(module, want) {
			t.Errorf("module missing %q:\n%s", want, module)
		}
	}
	if strings.Contains(module, "nested schemas are not yet projected") {
		t.Errorf("blocks must no longer be flattened:\n%s", module)
	}

	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping the evaluation half")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kcl.mod", "[package]\nname = \"nested_proof\"\n")
	write(ConfigSchemaModule+".k", module)
	write("forge/kcl.mod", "[package]\nname = \"forge\"\n")
	write("forge/core.k", "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n")
	write("main.k", `import `+ConfigSchemaModule+` as config_gen

_c = config_gen.AppConfig {
    stripe.site_url = "https://bark.example"
}
_env = config_gen.appConfigEnvMap(_c, ["STRIPE_SECRET_KEY"])
assert_stripe_site = str(_env["STRIPE_SITE_URL"]) == "https://bark.example"
assert_mailer_site_default = str(_env["MAILER_SITE_URL"]) == "http://mail"
assert_secret = _env["STRIPE_SECRET_KEY"].key == "stripe_secret_key"
assert_secret_optional = _env["STRIPE_SECRET_KEY"].optional == True
`)
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, out)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"assert_stripe_site", "assert_mailer_site_default", "assert_secret"} {
		if parsed[k] != true {
			t.Errorf("%s = %v, want true\n%s", k, parsed[k], out)
		}
	}
}

// The scaffolded config.k authors a block leaf by its PATH — a flat key would
// be "Cannot add member" against the nested schema.
func TestConfigKScaffold_AuthorsBlockLeavesByPath(t *testing.T) {
	msgs := []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "stripe", GoName: "Stripe", ProtoType: "message", MessageType: "StripeConfig"},
		}},
		{Name: "StripeConfig", Fields: []ConfigField{
			{Name: "account", GoName: "Account", GoType: "string", ProtoType: "string", EnvVar: "STRIPE_ACCOUNT", Required: true},
		}},
	}
	body := generateConfigKBody(FlattenBlockLeaves(msgs, "AppConfig"), "p", "prod")
	if !strings.Contains(body, `    stripe.account = ""`) {
		t.Fatalf("mandatory block leaf must be pinned by path:\n%s", body)
	}
}
