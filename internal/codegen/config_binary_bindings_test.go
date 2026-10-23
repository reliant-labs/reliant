package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// boundMessage is a one-field config message bound to binary, declared at
// config/v1/config.proto:line.
func boundMessage(name, binary string, line int) ConfigMessage {
	return ConfigMessage{
		Name:      name,
		Binary:    binary,
		ProtoFile: "config/v1/config.proto",
		Line:      line,
		Fields: []ConfigField{{
			Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32",
			EnvVar: strings.ToUpper(name) + "_PORT",
		}},
	}
}

// The repro from the forge-migration research: a subcommand LINE written
// where a binary name belongs, and a binary that does not exist, both used
// to generate green. Each is refused, named with its file:line, and the
// subcommand case says what `binary` actually is.
func TestValidateBinaryConfigBindings_RefusesUnknownBinaries(t *testing.T) {
	msgs := []ConfigMessage{
		boundMessage("ServerApiConfig", "x server api", 42),
		boundMessage("GhostConfig", "nonexistent", 57),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"x"})
	if err == nil {
		t.Fatal("bindings to binaries with no cmd/<name> generated cleanly; want a refusal")
	}
	got := err.Error()
	for _, want := range []string{
		`proto/config/v1/config.proto:42: message ServerApiConfig: (forge.v1.binary_config) = {binary: "x server api"}`,
		"there is no cmd/x server api/ directory",
		"not a command line",
		// The leading word IS a binary: the refusal writes the fix.
		`write {binary: "x", command: ["server", "api"]}`,
		`proto/config/v1/config.proto:57: message GhostConfig: (forge.v1.binary_config) = {binary: "nonexistent"}`,
		"there is no cmd/nonexistent/ directory",
		"Binaries in this project (cmd/<name>): x",
		"forge scaffold binary <name>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q\n---\n%s", want, got)
		}
	}
}

// A binary's NAME and its cmd/ package directory can differ by -/_
// (`forge scaffold binary workspace-proxy` writes cmd/workspace_proxy); the
// refusal points at the directory that does exist.
func TestValidateBinaryConfigBindings_SuggestsTheDirectorySpelling(t *testing.T) {
	err := ValidateBinaryConfigBindings(
		[]ConfigMessage{boundMessage("ProxyConfig", "workspace-proxy", 3)},
		[]string{"server", "workspace_proxy"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(err.Error(), `Did you mean "workspace_proxy"?`) {
		t.Errorf("refusal should suggest the cmd/ spelling:\n%s", err)
	}
}

func TestValidateBinaryConfigBindings_AcceptsRealBinaries(t *testing.T) {
	msgs := []ConfigMessage{
		{Name: "BaseConfig"}, // a shared block: bound to nothing
		{Name: "AppConfig"},  // the project-global root: bound to nothing
		boundMessage("AdminConfig", "admin", 10),
		boundMessage("GatewayConfig", "gateway", 20),
	}
	if err := ValidateBinaryConfigBindings(msgs, []string{"admin", "gateway", "server"}); err != nil {
		t.Fatalf("valid bindings refused: %v", err)
	}
}

// "forge could not tell" must never render as "it does not exist": an
// unreadable cmd/ (nil binaries) skips the existence check.
func TestValidateBinaryConfigBindings_UnknownBinarySetSkipsExistence(t *testing.T) {
	if err := ValidateBinaryConfigBindings([]ConfigMessage{boundMessage("AdminConfig", "admin", 1)}, nil); err != nil {
		t.Fatalf("nil binary set must skip the existence check, got: %v", err)
	}
}

// A project with no cmd/ at all has no binaries, so any binding is refused.
func TestValidateBinaryConfigBindings_NoBinariesRefusesEveryBinding(t *testing.T) {
	err := ValidateBinaryConfigBindings([]ConfigMessage{boundMessage("AdminConfig", "admin", 1)}, []string{})
	if err == nil || !strings.Contains(err.Error(), "This project has no cmd/ binaries.") {
		t.Fatalf("want a refusal naming the empty binary set, got: %v", err)
	}
}

// Two messages bound to one binary used to project the FIRST message's
// fields for both (the KCL emitter looked the binary up, not the message).
// A binary has one config surface, so the second binding is refused.
func TestValidateBinaryConfigBindings_RefusesTwoConfigsForOneBinary(t *testing.T) {
	msgs := []ConfigMessage{
		boundMessage("AdminConfig", "admin", 10),
		boundMessage("AdminExtraConfig", "admin", 30),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"admin"})
	if err == nil {
		t.Fatal("two configs for one binary generated cleanly; want a refusal")
	}
	got := err.Error()
	if !strings.Contains(got, "proto/config/v1/config.proto:30: message AdminExtraConfig") ||
		!strings.Contains(got, "already bound by AdminConfig") {
		t.Errorf("refusal should name the second binding and the first:\n%s", got)
	}
	if strings.Contains(got, "config.proto:10:") {
		t.Errorf("the FIRST binding is not the offender and should not be listed:\n%s", got)
	}
}

// commandMessage is a config message bound to one subcommand of binary.
func commandMessage(name, binary string, command []string, line int, fields ...ConfigField) ConfigMessage {
	m := boundMessage(name, binary, line)
	m.Command = command
	if len(fields) > 0 {
		m.Fields = fields
	}
	return m
}

func envField(name, envVar, flag string) ConfigField {
	return ConfigField{Name: name, GoName: name, GoType: "string", ProtoType: "string", EnvVar: envVar, Flag: flag}
}

// The reliant shape: one binary, a binary-wide root config, and one config
// per server role — keyed on (binary, command), so the same binary binds
// several messages without any of them being a duplicate.
func TestValidateBinaryConfigBindings_OneBinarySeveralProcesses(t *testing.T) {
	msgs := []ConfigMessage{
		commandMessage("ReliantConfig", "reliant", nil, 10, envField("log_level", "RELIANT_LOG_LEVEL", "log-level")),
		commandMessage("ApiServerConfig", "reliant", []string{"server", "api"}, 20, envField("grpc_port", "GRPC_PORT", "grpc-port")),
		commandMessage("WorkerConfig", "reliant", []string{"server", "worker"}, 30, envField("pprof_port", "PPROF_PORT", "pprof-port")),
		// The same flag on two DIFFERENT commands is fine: each is a local
		// flag on its own subcommand, read by its own process.
		commandMessage("GatewayConfig", "reliant", []string{"server", "gateway"}, 40, envField("pprof_port", "PPROF_PORT", "pprof-port")),
	}
	if err := ValidateBinaryConfigBindings(msgs, []string{"reliant"}); err != nil {
		t.Fatalf("one binary with a root config and per-command configs refused: %v", err)
	}
}

func TestValidateBinaryConfigBindings_RefusesMalformedCommands(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		want    string
	}{
		{"a whole line in one element", []string{"server api"}, `element "server api" contains whitespace. Each element is ONE cobra command name: ["server", "api"]`},
		{"an empty element", []string{"server", ""}, "`command` has an empty element"},
		{"a flag", []string{"server", "--port"}, `element "--port" is a flag`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateBinaryConfigBindings([]ConfigMessage{commandMessage("ApiConfig", "reliant", c.command, 5)}, []string{"reliant"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want refusal containing %q, got: %v", c.want, err)
			}
		})
	}
}

func TestValidateBinaryConfigBindings_RefusesCommandWithoutBinary(t *testing.T) {
	m := commandMessage("ApiConfig", "", []string{"server", "api"}, 5)
	err := ValidateBinaryConfigBindings([]ConfigMessage{m}, []string{"reliant"})
	if err == nil || !strings.Contains(err.Error(), "no `binary` is set") {
		t.Fatalf("want refusal of a command with no binary, got: %v", err)
	}
}

func TestValidateBinaryConfigBindings_RefusesTwoConfigsForOneCommand(t *testing.T) {
	msgs := []ConfigMessage{
		commandMessage("ApiServerConfig", "reliant", []string{"server", "api"}, 20),
		commandMessage("ApiExtrasConfig", "reliant", []string{"server", "api"}, 50),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"reliant"})
	if err == nil {
		t.Fatal("two configs for one command generated cleanly; want a refusal")
	}
	if got := err.Error(); !strings.Contains(got, "config.proto:50: message ApiExtrasConfig") ||
		!strings.Contains(got, "`reliant server api` is already bound by ApiServerConfig") {
		t.Errorf("refusal should name the second binding and the process:\n%s", got)
	}
}

// The `server api` process loads the root config AND its command config, so
// a leaf both declare — directly or through a composed block — is read
// twice. Refused, naming both declarations.
func TestValidateBinaryConfigBindings_RefusesARootAndCommandCollision(t *testing.T) {
	logBlock := ConfigMessage{Name: "LogConfig", Fields: []ConfigField{envField("level", "RELIANT_LOG_LEVEL", "log-level")}}
	msgs := []ConfigMessage{
		logBlock,
		commandMessage("ReliantConfig", "reliant", nil, 10,
			ConfigField{Name: "log", GoName: "Log", ProtoType: "message", MessageType: "LogConfig"}),
		commandMessage("ApiServerConfig", "reliant", []string{"server", "api"}, 20,
			// Composes the same block the root does.
			ConfigField{Name: "log", GoName: "Log", ProtoType: "message", MessageType: "LogConfig"}),
		commandMessage("WorkerConfig", "reliant", []string{"server", "worker"}, 30,
			// A different env var, same flag name as the root's.
			envField("verbosity", "WORKER_VERBOSITY", "log-level")),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"reliant"})
	if err == nil {
		t.Fatal("root and command configs declaring the same leaf generated cleanly; want a refusal")
	}
	got := err.Error()
	for _, want := range []string{
		"config.proto:20: message ApiServerConfig",
		"env var RELIANT_LOG_LEVEL (log.level) is also declared by ReliantConfig (log.level)",
		"The `reliant server api` process loads both",
		"config.proto:30: message WorkerConfig",
		"flag --log-level (verbosity) is also declared by ReliantConfig (log.level)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q\n---\n%s", want, got)
		}
	}
	// No unknown-binary footer for a project whose binaries all exist.
	if strings.Contains(got, "Binaries in this project") {
		t.Errorf("collision-only refusal should not carry the unknown-binary runbook:\n%s", got)
	}
}

// The `server api` process reads its root config and its command config from
// ONE config file, flat across both, so the two may not share a top-level
// field name even when the env vars and flags differ: the file key could not
// say which config it sets.
func TestValidateBinaryConfigBindings_RefusesARootAndCommandFileKeyCollision(t *testing.T) {
	msgs := []ConfigMessage{
		commandMessage("ReliantConfig", "reliant", nil, 10, envField("port", "PORT", "port")),
		commandMessage("ApiServerConfig", "reliant", []string{"server", "api"}, 20,
			envField("port", "API_PORT", "api-port")),
		commandMessage("WorkerConfig", "reliant", []string{"server", "worker"}, 30,
			envField("health_port", "HEALTH_PORT", "health-port")),
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"reliant"})
	if err == nil {
		t.Fatal("root and command configs sharing a top-level field name generated cleanly; want a refusal")
	}
	got := err.Error()
	for _, want := range []string{
		"config.proto:20: message ApiServerConfig",
		`config file key "port" is also a top-level field of ReliantConfig`,
		"ONE config file (--config, or RELIANT_CONFIG)",
		"Rename one of the two fields",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "WorkerConfig") {
		t.Errorf("WorkerConfig shares no key with the root config and must not be refused:\n%s", got)
	}
}

func TestConfigMessage_ProcessName(t *testing.T) {
	for _, c := range []struct {
		m    ConfigMessage
		want string
	}{
		{ConfigMessage{}, ""},
		{ConfigMessage{Binary: "reliant"}, "reliant"},
		{ConfigMessage{Binary: "reliant", Command: []string{"server", "api"}}, "reliant server api"},
	} {
		if got := c.m.ProcessName(); got != c.want {
			t.Errorf("ProcessName(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

// The position degrades rather than lying: no file renders no prefix, and
// a file with no line renders the file alone.
func TestConfigMessageWhere_Degrades(t *testing.T) {
	cases := []struct {
		m    ConfigMessage
		want string
	}{
		{ConfigMessage{}, ""},
		{ConfigMessage{ProtoFile: "config/v1/config.proto"}, "proto/config/v1/config.proto"},
		{ConfigMessage{ProtoFile: "config/v1/config.proto", Line: 7}, "proto/config/v1/config.proto:7"},
		{ConfigMessage{ProtoFile: "proto/config/v1/x.proto", Line: 2}, "proto/config/v1/x.proto:2"},
	}
	for _, c := range cases {
		if got := configMessageWhere(c.m); got != c.want {
			t.Errorf("configMessageWhere(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestProjectBinaries(t *testing.T) {
	dir := t.TempDir()
	if got := ProjectBinaries(dir); got == nil || len(got) != 0 {
		t.Fatalf("no cmd/ should be a KNOWN empty set, got %#v", got)
	}
	for _, d := range []string{"cmd/server", "cmd/admin"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file under cmd/ is not a binary.
	if err := os.WriteFile(filepath.Join(dir, "cmd", "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectBinaries(dir), []string{"admin", "server"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectBinaries = %v, want %v", got, want)
	}
}

// `{binary: "x server"}` and `{binary: "x", command: ["server"]}` print
// alike but are different bindings, and a binding refused for its own shape
// names no process: neither is reported again as a "duplicate".
func TestValidateBinaryConfigBindings_RefusedBindingsAreNotDuplicates(t *testing.T) {
	msgs := []ConfigMessage{
		boundMessage("LineConfig", "x server worker", 10),                     // not a binary
		commandMessage("ShapeConfig", "x", []string{"server worker"}, 20),     // not a path
		commandMessage("WorkerConfig", "x", []string{"server", "worker"}, 30), // valid
	}
	err := ValidateBinaryConfigBindings(msgs, []string{"x"})
	if err == nil {
		t.Fatal("want refusals for the two malformed bindings")
	}
	got := err.Error()
	if strings.Contains(got, "already bound") {
		t.Errorf("malformed bindings must not be reported as duplicates:\n%s", got)
	}
	if strings.Contains(got, "message WorkerConfig") {
		t.Errorf("the valid binding must not be refused:\n%s", got)
	}
	if strings.Index(got, "config.proto:10:") > strings.Index(got, "config.proto:20:") {
		t.Errorf("refusals should be listed in declaration order:\n%s", got)
	}
}
