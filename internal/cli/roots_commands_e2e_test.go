//go:build e2e

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EOneCommandPerRoot pins composition graph PR 4 on a real scaffold: a
// project that declares two roots gets two commands (cmd/<bin>/cmd/roots_gen.go),
// and each runs only the subgraph its root reaches.
//
//   - `server` holds the API listener: it serves the api service and never
//     starts the worker;
//   - `worker` holds the worker alone: it starts the worker, mounts no route,
//     answers /healthz and /readyz on the server address all the same (a
//     probe-only listener), and — reaching no database — never opens one or
//     migrates.
func TestE2EOneCommandPerRoot(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	newOut := runCmdKeep(t, dir, forgeBin, "project", "new", "roots", "--mod", "github.com/test/roots", "--service", "api")
	projectDir := filepath.Join(dir, "roots")
	addCorpusForgePkgReplace(t, projectDir)

	// One rpc, so "served" (401 from the auth interceptor) and "not served"
	// (404 from the mux) are distinguishable over HTTP.
	apiProtoPath := filepath.Join(projectDir, "proto", "services", "api", "v1", "api.proto")
	apiProto := readFileE2E(t, apiProtoPath)
	const rpcTODO = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(apiProto, rpcTODO) {
		t.Fatalf("scaffolded api.proto no longer carries the RPC TODO anchor:\n%s", apiProto)
	}
	apiProto = strings.Replace(apiProto, rpcTODO, "  rpc Ping(PingRequest) returns (PingResponse) {}\n", 1)
	apiProto += "\nmessage PingRequest {}\n\nmessage PingResponse {\n  string ok = 1;\n}\n"
	if err := os.WriteFile(apiProtoPath, []byte(apiProto), 0o644); err != nil {
		t.Fatal(err)
	}
	workerOut := runCmdKeep(t, projectDir, forgeBin, "scaffold", "worker", "tick")
	requireComposedE2E(t, projectDir, newOut, workerOut)
	// The tick worker needs no configuration: drop the root config the
	// scaffold hands it, so its root reaches only the process fields (a
	// component with settings takes a config block of its own).
	tickPath := filepath.Join(projectDir, "internal", "workers", "tick", "worker.go")
	mustReplaceInFile(t, tickPath, "\tConfig *config.Config\n", "")
	mustReplaceInFile(t, tickPath, "\n\t\"github.com/test/roots/pkg/config\"\n", "\n")

	rootsGo := `package app

import "github.com/test/roots/internal/workers/tick"

// Server is what ` + "`roots server`" + ` runs: the API.
//
//forge:root server
type Server struct {
	API *APIListener
}

// Worker is what ` + "`roots worker`" + ` runs: the tick worker alone.
//
//forge:root worker
type Worker struct {
	Tick *tick.Worker
}

// Both runs the two: the worker stops first, then the API.
//
//forge:root both
type Both struct {
	Tick *tick.Worker
	API  *APIListener
}
`
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "app", "roots.go"), []byte(rootsGo), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	cmdDir := filepath.Join(projectDir, "cmd", "roots", "cmd")
	assertPathNotExistsE2E(t, filepath.Join(cmdDir, "server.go"))
	gen := strings.Join(strings.Fields(readFileE2E(t, filepath.Join(cmdDir, "roots_gen.go"))), " ")
	for _, want := range []string{
		`rootSpec{ Name: "server", Migrate: true, Config: []string{`,
		`rootSpec{ Name: "worker", Migrate: false, Config: []string{`,
		`Run: (*app.Graph).RunServer, })`,
		`Run: (*app.Graph).RunWorker, })`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, gen)
		}
	}
	workerReach := gen[strings.Index(gen, `Name: "worker"`):]
	workerReach = workerReach[:strings.Index(workerReach, "Run:")]
	if strings.Contains(workerReach, `"database_url"`) || !strings.Contains(workerReach, `"port"`) {
		t.Errorf("the worker root reaches the process fields and not the database: %s", workerReach)
	}

	// ── Deploy: each workload's env is what its root reaches ────────────
	probe := `import config_gen
import forge
import forge.workloads as fw

_cfg = config_gen.AppConfig {}
_build = forge.GoBuild {cmd = "./cmd/roots", output_name = "roots"}
_api = fw.Workload {name = "api", kind = "service", image = "x.invalid/roots", build = _build, config_secrets = []} | config_gen.root_of("server")
_tick = fw.Workload {name = "tick", kind = "worker", image = "x.invalid/roots", build = _build} | config_gen.root_of("worker")
api_env = sorted([_k for _k in config_gen.workloadEnvMap(_cfg, _api)])
worker_env = sorted([_k for _k in config_gen.workloadEnvMap(_cfg, _tick)])
worker_args = _tick.args
`
	probePath := filepath.Join(projectDir, "deploy", "kcl", "roots_probe.k")
	if err := os.WriteFile(probePath, []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	evalList := func(field string) []string {
		t.Helper()
		var out []string
		if err := json.Unmarshal([]byte(runCmdStdout(t, projectDir, forgeBin, "kcl", "eval", probePath, "-S", field, "--format", "json")), &out); err != nil {
			t.Fatalf("eval %s: %v", field, err)
		}
		return out
	}
	apiEnv, workerEnv := evalList("api_env"), evalList("worker_env")
	if strings.Join(evalList("worker_args"), " ") != "worker" {
		t.Errorf("root_of(\"worker\") must set args [worker]")
	}
	if !slices.Contains(apiEnv, "DATABASE_URL") || slices.Contains(workerEnv, "DATABASE_URL") {
		t.Errorf("DATABASE_URL reaches the api (its root opens the database) and not the worker:\napi:    %v\nworker: %v", apiEnv, workerEnv)
	}
	for _, k := range workerEnv {
		if !slices.Contains(apiEnv, k) {
			t.Errorf("the worker's env %s is not one the api root reaches too: in a scaffold the API's providers take the whole config", k)
		}
	}
	if len(workerEnv) >= len(apiEnv) {
		t.Errorf("the worker root reaches less than the api root: worker %d keys, api %d", len(workerEnv), len(apiEnv))
	}

	// ── Plan<Root>: the start and stop order, without a database ────────
	// The worker root really builds (it opens nothing), so its plan is
	// pinned against Built(); the two-field root's stop order is its field
	// order, each field's dependencies after it.
	writeFileE2E(t, filepath.Join(projectDir, "internal", "app", "plan_e2e_test.go"), `package app_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/test/roots/internal/app"
	"github.com/test/roots/pkg/config"
)

func TestPlanIsWhatBuildConstructs(t *testing.T) {
	g := &app.Graph{Ctx: context.Background(), Cfg: &config.Config{}, Log: slog.Default()}
	if _, err := g.BuildWorker(); err != nil {
		t.Fatal(err)
	}
	plan := app.PlanWorker()
	if got, want := g.Built(), plan.Names()[1:]; !slices.Equal(got, want) {
		t.Errorf("Built() = %v, PlanWorker().Names() = %v", got, plan.Names())
	}
	if got := plan.Stop(); !slices.Equal(got, []string{"tick", "probe_listener"}) {
		t.Errorf("PlanWorker().Stop() = %v", got)
	}
	both := app.PlanBoth().Stop()
	if len(both) < 3 || both[0] != "tick" || both[1] != "api_listener" || slices.Index(both, "db") < 1 {
		t.Errorf("PlanBoth().Stop() = %v: the first field stops first, then the second, then what it reaches", both)
	}
	if slices.Contains(both, "probe_listener") || slices.Contains(app.PlanServer().Names(), "probe_listener") {
		t.Error("a root whose API listener serves the probes has no probe listener of its own")
	}
}
`)
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "TestPlanIsWhatBuildConstructs", "./internal/app/")

	bin := filepath.Join(projectDir, "roots-bin")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/roots")
	help := runCmdStdout(t, projectDir, bin, "--help")
	for _, want := range []string{"server", "worker"} {
		if !strings.Contains(help, "  "+want+" ") {
			t.Errorf("`roots --help` lists no %q command:\n%s", want, help)
		}
	}

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer cleanup()

	const ping = "/services.api.v1.APIService/Ping"
	serverLog := bootRoot(t, bin, projectDir, dsn, "server", map[string]int{ping: http.StatusUnauthorized})
	if strings.Contains(serverLog, "worker started") {
		t.Errorf("`server` started the worker its root does not reach:\n%s", serverLog)
	}
	// No DATABASE_URL at all: the worker root does not reach it, so the
	// process starts without it (config.LoadReach).
	workerLog := bootRoot(t, bin, projectDir, "", "worker", map[string]int{ping: http.StatusNotFound})
	if !strings.Contains(workerLog, `"worker":"tick"`) || !strings.Contains(workerLog, "worker started") {
		t.Errorf("`worker` never started the tick worker:\n%s", workerLog)
	}
	if strings.Contains(workerLog, "database connected") || strings.Contains(workerLog, "migration") {
		t.Errorf("`worker` reaches no database, yet opened one or migrated:\n%s", workerLog)
	}
}

// bootRoot runs `<bin> <command>`, waits for /readyz, asserts each path's
// status, stops it with SIGTERM and returns its output.
func bootRoot(t *testing.T, bin, projectDir, dsn, command string, want map[string]int) string {
	t.Helper()
	return bootRootArgs(t, bin, projectDir, []string{"DATABASE_URL=" + dsn}, []string{command}, want)
}

// bootRootArgs is bootRoot for the command line args, with env added to the
// process's environment.
func bootRootArgs(t *testing.T, bin, projectDir string, env, args []string, want map[string]int) string {
	t.Helper()
	return bootRootOnPort(t, bin, projectDir, "PORT", env, args, want)
}

// bootRootOnPort is bootRootArgs for a root whose listen port is the config
// field behind portEnv — a command config's own port.
func bootRootOnPort(t *testing.T, bin, projectDir, portEnv string, env, args []string, want map[string]int) string {
	t.Helper()
	command := strings.Join(args, " ")
	port := freePortE2E(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = projectDir
	cmd.Env = append(append(os.Environ(),
		fmt.Sprintf("%s=%d", portEnv, port),
		"ENVIRONMENT=development",
		// The assertions read JSON log lines; a LOG_FORMAT=text in the
		// caller's environment (a forge env up shell) must not change that.
		"LOG_FORMAT=json",
		"PPROF_ADDR=",
		"PRE_STOP_DELAY=-1s",
	), env...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", command, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("`%s` never became ready\noutput:\n%s", command, out.String())
	}
	for path, code := range want {
		if got := postStatus(t, base+path); got != code {
			t.Errorf("`%s`: %s = %d, want %d\noutput:\n%s", command, path, got, code, out.String())
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("`%s` did not shut down cleanly: %v\noutput:\n%s", command, err, out.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("`%s` did not exit within 30s of SIGTERM\noutput:\n%s", command, out.String())
	}
	return out.String()
}
