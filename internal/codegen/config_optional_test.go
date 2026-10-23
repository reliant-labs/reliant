package codegen

import (
	"strings"
	"testing"
)

// The rule these pin: a `sensitive` field with no value is an ERROR unless it
// also declares `optional: true`. The default must stay strict — the secret
// pre-flight exists so a missing production credential fails before anything
// starts, not from a runtime stack trace — so `optional` is opt-in and an
// author has to state the exception.

func optionalFixture() []ConfigMessage {
	return []ConfigMessage{{
		Name: "AppConfig",
		Fields: []ConfigField{
			{Name: "log_level", EnvVar: "LOG_LEVEL", ProtoType: "string"},
			{Name: "database_url", EnvVar: "DATABASE_URL", ProtoType: "string", Sensitive: true},
			{Name: "admin_password", EnvVar: "ADMIN_PASSWORD", ProtoType: "string", Sensitive: true, Optional: true},
		},
	}}
}

// required + optional say opposite things, so forge refuses rather than
// inventing a precedence the author would have to learn from source.
func TestRequiredAndOptionalTogetherIsRefused(t *testing.T) {
	messages := []ConfigMessage{{
		Name: "AppConfig",
		Fields: []ConfigField{
			{Name: "token", EnvVar: "TOKEN", ProtoType: "string", Required: true, Optional: true},
		},
	}}

	err := ValidateConfigOptionality(messages)
	if err == nil {
		t.Fatal("a field annotated both required and optional was accepted")
	}
	// The message must name the offender; a count alone is not actionable.
	if !strings.Contains(err.Error(), "AppConfig.token") {
		t.Errorf("error does not name the field: %v", err)
	}
	if !strings.Contains(err.Error(), "TOKEN") {
		t.Errorf("error does not name the env var: %v", err)
	}
}

func TestOrdinaryOptionalityIsAccepted(t *testing.T) {
	if err := ValidateConfigOptionality(optionalFixture()); err != nil {
		t.Fatalf("valid annotations were refused: %v", err)
	}
}

// Reporting every offender matters because these arrive in batches when a
// config block is split or copied.
func TestEveryContradictionIsReportedNotJustTheFirst(t *testing.T) {
	messages := []ConfigMessage{{
		Name: "AppConfig",
		Fields: []ConfigField{
			{Name: "a", EnvVar: "A", Required: true, Optional: true},
			{Name: "b", EnvVar: "B", Required: true, Optional: true},
		},
	}}

	err := ValidateConfigOptionality(messages)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"AppConfig.a", "AppConfig.b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %s, so fixing them takes one generate-run each: %v", want, err)
		}
	}
}

// The set the secret pre-flight consumes: sensitive AND optional, nothing else.
func TestOnlySensitiveOptionalFieldsAreExempted(t *testing.T) {
	got := OptionalSecretEnvVars(optionalFixture())

	if len(got) != 1 || got[0] != "ADMIN_PASSWORD" {
		t.Fatalf("OptionalSecretEnvVars = %v, want exactly [ADMIN_PASSWORD]", got)
	}
}

// A non-sensitive optional field must NOT appear: nothing pre-flights it, and
// including it would imply this set means something broader than it does.
func TestNonSensitiveOptionalIsNotASecretExemption(t *testing.T) {
	messages := []ConfigMessage{{
		Name: "AppConfig",
		Fields: []ConfigField{
			{Name: "nickname", EnvVar: "NICKNAME", ProtoType: "string", Optional: true},
		},
	}}

	if got := OptionalSecretEnvVars(messages); len(got) != 0 {
		t.Errorf("OptionalSecretEnvVars = %v, want empty — NICKNAME is not a secret", got)
	}
}

// THE END-TO-END LINK, and the one most likely to rot. The annotation is only
// useful if it survives into the generated KCL: the pre-flight reads rendered
// entities, not the proto, so a flag that stopped at the Go layer would be
// silently lost exactly where it is consumed.
func TestOptionalSensitiveFieldProjectsSecretOptionalIntoKCL(t *testing.T) {
	out := renderConfigEnvMapNamed(optionalFixture()[0].Fields, "AppConfig", "appConfigEnvMap")

	if !strings.Contains(out, `"ADMIN_PASSWORD" = forge.SecretRef {name = c.admin_password.name, key = c.admin_password.key, store_key = "ADMIN_PASSWORD", optional = True}`) {
		t.Errorf("optional sensitive field did not project optional:\n%s", out)
	}
	// The non-optional credential must stay strict — no flag, no exemption.
	if !strings.Contains(out, `"DATABASE_URL" = forge.SecretRef {name = c.database_url.name, key = c.database_url.key, store_key = "DATABASE_URL"}`) {
		t.Errorf("a NON-optional credential was altered:\n%s", out)
	}
	if strings.Contains(out, `store_key = "DATABASE_URL", optional`) {
		t.Error("optional leaked onto a field that never declared it")
	}
}

// The generated constant the pre-flight can read by name, rather than
// hand-listing vars that go stale the moment a field is added.
func TestOptionalSecretsAreListedInTheGeneratedConstant(t *testing.T) {
	out := renderConfigEnvMapNamed(optionalFixture()[0].Fields, "AppConfig", "appConfigEnvMap")

	if !strings.Contains(out, `APP_CONFIG_OPTIONAL_SECRET_ENV: [str] = ["ADMIN_PASSWORD"]`) {
		t.Errorf("the optional-secret constant is missing or wrong:\n%s", out)
	}
	// Both credentials are still sensitive; optionality does not remove them
	// from the sensitive set, it only exempts them from the value check.
	if !strings.Contains(out, `APP_CONFIG_SENSITIVE_ENV: [str] = ["DATABASE_URL", "ADMIN_PASSWORD"]`) {
		t.Errorf("optionality changed the sensitive set, which it must not:\n%s", out)
	}
}

func TestOptionalSecretsConstantNameFollowsTheSchema(t *testing.T) {
	for schema, want := range map[string]string{
		"":              "APP_CONFIG_OPTIONAL_SECRET_ENV",
		"AppConfig":     "APP_CONFIG_OPTIONAL_SECRET_ENV",
		"WorkerConfig":  "WORKER_CONFIG_OPTIONAL_SECRET_ENV",
		"TraderXConfig": "TRADER_X_CONFIG_OPTIONAL_SECRET_ENV",
	} {
		if got := KCLOptionalSecretsName(schema); got != want {
			t.Errorf("KCLOptionalSecretsName(%q) = %q, want %q", schema, got, want)
		}
	}
}
