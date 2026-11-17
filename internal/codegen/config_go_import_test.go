package codegen

import (
	"strings"
	"testing"
)

// pkg/config imports the Go package the config messages generate into, as
// the compiled protos declare it (go_package), not as forge would derive it:
// a go_package the author changed is followed. A descriptor that predates the
// field falls back to the layout's path.
func TestConfigGoImport(t *testing.T) {
	const fallback = "example.com/acme/gen/acme/config/v1"
	declared := []ConfigMessage{
		{Name: "AppConfig", ProtoFile: "acme/config/v1/config.proto", GoImportPath: "example.com/acme/gen/acme/config/v1"},
		{Name: "WebConfig", ProtoFile: "acme/config/v1/web_config.proto", GoImportPath: "example.com/acme/gen/acme/config/v1"},
	}
	if got, err := configGoImport(declared, "unused"); err != nil || got != "example.com/acme/gen/acme/config/v1" {
		t.Errorf("declared: got %q, %v", got, err)
	}
	if got, err := configGoImport([]ConfigMessage{{Name: "AppConfig"}}, fallback); err != nil || got != fallback {
		t.Errorf("older descriptor: got %q, %v; want the fallback", got, err)
	}

	// One shim imports one package: messages in two are refused, naming both.
	split := append(declared, ConfigMessage{Name: "AdminConfig", ProtoFile: "admin/v1/admin.proto", GoImportPath: "example.com/acme/gen/admin/v1"})
	_, err := configGoImport(split, fallback)
	if err == nil {
		t.Fatal("config messages in two Go packages were accepted")
	}
	for _, want := range []string{"AppConfig", "AdminConfig", "example.com/acme/gen/admin/v1", "proto/acme/config/v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}
