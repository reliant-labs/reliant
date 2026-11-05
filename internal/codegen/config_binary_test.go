package codegen

import (
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// perBinaryMessages models the target shape: a shared BaseConfig DEFINITION
// composed by two binaries, each also carrying leaves only it reads.
func perBinaryMessages() []ConfigMessage {
	base := ConfigMessage{
		Name: "BaseConfig",
		Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32",
				EnvVar: "PORT", Flag: "port", DefaultValue: "8080"},
			{Name: "log_level", GoName: "LogLevel", GoType: "string", ProtoType: "string",
				EnvVar: "LOG_LEVEL", Flag: "log-level", DefaultValue: "info"},
		},
	}
	admin := ConfigMessage{
		Name:   "AdminConfig",
		Binary: "admin",
		Fields: []ConfigField{
			{Name: "base", GoName: "Base", ProtoType: "message", MessageType: "BaseConfig"},
			{Name: "admin_api_key", GoName: "AdminApiKey", GoType: "string", ProtoType: "string",
				EnvVar: "ADMIN_API_KEY", Flag: "admin-api-key", Sensitive: true},
		},
	}
	gateway := ConfigMessage{
		Name:   "GatewayConfig",
		Binary: "gateway",
		Fields: []ConfigField{
			{Name: "base", GoName: "Base", ProtoType: "message", MessageType: "BaseConfig"},
			{Name: "upstream_timeout_ms", GoName: "UpstreamTimeoutMs", GoType: "int32", ProtoType: "int32",
				EnvVar: "UPSTREAM_TIMEOUT_MS", Flag: "upstream-timeout-ms", DefaultValue: "500"},
		},
	}
	return []ConfigMessage{base, admin, gateway}
}

func envVarsOf(fields []ConfigField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.EnvVar)
	}
	return out
}

func goPathsOf(fields []ConfigTemplateField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.GoPath)
	}
	return out
}

// The defining property: each binary's surface is its own leaves plus the
// shared base, and NOTHING from a sibling.
func TestBinaryConfigs_AreDisjoint(t *testing.T) {
	bcs := BinaryConfigsFromMessages(perBinaryMessages())
	if len(bcs) != 2 {
		t.Fatalf("got %d binary configs, want 2 (only annotated messages)", len(bcs))
	}

	admin, gateway := bcs[0], bcs[1]
	if admin.Binary != "admin" || gateway.Binary != "gateway" {
		t.Fatalf("binaries = %q/%q, want admin/gateway", admin.Binary, gateway.Binary)
	}

	// Each surface is the shared base, reached through the block field and
	// qualified — the same GoPath mechanism component config blocks already
	// use — plus exactly the binary's own leaves.
	if got, want := goPathsOf(admin.Fields), []string{"Base.Port", "Base.LogLevel", "AdminApiKey"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin fields = %v, want %v", got, want)
	}
	if got, want := goPathsOf(gateway.Fields), []string{"Base.Port", "Base.LogLevel", "UpstreamTimeoutMs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("gateway fields = %v, want %v", got, want)
	}

	// Neither binary's surface mentions the other's field.
	for _, p := range goPathsOf(admin.Fields) {
		if p == "UpstreamTimeoutMs" {
			t.Error("admin config leaked gateway's field")
		}
	}
	for _, p := range goPathsOf(gateway.Fields) {
		if p == "AdminApiKey" {
			t.Error("gateway config leaked admin's field")
		}
	}
}

// Deleting a field from one binary's message must not change the other's
// surface. This is the user's stated requirement, asserted directly.
func TestBinaryConfigs_DeletingAFieldDoesNotAffectSiblings(t *testing.T) {
	before := BinaryConfigsFromMessages(perBinaryMessages())
	gatewayBefore := goPathsOf(before[1].Fields)

	// Drop admin's own field entirely.
	msgs := perBinaryMessages()
	msgs[1].Fields = msgs[1].Fields[:1] // base only
	after := BinaryConfigsFromMessages(msgs)

	if got, want := goPathsOf(after[0].Fields), []string{"Base.Port", "Base.LogLevel"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin fields after delete = %v, want %v", got, want)
	}
	if got := goPathsOf(after[1].Fields); !reflect.DeepEqual(got, gatewayBefore) {
		t.Errorf("gateway surface changed after editing admin: %v -> %v", gatewayBefore, got)
	}
}

// The env projection for a config is built from ConfigFieldsForMessage, so
// this set IS what lands in that workload's Deployment.
func TestConfigFieldsForMessage_OnlyThatConfigsEnvVars(t *testing.T) {
	msgs := perBinaryMessages()

	adminEnv := envVarsOf(ConfigFieldsForMessage(msgs, "AdminConfig"))
	wantAdmin := []string{"PORT", "LOG_LEVEL", "ADMIN_API_KEY"}
	if !reflect.DeepEqual(adminEnv, wantAdmin) {
		t.Errorf("admin env vars = %v, want %v", adminEnv, wantAdmin)
	}

	gwEnv := envVarsOf(ConfigFieldsForMessage(msgs, "GatewayConfig"))
	wantGW := []string{"PORT", "LOG_LEVEL", "UPSTREAM_TIMEOUT_MS"}
	if !reflect.DeepEqual(gwEnv, wantGW) {
		t.Errorf("gateway env vars = %v, want %v", gwEnv, wantGW)
	}

	// The security payoff: admin's secret is absent from gateway's manifest.
	for _, e := range gwEnv {
		if e == "ADMIN_API_KEY" {
			t.Error("gateway workload would carry admin's secret env var")
		}
	}

	if got := ConfigFieldsForMessage(msgs, "NoSuchConfig"); got != nil {
		t.Errorf("unknown message = %v, want nil", got)
	}
}

// A project that annotates NOTHING must be completely unaffected: no binary
// configs, so every caller stays on the single-AppConfig path.
func TestBinaryConfigs_UnannotatedProjectIsUnchanged(t *testing.T) {
	if got := BinaryConfigsFromMessages(DefaultConfigMessages()); len(got) != 0 {
		t.Errorf("unannotated project produced %d binary configs, want 0", len(got))
	}

	// And the existing flat template data is byte-for-byte what it was:
	// the scaffold's fields all land on the root Config with unqualified
	// GoPaths, exactly as before this feature existed.
	data := BuildConfigTemplateData(DefaultConfigMessages())
	if len(data.Fields) == 0 {
		t.Fatal("expected the default scaffold to produce config fields")
	}
	for _, f := range data.Fields {
		if f.GoPath != f.GoName {
			t.Errorf("field %s has qualified GoPath %q; the single-AppConfig shape must stay flat", f.GoName, f.GoPath)
		}
	}
	if data.RoleModeField == "" {
		t.Error("role=MODE selection regressed for the unannotated project")
	}
}

// A shared block is a DEFINITION, not a binary: it must never produce a
// config surface of its own even though it carries config fields.
func TestBinaryConfigs_SharedBlockIsNotABinary(t *testing.T) {
	for _, bc := range BinaryConfigsFromMessages(perBinaryMessages()) {
		if bc.MessageName == "BaseConfig" {
			t.Error("BaseConfig must not be treated as a binary config")
		}
	}
}

func renderConfigGo(t *testing.T, msgs []ConfigMessage) string {
	t.Helper()
	d := BuildConfigTemplateData(msgs)
	d.Module = "example.com/app"
	out, err := templates.ProjectTemplates().Render("config.go.tmpl", d)
	if err != nil {
		t.Fatalf("render config.go.tmpl: %v", err)
	}
	// Every .go render goes through the canonical formatter on write
	// (checksums.WriteGeneratedFile), so compile-shape assertions are made
	// against the formatted bytes, which is what lands on disk.
	formatted, err := format.Source(out)
	if err != nil {
		t.Fatalf("rendered config.go is not valid Go: %v\n%s", err, out)
	}
	return string(formatted)
}

// The single-binary project — the common case — must not pay for per-binary
// config: its generated pkg/config carries the same surface it always did and
// nothing per-binary at all.
func TestConfigTemplate_SingleBinaryIsUnchanged(t *testing.T) {
	got := renderConfigGo(t, DefaultConfigMessages())

	for _, want := range []string{
		"type Config = configv1.AppConfig",
		"func RegisterFlags(cmd *cobra.Command)",
		"func Load(cmd *cobra.Command) (*Config, error)",
		"func Validate(cfg *Config) error",
		"func ModeOf(cfg *Config) Mode",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("single-binary config.go missing %q", want)
		}
	}
	if strings.Contains(got, "Per-binary configs") {
		t.Error("single-binary config.go must not carry the per-binary section")
	}
}

// A per-binary project gets one type + one flag pair + one loader PER BINARY,
// and the two binaries' surfaces do not mention each other.
func TestConfigTemplate_PerBinarySurface(t *testing.T) {
	msgs := perBinaryMessages()
	got := renderConfigGo(t, msgs)

	for _, want := range []string{
		"type AdminConfig = configv1.AdminConfig",
		"type GatewayConfig = configv1.GatewayConfig",
		// A binary's root config is its binary-wide surface: every leaf,
		// composed blocks included, persistent on the root command.
		"func RegisterAdminConfigFlags(cmd *cobra.Command)",
		"forgeconfig.RegisterPersistentFlags(cmd, &configv1.AdminConfig{})",
		"func LoadAdminConfig(cmd *cobra.Command) (*AdminConfig, error)",
		"func LoadGatewayConfig(cmd *cobra.Command) (*GatewayConfig, error)",
		"func ValidateAdminConfig(cfg *AdminConfig) error",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("per-binary config.go missing %q", want)
		}
	}
}

// A project that has moved ENTIRELY to per-binary configs — no AppConfig left
// — must still generate valid Go rather than aliasing a type that is gone.
func TestConfigTemplate_PurePerBinaryOmitsRootConfig(t *testing.T) {
	msgs := perBinaryMessages() // BaseConfig + two annotated binaries, no AppConfig
	got := renderConfigGo(t, msgs)

	if strings.Contains(got, "type Config = configv1.") {
		t.Error("no AppConfig exists, so no Config alias should be emitted")
	}
	if strings.Contains(got, "func RegisterFlags(cmd *cobra.Command)") {
		t.Error("project-global RegisterFlags should be omitted when there is no root config")
	}
	// The per-binary half is still fully present.
	if !strings.Contains(got, "func LoadAdminConfig(") {
		t.Error("per-binary loaders must still be emitted")
	}
	// Mode is re-exported unconditionally — it is a library type, not tied
	// to the root config.
	if !strings.Contains(got, "type Mode = forgeconfig.RuntimeMode") {
		t.Error("Mode alias should always be emitted")
	}
}

// One binary, several processes: the root config registers persistent on the
// binary's root, each command config local on its subcommand, and a command
// config's loader checks it is called from that subcommand.
func TestConfigTemplate_CommandScopedSurface(t *testing.T) {
	got := renderConfigGo(t, commandScopedMessages())

	for _, want := range []string{
		// Root config: the binary-wide surface.
		"// ReliantConfig is the reliant binary's configuration: its binary-wide\n// surface, loaded by every reliant process.",
		"func RegisterReliantConfigFlags(cmd *cobra.Command) {\n\tif err := forgeconfig.RegisterPersistentFlags(cmd, &configv1.ReliantConfig{}); err != nil {",
		"func LoadReliantConfig(cmd *cobra.Command) (*ReliantConfig, error) {\n\treturn forgeconfig.LoadTyped[*configv1.ReliantConfig](cmd)\n}",
		// Command config: local flags on its subcommand, path-checked load.
		"// ApiServerConfig is the configuration of the `reliant server api` process: the\n// reliant binary's \"server api\" subcommand.",
		// The root config owns --config; the command config does not shadow it.
		"func RegisterApiServerConfigFlags(cmd *cobra.Command) {\n\tif err := forgeconfig.RegisterLocalFlags(cmd, &configv1.ApiServerConfig{}); err != nil {",
		"func LoadApiServerConfig(cmd *cobra.Command) (*ApiServerConfig, error) {\n\tif err := forgeconfig.CheckCommandPath(cmd, \"ApiServerConfig\", \"server\", \"api\"); err != nil {",
		`forgeconfig.CheckCommandPath(cmd, "WorkerConfig", "server", "worker")`,
		`forgeconfig.CheckCommandPath(cmd, "MonolithConfig", "server")`,
		"func ValidateWorkerConfig(cfg *WorkerConfig) error",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.go missing %q\n---\n%s", want, got)
		}
	}
	// The pre-command split API is gone: a root config registers every leaf
	// in one call, so there is no separate shared half to register.
	if strings.Contains(got, "SharedFlags") {
		t.Error("per-binary config.go must not emit Register<Msg>SharedFlags any more")
	}
	// The root config's loader is callable from every subcommand: no path check.
	root := got[strings.Index(got, "func LoadReliantConfig"):]
	root = root[:strings.Index(root, "\n}\n")]
	if strings.Contains(root, "CheckCommandPath") {
		t.Errorf("the root config's loader must not check the command path:\n%s", root)
	}
}

// A binary that binds ONLY command configs has no root config to carry the
// --config file flag, so each command config registers it itself.
func TestConfigTemplate_CommandConfigWithoutRootRegistersConfigFlag(t *testing.T) {
	msgs := commandScopedMessages()
	got := renderConfigGo(t, []ConfigMessage{msgs[0], msgs[2]}) // LogConfig + ApiServerConfig
	if !strings.Contains(got, "forgeconfig.RegisterFlags(cmd, &configv1.ApiServerConfig{})") {
		t.Errorf("a command config with no root config beside it must register --config:\n%s", got)
	}
}

// With every config per-binary, Config aliases the primary binary's ROOT
// config — never one of its command configs, which is one process's surface
// rather than the binary's whole component graph's.
func TestGenerateConfigLoader_PrimaryAliasIgnoresCommandConfigs(t *testing.T) {
	newPrimary := func(t *testing.T, bin string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		treeDir := filepath.Join(dir, "cmd", bin, "cmd")
		if err := os.MkdirAll(treeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(treeDir, "root.go"), []byte("package cmd\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("root config present", func(t *testing.T) {
		// The command configs are declared FIRST, so a selection that only
		// matched the binary would pick ApiServerConfig.
		msgs := commandScopedMessages()
		ordered := []ConfigMessage{msgs[0], msgs[2], msgs[3], msgs[1]}
		dir := newPrimary(t, "reliant")
		if err := GenerateConfigLoader(ordered, dir, nil); err != nil {
			t.Fatalf("GenerateConfigLoader: %v", err)
		}
		src, err := os.ReadFile(filepath.Join(dir, "pkg", "config", "config_gen.go"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "type Config = configv1.ReliantConfig") {
			t.Errorf("Config must alias the primary binary's ROOT config:\n%s", src)
		}
	})

	t.Run("only command configs", func(t *testing.T) {
		msgs := commandScopedMessages()
		dir := newPrimary(t, "reliant")
		err := GenerateConfigLoader([]ConfigMessage{msgs[2], msgs[3]}, dir, nil)
		if err == nil {
			t.Fatal("a primary binary with only command configs has no Config to alias; want an error")
		}
		if !strings.Contains(err.Error(), "no ROOT config message is bound to the primary binary \"reliant\"") {
			t.Errorf("error should say the primary binary lacks a ROOT config: %v", err)
		}
	})
}
