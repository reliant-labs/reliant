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

// Subtree configs: one config several commands load, without every process
// of the binary loading it. reliant's shape: the CLI connection settings every
// end-user command reads (server URL, token) must not reach the server roles'
// workloads, and `daemon start` and `open` both run the daemon guard policy.

func subtreeMessage(name, binary string, line int, subtrees [][]string, fields ...ConfigField) ConfigMessage {
	m := boundMessage(name, binary, line)
	m.Subtrees = subtrees
	if len(fields) > 0 {
		m.Fields = fields
	}
	return m
}

func TestValidateBinaryConfigBindings_SubtreeConfigsBindSeveralCommands(t *testing.T) {
	guards := ConfigMessage{Name: "GuardsConfig", Fields: []ConfigField{envField("reclaim_at", "RELIANT_DISK_RECLAIM_AT", "reclaim-at")}}
	guardRef := ConfigField{Name: "guards", GoName: "Guards", ProtoType: "message", MessageType: "GuardsConfig"}
	msgs := []ConfigMessage{
		guards,
		commandMessage("ReliantConfig", "reliant", nil, 10, envField("log_level", "RELIANT_LOG_LEVEL", "log-level")),
		subtreeMessage("CLIConfig", "reliant", 20, [][]string{{"auth"}, {"daemon"}, {"open"}},
			envField("server_url", "RELIANT_SERVER_URL", "server")),
		commandMessage("DaemonConfig", "reliant", []string{"daemon", "start"}, 30, guardRef),
		// A sibling of `daemon start` composing the same block: one
		// definition, two values on two processes — not a collision.
		commandMessage("OpenConfig", "reliant", []string{"open"}, 40, guardRef),
		commandMessage("ApiServerConfig", "reliant", []string{"server", "api"}, 50, envField("grpc_port", "GRPC_PORT", "grpc-port")),
	}
	if err := ValidateBinaryConfigBindings(msgs, []string{"reliant"}); err != nil {
		t.Fatalf("a subtree config beside root and command configs was refused: %v", err)
	}
}

func TestValidateBinaryConfigBindings_RefusesMalformedSubtrees(t *testing.T) {
	for _, c := range []struct {
		name string
		m    ConfigMessage
		want string
	}{
		{"with command", func() ConfigMessage {
			m := subtreeMessage("X", "reliant", 10, [][]string{{"daemon"}})
			m.Command = []string{"open"}
			return m
		}(), "`command` and `subtrees` are both set"},
		{"empty path", subtreeMessage("X", "reliant", 10, [][]string{{}}), "`subtrees` has an empty path"},
		{"whitespace", subtreeMessage("X", "reliant", 10, [][]string{{"daemon start"}}), `in ` + "`subtrees`" + `: ` + "`command`" + ` element "daemon start" contains whitespace`},
		{"nested", subtreeMessage("X", "reliant", 10, [][]string{{"daemon"}, {"daemon", "start"}}), `subtree ["daemon", "start"] lies at or below subtree ["daemon"]`},
		{"duplicate", subtreeMessage("X", "reliant", 10, [][]string{{"open"}, {"open"}}), `subtree ["open"] lies at or below subtree ["open"]`},
		{"no binary", subtreeMessage("X", "", 10, [][]string{{"open"}}), "no `binary` is set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateBinaryConfigBindings([]ConfigMessage{c.m}, []string{"reliant"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal containing %q, got: %v", c.want, err)
			}
		})
	}
}

// A process under a subtree loads the subtree config together with the root
// config, its own command config, and any other subtree config over its path,
// so none of them may declare the same leaf. Configs no process loads
// together (sibling subtrees) may.
func TestValidateBinaryConfigBindings_RefusesSubtreeCollisions(t *testing.T) {
	msgs := []ConfigMessage{
		commandMessage("ReliantConfig", "reliant", nil, 10, envField("log_level", "RELIANT_LOG_LEVEL", "log-level")),
		subtreeMessage("CLIConfig", "reliant", 20, [][]string{{"daemon"}, {"open"}},
			envField("server_url", "RELIANT_SERVER_URL", "server")),
		// Command under the subtree, same env var: refused.
		commandMessage("DaemonConfig", "reliant", []string{"daemon", "start"}, 30,
			envField("gateway_url", "RELIANT_SERVER_URL", "grpc-url")),
		// A deeper subtree, same flag: refused.
		subtreeMessage("DaemonOpsConfig", "reliant", 40, [][]string{{"daemon", "ops"}},
			envField("target", "OPS_TARGET", "server")),
		// Same env var as the root config: refused.
		subtreeMessage("AuthConfig", "reliant", 50, [][]string{{"auth"}},
			envField("verbosity", "RELIANT_LOG_LEVEL", "verbosity")),
		// A sibling subtree reusing CLIConfig's flag: no process loads both.
		subtreeMessage("ServerToolsConfig", "reliant", 60, [][]string{{"server"}},
			envField("api", "SERVER_TOOLS_API", "server")),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"reliant"})
	if err == nil {
		t.Fatal("subtree configs sharing leaves with configs loaded beside them generated cleanly")
	}
	got := err.Error()
	for _, want := range []string{
		"config.proto:30: message DaemonConfig",
		"env var RELIANT_SERVER_URL (gateway_url) is also declared by CLIConfig (server_url), the subtree config for `reliant daemon`, `reliant open`. The `reliant daemon start` process loads both",
		"config.proto:40: message DaemonOpsConfig",
		"flag --server (target) is also declared by CLIConfig (server_url)",
		"The `reliant daemon ops` process loads both",
		"config.proto:50: message AuthConfig",
		"env var RELIANT_LOG_LEVEL (verbosity) is also declared by ReliantConfig (log_level), the binary's root config. The `reliant auth` process loads both",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "ServerToolsConfig") {
		t.Errorf("a sibling subtree no process loads with CLIConfig must not be refused:\n%s", got)
	}
}

func TestBinaryConfigsFromMessages_SubtreeConfigTemplateData(t *testing.T) {
	msgs := []ConfigMessage{
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{envField("log_level", "RELIANT_LOG_LEVEL", "log-level")}},
		{Name: "CLIConfig", Binary: "reliant", Subtrees: [][]string{{"auth", "login"}, {"open"}},
			Fields: []ConfigField{envField("server_url", "RELIANT_SERVER_URL", "server")}},
	}
	var cli BinaryConfig
	for _, bc := range BinaryConfigsFromMessages(msgs) {
		if bc.MessageName == "CLIConfig" {
			cli = bc
		}
	}
	if !cli.BinaryHasRootConfig {
		t.Error("a subtree config beside a root config must leave --config to the root")
	}
	if got, want := cli.SubtreesGoArgs(), `[]string{"auth", "login"}, []string{"open"}`; got != want {
		t.Errorf("SubtreesGoArgs = %s, want %s", got, want)
	}
	if got := strings.Join(cli.SubtreeLines(), "|"); got != "auth login|open" {
		t.Errorf("SubtreeLines = %q", got)
	}
}

func subtreeKCLMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{
			{Name: "log_level", GoName: "LogLevel", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_LOG_LEVEL", Flag: "log-level", DefaultValue: "info"},
		}},
		{Name: "CLIConfig", Binary: "reliant", Subtrees: [][]string{{"daemon"}, {"open"}}, Fields: []ConfigField{
			{Name: "server_url", GoName: "ServerUrl", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_SERVER_URL", Flag: "server", DefaultValue: "https://api"},
			{Name: "token", GoName: "Token", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_TOKEN", Sensitive: true},
		}},
		{Name: "DaemonConfig", Binary: "reliant", Command: []string{"daemon", "start"}, Fields: []ConfigField{
			{Name: "listen_port", GoName: "ListenPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "DAEMON_LISTEN_PORT", Flag: "listen-port", DefaultValue: "9190"},
		}},
		{Name: "ApiServerConfig", Binary: "reliant", Command: []string{"server", "api"}, Fields: []ConfigField{
			{Name: "grpc_port", GoName: "GrpcPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "GRPC_PORT", Flag: "grpc-port", DefaultValue: "9090"},
		}},
	}
}

// The deploy half, evaluated with the real kcl binary: a workload's env is
// the root config, plus every subtree config over its command, plus its
// command config — and a server role's workload carries none of the CLI
// config's keys or credentials.
func TestPerBinaryKCL_ProcessSelectorAddsSubtreeConfigs(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping subtree selector proof")
	}
	if testing.Short() {
		t.Skip("-short: evaluates the generated module with the kcl binary; runs in task test")
	}
	mod, err := GenerateConfigKCLPerBinary(nil, BinaryConfigFieldsFrom(subtreeKCLMessages()), "reliant")
	if err != nil {
		t.Fatalf("GenerateConfigKCLPerBinary: %v", err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod":                 "[package]\nname = \"subtree_proof\"\n",
		ConfigSchemaModule + ".k": mod,
		"forge/kcl.mod":           "[package]\nname = \"forge\"\n",
		"forge/core.k":            "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n",
		"main.k": `import ` + ConfigSchemaModule + ` as config_gen

_p = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {log_level = "debug"}
    cli_config = config_gen.CLIConfig {server_url = "https://cp"}
    daemon_config = config_gen.DaemonConfig {}
    api_server_config = config_gen.ApiServerConfig {}
}
_env = lambda argv: [str], secrets: [str] -> {str:} {
    config_gen.reliantProcessEnvMap(_p, argv, secrets)
}
_start = _env(["reliant", "daemon", "start"], ["RELIANT_TOKEN"])
_status = _env(["reliant", "daemon", "status"], [])
_open = _env(["reliant", "open", "--x"], [])
_api = _env(["reliant", "server", "api"], [])

assert_start = _start["RELIANT_LOG_LEVEL"] == "debug" and _start["RELIANT_SERVER_URL"] == "https://cp" and _start["DAEMON_LISTEN_PORT"] == "9190"
assert_start_secret = _start["RELIANT_TOKEN"].store_key == "RELIANT_TOKEN"
assert_status = _status["RELIANT_SERVER_URL"] == "https://cp" and "DAEMON_LISTEN_PORT" not in _status
assert_open = _open["RELIANT_SERVER_URL"] == "https://cp"
assert_api_least_knowledge = "RELIANT_SERVER_URL" not in _api and "RELIANT_TOKEN" not in _api and _api["GRPC_PORT"] == "9090"
`,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s\n--- module ---\n%s", err, out, mod)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal kcl json: %v\n%s", err, out)
	}
	var asserts int
	for k, v := range parsed {
		if !strings.HasPrefix(k, "assert_") {
			continue
		}
		asserts++
		if b, ok := v.(bool); !ok || !b {
			t.Errorf("assertion %q is %v", k, v)
		}
	}
	if asserts != 5 {
		t.Fatalf("want 5 assert_* results, got %d:\n%s", asserts, out)
	}
}

// The generated surface of a subtree config: persistent flags on each
// subtree's command (leaving --config to the root config), and a loader that
// refuses a command outside every subtree.
func TestConfigTemplate_SubtreeSurface(t *testing.T) {
	got := renderConfigGo(t, subtreeKCLMessages())
	for _, want := range []string{
		"func RegisterCLIConfigFlags(cmd *cobra.Command) {\n\tif err := forgeconfig.RegisterInheritedFlags(cmd, &configv1.CLIConfig{}); err != nil {",
		"func LoadCLIConfig(cmd *cobra.Command) (*CLIConfig, error) {\n\tif err := forgeconfig.CheckCommandSubtree(cmd, \"CLIConfig\", []string{\"daemon\"}, []string{\"open\"}); err != nil {",
		`command at or below "daemon", "open"`,
		"func ValidateCLIConfig(cfg *CLIConfig) error",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.go missing %q\n---\n%s", want, got)
		}
	}
	// With no root config beside it, the subtree config carries --config.
	msgs := subtreeKCLMessages()[1:]
	if got := renderConfigGo(t, msgs); !strings.Contains(got, "forgeconfig.RegisterPersistentFlags(cmd, &configv1.CLIConfig{})") {
		t.Errorf("a subtree config with no root config must register --config itself:\n%s", got)
	}
}
