// Component config-block tests — the kalshi-trader acceptance shape
// (fr-ad24278452): a worker declares `WTIPersistMaxPerTick int` in Deps
// and wire_gen can never resolve it because scalars are CONFIGURATION,
// not collaborators. The fix is a per-component config block:
//
//	proto/config/v1/config.proto:
//	    message TraderConfig {
//	        int32 max_per_tick = 1 [(forge.v1.config) = {
//	            env_var: "TRADER_MAX_PER_TICK", default_value: "10", ...}];
//	    }
//	    message AppConfig { ... TraderConfig trader = 21; }
//
//	workers/trader/worker.go:
//	    type Deps struct {
//	        Logger *slog.Logger
//	        Cfg    config.TraderConfig // resolved by TYPE → cfg.Trader
//	    }
//
// These tests cover the chain end-to-end at the codegen layer:
// descriptor-shaped messages → pkg/config nested struct + env/flag →
// wire_gen type-based resolution (value + pointer + ambiguity error) →
// per-env deploy projection of a block leaf → regenerate idempotency.
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

// traderConfigMessages is the canonical fixture: a root AppConfig with
// one scalar field + one block reference, and the TraderConfig block
// with an int leaf (the kalshi MaxPerTick shape).
func traderConfigMessages() []ConfigMessage {
	return []ConfigMessage{
		{
			Name: "AppConfig",
			Fields: []ConfigField{
				{
					Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32",
					EnvVar: "PORT", Flag: "port", DefaultValue: "8080",
					Description: "HTTP server port",
				},
				{
					Name: "trader", GoName: "Trader", ProtoType: "message",
					MessageType: "TraderConfig",
				},
			},
		},
		{
			Name: "TraderConfig",
			Fields: []ConfigField{
				{
					Name: "max_per_tick", GoName: "MaxPerTick", GoType: "int32", ProtoType: "int32",
					EnvVar: "TRADER_MAX_PER_TICK", Flag: "trader-max-per-tick", DefaultValue: "10",
					Description: "Maximum WTI persists per tick",
				},
			},
		},
	}
}

// writeConfigDescriptor writes go.mod + gen/forge_descriptor.json so
// loadConfigBlockIndex (via ParseConfigProto's go.mod walk-up) sees the
// same config messages GenerateConfigLoader consumed.
func writeConfigDescriptor(t *testing.T, projectDir string, messages []ConfigMessage) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	desc := ForgeDescriptor{Configs: messages}
	data, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "gen", "forge_descriptor.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildConfigTemplateData_PartitionsBlocks(t *testing.T) {
	data := BuildConfigTemplateData(traderConfigMessages())

	// Root: just Port. The block reference itself is not a root leaf.
	if len(data.RootFields) != 1 || data.RootFields[0].GoName != "Port" {
		t.Fatalf("RootFields = %+v, want exactly [Port]", data.RootFields)
	}
	// All leaves: Port + the block leaf with a qualified GoPath.
	if len(data.Fields) != 2 {
		t.Fatalf("Fields = %+v, want 2 leaves", data.Fields)
	}
	if data.Fields[1].GoPath != "Trader.MaxPerTick" {
		t.Errorf("block leaf GoPath = %q, want Trader.MaxPerTick", data.Fields[1].GoPath)
	}
	if len(data.BlockTypes) != 1 || data.BlockTypes[0].TypeName != "TraderConfig" {
		t.Fatalf("BlockTypes = %+v, want [TraderConfig]", data.BlockTypes)
	}
	if len(data.BlockFields) != 1 || data.BlockFields[0].GoName != "Trader" || data.BlockFields[0].TypeName != "TraderConfig" {
		t.Fatalf("BlockFields = %+v, want [Trader TraderConfig]", data.BlockFields)
	}
	// No field carries role=MODE here, so no dev-mode field is selected.
	// (Semantic role selection replaced the old name-keyed FieldNames map.)
	if data.RoleModeField != "" {
		t.Errorf("RoleModeField = %q, want empty (no role=MODE field declared)", data.RoleModeField)
	}

	refs := ConfigBlocksFromMessages(traderConfigMessages())
	if len(refs) != 1 || refs[0].FieldName != "Trader" || refs[0].TypeName != "TraderConfig" {
		t.Fatalf("ConfigBlocksFromMessages = %+v, want [{Trader TraderConfig}]", refs)
	}
}

// TestBuildConfigTemplateData_FlatStaysFlat pins backwards
// compatibility: messages with no block references render exactly the
// legacy flat shape (every field a root leaf, GoPath == GoName).
func TestBuildConfigTemplateData_FlatStaysFlat(t *testing.T) {
	data := BuildConfigTemplateData(DefaultConfigMessages())
	if len(data.BlockTypes) != 0 || len(data.BlockFields) != 0 {
		t.Fatalf("flat scaffold config must produce no blocks; got types=%+v fields=%+v", data.BlockTypes, data.BlockFields)
	}
	if len(data.Fields) != len(data.RootFields) {
		t.Fatalf("flat config: Fields (%d) != RootFields (%d)", len(data.Fields), len(data.RootFields))
	}
	for _, f := range data.Fields {
		if f.GoPath != f.GoName {
			t.Errorf("flat field %s: GoPath %q != GoName", f.GoName, f.GoPath)
		}
	}
}

// TestGenerateConfigLoader_ComponentBlock: with a config block in the proto,
// the generated config.go stays the THIN ALIAS shim (the block struct lives
// in the gen proto package as a nested message — NOT re-declared here).
func TestGenerateConfigLoader_ComponentBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateConfigLoader(traderConfigMessages(), dir, nil); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pkg", "config", "config_gen.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	content := string(data)

	// The thin shim is emitted; the block's leaf struct is NOT re-declared
	// here (it is a nested message on the gen proto type).
	if !strings.Contains(content, "type Config = configv1.AppConfig") {
		t.Errorf("config.go should be the thin alias shim\n%s", content)
	}
	// The block is re-exported under its own name, so a component takes it
	// as `Cfg *config.TraderConfig` (#748) — an alias, never a second struct.
	if !strings.Contains(content, "type TraderConfig = configv1.TraderConfig") {
		t.Errorf("config.go must alias the composed block type\n%s", content)
	}
	for _, banned := range []string{"type TraderConfig struct", "Trader TraderConfig", "loadField("} {
		if strings.Contains(content, banned) {
			t.Errorf("config.go must NOT re-declare the block struct or per-field loader (%q)\n%s", banned, content)
		}
	}

	// The generated file must be syntactically valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "config_gen.go", data, 0); err != nil {
		t.Fatalf("generated config.go does not parse: %v\n--- content ---\n%s", err, content)
	}
}

// NOTE: the config-block-by-TYPE wiring tests
// (TestGenerateWireGen_ConfigBlock{ByType,Pointer,Ambiguous} and the
// wire half of TestConfigBlock_RegenerateIdempotent) were removed with
// the old name-matched wire_gen unit (FORGE_SHAPE_REDESIGN §2). The
// config.go projection idempotency below is preserved.

// TestConfigBlock_RegenerateIdempotent: generating twice produces
// byte-identical pkg/config/config.go (the "forever diff" regression
// class this feature kills).
func TestConfigBlock_RegenerateIdempotent(t *testing.T) {
	projectDir := t.TempDir()
	writeConfigDescriptor(t, projectDir, traderConfigMessages())

	gen := func() string {
		if err := GenerateConfigLoader(traderConfigMessages(), projectDir, nil); err != nil {
			t.Fatalf("GenerateConfigLoader: %v", err)
		}
		cfgGo, err := os.ReadFile(filepath.Join(projectDir, "pkg", "config", "config_gen.go"))
		if err != nil {
			t.Fatal(err)
		}
		return string(cfgGo)
	}

	cfg1 := gen()
	cfg2 := gen()
	if cfg1 != cfg2 {
		t.Error("pkg/config/config.go is not idempotent across regenerates")
	}
}

// TestGenerateConfigKScaffold_BlockLeaf: the per-env config.k scaffolder emits
// only the fields an env must pin (KCL-mandatory + the dev MODE/DSN markers) and
// skips the block-REFERENCE field (message-typed, no env_var). A block leaf with
// a schema default is not pinned (it inherits the default), so it does not leak
// into a non-dev env's config.k.
func TestGenerateConfigKScaffold_BlockLeaf(t *testing.T) {
	dir := t.TempDir()

	// Flatten root + block messages the way generatePerEnvDeployConfig does —
	// block leaves participate via their own ConfigMessage.
	var fields []ConfigField
	for _, m := range traderConfigMessages() {
		fields = append(fields, m.Fields...)
	}

	if _, err := GenerateConfigKScaffold(fields, "demo", filepath.Join(dir, "deploy", "kcl"), "prod"); err != nil {
		t.Fatalf("GenerateConfigKScaffold: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "prod", "config.k"))
	if err != nil {
		t.Fatalf("read config.k: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "app_config: "+ConfigSchemaModule+".AppConfig = {") {
		t.Errorf("config.k should declare the typed AppConfig instance:\n%s", content)
	}
	// The block-reference field carries no env_var and must never be pinned.
	if strings.Contains(content, "trader ") || strings.Contains(content, "TraderConfig") {
		t.Errorf("block reference field must not appear in config.k:\n%s", content)
	}
	// max_per_tick has a schema default, so a non-dev env inherits it (no pin).
	if strings.Contains(content, "max_per_tick") {
		t.Errorf("defaulted block leaf must inherit its schema default (not pinned):\n%s", content)
	}
}
