package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// Forge bug e7e3b414: a scaffolded project keeps its project-global
// AppConfig, and binding one more config to its primary binary's `server`
// command (`option (forge.v1.binary_config) = {binary: "demo", command:
// ["server"]}`) made config_gen.k drop ROOTS, root_of and workloadEnvMap —
// while the scaffolded dev main.k calls config_gen.workloadEnvMap, so the env
// no longer rendered.

// appConfigBesideCommandMessages is the scaffold's AppConfig plus a command
// config bound to the primary binary `demo`'s `server` command.
func appConfigBesideCommandMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "database_url", GoName: "DatabaseUrl", GoType: "string", ProtoType: "string", EnvVar: "DATABASE_URL", Sensitive: true, Required: true},
		}},
		{Name: "ServeConfig", Binary: "demo", Command: []string{"server"}, Fields: []ConfigField{
			{Name: "greeting", GoName: "Greeting", GoType: "string", ProtoType: "string", EnvVar: "GREETING", DefaultValue: "hi"},
			{Name: "token", GoName: "Token", GoType: "string", ProtoType: "string", EnvVar: "SERVE_TOKEN", Sensitive: true, Optional: true},
		}},
	}
}

// writeGraphProject is a graph-mode project whose primary binary is `bin`
// (cmd/<bin>/cmd/root.go), with a deploy/kcl directory; "" writes no
// command tree.
func writeGraphProject(t *testing.T, bin string) (dir, kclDir string) {
	t.Helper()
	dir = t.TempDir()
	if bin != "" {
		tree := filepath.Join(dir, "cmd", bin, "cmd")
		if err := os.MkdirAll(tree, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "root.go"), []byte("package cmd\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclDir = filepath.Join(dir, "deploy", "kcl")
	if err := os.MkdirAll(kclDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !GraphComposition(dir) {
		t.Fatal("fixture must be a graph-mode project")
	}
	return dir, kclDir
}

// perBinaryConfigModule runs the per-binary emitter over messages in a graph
// project with primary binary bin, and returns config_gen.k.
func perBinaryConfigModule(t *testing.T, messages []ConfigMessage, bin string, roots []GraphRoot) (string, string) {
	t.Helper()
	dir, kclDir := writeGraphProject(t, bin)
	if err := GenerateConfigNativeSharedPerBinaryRoots(configFieldsOf(messages), BinaryConfigFieldsFrom(messages), roots, "demo", dir, kclDir, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(kclDir, ConfigSchemaModule+".k"))
	if err != nil {
		t.Fatal(err)
	}
	return string(got), kclDir
}

// configFieldsOf is the project-global AppConfig's fields of messages.
func configFieldsOf(messages []ConfigMessage) []ConfigField {
	for _, m := range messages {
		if m.Name == "AppConfig" && m.Binary == "" {
			return m.Fields
		}
	}
	return nil
}

func TestPerBinaryRoots_AppConfigBesideACommandConfigKeepsWorkloadEnvMap(t *testing.T) {
	src, _ := perBinaryConfigModule(t, appConfigBesideCommandMessages(), "demo", nil)
	for _, want := range []string{
		`"server" = Root {`,
		`env = ["PORT", "GREETING"]`,
		`secrets = ["DATABASE_URL", "SERVE_TOKEN"]`,
		`required_secrets = ["DATABASE_URL"]`,
		"root_of = lambda name: str",
		"_rootsProcessEnvMap = lambda c: AppConfig, p: DemoProcess, argv: [str], config_secrets: [str]",
		"_root_secrets = [_n for _n in config_secrets if _n in APP_CONFIG_SENSITIVE_ENV]",
		"appConfigEnvMap(c, _root_secrets) | demoProcessEnvMap(p, argv, [_n for _n in config_secrets if _n not in _root_secrets])",
		"rootEnvMap = lambda c: AppConfig, w: any, p: DemoProcess = DemoProcess {}",
		"_all = _rootsProcessEnvMap(c, p, _r.args, ",
		"workloadEnvMap = lambda c: AppConfig, w: any, p: DemoProcess = DemoProcess {}",
		"config_gen.workloadEnvMap(appcfg.app_config, w, appcfg.demo_process)",
		"rootEnvMap(c, w, p) if w.root else _rootsProcessEnvMap(c, p, (w.command or []) + (w.args or []), w.config_secrets or [])",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("config_gen.k lacks %q", want)
		}
	}
}

// Beside a binary root config alone (no command), the binary's instance is
// that config, projected by its own lambda, which takes no argv.
func TestPerBinaryRoots_AppConfigBesideABinaryRootConfig(t *testing.T) {
	messages := appConfigBesideCommandMessages()
	messages[1].Command = nil
	src, _ := perBinaryConfigModule(t, messages, "demo", nil)
	for _, want := range []string{
		"workloadEnvMap = lambda c: AppConfig, w: any, p: ServeConfig = ServeConfig {}",
		"appConfigEnvMap(c, _root_secrets) | serveConfigEnvMap(p, [_n for _n in config_secrets if _n not in _root_secrets])",
		"config_gen.workloadEnvMap(appcfg.app_config, w, appcfg.serve_config)",
		`env = ["PORT", "GREETING"]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("config_gen.k lacks %q", want)
		}
	}
}

// Every other shape of a per-binary project's configs still declares
// workloadEnvMap: through appConfigEnvMap with no primary binary, through the
// process selector with no root config, and, with no config a root's process
// loads at all, one that fails the render saying so.
func TestPerBinaryRoots_EveryShapeDeclaresWorkloadEnvMap(t *testing.T) {
	commandOnly := appConfigBesideCommandMessages()[1:]
	otherBinary := appConfigBesideCommandMessages()
	otherBinary[1].Binary = "admin"
	for name, tc := range map[string]struct {
		messages []ConfigMessage
		bin      string
		want     []string
	}{
		"AppConfig and no primary binary": {otherBinary, "", []string{
			"workloadEnvMap = lambda c: AppConfig, w: any ->",
			"rootEnvMap(c, w) if w.root else appConfigEnvMap(c, w.config_secrets or [])",
		}},
		"AppConfig and a config bound to another binary": {otherBinary, "demo", []string{
			"workloadEnvMap = lambda c: AppConfig, w: any ->",
			`env = ["PORT"]`,
		}},
		"only a command config": {commandOnly, "demo", []string{
			"workloadEnvMap = lambda c: DemoProcess, w: any ->",
			`env = ["GREETING"]`,
		}},
		"no config a root's process loads": {otherBinary[1:], "demo", []string{
			"ROOTS are not projected: no config is loaded by a root's process",
			"workloadEnvMap = lambda c: any, w: any ->",
			"root_of = lambda name: str ->",
			"Declare an unannotated AppConfig",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			src, _ := perBinaryConfigModule(t, tc.messages, tc.bin, nil)
			for _, want := range tc.want {
				if !strings.Contains(src, want) {
					t.Errorf("config_gen.k lacks %q:\n%s", want, src)
				}
			}
		})
	}
}

// A root whose reach generate could not read reaches what its process loads,
// rather than leaving ROOTS out.
func TestGraphRootsKCL_FillsAnUnknownReach(t *testing.T) {
	src, _ := perBinaryConfigModule(t, appConfigBesideCommandMessages(), "demo", []GraphRoot{
		{Name: "server", Args: []string{"server"}},
		{Name: "worker", Args: []string{"worker"}},
	})
	for _, want := range []string{
		`env = ["PORT", "GREETING"]`, // server: AppConfig + the `server` command config
		`env = ["PORT"]`,             // worker: AppConfig alone
		"workloadEnvMap = lambda",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("config_gen.k lacks %q", want)
		}
	}
}

// End to end through the native KCL runtime: the scaffolded per-binary
// config.k plus the dev main.k's own call, `workloadEnvMap(appcfg.app_config,
// w)`, renders the scaffolded API workload — the bug's failing render — and
// the three-argument form projects the command config's values and secrets.
func TestPerBinaryRoots_AppConfigBesideACommandConfigRenders(t *testing.T) {
	testkit.Slow(t, "evaluates KCL through the native runtime")
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	messages := appConfigBesideCommandMessages()
	module, kclDir := perBinaryConfigModule(t, messages, "demo", nil)
	if _, err := GenerateConfigKScaffoldPerBinary(configFieldsOf(messages), BinaryConfigFieldsFrom(messages), "demo", kclDir, "dev"); err != nil {
		t.Fatal(err)
	}
	main := `import config_gen
import .config as appcfg

# The scaffolded API workload (APIWorkloadStanza), as the dev main.k's _env
# projects it.
_api = {name = "api", args = ["server"], root = "server", config_secrets = ["DATABASE_URL"]}
_worker = {name = "worker", args = ["worker"]}
dev = config_gen.workloadEnvMap(appcfg.app_config, _api)
_p = appcfg.demo_process | {serve_config = config_gen.ServeConfig {greeting = "hello"}}
full = config_gen.workloadEnvMap(appcfg.app_config, _api | {config_secrets = ["DATABASE_URL", "SERVE_TOKEN"]}, _p)
unbound = config_gen.workloadEnvMap(appcfg.app_config, _worker)
# Workload overrides win over composed secrets, for bound and unbound workloads.
_plain = {SERVE_TOKEN = "local-token", DATABASE_URL = "local-db"}
_ref = {SERVE_TOKEN = {name = "custom-token", key = "token"}, DATABASE_URL = {name = "custom-db", key = "url"}}
_bound = _api | {config_secrets = ["DATABASE_URL", "SERVE_TOKEN"]}
_unbound = {name = "custom", args = ["server"], config_secrets = ["DATABASE_URL", "SERVE_TOKEN"]}
bound_plain = config_gen.workloadEnvMap(appcfg.app_config, _bound | {env = _plain}, _p) | _plain
bound_ref = config_gen.workloadEnvMap(appcfg.app_config, _bound | {env = _ref}, _p) | _ref
unbound_plain = config_gen.workloadEnvMap(appcfg.app_config, _unbound | {env = _plain}, _p) | _plain
unbound_ref = config_gen.workloadEnvMap(appcfg.app_config, _unbound | {env = _ref}, _p) | _ref
`
	for name, body := range map[string]string{
		"kcl.mod":    "[package]\nname = \"roots_proof\"\n",
		"dev/main.k": main,
	} {
		p := filepath.Join(kclDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(kclDir, filepath.Join(kclDir, "dev"), nil)
	if err != nil {
		t.Fatalf("render: %v\n--- config_gen.k ---\n%s", err, module)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	keys := func(m map[string]any) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	if k := keys(got["dev"]); k != "DATABASE_URL,GREETING,PORT" {
		t.Errorf("dev env = %s, want DATABASE_URL,GREETING,PORT", k)
	}
	if got["dev"]["GREETING"] != "hi" {
		t.Errorf("dev GREETING = %v, want the default \"hi\"", got["dev"]["GREETING"])
	}
	if k := keys(got["full"]); k != "DATABASE_URL,GREETING,PORT,SERVE_TOKEN" {
		t.Errorf("full env = %s", k)
	}
	if got["full"]["GREETING"] != "hello" {
		t.Errorf("full GREETING = %v, want the env's \"hello\"", got["full"]["GREETING"])
	}
	if _, ok := got["full"]["SERVE_TOKEN"].(map[string]any); !ok {
		t.Errorf("full SERVE_TOKEN = %#v, want a secret reference", got["full"]["SERVE_TOKEN"])
	}
	if k := keys(got["unbound"]); k != "PORT" {
		t.Errorf("a workload running no command config gets AppConfig's env alone: %s", k)
	}
	for _, name := range []string{"bound_plain", "unbound_plain"} {
		if got[name]["SERVE_TOKEN"] != "local-token" || got[name]["DATABASE_URL"] != "local-db" {
			t.Errorf("%s did not preserve plain workload overrides: %#v", name, got[name])
		}
	}
	for _, name := range []string{"bound_ref", "unbound_ref"} {
		for key, want := range map[string]string{"SERVE_TOKEN": "custom-token", "DATABASE_URL": "custom-db"} {
			ref, ok := got[name][key].(map[string]any)
			if !ok || ref["name"] != want {
				t.Errorf("%s %s = %#v, want workload secret %s", name, key, got[name][key], want)
			}
		}
	}
}

// With no config a root's process loads, an env that calls workloadEnvMap
// fails its render saying why, not on a missing symbol.
func TestPerBinaryRoots_UnavailableFailsTheRenderNamingTheFix(t *testing.T) {
	testkit.Slow(t, "evaluates KCL through the native runtime")
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	messages := appConfigBesideCommandMessages()[1:]
	messages[0].Binary = "admin"
	_, kclDir := perBinaryConfigModule(t, messages, "demo", nil)
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"roots_proof\"\n",
		"main.k":  "import config_gen\n\nenv = config_gen.workloadEnvMap({}, {name = \"api\"})\n",
	} {
		if err := os.WriteFile(filepath.Join(kclDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(kclDir, kclDir, nil)
	if err == nil {
		t.Fatalf("render must fail: %s", out)
	}
	for _, want := range []string{"workload 'api'", "no config is loaded by a root's process", "Declare an unannotated AppConfig"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("render error lacks %q: %v", want, err)
		}
	}
}
