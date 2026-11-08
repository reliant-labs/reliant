//go:build e2e

package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// rootsRunRootProto binds one command config to each of `rrr server api` and
// `rrr server worker`, and each holds what reliant keeps per role: its listen
// port and its drain budget, under env names of its own. The binary's root
// config (AppConfig) declares none of them.
const rootsRunRootProto = `syntax = "proto3";

package rrr.config.v1;

import "forge/v1/forge.proto";
import "google/protobuf/duration.proto";

option go_package = "github.com/test/rrr/gen/rrr/config/v1;configv1";

// ApiConfig is the ` + "`rrr server api`" + ` process's config.
message ApiConfig {
  option (forge.v1.binary_config) = {
    binary: "rrr"
    command: ["server", "api"]
  };

  int32 port = 1 [(forge.v1.config) = {
    env_var: "API_PORT"
    flag: "port"
    default_value: "18081"
    description: "Port the api listens on."
  }];
  google.protobuf.Duration pre_stop_delay = 2 [(forge.v1.config) = {
    env_var: "API_PRE_STOP_DELAY"
    flag: "pre-stop-delay"
    default_value: "-1s"
    description: "The api's pre-stop pause (negative: none)."
  }];
  google.protobuf.Duration shutdown_timeout = 3 [(forge.v1.config) = {
    env_var: "API_SHUTDOWN_TIMEOUT"
    flag: "shutdown-timeout"
    default_value: "7s"
    description: "The api's shutdown window."
  }];
}

// WorkerConfig is the ` + "`rrr server worker`" + ` process's config.
message WorkerConfig {
  option (forge.v1.binary_config) = {
    binary: "rrr"
    command: ["server", "worker"]
  };

  int32 port = 1 [(forge.v1.config) = {
    env_var: "WORKER_PORT"
    flag: "port"
    default_value: "18082"
    description: "Port the worker's probes listen on."
  }];
  google.protobuf.Duration pre_stop_delay = 2 [(forge.v1.config) = {
    env_var: "WORKER_PRE_STOP_DELAY"
    flag: "pre-stop-delay"
    default_value: "-1s"
    description: "The worker's pre-stop pause (negative: none)."
  }];
  google.protobuf.Duration shutdown_timeout = 3 [(forge.v1.config) = {
    env_var: "WORKER_SHUTDOWN_TIMEOUT"
    flag: "shutdown-timeout"
    default_value: "9s"
    description: "The worker's shutdown window."
  }];
}
`

// TestE2ERunRootReadsEachRolesOwnConfig pins the roots plan's F1, F2 and F6 on
// a real scaffold: a binary_config project whose root config has no process
// field, two roots whose port and drain budget live in their own command
// configs, and a worker root declared migrate=wait.
//
//   - generate, then go build, succeed: the scaffolded serve.go reads no
//     `cfg.Port` off a root config without one (F1);
//   - each root listens on the port its command config declares and drains
//     with its own budget, because runRoot reads the configs its command
//     loaded before the policy runs (F2);
//   - the worker, against a database nobody has migrated, answers /healthz
//     and reports /readyz 503 naming the schema — it migrates nothing — and
//     becomes ready once the api root applies the migrations (F6).
func TestE2ERunRootReadsEachRolesOwnConfig(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "rrr", "--mod", "github.com/test/rrr", "--service", "item")
	projectDir := filepath.Join(dir, "rrr")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "tick")

	// The root config binds to the binary and gives up every per-role
	// setting to the command configs.
	configProto := filepath.Join(projectDir, "proto", "rrr", "config", "v1", "config.proto")
	mustReplaceInFile(t, configProto, "message AppConfig {", "message AppConfig {\n  option (forge.v1.binary_config) = {binary: \"rrr\"};\n")
	for _, decl := range []string{"int32 port = 1 [", "google.protobuf.Duration pre_stop_delay = 18 [", "google.protobuf.Duration shutdown_timeout = 19 ["} {
		dropProtoFieldE2E(t, configProto, decl)
	}
	writeFileE2E(t, filepath.Join(projectDir, "proto", "rrr", "config", "v1", "roles.proto"), rootsRunRootProto)
	writeFileE2E(t, filepath.Join(projectDir, "db", "migrations", "20260101000000_widgets.up.sql"),
		"-- forge:backward-compatible — a new table nothing reads yet\nCREATE TABLE widgets (id BIGINT PRIMARY KEY);\n")

	writeFileE2E(t, filepath.Join(projectDir, "internal", "app", "roots.go"), `package app

import "github.com/test/rrr/internal/workers/tick"

// API is what `+"`rrr server api`"+` runs. It owns the schema.
//
//forge:root api command=server,api migrate=apply
type API struct {
	Listener *APIListener
}

// Worker is what `+"`rrr server worker`"+` runs. It leaves the schema to the api.
//
//forge:root worker command=server,worker migrate=wait
type Worker struct {
	Tick *tick.Worker
}
`)

	// serve.go and the envs' config.k were born against the old config:
	// have forge write them again for this one.
	rescaffold := []string{filepath.Join("cmd", "rrr", "cmd", "serve.go")}
	for _, env := range []string{"dev", "staging", "prod"} {
		mainK := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
		mustReplaceInFile(t, mainK, "appcfg.app_config", "appcfg.rrr_process")
		mustReplaceInFile(t, mainK, "    args = [\"server\"]\n    root = \"server\"", "    args = [\"server\", \"api\"]\n    root = \"api\"")
		rescaffold = append(rescaffold, filepath.Join("deploy", "kcl", env, "config.k"))
	}
	for _, rel := range rescaffold {
		if err := os.Remove(filepath.Join(projectDir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	runCmd(t, projectDir, forgeBin, append([]string{"project", "rescaffold"}, rescaffold...)...)
	runCmd(t, projectDir, forgeBin, "generate")

	serve := readFileE2E(t, filepath.Join(projectDir, "cmd", "rrr", "cmd", "serve.go"))
	for _, want := range []string{"serverkitConfig(cfg, root.Configs)", "if c := configs.ApiConfig; c != nil {", "} else if root.AwaitMigrations {"} {
		if !strings.Contains(serve, want) {
			t.Errorf("serve.go lacks %q:\n%s", want, serve)
		}
	}
	gen := strings.Join(strings.Fields(readFileE2E(t, filepath.Join(projectDir, "cmd", "rrr", "cmd", "roots_gen.go"))), " ")
	for _, want := range []string{"Configs: rootConfigs{ ApiConfig: apiConfig, },", "AwaitMigrations: true,"} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, gen)
		}
	}

	bin := filepath.Join(projectDir, "rrr-bin")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/rrr")

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer cleanup()

	// ── The worker waits for a schema nobody has applied ─────────────────
	workerPort := freePortE2E(t)
	worker := exec.Command(bin, "server", "worker")
	worker.Dir = projectDir
	worker.Env = append(os.Environ(), "DATABASE_URL="+dsn, fmt.Sprintf("WORKER_PORT=%d", workerPort), "ENVIRONMENT=development", "PPROF_ADDR=")
	var workerOut strings.Builder
	worker.Stdout, worker.Stderr = &workerOut, &workerOut
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Wait() }()
	defer func() {
		if worker.ProcessState == nil {
			_ = worker.Process.Kill()
			<-workerDone
		}
	}()
	workerBase := fmt.Sprintf("http://127.0.0.1:%d", workerPort)
	var ready string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if code, body := getE2E(workerBase + "/readyz"); code == http.StatusServiceUnavailable && strings.Contains(body, `"name":"schema"`) {
			ready = body
			break
		}
	}
	if ready == "" {
		t.Fatalf("`server worker` over an unmigrated database never reported /readyz 503 naming the schema on its own port %d\noutput:\n%s", workerPort, workerOut.String())
	}
	if code, _ := getE2E(workerBase + "/healthz"); code != http.StatusOK {
		t.Errorf("the waiting worker's /healthz = %d, want 200: it is alive, only not ready", code)
	}
	time.Sleep(time.Second)
	if strings.Contains(workerOut.String(), "worker started") || strings.Contains(workerOut.String(), "db migration completed") {
		t.Fatalf("the worker started, or migrated, before the schema was current:\n%s", workerOut.String())
	}

	// ── The api applies the migrations, on its own port and budget ───────
	apiLog := bootRootOnPort(t, bin, projectDir, "API_PORT", []string{"DATABASE_URL=" + dsn, "AUTO_MIGRATE=true"}, []string{"server", "api"}, map[string]int{"/healthz": http.StatusOK})
	if !strings.Contains(apiLog, "db migration completed") {
		t.Errorf("`server api` (migrate=apply) did not migrate:\n%s", apiLog)
	}
	if !strings.Contains(apiLog, `"shutdown_timeout":7000000000`) {
		t.Errorf("`server api` did not drain within its command config's shutdown_timeout (7s):\n%s", apiLog)
	}

	// ── The worker proceeds once the schema is current ───────────────────
	if !waitForServing(t, workerBase, 30*time.Second) {
		t.Fatalf("`server worker` never became ready after the api migrated:\n%s", workerOut.String())
	}
	if err := worker.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("`server worker` did not shut down cleanly: %v\n%s", err, workerOut.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("`server worker` did not exit within 30s of SIGTERM:\n%s", workerOut.String())
	}
	log := workerOut.String()
	if !strings.Contains(log, `"worker":"tick"`) {
		t.Errorf("the worker never started its tick worker:\n%s", log)
	}
	if strings.Contains(log, "db migration completed") {
		t.Errorf("the migrate=wait worker ran a migration:\n%s", log)
	}
	if !strings.Contains(log, `"shutdown_timeout":9000000000`) {
		t.Errorf("`server worker` did not drain within its command config's shutdown_timeout (9s):\n%s", log)
	}
}

// dropProtoFieldE2E deletes the message field declared by decl (its first
// line's prefix, e.g. "int32 port = 1 [", indented as a field — not the same
// text in a comment) and its option block from a proto file.
func dropProtoFieldE2E(t *testing.T, path, decl string) {
	t.Helper()
	src := readFileE2E(t, path)
	i := strings.Index(src, "\n  "+decl)
	if i >= 0 {
		i += len("\n  ")
	}
	if i < 0 {
		t.Fatalf("%s declares no %q", path, decl)
	}
	start := strings.LastIndex(src[:i], "\n") + 1
	end := strings.Index(src[i:], "}];")
	if end < 0 {
		t.Fatalf("%s: %q has no closing }];", path, decl)
	}
	end += i + len("}];")
	if end < len(src) && src[end] == '\n' {
		end++
	}
	writeFileE2E(t, path, src[:start]+src[end:])
}

// getE2E GETs url, returning its status and body (0 when unreachable).
func getE2E(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
