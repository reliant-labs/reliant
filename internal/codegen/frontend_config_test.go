package codegen

import (
	"strings"
	"testing"
)

// publicField builds a plain, browser-safe config leaf.
func publicField(name, envVar string) ConfigField {
	return ConfigField{
		Name: name, GoName: name, GoType: "string", ProtoType: "string",
		EnvVar: envVar,
	}
}

// secretField builds a leaf annotated sensitive.
func secretField(name, envVar string) ConfigField {
	f := publicField(name, envVar)
	f.Sensitive = true
	return f
}

// blockRef builds the message-typed field that composes a shared block.
func blockRef(fieldName, msgType string) ConfigField {
	return ConfigField{
		Name: fieldName, GoName: fieldName, ProtoType: "message", MessageType: msgType,
	}
}

// A legitimate public frontend config must generate without complaint —
// the guard has to be silent on the common case or it is just noise.
func TestValidateFrontendConfigs_AllowsPublicFields(t *testing.T) {
	msgs := []ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields: []ConfigField{
			publicField("api_url", "API_URL"),
			publicField("oidc_issuer", "OIDC_ISSUER"),
			publicField("oidc_client_id", "OIDC_CLIENT_ID"),
		},
	}}
	if err := ValidateFrontendConfigs(msgs); err != nil {
		t.Fatalf("public frontend config was refused: %v", err)
	}
}

// A sensitive field declared directly on a frontend config is the blunt
// case: refuse, and name the field.
func TestValidateFrontendConfigs_RefusesDirectSensitive(t *testing.T) {
	msgs := []ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields: []ConfigField{
			publicField("api_url", "API_URL"),
			secretField("stripe_secret", "STRIPE_SECRET"),
		},
	}}
	err := ValidateFrontendConfigs(msgs)
	if err == nil {
		t.Fatal("expected a sensitive field on a frontend config to be refused")
	}
	got := err.Error()
	for _, want := range []string{"stripe_secret", "WebConfig", "web", "STRIPE_SECRET", "forge generate"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, got)
		}
	}
}

// The mistake that actually happens: a shared block holding both a public
// issuer and a secret, composed onto a frontend. The guard must follow
// composition and name the DECLARATION site, not just the frontend config.
func TestValidateFrontendConfigs_RefusesSensitiveViaSharedBlock(t *testing.T) {
	msgs := []ConfigMessage{
		{
			Name: "BaseConfig",
			Fields: []ConfigField{
				publicField("oidc_issuer", "OIDC_ISSUER"),
				secretField("oidc_client_secret", "OIDC_CLIENT_SECRET"),
			},
		},
		{
			Name:     "WebConfig",
			Frontend: "web",
			Fields:   []ConfigField{blockRef("base", "BaseConfig")},
		},
	}
	err := ValidateFrontendConfigs(msgs)
	if err == nil {
		t.Fatal("expected a sensitive field reaching a frontend via a shared block to be refused")
	}
	got := err.Error()
	for _, want := range []string{"oidc_client_secret", "BaseConfig", "base", "web"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, got)
		}
	}
	// The whole point of following composition is telling the user to split
	// the block — a bare "field is sensitive" would not be actionable.
	if !strings.Contains(got, "split") {
		t.Errorf("refusal should advise splitting the shared block:\n%s", got)
	}
}

// A sensitive field on a BINARY config is correct and must stay silent —
// the rule is about the browser, not about secrets in general.
func TestValidateFrontendConfigs_IgnoresSensitiveOnBinaryConfig(t *testing.T) {
	msgs := []ConfigMessage{
		{
			Name:   "ServerConfig",
			Binary: "server",
			Fields: []ConfigField{secretField("db_password", "DB_PASSWORD")},
		},
		{
			Name:     "WebConfig",
			Frontend: "web",
			Fields:   []ConfigField{publicField("api_url", "API_URL")},
		},
	}
	if err := ValidateFrontendConfigs(msgs); err != nil {
		t.Fatalf("a secret on a binary config must not be refused: %v", err)
	}
}

// Every offender should be reported in one run; fixing them one
// generate-at-a-time is the failure mode this avoids.
func TestValidateFrontendConfigs_ReportsAllOffenders(t *testing.T) {
	msgs := []ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields: []ConfigField{
			secretField("first_secret", "FIRST"),
			secretField("second_secret", "SECOND"),
		},
	}}
	err := ValidateFrontendConfigs(msgs)
	if err == nil {
		t.Fatal("expected refusal")
	}
	got := err.Error()
	if !strings.Contains(got, "first_secret") || !strings.Contains(got, "second_secret") {
		t.Errorf("both offenders should be named:\n%s", got)
	}
	if !strings.Contains(got, "2 sensitive fields") {
		t.Errorf("refusal should count the offenders:\n%s", got)
	}
}

// Flattening must mirror ConfigFieldsForBinary: own leaves plus composed
// block leaves, so a shared fact is declared once and lands in both.
func TestFrontendConfigsFromMessages_FlattensComposedBlocks(t *testing.T) {
	msgs := []ConfigMessage{
		{
			Name:   "BaseConfig",
			Fields: []ConfigField{publicField("oidc_issuer", "OIDC_ISSUER")},
		},
		{
			Name:     "WebConfig",
			Frontend: "web",
			Fields: []ConfigField{
				publicField("api_url", "API_URL"),
				blockRef("base", "BaseConfig"),
			},
		},
	}
	got := FrontendConfigsFromMessages(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 frontend config, got %d", len(got))
	}
	if got[0].Frontend != "web" || got[0].MessageName != "WebConfig" {
		t.Fatalf("unexpected binding: %+v", got[0])
	}
	var names []string
	for _, f := range got[0].Fields {
		names = append(names, f.Name)
	}
	want := []string{"api_url", "oidc_issuer"}
	if len(names) != len(want) {
		t.Fatalf("flattened fields = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("flattened fields = %v, want %v", names, want)
		}
	}
}

// A project that annotates nothing must be entirely unaffected.
func TestFrontendConfigsFromMessages_UnannotatedProjectIsUnchanged(t *testing.T) {
	msgs := []ConfigMessage{{
		Name:   "AppConfig",
		Fields: []ConfigField{publicField("port", "PORT"), secretField("db_password", "DB_PASSWORD")},
	}}
	if got := FrontendConfigsFromMessages(msgs); len(got) != 0 {
		t.Fatalf("expected no frontend configs, got %+v", got)
	}
	if err := ValidateFrontendConfigs(msgs); err != nil {
		t.Fatalf("unannotated project must not be refused: %v", err)
	}
}
