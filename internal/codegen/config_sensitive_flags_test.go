package codegen

import (
	"strings"
	"testing"
)

func TestSensitiveConfigFlags_NamesEachFlaggedSecretPerMessage(t *testing.T) {
	messages := []ConfigMessage{
		{Name: "AppConfig", ProtoFile: "proto/demo/config/v1/config.proto", Fields: []ConfigField{
			{Name: "port", EnvVar: "PORT", Flag: "port"},
			{Name: "database_url", EnvVar: "DATABASE_URL", Flag: "database-url", Sensitive: true},
			{Name: "jwt_secret", EnvVar: "JWT_SECRET", Flag: "jwt-secret", Sensitive: true},
			{Name: "api_key", EnvVar: "API_KEY", Sensitive: true},
		}},
		{Name: "WorkerConfig", Fields: []ConfigField{
			{Name: "queue", EnvVar: "QUEUE", Flag: "queue"},
		}},
	}
	got := SensitiveConfigFlags(messages)
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want 1 (AppConfig only): %q", len(got), got)
	}
	for _, want := range []string{"proto/demo/config/v1/config.proto: AppConfig", "database_url (--database-url)", "jwt_secret (--jwt-secret)"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("warning %q does not name %q", got[0], want)
		}
	}
	for _, not := range []string{"port", "api_key", "WorkerConfig"} {
		if strings.Contains(got[0], not) {
			t.Errorf("warning %q names %q, which is not a flagged sensitive field", got[0], not)
		}
	}
}

func TestSensitiveConfigFlags_TheDefaultSetIsClean(t *testing.T) {
	if got := SensitiveConfigFlags(DefaultConfigMessages()); len(got) != 0 {
		t.Errorf("forge's fallback config set declares a flag on a sensitive field: %q", got)
	}
}
