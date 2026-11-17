package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

// adapterBlock is the block `forge scaffold adapter <name>` composes (#748).
func adapterBlock(name, message string) ConfigBlockSpec {
	return ConfigBlockSpec{
		Message:  message,
		Field:    name,
		Doc:      []string{message + " configures the " + name + " adapter."},
		FieldDoc: "Configuration of the " + name + " adapter.",
		Leaves: []ConfigBlockLeaf{{
			Type: "string", Name: "base_url", EnvVar: strings.ToUpper(name) + "_BASE_URL",
			Description: "Base URL of the " + name + " downstream.",
		}},
	}
}

// writeConfigProto writes src as proto/config/v1/config.proto under a fresh
// project root and returns the config dir and the file.
func writeConfigProto(t *testing.T, src string) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "proto", "config", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "config.proto")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

// scaffoldedConfigProto is the config.proto `forge project new` writes.
func scaffoldedConfigProto(t *testing.T) string {
	t.Helper()
	out, err := templates.ProjectTemplates().Render("config.proto.tmpl", configproto.For("proj").TemplateData("example.com/proj"))
	if err != nil {
		t.Fatalf("render config.proto.tmpl: %v", err)
	}
	return string(out)
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// message returns the scanned message name in dir, failing when absent.
func message(t *testing.T, dir, name string) codegen.RawProtoMessage {
	t.Helper()
	scan, err := codegen.ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, m := range scan.Messages {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("message %s not declared in %s", name, dir)
	return codegen.RawProtoMessage{}
}

// TestEnsureConfigBlock_ComposesOnTheScaffoldedConfig: on the config.proto a
// project is born with, the block is declared and composed on AppConfig
// under the next free number. That proto retires tag 22 and its fields run
// to 40, so the block takes 41 — and a second adapter takes 42, not 41.
func TestEnsureConfigBlock_ComposesOnTheScaffoldedConfig(t *testing.T) {
	dir, file := writeConfigProto(t, scaffoldedConfigProto(t))
	before := message(t, dir, ConfigRootMessage).MaxFieldNumber

	res, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig"))
	if err != nil {
		t.Fatalf("EnsureConfigBlock: %v", err)
	}
	if !res.AddedMessage || !res.AddedField || res.FieldNumber != before+1 || res.File != file {
		t.Fatalf("result = %+v, want both halves added at field %d of %s", res, before+1, file)
	}
	src := readString(t, file)
	for _, want := range []string{
		"AlpacaConfig alpaca = " + strconv.Itoa(before+1) + ";",
		"message AlpacaConfig {",
		`env_var: "ALPACA_BASE_URL"`,
		"string base_url = 1 [(forge.v1.config) = {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("config.proto missing %q:\n%s", want, src)
		}
	}
	root := message(t, dir, ConfigRootMessage)
	if root.MaxFieldNumber != before+1 {
		t.Errorf("AppConfig high-water mark = %d, want %d", root.MaxFieldNumber, before+1)
	}
	if !composes(root, "proj.config.v1.AlpacaConfig", "alpaca") {
		t.Errorf("AppConfig does not compose AlpacaConfig as alpaca: %+v", root.Fields)
	}
	if block := message(t, dir, "AlpacaConfig"); len(block.Fields) != 1 || block.Fields[0].Name != "base_url" {
		t.Errorf("AlpacaConfig fields = %+v, want just base_url", block.Fields)
	}

	res, err = EnsureConfigBlock(dir, adapterBlock("beta", "BetaConfig"))
	if err != nil {
		t.Fatalf("second adapter: %v", err)
	}
	if res.FieldNumber != before+2 {
		t.Errorf("second block field number = %d, want %d (never a number already taken)", res.FieldNumber, before+2)
	}
}

// Re-running the scaffold for the same adapter changes nothing.
func TestEnsureConfigBlock_IsIdempotent(t *testing.T) {
	dir, file := writeConfigProto(t, scaffoldedConfigProto(t))
	if _, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig")); err != nil {
		t.Fatal(err)
	}
	once := readString(t, file)
	res, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig"))
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if res.AddedMessage || res.AddedField {
		t.Errorf("re-run reported an edit: %+v", res)
	}
	if twice := readString(t, file); twice != once {
		t.Errorf("re-run rewrote config.proto:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// A reserved range above every field still bounds the new number, and a
// one-line root body is extended in place.
func TestEnsureConfigBlock_NeverTakesAReservedNumber(t *testing.T) {
	dir, file := writeConfigProto(t, `syntax = "proto3";

package config.v1;

import "forge/v1/forge.proto";

message AppConfig { int32 port = 1; reserved 50 to 60; }
`)
	res, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig"))
	if err != nil {
		t.Fatalf("EnsureConfigBlock: %v", err)
	}
	if res.FieldNumber != 61 {
		t.Errorf("field number = %d, want 61 (past reserved 50 to 60)", res.FieldNumber)
	}
	if root := message(t, dir, ConfigRootMessage); !composes(root, "config.v1.AlpacaConfig", "alpaca") {
		t.Errorf("AppConfig does not compose the block:\n%s", readString(t, file))
	}
}

// A block message the author already declared is composed as it is: only the
// root field is added.
func TestEnsureConfigBlock_ComposesAnExistingMessage(t *testing.T) {
	dir, file := writeConfigProto(t, `syntax = "proto3";

package config.v1;

import "forge/v1/forge.proto";

message AppConfig {
  int32 port = 1;
}

message AlpacaConfig {
  string base_url = 1 [(forge.v1.config) = {env_var: "ALPACA_URL"}];
  string api_key = 2 [(forge.v1.config) = {env_var: "ALPACA_API_KEY", sensitive: true}];
}
`)
	res, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig"))
	if err != nil {
		t.Fatalf("EnsureConfigBlock: %v", err)
	}
	if res.AddedMessage || !res.AddedField || res.FieldNumber != 2 {
		t.Errorf("result = %+v, want only the root field, numbered 2", res)
	}
	if n := strings.Count(readString(t, file), "message AlpacaConfig"); n != 1 {
		t.Errorf("AlpacaConfig declared %d times, want 1", n)
	}
}

// Conflicts are refused with the file untouched, never guessed around.
func TestEnsureConfigBlock_RefusesConflicts(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{
			name: "root field name taken by another type",
			src: `syntax = "proto3";
package config.v1;
message AppConfig {
  string alpaca = 1;
}
`,
			wantErr: "already has a field named alpaca",
		},
		{
			name: "existing block lacks the leaf the adapter reads",
			src: `syntax = "proto3";
package config.v1;
message AppConfig {
  int32 port = 1;
}
message AlpacaConfig {
  string endpoint = 1;
}
`,
			wantErr: "without a base_url field",
		},
		{
			name: "leaf env var already bound",
			src: `syntax = "proto3";
package config.v1;
message AppConfig {
  string alpaca_url = 1 [(forge.v1.config) = {env_var: "ALPACA_BASE_URL"}];
}
`,
			wantErr: "env var ALPACA_BASE_URL is already bound",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, file := writeConfigProto(t, c.src)
			_, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig"))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if got := readString(t, file); got != c.src {
				t.Errorf("a refused edit changed config.proto:\n%s", got)
			}
		})
	}
}

// No AppConfig to compose on — no proto/config at all, or only per-binary
// configs — is ErrNoConfigRoot, which the scaffold reports before writing
// adapter files. No scalar fallback is emitted.
func TestEnsureConfigBlock_NoRoot(t *testing.T) {
	if _, err := EnsureConfigBlock(filepath.Join(t.TempDir(), "proto", "config", "v1"), adapterBlock("alpaca", "AlpacaConfig")); !errors.Is(err, ErrNoConfigRoot) {
		t.Errorf("missing proto/config: err = %v, want ErrNoConfigRoot", err)
	}
	src := `syntax = "proto3";
package config.v1;
message AdminConfig {
  int32 port = 1;
}
`
	dir, file := writeConfigProto(t, src)
	if _, err := EnsureConfigBlock(dir, adapterBlock("alpaca", "AlpacaConfig")); !errors.Is(err, ErrNoConfigRoot) {
		t.Errorf("no AppConfig: err = %v, want ErrNoConfigRoot", err)
	}
	if got := readString(t, file); got != src {
		t.Errorf("config.proto changed without a root to compose on:\n%s", got)
	}
}

func composes(root codegen.RawProtoMessage, typeName, field string) bool {
	for _, f := range root.Fields {
		if f.Name == field && f.TypeName == typeName {
			return true
		}
	}
	return false
}
