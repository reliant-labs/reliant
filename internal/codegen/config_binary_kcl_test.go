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

// perBinaryKCL renders the per-binary config module for the two-binary
// fixture: a shared BaseConfig composed by admin (which owns a SENSITIVE
// key) and gateway (which owns a plain int).
func perBinaryKCL(t *testing.T) string {
	t.Helper()
	out, err := GenerateConfigKCLPerBinary(nil, BinaryConfigFieldsFrom(perBinaryMessages()), "myproj")
	if err != nil {
		t.Fatalf("GenerateConfigKCLPerBinary: %v", err)
	}
	return out
}

// One schema + one projection lambda PER BINARY, each carrying only that
// binary's fields.
func TestPerBinaryKCL_EmitsOneSchemaAndLambdaPerBinary(t *testing.T) {
	got := perBinaryKCL(t)

	for _, want := range []string{
		"schema ConfigSecretRef:", // declared once for the module
		"schema AdminConfig:",
		"schema GatewayConfig:",
		"adminConfigEnvMap = lambda c: AdminConfig, config_secrets: [str] -> {str: str | forge.SecretRef} {",
		"gatewayConfigEnvMap = lambda c: GatewayConfig, config_secrets: [str] -> {str: str | forge.SecretRef} {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("per-binary config module missing %q\n%s", want, got)
		}
	}

	// ConfigSecretRef is declared exactly once — a second declaration in the
	// same KCL module would be a redefinition.
	if n := strings.Count(got, "schema ConfigSecretRef:"); n != 1 {
		t.Errorf("ConfigSecretRef declared %d times, want exactly 1", n)
	}
}

// The security property, at the level of the emitted projection: admin's
// secret appears ONLY in admin's lambda, so only admin's workload can carry
// it into a manifest.
func TestPerBinaryKCL_SecretIsScopedToItsBinary(t *testing.T) {
	got := perBinaryKCL(t)

	adminBlock := got[strings.Index(got, "adminConfigEnvMap"):]
	if end := strings.Index(adminBlock, "schema GatewayConfig"); end > 0 {
		adminBlock = adminBlock[:end]
	}
	gatewayBlock := got[strings.Index(got, "gatewayConfigEnvMap"):]

	if !strings.Contains(adminBlock, `"ADMIN_API_KEY" = forge.SecretRef {`) {
		t.Error("admin's projection should carry ADMIN_API_KEY as a secret reference")
	}
	if strings.Contains(gatewayBlock, "ADMIN_API_KEY") {
		t.Error("gateway's projection must NOT mention admin's secret")
	}
	if strings.Contains(adminBlock, "UPSTREAM_TIMEOUT_MS") {
		t.Error("admin's projection must NOT mention gateway's field")
	}

	// The shared base is present in BOTH — a shared DEFINITION, resolved
	// per-process.
	for _, block := range []string{adminBlock, gatewayBlock} {
		if !strings.Contains(block, `"LOG_LEVEL"`) {
			t.Error("both binaries should project the shared LOG_LEVEL")
		}
	}
}

// The hermetic end-to-end proof: evaluate the generated module with the real
// kcl binary and assert on the projected env maps. This is what a workload's
// Deployment env is built from, so it is the honest statement of "each
// binary carries only its own env vars".
func TestPerBinaryKCL_EndToEndDisjointEnvMaps(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping per-binary env-map e2e proof")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("kcl.mod", "[package]\nname = \"perbin_proof\"\n")
	write(ConfigSchemaModule+".k", perBinaryKCL(t))
	// A minimal local `forge` module supplying just SecretRef. The real
	// forge module is deliberately NOT used here: this test is about the
	// CONFIG projection, and the deploy vocabulary around it is being
	// reshaped separately.
	write("forge/kcl.mod", "[package]\nname = \"forge\"\n")
	write("forge/core.k", "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n")

	write("main.k", `import `+ConfigSchemaModule+` as config_gen

# log_level lives in the composed BaseConfig block, so it is authored by its
# path — the block is a nested schema, not flattened into AdminConfig.
_admin = config_gen.AdminConfig {base.log_level = "debug"}
_gateway = config_gen.GatewayConfig {upstream_timeout_ms = 1500}

# The two mechanisms COMPOSE, and this is where that shows. Per-binary
# configs make the FIELD SETS disjoint (gateway's schema has no
# admin_api_key at all); config_secrets then gates which of a binary's own
# sensitive fields reach a given workload. Admin declares its credential,
# so it gets it.
_admin_env = config_gen.adminConfigEnvMap(_admin, ["ADMIN_API_KEY"])
_gateway_env = config_gen.gatewayConfigEnvMap(_gateway, [])

# Each binary carries its OWN leaf ...
assert_admin_has_own = "ADMIN_API_KEY" in _admin_env
assert_gateway_has_own = "UPSTREAM_TIMEOUT_MS" in _gateway_env
# ... and NOT its sibling's. This is the disjointness claim: admin's
# credential is ABSENT from gateway's env, not merely unread.
assert_gateway_lacks_admin_secret = "ADMIN_API_KEY" not in _gateway_env
assert_admin_lacks_gateway_field = "UPSTREAM_TIMEOUT_MS" not in _admin_env

# The sensitive field routes through the secret channel, never inline.
# Compared field-by-field: a typed schema instance does not compare equal to
# a bare dict literal in KCL, so == against {name = ..., key = ...} would be
# false even when the value is right.
assert_secret_name = _admin_env["ADMIN_API_KEY"].name == "myproj-secrets"
assert_secret_key = _admin_env["ADMIN_API_KEY"].key == "admin_api_key"
assert_secret_not_inline = typeof(_admin_env["ADMIN_API_KEY"]) != "str"

# The SHARED base is present in both, resolved independently per process:
# admin pinned debug, gateway inherited the default.
assert_shared_admin = str(_admin_env["LOG_LEVEL"]) == "debug"
assert_shared_gateway = str(_gateway_env["LOG_LEVEL"]) == "info"
`)

	// kcltest.Run, not exec+CombinedOutput: under a parallel `go test ./...`
	// kcl prints "waiting for package-cache lock..." on stdout ahead of the
	// JSON, which made this assertion fail with `invalid character 'w'`
	// against a render that was actually correct. See internal/kcltest.
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, out)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal kcl json: %v\n%s", err, out)
	}
	var sawAssert bool
	for k, v := range parsed {
		if !strings.HasPrefix(k, "assert_") {
			continue
		}
		sawAssert = true
		b, ok := v.(bool)
		if !ok {
			t.Errorf("identifier %q not a bool: %v", k, v)
			continue
		}
		if !b {
			t.Errorf("assertion %q is false", k)
		}
	}
	if !sawAssert {
		t.Fatalf("no assert_* identifiers found in kcl output:\n%s", out)
	}
}

// Authoring a field on the WRONG binary's config must be a KCL type error,
// not a silently ignored line — the schema split is enforced by the type
// system, not by convention.
func TestPerBinaryKCL_CrossBinaryFieldIsATypeError(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping cross-binary type-error proof")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kcl.mod", "[package]\nname = \"perbin_typeerr\"\n")
	write(ConfigSchemaModule+".k", perBinaryKCL(t))
	write("forge/kcl.mod", "[package]\nname = \"forge\"\n")
	write("forge/core.k", "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n")
	write("main.k", "import "+ConfigSchemaModule+" as config_gen\n\nbad = config_gen.GatewayConfig {admin_api_key = \"leak\"}\n")

	cmd := exec.CommandContext(t.Context(), "kcl", "run", ".", "--format", "json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a KCL type error authoring admin's field on GatewayConfig, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "admin_api_key") {
		t.Errorf("error should name the offending field:\n%s", out)
	}
}

// commandScopedMessages is the one-binary-several-processes shape: the
// `reliant` binary's root config (a shared LogConfig block plus the server
// URL) and one config per server role, each owning a credential.
func commandScopedMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "LogConfig", Fields: []ConfigField{
			{Name: "level", GoName: "Level", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_LOG_LEVEL", Flag: "log-level", DefaultValue: "info"},
		}},
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{
			{Name: "log", GoName: "Log", ProtoType: "message", MessageType: "LogConfig"},
			{Name: "server_url", GoName: "ServerUrl", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_SERVER_URL", Flag: "server-url", DefaultValue: "https://app"},
		}},
		{Name: "ApiServerConfig", Binary: "reliant", Command: []string{"server", "api"}, Fields: []ConfigField{
			{Name: "grpc_port", GoName: "GrpcPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "GRPC_PORT", Flag: "grpc-port", DefaultValue: "9090"},
			{Name: "auth_api_key", GoName: "AuthApiKey", GoType: "string", ProtoType: "string",
				EnvVar: "AUTH_API_KEY", Sensitive: true},
		}},
		{Name: "WorkerConfig", Binary: "reliant", Command: []string{"server", "worker"}, Fields: []ConfigField{
			{Name: "pprof_port", GoName: "PprofPort", GoType: "int32", ProtoType: "int32",
				EnvVar: "PPROF_PORT", Flag: "pprof-port", DefaultValue: "6060"},
			{Name: "vault_key", GoName: "VaultKey", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_VAULT_KEY", Sensitive: true},
		}},
		// A config for the intermediate `server` command (the monolith):
		// matched only when no longer command is.
		{Name: "MonolithConfig", Binary: "reliant", Command: []string{"server"}, Fields: []ConfigField{
			{Name: "roles", GoName: "Roles", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_ROLES", DefaultValue: "api,worker"},
		}},
	}
}

func commandScopedKCL(t *testing.T) string {
	t.Helper()
	out, err := GenerateConfigKCLPerBinary(nil, BinaryConfigFieldsFrom(commandScopedMessages()), "reliant")
	if err != nil {
		t.Fatalf("GenerateConfigKCLPerBinary: %v", err)
	}
	return out
}

// Each config still projects alone; the binary additionally gets ONE process
// bundle + selector, with its commands ordered longest first.
func TestPerBinaryKCL_CommandScopedConfigsGetAProcessSelector(t *testing.T) {
	got := commandScopedKCL(t)
	for _, want := range []string{
		"schema ReliantConfig:",
		"schema ApiServerConfig:",
		"apiServerConfigEnvMap = lambda c: ApiServerConfig, config_secrets: [str]",
		"schema ReliantProcess:",
		"    reliant_config?: ReliantConfig",
		"    # `reliant server api`\n    api_server_config?: ApiServerConfig",
		"reliantProcessEnvMap = lambda p: ReliantProcess, argv: [str], config_secrets: [str] -> {str: str | forge.SecretRef} {",
		`_cmd = "api_server_config" if _path[:2] == ["server", "api"] else "worker_config" if _path[:2] == ["server", "worker"] else "monolith_config" if _path[:1] == ["server"] else ""`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config module missing %q\n%s", want, got)
		}
	}
	if n := strings.Count(got, "schema ReliantProcess:"); n != 1 {
		t.Errorf("ReliantProcess declared %d times, want once per binary", n)
	}
	// A binary with only a root config needs no selector.
	if strings.Contains(perBinaryKCL(t), "Process:") {
		t.Error("binaries without command configs must not get a process selector")
	}
}

// The deploy half of command scoping, evaluated with the real kcl binary:
// which workload gets which env is decided by the workload's own argv, in
// every shape control-plane writes one, and a credential reaches only the
// process whose config declares it.
func TestPerBinaryKCL_ProcessSelectorRoutesEnvByArgv(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping process-selector proof")
	}
	if testing.Short() {
		t.Skip("-short: evaluates the generated module with the kcl binary; runs in task test")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kcl.mod", "[package]\nname = \"process_proof\"\n")
	write(ConfigSchemaModule+".k", commandScopedKCL(t))
	write("forge/kcl.mod", "[package]\nname = \"forge\"\n")
	write("forge/core.k", "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n")
	write("main.k", `import `+ConfigSchemaModule+` as config_gen

_p = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {log.level = "debug"}
    api_server_config = config_gen.ApiServerConfig {grpc_port = 9191}
    worker_config = config_gen.WorkerConfig {}
    monolith_config = config_gen.MonolithConfig {}
}
_secrets = ["AUTH_API_KEY", "RELIANT_VAULT_KEY"]
_env = lambda argv: [str] -> {str:} {
    config_gen.reliantProcessEnvMap(_p, argv, [_n for _n in _secrets if _n in config_gen.API_SERVER_CONFIG_SENSITIVE_ENV + config_gen.WORKER_CONFIG_SENSITIVE_ENV + config_gen.RELIANT_CONFIG_SENSITIVE_ENV and _n in _want(argv)])
}
# Each workload declares only the credentials its process reads.
_want = lambda argv: [str] -> [str] {
    ["AUTH_API_KEY"] if "api" in argv else ["RELIANT_VAULT_KEY"] if "worker" in argv else []
}

# The three argv shapes a workload binds reliant with today:
_api_cluster = _env(["reliant", "server", "api"])
_api_host = _env(["go", "run", "./cmd/reliant", "server", "api", "--grpc-port", "1"])
_api_image = _env(["server", "api"])
_worker = _env(["/app/reliant", "server", "worker"])
_monolith = _env(["reliant", "server"])
_daemon = _env(["reliant", "daemon", "start"])

# The api process: root + api config, its credential, nothing of the worker's.
assert_api_root = _api_cluster["RELIANT_LOG_LEVEL"] == "debug" and _api_cluster["RELIANT_SERVER_URL"] == "https://app"
assert_api_own = _api_cluster["GRPC_PORT"] == "9191"
assert_api_secret = _api_cluster["AUTH_API_KEY"].key == "auth_api_key"
assert_api_lacks_worker = "PPROF_PORT" not in _api_cluster and "RELIANT_VAULT_KEY" not in _api_cluster
assert_api_lacks_monolith = "RELIANT_ROLES" not in _api_cluster
assert_api_host_same = _api_host["GRPC_PORT"] == "9191" and "PPROF_PORT" not in _api_host
assert_api_image_same = _api_image["GRPC_PORT"] == "9191" and "PPROF_PORT" not in _api_image
# The worker process: root + worker config, and NOT the api's credential.
assert_worker_own = _worker["PPROF_PORT"] == "6060" and _worker["RELIANT_VAULT_KEY"].key == "reliant_vault_key"
assert_worker_lacks_api = "GRPC_PORT" not in _worker and "AUTH_API_KEY" not in _worker
assert_worker_root = _worker["RELIANT_SERVER_URL"] == "https://app"
# The longest declared prefix wins: bare `+"`server`"+` is the monolith.
assert_monolith = _monolith["RELIANT_ROLES"] == "api,worker" and "GRPC_PORT" not in _monolith
# A subcommand with no config of its own gets the root config alone.
assert_daemon_root_only = _daemon["RELIANT_LOG_LEVEL"] == "debug" and "GRPC_PORT" not in _daemon and "RELIANT_ROLES" not in _daemon
`)
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, out)
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
	if asserts == 0 {
		t.Fatalf("no assert_* identifiers found in kcl output:\n%s", out)
	}
}

// The selector's refusals: a credential no config of THIS process declares,
// and a process whose command config was not supplied.
func TestPerBinaryKCL_ProcessSelectorRefusals(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping process-selector refusals")
	}
	if testing.Short() {
		t.Skip("-short: evaluates the generated module with the kcl binary; runs in task test")
	}
	cases := []struct {
		name, main, want string
	}{
		{
			"the worker's credential on the api workload",
			`_x = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {reliant_config = config_gen.ReliantConfig {}, api_server_config = config_gen.ApiServerConfig {}}, ["reliant", "server", "api"], ["RELIANT_VAULT_KEY"])`,
			"config_secrets names [RELIANT_VAULT_KEY], which is not a `sensitive` field of the config(s) a `reliant` process running [server, api] loads. Sensitive fields: [AUTH_API_KEY]",
		},
		{
			"the api process with no api instance",
			`_x = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {reliant_config = config_gen.ReliantConfig {}}, ["reliant", "server", "api"], [])`,
			"this workload runs `reliant server api`, which loads ApiServerConfig",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"kcl.mod":                 "[package]\nname = \"process_refusal\"\n",
				ConfigSchemaModule + ".k": commandScopedKCL(t),
				"forge/kcl.mod":           "[package]\nname = \"forge\"\n",
				"forge/core.k":            "schema SecretRef:\n    name: str\n    key: str\n    optional?: bool\n    store_key?: str\n",
				"main.k":                  "import " + ConfigSchemaModule + " as config_gen\n\n" + c.main + "\n",
			} {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "kcl", "run", ".", "--format", "json")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("want a KCL assertion failure, got success:\n%s", out)
			}
			if !strings.Contains(string(out), c.want) {
				t.Errorf("failure should contain %q:\n%s", c.want, out)
			}
		})
	}
}
