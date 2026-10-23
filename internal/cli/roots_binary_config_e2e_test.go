//go:build e2e

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// rootsBinaryConfigProto is a binary that binds config per command
// ((forge.v1.binary_config), #957): a subtree config every `server` process
// loads, and one command config for each of `server api` and `server worker`.
// Each declares a required field the OTHER root's closure never reads.
const rootsBinaryConfigProto = `syntax = "proto3";

package rbc.config.v1;

import "forge/v1/forge.proto";

option go_package = "github.com/test/rbc/gen/rbc/config/v1;configv1";

// ServerConfig is every ` + "`rbc server ...`" + ` process's config: the
// region and audit sink a node reads, the telemetry settings runRoot reads.
message ServerConfig {
  option (forge.v1.binary_config) = {
    binary: "rbc"
    subtrees: [{path: ["server"]}]
  };

  string region = 1 [(forge.v1.config) = {
    env_var: "REGION"
    flag: "region"
    default_value: "local"
    description: "Region the process runs in."
  }];
  string audit_sink = 2 [(forge.v1.config) = {
    env_var: "AUDIT_SINK"
    flag: "audit-sink"
    required: true
    description: "Where the API writes audit records."
  }];
  // Read by the owned process policy (serve.go), never by a node.
  string otel_service_name = 3 [(forge.v1.config) = {
    env_var: "OTEL_SERVICE_NAME"
    default_value: "rbc"
    description: "Service name the process reports telemetry under."
  }];
  bool prometheus_enabled = 4 [(forge.v1.config) = {
    env_var: "PROMETHEUS_ENABLED"
    description: "Serve Prometheus metrics."
  }];
}

// ApiConfig is the ` + "`rbc server api`" + ` process's config.
message ApiConfig {
  option (forge.v1.binary_config) = {
    binary: "rbc"
    command: ["server", "api"]
  };

  string signing_key = 1 [(forge.v1.config) = {
    env_var: "API_SIGNING_KEY"
    required: true
    sensitive: true
    description: "Key the API signs tokens with."
  }];
  string greeting = 2 [(forge.v1.config) = {
    env_var: "API_GREETING"
    flag: "greeting"
    default_value: "hi"
    description: "The API's greeting."
  }];
  map<string, bool> flags = 3 [(forge.v1.config) = {
    env_prefix: "API_FLAG_"
    env_suffix: "_ENABLED"
    description: "The API's feature flags, API_FLAG_<NAME>_ENABLED."
  }];
}

// WorkerConfig is the ` + "`rbc server worker`" + ` process's config.
message WorkerConfig {
  option (forge.v1.binary_config) = {
    binary: "rbc"
    command: ["server", "worker"]
  };

  string tick_every = 1 [(forge.v1.config) = {
    env_var: "TICK_EVERY"
    flag: "tick-every"
    default_value: "1s"
    description: "Tick cadence."
  }];
}
`

// TestE2EBinaryConfigRootsReachTheirOwnConfig pins root reach for a binary
// that binds config per command: two roots on different command paths
// (`server api`, `server worker`), each loading the binary's root config, the
// subtree config over `server` and its own command config.
//
//   - roots_gen.go loads each root's configs with only the fields its closure
//     reads demanded (config.Load<Msg>Reach);
//   - config_gen.k's ROOTS projects each root's env across every config it
//     loads, so the two workloads' env is disjoint beyond the process fields;
//   - the worker root boots with none of the api's required settings — not
//     its command config's credential, not the shared subtree config's
//     field only the api reads, not the database — and the api refuses to
//     start without them;
//   - what the owned serve.go reads of a loaded config (its telemetry
//     settings, through a helper of its own) is every root's reach: in
//     roots_gen.go's loads, in ROOTS and in Roots(), whose Validate does
//     not call it unreached;
//   - the `server` group command's help is its //forge:group comment.
func TestE2EBinaryConfigRootsReachTheirOwnConfig(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "rbc", "--mod", "github.com/test/rbc", "--service", "item")
	projectDir := filepath.Join(dir, "rbc")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "tick")

	// The root config binds to the binary; the per-command configs beside it.
	configProto := filepath.Join(projectDir, "proto", "rbc", "config", "v1", "config.proto")
	mustReplaceInFile(t, configProto, "message AppConfig {", "message AppConfig {\n  option (forge.v1.binary_config) = {binary: \"rbc\"};\n")
	writeFileE2E(t, filepath.Join(projectDir, "proto", "rbc", "config", "v1", "roles.proto"), rootsBinaryConfigProto)

	// The api's item handler takes its whole command config and the audit
	// sink; the worker's tick reads its cadence and the region.
	itemPath := filepath.Join(projectDir, "internal", "handlers", "item", "service.go")
	mustReplaceInFile(t, itemPath, "\tConfig *config.Config\n", "\tConfig    *config.Config\n\tAPI       *config.ApiConfig\n\tAuditSink string\n")
	tickPath := filepath.Join(projectDir, "internal", "workers", "tick", "worker.go")
	mustReplaceInFile(t, tickPath, "\tConfig *config.Config\n", "\tEvery  string\n\tRegion string\n")
	mustReplaceInFile(t, tickPath, "\n\t\"github.com/test/rbc/pkg/config\"\n", "\n")
	mustReplaceInFile(t, tickPath, `w.deps.Logger.InfoContext(ctx, "worker started", "worker", w.Name())`,
		`w.deps.Logger.InfoContext(ctx, "worker started", "worker", w.Name(), "every", w.deps.Every, "region", w.deps.Region)`)
	tickTest := filepath.Join(projectDir, "internal", "workers", "tick", "worker_test.go")
	if src, err := os.ReadFile(tickTest); err == nil {
		out := strings.ReplaceAll(string(src), "\t\tConfig: &config.Config{},\n", "")
		out = strings.Replace(out, "\t\"github.com/test/rbc/pkg/config\"\n", "", 1)
		writeFileE2E(t, tickTest, out)
	}
	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	mustReplaceInFile(t, composePath, "\treturn tick.New(tick.Deps{\n\t\tLogger: g.Log,\n\t\tConfig: g.Cfg,\n\t}), nil",
		"\treturn tick.New(tick.Deps{\n\t\tLogger: g.Log,\n\t\tEvery:  g.WorkerConfig.GetTickEvery(),\n\t\tRegion: g.ServerConfig.Region,\n\t}), nil")
	mustReplaceInFile(t, composePath, "\treturn item.New(item.Deps{\n\t\tLogger: g.Log,\n\t\tConfig: g.Cfg,\n\t})",
		"\treturn item.New(item.Deps{\n\t\tLogger:    g.Log,\n\t\tConfig:    g.Cfg,\n\t\tAuditSink: g.ServerConfig.GetAuditSink(),\n\t})")

	// The owned process policy reads the subtree config's telemetry
	// settings, through a helper of its own.
	servePath := filepath.Join(projectDir, "cmd", "rbc", "cmd", "serve.go")
	mustReplaceInFile(t, servePath, "\tslog.SetDefault(logger)\n", "\tslog.SetDefault(logger)\n\tlogger.Info(\"telemetry\", telemetryAttrs(root.Configs.ServerConfig)...)\n")
	writeFileE2E(t, servePath, readFileE2E(t, servePath)+`
// telemetryAttrs is what the process reports of its telemetry settings.
func telemetryAttrs(c *config.ServerConfig) []any {
	return []any{"otel_service_name", c.GetOtelServiceName(), "prometheus_enabled", c.GetPrometheusEnabled()}
}
`)

	writeFileE2E(t, filepath.Join(projectDir, "internal", "app", "roots.go"), `package app

import "github.com/test/rbc/internal/workers/tick"

// Run the rbc server roles
//
// Each subcommand runs one role of a split deployment.
//
//forge:group server

// API is what `+"`rbc server api`"+` runs.
//
//forge:root api command=server,api
type API struct {
	Listener *APIListener
}

// Worker is what `+"`rbc server worker`"+` runs: the tick worker alone.
//
//forge:root worker command=server,worker
type Worker struct {
	Tick *tick.Worker
}
`)

	// The envs are yours: bind the API to the api root, and project the env
	// from the binary's process bundle, which the per-binary config.k (re-
	// scaffolded below) declares.
	var configKs []string
	for _, env := range []string{"dev", "staging", "prod"} {
		mainK := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
		mustReplaceInFile(t, mainK, "appcfg.app_config", "appcfg.rbc_process")
		mustReplaceInFile(t, mainK, "    args = [\"server\"]\n    root = \"server\"", "    args = [\"server\", \"api\"]\n    root = \"api\"")
		configK := filepath.Join("deploy", "kcl", env, "config.k")
		if err := os.Remove(filepath.Join(projectDir, configK)); err != nil {
			t.Fatal(err)
		}
		configKs = append(configKs, configK)
	}
	runCmd(t, projectDir, forgeBin, append([]string{"project", "rescaffold"}, configKs...)...)
	runCmd(t, projectDir, forgeBin, "generate")

	// ── Runtime: each root loads its own configs, demanding its reach ────
	gen := strings.Join(strings.Fields(readFileE2E(t, filepath.Join(projectDir, "cmd", "rbc", "cmd", "roots_gen.go"))), " ")
	for _, want := range []string{
		`config.RegisterServerConfigFlags(serverCmd)`,
		`apiConfig, err := config.LoadApiConfigReach(cmd, []string{ "signing_key", "greeting", "flags", })`,
		`serverConfig, err := config.LoadServerConfigReach(cmd, []string{ "audit_sink", "otel_service_name", "prometheus_enabled", })`,
		`serverConfig, err := config.LoadServerConfigReach(cmd, []string{ "region", "otel_service_name", "prometheus_enabled", })`,
		`workerConfig, err := config.LoadWorkerConfigReach(cmd, []string{ "tick_every", })`,
		`g.WorkerConfig = workerConfig`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, gen)
		}
	}
	if !strings.Contains(readFileE2E(t, composePath), "API:       g.ApiConfig,") {
		t.Errorf("item.Deps.API (*config.ApiConfig) resolves by type to the g.ApiConfig input:\n%s", readFileE2E(t, composePath))
	}

	// ── Deploy: each workload's env is what its root reaches ────────────
	probe := `import config_gen
import forge
import forge.workloads as fw
import .dev.config as appcfg

_build = forge.GoBuild {cmd = "./cmd/rbc", output_name = "rbc"}
_api = fw.Workload {name = "api", kind = "service", image = "x.invalid/rbc", build = _build} | config_gen.root_of("api")
_worker = fw.Workload {name = "worker", kind = "worker", image = "x.invalid/rbc", build = _build} | config_gen.root_of("worker")
api_env = sorted([_k for _k in config_gen.workloadEnvMap(appcfg.rbc_process, _api)])
worker_env = sorted([_k for _k in config_gen.workloadEnvMap(appcfg.rbc_process, _worker)])
`
	probePath := filepath.Join(projectDir, "deploy", "kcl", "roots_probe.k")
	writeFileE2E(t, probePath, probe)
	var envs struct {
		API    []string `json:"api_env"`
		Worker []string `json:"worker_env"`
	}
	if err := json.Unmarshal([]byte(runCmdStdout(t, projectDir, forgeBin, "kcl", "eval", probePath, "--format", "json")), &envs); err != nil {
		t.Fatalf("eval probe: %v", err)
	}
	for _, k := range []string{"API_SIGNING_KEY", "API_GREETING", "AUDIT_SINK", "DATABASE_URL"} {
		if !slices.Contains(envs.API, k) || slices.Contains(envs.Worker, k) {
			t.Errorf("%s reaches the api (its closure reads it) and not the worker:\napi:    %v\nworker: %v", k, envs.API, envs.Worker)
		}
	}
	for _, k := range []string{"OTEL_SERVICE_NAME", "PROMETHEUS_ENABLED"} {
		if !slices.Contains(envs.API, k) || !slices.Contains(envs.Worker, k) {
			t.Errorf("%s, which runRoot reads, reaches every root:\napi:    %v\nworker: %v", k, envs.API, envs.Worker)
		}
	}
	for _, k := range []string{"REGION", "TICK_EVERY"} {
		if !slices.Contains(envs.Worker, k) || slices.Contains(envs.API, k) {
			t.Errorf("%s reaches the worker and not the api:\napi:    %v\nworker: %v", k, envs.API, envs.Worker)
		}
	}
	for _, k := range envs.Worker {
		if slices.Contains(envs.API, k) && !slices.Contains([]string{"PORT", "LOG_LEVEL", "LOG_FORMAT", "ENVIRONMENT"}, k) && !strings.HasPrefix(k, "CORS_") &&
			!strings.HasPrefix(k, "TLS_") && !slices.Contains([]string{"PPROF_ADDR", "PRE_STOP_DELAY", "SHUTDOWN_TIMEOUT", "SECURITY_HEADERS_ENABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "READ_MAX_BYTES", "SEND_MAX_BYTES", "LIVENESS_PATHS", "READINESS_PATHS", "OTEL_SERVICE_NAME", "PROMETHEUS_ENABLED"}, k) {
			t.Errorf("both roots reach %s, which is no process field", k)
		}
	}

	// ── pkg/config.Roots: ROOTS as Go, and the boot load over an env ─────
	// The data is ROOTS exactly; ValidateRootEnv accepts a correct env and
	// names a missing required var, a misspelt family entry, a value the
	// process refuses, and a variable the root never reads.
	rootsProbe := filepath.Join(projectDir, "deploy", "kcl", "roots_dump.k")
	writeFileE2E(t, rootsProbe, "import config_gen\n\nroots = config_gen.ROOTS\n")
	writeFileE2E(t, filepath.Join(projectDir, "pkg", "config", "testdata", "roots.json"),
		runCmdStdout(t, projectDir, forgeBin, "kcl", "eval", rootsProbe, "-S", "roots", "--format", "json"))
	writeFileE2E(t, filepath.Join(projectDir, "pkg", "config", "roots_e2e_test.go"), rootsValidateTest)
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "TestRoots", "./pkg/config/")

	// ── Boot ─────────────────────────────────────────────────────────────
	bin := filepath.Join(projectDir, "rbc-bin")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/rbc")

	// The group command's help is its //forge:group comment.
	if help := runCmdStdout(t, projectDir, bin, "server", "--help"); !strings.HasPrefix(help, "Each subcommand runs one role of a split deployment.") {
		t.Errorf("`rbc server --help` is not the //forge:group help:\n%s", help)
	}
	if help := runCmdStdout(t, projectDir, bin, "--help"); !strings.Contains(help, "Run the rbc server roles") {
		t.Errorf("`rbc --help` lists `server` without its declared Short:\n%s", help)
	}

	// The worker starts with every api-only setting unset, required or not.
	none := []string{"DATABASE_URL=", "API_SIGNING_KEY=", "AUDIT_SINK=", "TICK_EVERY=2s", "REGION=eu", "PROMETHEUS_ENABLED=true"}
	workerLog := bootRootArgs(t, bin, projectDir, none, []string{"server", "worker"}, nil)
	if !strings.Contains(workerLog, `"prometheus_enabled":true`) {
		t.Errorf("`server worker`'s runRoot never read PROMETHEUS_ENABLED:\n%s", workerLog)
	}
	if !strings.Contains(workerLog, `"worker":"tick"`) || !strings.Contains(workerLog, `"every":"2s"`) || !strings.Contains(workerLog, `"region":"eu"`) {
		t.Errorf("`server worker` never started the tick worker with its own config:\n%s", workerLog)
	}

	// The api demands what its closure reads: the subtree config's audit
	// sink, then its command config's credential.
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"AUDIT_SINK=", "API_SIGNING_KEY=k"}, "audit_sink"},
		{[]string{"AUDIT_SINK=s3://audit", "API_SIGNING_KEY="}, "signing_key"},
	} {
		cmd := exec.Command(bin, "server", "api")
		cmd.Dir = projectDir
		cmd.Env = append(os.Environ(), append([]string{"DATABASE_URL=postgres://x@127.0.0.1:1/x", "PPROF_ADDR="}, tc.env...)...)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "required config field "+tc.want) {
			t.Errorf("`server api` with %v must refuse to start naming %s: %v\n%s", tc.env, tc.want, err, out)
		}
	}

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer cleanup()
	apiLog := bootRootArgs(t, bin, projectDir, []string{"DATABASE_URL=" + dsn, "API_SIGNING_KEY=k", "AUDIT_SINK=s3://audit"}, []string{"server", "api"}, nil)
	if strings.Contains(apiLog, "worker started") {
		t.Errorf("`server api` started the worker its root does not reach:\n%s", apiLog)
	}
}

// rootsValidateTest is the scaffolded project's own test of pkg/config's
// Roots, the way a deployment repo importing it would test its envs.
const rootsValidateTest = `package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"

	"github.com/test/rbc/pkg/config"
)

func TestRootsAreROOTS(t *testing.T) {
	raw, err := os.ReadFile("testdata/roots.json")
	if err != nil {
		t.Fatal(err)
	}
	var kcl map[string]struct {
		Args            []string ` + "`json:\"args\"`" + `
		Env             []string ` + "`json:\"env\"`" + `
		Secrets         []string ` + "`json:\"secrets\"`" + `
		RequiredSecrets []string ` + "`json:\"required_secrets\"`" + `
	}
	if err := json.Unmarshal(raw, &kcl); err != nil {
		t.Fatal(err)
	}
	if len(kcl) != len(config.Roots()) {
		t.Fatalf("ROOTS has %d roots, config.Roots %d", len(kcl), len(config.Roots()))
	}
	for _, r := range config.Roots() {
		k, ok := kcl[r.Name]
		if !ok {
			t.Fatalf("ROOTS has no root %q", r.Name)
		}
		norm := func(s []string) []string { return append([]string{}, s...) }
		if !slices.Equal(norm(k.Args), r.Args) || !slices.Equal(norm(k.Env), r.Env()) || !slices.Equal(norm(k.Secrets), r.Secrets()) || !slices.Equal(norm(k.RequiredSecrets), r.RequiredSecrets()) {
			t.Errorf("root %q: ROOTS %+v\nconfig.Roots args %v env %v secrets %v required %v", r.Name, k, r.Args, r.Env(), r.Secrets(), r.RequiredSecrets())
		}
	}
}

func TestRootsValidateEnv(t *testing.T) {
	good := map[string]string{
		"DATABASE_URL":         "postgres://x@db/x",
		"API_SIGNING_KEY":      "k",
		"AUDIT_SINK":           "s3://audit",
		"API_FLAG_BETA_ENABLED": "true",
		// Read by runRoot (serve.go), not by a node: still the root's.
		"OTEL_SERVICE_NAME":  "rbc-api",
		"PROMETHEUS_ENABLED": "true",
	}
	if problems := config.ValidateRootEnv("api", good); len(problems) != 0 {
		t.Fatalf("a correct api env has no problems: %v", problems)
	}
	bad := map[string]string{
		"DATABASE_URL":          "postgres://x@db/x",
		"API_SIGNING_KEY":       "k",
		"API_FLAG_BETA_ENABLD":  "true",
		"API_FLAG_DARK_ENABLED": "maybe",
		"TICK_EVERY":            "1s",
	}
	problems := config.ValidateRootEnv("api", bad)
	var all []string
	var unreached []string
	for _, p := range problems {
		all = append(all, p.Error())
		var u *forgeconfig.UnreachedVarError
		if errors.As(p, &u) {
			unreached = append(unreached, u.Name)
		}
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"required config field audit_sink is not set", "API_FLAG_BETA_ENABLD is under the config family prefix API_FLAG_", "invalid value \"maybe\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems lack %q:\n%s", want, joined)
		}
	}
	if !slices.Equal(unreached, []string{"TICK_EVERY"}) {
		t.Errorf("unreached = %v, want [TICK_EVERY]:\n%s", unreached, joined)
	}
	if problems := config.ValidateRootEnv("nope", good); len(problems) != 1 || !strings.Contains(problems[0].Error(), "the roots are") {
		t.Errorf("an unknown root is one problem naming the roots: %v", problems)
	}
}
`
