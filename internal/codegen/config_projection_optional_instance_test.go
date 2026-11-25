package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A config whose schema has no required member is optional on the process
// bundle (obs 08a23d1c). Adding such a config to a binary — reliant's
// AuthConfig for its `auth` commands — must not fail every existing env's
// config.k, which predates the message and cannot set it: an unset instance
// projects as the default `<Msg> {}`. A config with a required member keeps
// the assert naming the instance to set.
func optionalInstanceMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{
			{Name: "log_level", GoName: "LogLevel", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_LOG_LEVEL", DefaultValue: "info"},
		}},
		// Required member, no default: the bundle must carry an instance.
		{Name: "CLIConfig", Binary: "reliant", Subtrees: [][]string{{"daemon"}, {"open"}}, Fields: []ConfigField{
			{Name: "server_url", GoName: "ServerUrl", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_SERVER_URL", Required: true},
		}},
		// Every member defaulted (a zero, a default_value, a secret's default
		// backend): `AuthConfig {}` is a valid instance.
		{Name: "AuthConfig", Binary: "reliant", Subtrees: [][]string{{"auth"}}, Fields: []ConfigField{
			{Name: "issuer", GoName: "Issuer", GoType: "string", ProtoType: "string", EnvVar: "AUTH_ISSUER"},
			{Name: "timeout_s", GoName: "TimeoutS", GoType: "int32", ProtoType: "int32",
				EnvVar: "AUTH_TIMEOUT_S", DefaultValue: "30"},
			{Name: "client_secret", GoName: "ClientSecret", GoType: "string", ProtoType: "string",
				EnvVar: "AUTH_CLIENT_SECRET", Sensitive: true, Required: true},
		}},
		{Name: "DaemonConfig", Binary: "reliant", Command: []string{"daemon", "start"}, Fields: []ConfigField{
			{Name: "listen_port", GoName: "ListenPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "DAEMON_LISTEN_PORT", DefaultValue: "9190"},
		}},
		{Name: "ApiServerConfig", Binary: "reliant", Command: []string{"server", "api"}, Fields: []ConfigField{
			{Name: "grpc_port", GoName: "GrpcPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "GRPC_PORT", Required: true},
		}},
	}
}

func optionalInstanceSelector(t *testing.T) ProcessSelector {
	t.Helper()
	sels := ProcessSelectorsFrom(BinaryConfigFieldsFrom(optionalInstanceMessages()))
	if len(sels) != 1 {
		t.Fatalf("want one process selector, got %d", len(sels))
	}
	return sels[0]
}

func TestProcessSelector_ConfigWithNoRequiredMemberNeedsNoInstance(t *testing.T) {
	got := renderProcessSelector(optionalInstanceSelector(t))
	for _, inst := range []string{"reliant_config", "auth_config", "daemon_config"} {
		if strings.Contains(got, "set `"+inst+"`") {
			t.Errorf("%s has no required member, yet the selector still asserts it is set:\n%s", inst, got)
		}
	}
	for _, want := range []string{
		"p.auth_config if p.auth_config not in [None, Undefined] else AuthConfig {}",
		"p.daemon_config if p.daemon_config not in [None, Undefined] else DaemonConfig {}",
		"p.reliant_config if p.reliant_config not in [None, Undefined] else ReliantConfig {}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("selector lacks the default-instance fallback %q:\n%s", want, got)
		}
	}
}

func TestProcessSelector_ConfigWithARequiredMemberStillAsserts(t *testing.T) {
	got := renderProcessSelector(optionalInstanceSelector(t))
	for _, want := range []string{
		"assert not _in_cli_config or p.cli_config not in [None, Undefined]",
		`assert _cmd != "api_server_config" or p.api_server_config not in [None, Undefined]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("selector lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CLIConfig {}") || strings.Contains(got, "ApiServerConfig {}") {
		t.Errorf("a config with a required member must never default its instance:\n%s", got)
	}
}

// The selector agrees with the schema the same run emits: it asserts a
// config's instance is set exactly when that schema has a member with no
// `= <default>`.
func TestProcessSelector_AssertsExactlyTheConfigsWhoseSchemaHasARequiredMember(t *testing.T) {
	selector := renderProcessSelector(optionalInstanceSelector(t))
	for _, bc := range BinaryConfigFieldsFrom(optionalInstanceMessages()) {
		body, _ := renderConfigSchemaFields(bc.Fields, "reliant")
		undefaulted := false
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
				undefaulted = true
			}
		}
		if asserted := strings.Contains(selector, "set `"+InstanceName(bc.MessageName)+"`"); asserted != undefaulted {
			t.Errorf("%s: selector asserts its instance = %v, but its schema has an undefaulted member = %v:\n%s",
				bc.MessageName, asserted, undefaulted, body)
		}
	}
}

// The deploy half, through the native KCL runtime: an env bundle written
// before AuthConfig and DaemonConfig existed still renders, and an unset
// optional instance projects exactly what `<Msg> {}` would.
func TestPerBinaryKCL_UnsetOptionalConfigRendersAsItsDefault(t *testing.T) {
	testkit.Slow(t, "evaluates KCL through the native runtime")
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	mod, err := GenerateConfigKCLPerBinary(nil, BinaryConfigFieldsFrom(optionalInstanceMessages()), "reliant")
	if err != nil {
		t.Fatalf("GenerateConfigKCLPerBinary: %v", err)
	}
	render := func(t *testing.T, mainK string) ([]byte, error) {
		t.Helper()
		dir := t.TempDir()
		for name, body := range map[string]string{
			"kcl.mod":                 "[package]\nname = \"optional_instance_proof\"\n",
			ConfigSchemaModule + ".k": mod,
			"main.k":                  mainK,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return kclrender.Run(dir, dir, nil)
	}

	t.Run("an existing bundle renders", func(t *testing.T) {
		out, err := render(t, `import config_gen

# An env's bundle from before AuthConfig and DaemonConfig were declared.
_p = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {log_level = "debug"}
    cli_config = config_gen.CLIConfig {server_url = "https://cp"}
    api_server_config = config_gen.ApiServerConfig {grpc_port = 9090}
}
_env = lambda argv: [str], secrets: [str] -> {str:} {
    config_gen.reliantProcessEnvMap(_p, argv, secrets)
}
login = _env(["reliant", "auth", "login"], ["AUTH_CLIENT_SECRET"])
start = _env(["reliant", "daemon", "start"], [])
api = _env(["reliant", "server", "api"], [])
`)
		if err != nil {
			t.Fatalf("render: %v\n--- module ---\n%s", err, mod)
		}
		var got struct {
			Login map[string]any `json:"login"`
			Start map[string]any `json:"start"`
			API   map[string]any `json:"api"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		login, start, api := got.Login, got.Start, got.API
		if login["AUTH_TIMEOUT_S"] != "30" || login["AUTH_ISSUER"] != "" || login["RELIANT_LOG_LEVEL"] != "debug" {
			t.Errorf("auth login env = %v, want AuthConfig {}'s values beside the root config", login)
		}
		if ref, ok := login["AUTH_CLIENT_SECRET"].(map[string]any); !ok || ref["key"] != "auth_client_secret" {
			t.Errorf("AUTH_CLIENT_SECRET = %#v, want the default backend's SecretRef", login["AUTH_CLIENT_SECRET"])
		}
		if start["DAEMON_LISTEN_PORT"] != "9190" || start["RELIANT_SERVER_URL"] != "https://cp" {
			t.Errorf("daemon start env = %v, want DaemonConfig {}'s port and the CLI config", start)
		}
		for k := range api {
			if strings.HasPrefix(k, "AUTH_") || strings.HasPrefix(k, "DAEMON_") {
				t.Errorf("server api env carries %s, which no config its process loads declares", k)
			}
		}
	})

	t.Run("a required config still has to be set", func(t *testing.T) {
		_, err := render(t, `import config_gen

_p = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}
status = config_gen.reliantProcessEnvMap(_p, ["reliant", "daemon", "status"], [])
`)
		if err == nil || !strings.Contains(err.Error(), "set `cli_config`") {
			t.Fatalf("render = %v, want the assert naming cli_config", err)
		}
	})
}
