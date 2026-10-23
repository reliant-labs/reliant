//go:build e2e

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EScaffoldBasicProject creates a project with a single service,
// runs generate, and verifies the full toolchain: build, vet, test, lint.
func TestE2EScaffoldBasicProject(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Create project
	runCmd(t, dir, forgeBin, "project", "new", "basicapp", "--mod", "example.com/basicapp", "--service", "api")

	projectDir := filepath.Join(dir, "basicapp")
	addCorpusForgePkgReplace(t, projectDir)
	assertPathExistsE2E(t, filepath.Join(projectDir, "forge.yaml"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "go.mod"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "api"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "proto", "services", "api", "v1", "api.proto"))

	// Generate code
	runCmd(t, projectDir, forgeBin, "generate")

	// Verify generated code exists
	assertPathExistsE2E(t, filepath.Join(projectDir, "gen", "services", "api", "v1"))
	// §2 hybrid DI: the live composition layer is internal/app; the old
	// name-matched pkg/app DI unit (bootstrap.go) is retired. The by-type
	// injector that briefly replaced it (inject_gen.go) is retired too —
	// composition is now the explicit per-binary compose.go site.
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "internal", "app", "inject_gen.go"))
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "pkg", "app", "bootstrap.go"))

	// go mod tidy (may be needed after generate)
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	// Build
	runCmd(t, projectDir, "go", "build", "./...")

	// Vet
	runCmd(t, projectDir, "go", "vet", "./...")

	// Test
	runCmd(t, projectDir, "go", "test", "./...")

	// Lint gates. These are LAST in the function on purpose: requireTool
	// skips the whole test on a laptop without the tool, and everything
	// above has already asserted by the time it can fire.
	requireTool(t, "golangci-lint", "buf")
	runGolangciLintE2E(t, projectDir, "./...")
	runCmd(t, projectDir, "buf", "lint")
}

// TestE2EScaffoldMultiServiceProject creates a project with multiple services
// and a frontend, then verifies everything builds.
func TestE2EScaffoldMultiServiceProject(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Create project with multiple services and a frontend
	runCmd(t, dir, forgeBin, "project", "new", "multiapp",
		"--mod", "example.com/multiapp",
		"--service", "api,users,orders",
		"--frontend", "web",
	)

	projectDir := filepath.Join(dir, "multiapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Verify all services exist
	for _, svc := range []string{"api", "users", "orders"} {
		assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", svc, "service.go"))
		assertPathExistsE2E(t, filepath.Join(projectDir, "proto", "services", svc, "v1", svc+".proto"))
	}

	// Verify frontend exists
	assertPathExistsE2E(t, filepath.Join(projectDir, "frontends", "web", "package.json"))

	// Verify the thin auth-policy middleware file is generated (CORS and
	// the other mechanisms come from forge/pkg/middleware, wired in
	// cmd/server.go).
	assertPathExistsE2E(t, filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))

	// Generate code
	runCmd(t, projectDir, forgeBin, "generate")

	// go mod tidy
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	// Build
	runCmd(t, projectDir, "go", "build", "./...")

	// Vet
	runCmd(t, projectDir, "go", "vet", "./...")

	// Test
	runCmd(t, projectDir, "go", "test", "./...")

	// golangci-lint — last in the function; see TestE2EScaffoldBasicProject.
	requireTool(t, "golangci-lint")
	runGolangciLintE2E(t, projectDir, "./...")
}

// TestE2EScaffoldWithEntityProto and TestE2EScaffoldLifecycle (scaffold_lifecycle_e2e_test.go)
// were deleted with the entity-proto subsystem: entity annotations are ignored now and the
// schema-truth lifecycle gate in fixture_corpus_e2e_test.go supersedes them.

// TestE2EScaffoldAddService creates a project, then adds a service using
// `forge scaffold service`, regenerates, and verifies the build.
func TestE2EScaffoldAddService(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Create initial project
	// The intended behavior is project-may-start-with-or-without-services;
	// use --service for the canonical full scaffold.
	runCmd(t, dir, forgeBin, "project", "new", "addtest", "--mod", "example.com/addtest", "--service", "api")

	projectDir := filepath.Join(dir, "addtest")
	addCorpusForgePkgReplace(t, projectDir)

	// Add a new service
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "billing")

	// Verify both services exist
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "api", "service.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "billing", "service.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "proto", "services", "billing", "v1", "billing.proto"))

	// Regenerate
	runCmd(t, projectDir, forgeBin, "generate")

	// go mod tidy
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	// Build
	runCmd(t, projectDir, "go", "build", "./...")

	// Verify both services are constructed at the composition graph's
	// construction site (internal/app/compose.go — one constructor method per
	// node, which replaced NewComponents, the by-type injector and, before
	// it, the name-matched wire_gen/services_gen path).
	composeContent := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	if !strings.Contains(composeContent, "api.New(") {
		t.Fatal("expected compose.go to construct the api service")
	}
	if !strings.Contains(composeContent, "billing.New(") {
		t.Fatal("expected compose.go to construct the billing service")
	}
}

// TestE2EScaffoldVersion verifies the version subcommand works.
func TestE2EScaffoldVersion(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)

	output := runCmdOutput(t, t.TempDir(), forgeBin, "version")
	if !strings.Contains(strings.ToLower(output), "forge version") && !strings.Contains(output, "version") {
		t.Fatalf("expected version output, got: %s", output)
	}
}

// TestE2EScaffoldServerStartup creates a project and verifies the server
// can start and respond to health checks.
func TestE2EScaffoldServerStartup(t *testing.T) {
	// Boot needs a real database (see the DATABASE_URL note below); take it
	// from the ONE shared embedded server rather than starting another.
	sharedTestPostgres(t)
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Create project
	runCmd(t, dir, forgeBin, "project", "new", "srvtest", "--mod", "example.com/srvtest", "--service", "api")

	projectDir := filepath.Join(dir, "srvtest")
	addCorpusForgePkgReplace(t, projectDir)

	// Generate code
	runCmd(t, projectDir, forgeBin, "generate")

	// go mod tidy
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	// Build the server binary. The cobra tree under cmd/<bin>/ is several
	// packages (cmd, services, workers, operators) of which exactly ONE is
	// package main, so `-o <file> ./cmd/...` is refused as "multiple
	// packages to non-directory" — name the binary package itself.
	serverBin := filepath.Join(projectDir, "server")
	runCmd(t, projectDir, "go", "build", "-o", serverBin, "./cmd/srvtest")

	// Start the server with a free port (parallel e2e tests must never
	// share a hard-coded port).
	port := freePortE2E(t)
	dsn, dsnCleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer dsnCleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		// A REAL database, even though this test only reads /healthz.
		// database_url is required config (checkRequired fires before bind)
		// AND boot pings the pool, so neither an empty value nor a
		// well-formed URL pointing at nothing gets the server to ready —
		// the old "no DB needed for health check" comment described a boot
		// sequence that no longer exists.
		"DATABASE_URL="+dsn,
		// This test is about the serve lifecycle (healthz/readyz). The
		// scaffold's SetupAuth always builds a real JWT validator, so the
		// server boots in validate mode; /healthz and /readyz are on the
		// unauthenticated allow-list and need no token.
		"ENVIRONMENT=development",
	)

	// Capture output for debugging
	var serverOutput strings.Builder
	cmd.Stdout = &serverOutput
	cmd.Stderr = &serverOutput

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Wait for server to be ready
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := waitForServer(t, addr+"/healthz", 10*time.Second)
	if !ready {
		t.Fatalf("server did not become ready within timeout\nserver output:\n%s", serverOutput.String())
	}

	// Verify health endpoint
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /healthz, got %d", resp.StatusCode)
	}

	// Readiness. /healthz answers from the top of boot, while the graph is
	// still constructing and starting its nodes; /readyz opens only once the
	// root has started them all (Drainer.MarkReady). Reading /readyz once,
	// right after /healthz, raced that boot and saw 503 — so WAIT for it,
	// with a bound that still fails a server that never becomes ready.
	if !waitForServing(t, addr, 15*time.Second) {
		resp, err := http.Get(addr + "/readyz")
		status := "unreachable"
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			status = fmt.Sprintf("%d %s", resp.StatusCode, body)
		}
		t.Fatalf("/readyz never answered 200 (last: %s)\nserver output:\n%s", status, serverOutput.String())
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// forgeBinaryOnce builds the forge binary exactly once per `go test`
// process and shares the path across every e2e test. Previously each of
// the ~8 e2e tests rebuilt it into its own t.TempDir() — ~15s × 8 of
// pure duplicated compilation per run, the single biggest e2e cost.
//
// The binary is built into a process-scoped temp dir (NOT t.TempDir(),
// which is cleaned when the first test that triggered the build ends —
// that would yank the binary out from under later tests). The OS reaps
// /tmp; we don't bother removing it. sync.Once makes concurrent callers
// (t.Parallel e2e tests) safe.
var (
	forgeBinaryOnce sync.Once
	forgeBinaryPath string
	forgeBinaryErr  error
)

// sharedTestPostgres boots ONE embedded postgres for the whole `go test`
// process and publishes its DSN via FORGE_TEST_POSTGRES_URL, so every
// forge subprocess (schema introspection) and every in-process testkit DB
// connect to the SAME server and only create cheap per-call databases.
//
// Without this, each `forge generate` subprocess would boot its OWN
// embedded postgres; the parallel corpus then spins up dozens at once and
// exhausts the kernel's shared-memory limits. One shared server is both
// far faster and the only way the parallel corpus stays within those
// limits.
//
// os.Setenv (process-global, NOT t.Setenv) is used deliberately: the
// value must reach exec'd subprocesses through os.Environ(), and it is a
// shared read-only resource URL, so it is safe to set once across the
// parallel corpus (unlike the t.Setenv/t.Chdir combo the suite forbids).
// pgtest itself honors FORGE_TEST_POSTGRES_URL, so the first call boots
// the server and later calls (including the subprocesses) reuse it.
func sharedTestPostgres(t *testing.T) {
	t.Helper()
	sharedPGOnce.Do(func() {
		if os.Getenv(pgtest.EnvBaseURL) != "" {
			return // an external server was provided; honor it.
		}
		dsn, _, err := pgtest.NewURL()
		if err != nil {
			sharedPGErr = err
			return
		}
		// Point at the maintenance database so subprocesses can CREATE
		// DATABASE off it; pgtest.New/NewURL derive per-call databases.
		base := dsn
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			if q := strings.IndexByte(base[i:], '?'); q >= 0 {
				base = base[:i+1] + "postgres" + base[i+q:]
			} else {
				base = base[:i+1] + "postgres"
			}
		}
		sharedPGErr = os.Setenv(pgtest.EnvBaseURL, base)
	})
	if sharedPGErr != nil {
		t.Fatalf("provision shared test postgres: %v", sharedPGErr)
	}
}

var (
	sharedPGOnce sync.Once
	sharedPGErr  error
)

// forgeE2EBinaryEnv names a forge binary already built from this checkout,
// used instead of building one. e2e-suite.yml's prepare job builds forge once
// — with the same `go build -ldflags <devForgeRootLDFlag> -o <bin> ./cmd/forge`
// as below, at the same checkout path — and hands it to every shard, so no
// shard compiles forge. Unset (a developer's run), the binary is built here
// as always.
const forgeE2EBinaryEnv = "FORGE_E2E_BINARY"

// devForgeRootLDFlag stamps buildinfo.DevForgeRoot with the checkout the
// binary under test is built from. Every fixture scaffolds with --link-forge,
// which needs that checkout. Unstamped, forge recovers it from its compiled
// source paths, and -trimpath erases those: under GOFLAGS=-trimpath (the Go
// default in the Reliant workspace image) every scaffolding fixture refused
// with "cannot locate the checkout it was built from". Without -trimpath the
// stamp is the path discovery would have found, so nothing else changes.
func devForgeRootLDFlag(repoRoot string) string {
	return "-X github.com/reliant-labs/forge/internal/buildinfo.DevForgeRoot=" + repoRoot
}

func buildforgeBinary(t *testing.T) string {
	t.Helper()
	forgeBinaryOnce.Do(func() {
		if bin := os.Getenv(forgeE2EBinaryEnv); bin != "" {
			fi, err := os.Stat(bin)
			switch {
			case err != nil:
				forgeBinaryErr = fmt.Errorf("%s=%s: %w", forgeE2EBinaryEnv, bin, err)
			case fi.IsDir():
				forgeBinaryErr = fmt.Errorf("%s=%s is a directory, not the forge binary", forgeE2EBinaryEnv, bin)
			default:
				forgeBinaryPath = bin
			}
			return
		}
		repoRoot := findRepoRoot(t)
		dir, err := os.MkdirTemp("", "forge-e2e-bin-")
		if err != nil {
			forgeBinaryErr = err
			return
		}
		// Removed by TestMain (main_test.go), not t.Cleanup: the binary is
		// shared across every fixture through this sync.Once, so the first
		// test to finish must not delete what the rest still execute.
		registerSharedTempDir(dir)
		bin := filepath.Join(dir, "forge")
		cmd := exec.Command("go", "build", "-ldflags", devForgeRootLDFlag(repoRoot), "-o", bin, "./cmd/forge")
		cmd.Dir = repoRoot
		if output, berr := cmd.CombinedOutput(); berr != nil {
			forgeBinaryErr = fmt.Errorf("failed to build forge binary: %w\n%s", berr, output)
			return
		}
		forgeBinaryPath = bin
	})
	if forgeBinaryErr != nil {
		t.Fatalf("%v", forgeBinaryErr)
	}
	// Every fixture goes through buildforgeBinary; provisioning the shared
	// postgres here means every forge subprocess inherits
	// FORGE_TEST_POSTGRES_URL and connects to the one shared server
	// instead of booting its own. (No-DB fixtures pay one process-wide pg
	// boot; that is cheaper than the parallel corpus booting dozens.)
	sharedTestPostgres(t)
	// The binary under test is an unreleased build of THIS checkout, so every
	// scaffold must compile forge from it — and since that bridge is opt-in
	// (`project new --link-forge`), the corpus opts in once, here, for every
	// fixture present and future. Process-global for the same reason as the
	// postgres URL above: it must reach the exec'd forge subprocesses.
	if err := os.Setenv(linkForgeEnv, "1"); err != nil {
		t.Fatalf("opt the e2e corpus into --link-forge: %v", err)
	}
	return forgeBinaryPath
}

// findRepoRoot walks up from the working directory to find the forge repo root.
func findRepoRoot(t *testing.T) string {
	t.Helper()

	// The test runs from internal/cli/ — walk up to repo root
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	dir := cwd
	for {
		goMod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(goMod); err == nil {
			if strings.Contains(string(data), "module github.com/reliant-labs/forge") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find forge repo root from %s", cwd)
		}
		dir = parent
	}
}

// freePortE2E asks the kernel for an ephemeral port and returns it.
// e2e tests that boot servers run in parallel, so a hard-coded port is
// a collision waiting to happen. There is an inherent TOCTOU window
// between closing the probe listener and the server binding the port,
// but ephemeral allocation makes two parallel tests racing for the
// SAME port vanishingly unlikely (vs. guaranteed with a constant).
func freePortE2E(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// addforgeReplace adds a replace directive for github.com/reliant-labs/forge
// in the gen/go.mod so that generated code resolves the ORM package from the
// local repo checkout (the ORM now lives in-repo under pkg/).
func addforgeReplace(t *testing.T, genDir string) {
	t.Helper()

	repoRoot := findRepoRoot(t)

	goModPath := filepath.Join(genDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read gen/go.mod: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "github.com/reliant-labs/forge") &&
		!strings.Contains(content, "replace github.com/reliant-labs/forge") {
		// Add replace directive pointing at the repo root
		content += fmt.Sprintf("\nreplace github.com/reliant-labs/forge => %s\n", repoRoot)
		if err := os.WriteFile(goModPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write gen/go.mod: %v", err)
		}
	}
}

// runCmd runs a command and fails the test on error.
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	_ = runCmdKeep(t, dir, name, args...)
}

// runCmdKeep is runCmd returning the combined output, for a test that must
// show what a command that succeeded printed when a later assertion fails.
func runCmdKeep(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(),
		"GOFLAGS=",       // Clear any global GOFLAGS (-trimpath goes back on)
		"GONOSUMCHECK=*", // Don't check sums for test modules
		"GOPROXY=https://proxy.golang.org,direct", // Ensure module proxy is set
		"GONOSUMDB=*", // Don't verify sums for test modules
	))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %q in %s failed: %v\n%s", append([]string{name}, args...), dir, err, output)
	}
	return string(output)
}

// requireComposedE2E fails, with the output of the commands that should
// have composed it, when the project has no internal/app/compose.go. Those
// commands fail when their generate does, but a generate that succeeds
// without composing would otherwise surface later as an unrelated missing
// file.
func requireComposedE2E(t *testing.T, projectDir string, outputs ...string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "app", "compose.go")); err != nil {
		t.Fatalf("the project was never composed (no internal/app/compose.go: %v); what the commands printed:\n%s",
			err, strings.Join(outputs, "\n----\n"))
	}
}

// runCmdOutput runs a command and returns its combined output.
func runCmdOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %q failed: %v\n%s", append([]string{name}, args...), err, output)
	}
	return string(output)
}

// runCmdStdout is runCmdOutput for a command whose stdout is parsed, e.g.
// `--json`. Only stdout is returned: forge writes warnings to stderr (the
// forge_version skew warning, for one), and folding them in breaks the
// parse. stderr is still shown when the command fails.
func runCmdStdout(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("command %q failed: %v\n%s%s", append([]string{name}, args...), err, output, stderr.Bytes())
	}
	return string(output)
}

// ── golangci-lint's machine-global lock ─────────────────────────────────────
//
// golangci-lint takes an exclusive OS file lock on $TMPDIR/golangci-lint.lock
// for the whole of a run, and by default waits only FIVE SECONDS for it before
// exiting 3 with:
//
//	Error: parallel golangci-lint is running
//
// A lint of a scaffolded project takes tens of seconds, so five seconds of
// patience is no patience at all: any two e2e tests whose lint steps overlap
// leave one of them dead, and which one is decided by scheduling rather than by
// anything in the code. That is why TestE2EScaffoldIsReviveExportedClean and
// TestE2EFreshScaffoldLintExitsZero each pass alone and fail in the full suite,
// and it gets worse under CI sharding, not better.
//
// The lock is contention control, not analysis: neither knob below can change a
// single finding. It is also NOT in the cache directory — pointing
// GOLANGCI_LINT_CACHE somewhere private leaves the run dying after the same 5.1
// seconds, because the lock lives in os.TempDir() and is shared by every
// golangci-lint on the machine regardless of cache.
//
// `go test -parallel` already budgets how much of this suite runs at once. A
// second, hidden, machine-global serialization point inside one of the tools
// makes that budget a lie, so the instruction to give is "these runs are
// independent" — spelled two ways, because which one is available depends on
// whether this suite owns the argv:
//
//   - It does for a DIRECT invocation, so those pass --allow-parallel-runners,
//     the documented flag for exactly this ("Allow multiple parallel
//     golangci-lint instances running"). golangciLintRunArgs is the only place
//     that spells it.
//
//   - It does NOT when golangci-lint is a GRANDCHILD under `forge lint`:
//     nothing in that command's surface forwards flags to it. There the same
//     instruction has to arrive as environment, and golangciLockIsolationEnv
//     gives the subprocess its own TMPDIR — hence its own lock file, hence
//     nothing to contend with. This suite deliberately does not change what the
//     shipped `forge lint` does to a user's machine in order to unflake itself;
//     if `forge lint` grows the flag (it has the same defect against a user's
//     editor or a sibling CI step), delete golangciLockIsolationEnv and its
//     three call sites.

// golangciLintRunArgs returns the argv for `golangci-lint run` with the
// parallel-safety flag ahead of the caller's arguments. Every direct invocation
// in this suite must build its arguments here.
func golangciLintRunArgs(args ...string) []string {
	return append([]string{"run", "--allow-parallel-runners"}, args...)
}

// runGolangciLintE2E runs golangci-lint in dir and fails the test on a non-zero
// exit. Callers that need the output, or that must tolerate a non-zero exit
// because findings are the thing under test, build their own exec.Cmd from
// golangciLintRunArgs instead.
func runGolangciLintE2E(t *testing.T, dir string, args ...string) {
	t.Helper()
	runCmd(t, dir, "golangci-lint", golangciLintRunArgs(args...)...)
}

// golangciLockIsolationEnv returns a TMPDIR= entry pointing at a fresh
// directory, so a golangci-lint launched somewhere below the subprocess gets a
// lock file no other test shares. Append it to cmd.Env of anything that runs
// `forge lint`; os/exec keeps the last duplicate of a key, so it wins over the
// inherited TMPDIR.
func golangciLockIsolationEnv(t *testing.T) string {
	t.Helper()
	return "TMPDIR=" + t.TempDir()
}

// toolAvailable checks if a tool is on PATH.
//
// PREFER requireTool. A bare boolean has exactly one honest use — deciding
// which of two real code paths to take — and zero honest uses as the guard on
// an assertion, because the false branch is a test that passes without
// testing. Every remaining caller is a conversion that has not happened yet.
func toolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ── Required-tool gate ──────────────────────────────────────────────────────
//
// A missing external tool has two honest answers and they are not the same
// answer. On a contributor's laptop the tool is genuinely absent and no
// checkout can supply it, so SKIP is right: the run stays useful and says out
// loud what it did not do. In CI the tool is the WORKFLOW'S JOB to install, so
// a missing tool is a broken gate — and a broken gate that skips reports the
// same green as a gate that passed. That is how 47 of this suite's 52 tests
// went a year without running while CI stayed green.
//
// requireTool collapses both answers into one call so no test has to choose:
//
//	requireTool(t, "node", "npm")   // everything below this line is real
//
// Trigger, in precedence order:
//
//	FORGE_E2E_REQUIRE_TOOLS  Explicit; wins in both directions. The e2e
//	                         workflow sets it at the job level so the
//	                         requirement is written down next to the installs
//	                         rather than inferred three layers away. Setting
//	                         it to 0/false is the escape hatch for a CI job
//	                         that genuinely cannot provision something — and
//	                         that job then has to say so, in the workflow, in
//	                         writing, where review can see it.
//	CI                       Set by GitHub Actions and every other CI. Strict
//	                         is the DEFAULT under automation on purpose: a new
//	                         workflow that runs `-tags e2e` without knowing
//	                         this env var exists still fails loudly instead of
//	                         quietly covering less than it appears to.
//	(neither)                Laptop. Skip, naming the tool.
//
// Deliberately NOT inferred from GITHUB_ACTIONS alone: the same strictness is
// wanted from any runner (act, a future buildkite lane, a nightly cron box),
// and `CI` is the portable spelling.
func requireTool(t *testing.T, names ...string) {
	t.Helper()
	if len(names) == 0 {
		t.Fatal("requireTool called with no tools — a gate that requires nothing is the exact bug this helper exists to prevent")
	}
	var missing []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	list := strings.Join(missing, ", ")
	// The skip is written as the guarded branch and the failure as the
	// fall-through on purpose: internal/vacuousguard's dead-skip rule reads
	// the AST, and a t.Skip with no enclosing conditional is — correctly —
	// an unconditional skip. Spelling it this way makes the condition the
	// skip depends on visible to the guard and to the reader.
	if !e2eToolsRequired() {
		t.Skipf("%s not on PATH — skipped locally. This is a HARD FAILURE in CI; "+
			"run with FORGE_E2E_REQUIRE_TOOLS=1 to reproduce that here.", list)
	}
	t.Fatalf("required tool(s) not on PATH: %s\n"+
		"This run is under CI (or FORGE_E2E_REQUIRE_TOOLS is set), where a missing tool is a "+
		"provisioning bug in the workflow, not a property of the machine. Install it in the job "+
		"that runs `go test -tags e2e`; skipping here would report green for a check that never ran.",
		list)
}

// e2eToolsRequired reports whether a missing tool must fail rather than skip.
// See requireTool for the precedence rationale.
func e2eToolsRequired() bool {
	if v, ok := os.LookupEnv("FORGE_E2E_REQUIRE_TOOLS"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "0", "false", "no", "off":
			return false
		default:
			return true
		}
	}
	return os.Getenv("CI") != ""
}

// requireDockerDaemon is requireTool for the docker DAEMON. A docker CLI on
// PATH says nothing about whether an engine answers it — Docker Desktop
// stopped, a socket that moved — and a test that only looked up the binary
// then failed deep inside `k3d cluster create` with a message about networks.
// Same contract as requireTool: a named skip on a laptop, a hard failure
// under CI (or FORGE_E2E_REQUIRE_TOOLS=1), where a dead daemon is a
// provisioning bug rather than a property of the machine.
//
// The probe is `docker version`, NOT `docker info --format`: with no daemon,
// `docker info --format '{{.ServerVersion}}'` prints the error, an empty
// version, and EXITS 0 (docker CLI 29). An empty server version is treated
// as no daemon too.
func requireDockerDaemon(t *testing.T) {
	t.Helper()
	requireTool(t, "docker")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return
	}
	if !e2eToolsRequired() {
		t.Skipf("no docker daemon answers (`docker version`: %v) — skipped locally. This is a HARD FAILURE in CI; "+
			"run with FORGE_E2E_REQUIRE_TOOLS=1 to reproduce that here.\n%s", err, out)
	}
	t.Fatalf("no docker daemon answers (`docker version`: %v)\n%s\n"+
		"This run is under CI (or FORGE_E2E_REQUIRE_TOOLS is set): start the daemon in the job that runs "+
		"`go test -tags e2e`; skipping here would report green for a check that never ran.", err, out)
}

// waitForServer polls a URL until it gets a 200 or the timeout expires.
func waitForServer(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// waitForServing waits until the server at base is composed and serving its
// routes: /readyz answers 200. /healthz cannot say that. serverkit's boot
// probes answer it from the top of boot, while the database connect and
// AutoMigrate are still running and before any route is mounted, so a test
// that calls a route as soon as /healthz is up races composition and reads
// the boot listener's 404.
func waitForServing(t *testing.T, base string, timeout time.Duration) bool {
	t.Helper()
	return waitForServer(t, base+"/readyz", timeout)
}

// assertPathExistsE2E fails the test if the path does not exist.
func assertPathExistsE2E(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path %s to exist: %v", path, err)
	}
}

// assertPathNotExistsE2E fails the test if the path exists.
func assertPathNotExistsE2E(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected path %s to NOT exist", path)
	}
}

// containsTSE2E reports whether a scaffolded TS/TSX source contains the code
// in needle, ignoring layout.
//
// Since #496 a page is handed to the frontend's OWN prettier at birth, so
// its line breaks, indentation, JSX brace padding and trailing commas are
// whatever the project's prettier version and config decide — not what the
// template wrote. An assertion on the template's exact bytes then fails on a
// correct page (`defaultValue={ X }` is born `defaultValue={X}`), and a
// NEGATIVE assertion silently stops checking anything, because the bytes it
// forbids can no longer occur in any spelling it would recognise.
//
// So both sides are compared with every whitespace character removed and
// every trailing comma (a `,` directly before `)`, `]` or `}`) dropped —
// exactly the freedom prettier takes with TS/TSX that cannot change what the
// code means. Every token, operator, bracket and literal still has to match.
//
// A comma that ENDS the needle is dropped too: `status: item.status,` names
// one object member, and in the page that member may be the last one, whose
// comma is a trailing comma the haystack side just lost.
func containsTSE2E(haystack, needle string) bool {
	return strings.Contains(normalizeTSE2E(haystack), strings.TrimRight(normalizeTSE2E(needle), ","))
}

var trailingCommaE2E = regexp.MustCompile(`,([)\]}])`)

func normalizeTSE2E(s string) string {
	s = strings.Join(strings.Fields(s), "")
	return trailingCommaE2E.ReplaceAllString(s, "$1")
}

// readFileE2E reads a file and fails the test on error.
func readFileE2E(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}
