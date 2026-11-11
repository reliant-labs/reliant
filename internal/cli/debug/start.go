package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/codegen"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
	"github.com/reliant-labs/forge/internal/goexec"
)

// startOptions are `forge debug start`'s flags.
type startOptions struct {
	attachPID  int
	port       int
	appPort    int
	env        string
	envSet     bool
	workload   string
	noEnv      bool
	run        bool
	jsonOutput bool
	dockerMode bool
}

func newStartCmd(f *factory.Factory) *cobra.Command {
	var o startOptions

	cmd := &cobra.Command{
		Use:   "start <service>",
		Short: "Start a debug session for a service",
		Long: `Start a debug session for a Go service.

The binary is built with debug flags (-gcflags=all=-N -l) and launched under
a headless Delve server on 127.0.0.1, halted at its entry point: set
breakpoints, then 'forge debug continue'. --run lets it run from the start
instead, so it serves while you set breakpoints against it.

The service gets its environment's configuration the way 'forge env exec'
resolves it: the workload's declared values for --env (default dev) layered
over your shell. The workload is --workload, else the one named <service>,
else "api". --no-env launches with the shell environment alone. Program
output goes to .forge/logs/<env>/debug_<service>.log.

If the argument contains "/" or ".", it is treated as a direct path to a main
package. Otherwise it names a service: its own main package under cmd/, or
the project's single binary, run with the matching cobra subcommand (falling
back to "server").

--port is Delve's listen port. --app-port sets the service's PORT, overriding
the configured one — needed when 'forge env up' already serves that port.
Without --app-port, --port also sets the PORT of a service launched with a
cobra subcommand, as it always has (deprecated: pass --app-port).

--attach <pid> attaches to a running process instead, leaving it halted where
attach found it (--run resumes it at once). forge does not own it: 'stop'
detaches and leaves it running. To drive a dlv server forge did not
start — an env's delve runner — use 'forge debug connect <addr>'.

Examples:
  forge debug start api
  forge debug start api --run --app-port 18080
  forge debug start --attach 12345 --run
  forge debug start --port 2345 --env dev api
  forge debug start ./cmd/server --no-env`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.envSet = cmd.Flags().Changed("env") || cmd.Flags().Changed("workload")
			if o.dockerMode {
				return runDebugStartDocker(cmd.Context())
			}
			if o.attachPID > 0 {
				return runDebugStartAttach(cmd.Context(), o)
			}
			if len(args) == 0 {
				return fmt.Errorf("provide a service name or path, or use --attach <pid>")
			}
			return runDebugStartService(cmd.Context(), f, args[0], o)
		},
	}

	cmd.Flags().IntVar(&o.attachPID, "attach", 0, "Attach to an existing process by PID")
	cmd.Flags().IntVar(&o.port, "port", 0, "Delve listen port on 127.0.0.1 (0 = auto)")
	cmd.Flags().IntVar(&o.appPort, "app-port", 0, "Set the service's PORT, overriding its configured one")
	cmd.Flags().StringVar(&o.env, "env", "dev", "Environment whose workload configuration the service runs with")
	cmd.Flags().StringVar(&o.workload, "workload", "", "Workload whose configuration to use (default: <service> if declared, else api)")
	cmd.Flags().BoolVar(&o.noEnv, "no-env", false, "Launch with the shell environment only; resolve no env configuration")
	cmd.Flags().BoolVar(&o.run, "run", false, "Let the target run (resume an attached process) instead of leaving it halted")
	cmd.Flags().BoolVar(&o.jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&o.dockerMode, "docker", false, "Start debug session in Docker container")

	return cmd
}

func runDebugStartAttach(ctx context.Context, o startOptions) error {
	pid := o.attachPID
	d := dbgsvc.NewDelveDebugger()
	logPath := filepath.Join(".forge", "debug", fmt.Sprintf("dlv-attach-%d.log", pid))
	if err := d.Attach(ctx, dbgsvc.AttachOptions{PID: pid, ListenPort: o.port, OutputPath: logPath, StopOnStart: !o.run}); err != nil {
		return fmt.Errorf("attaching to PID %d: %w", pid, err)
	}

	session := &dbgsvc.SessionInfo{
		Type:   "delve",
		Addr:   d.Addr(),
		PID:    d.PID(),
		DlvPID: d.DlvPID(),
		Binary: fmt.Sprintf("pid:%d", pid),
		// ATTACH: forge does NOT own this process. `stop` must detach and
		// leave it running — never kill it.
		Owned:   false,
		Resumed: o.run,
		Started: time.Now(),
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	if o.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(session)
		return nil
	}
	fmt.Printf("Attached to PID %d (forge does not own this process; 'stop' will detach, not kill)\n", pid)
	fmt.Printf("Delve listening at %s\n", d.Addr())
	printRunState(!o.run, "where attach found it")
	return nil
}

// printRunState tells the user whether the target is running — the one
// thing a silent debugger used to leave them guessing.
func printRunState(halted bool, where string) {
	if halted {
		fmt.Printf("Target is HALTED (%s). Set breakpoints, then 'forge debug continue' to run it.\n", where)
		return
	}
	fmt.Println("Target is RUNNING. Set breakpoints with 'forge debug break', then 'forge debug continue --timeout 60s' waits for the next hit.")
}

func runDebugStartService(ctx context.Context, f *factory.Factory, target string, o startOptions) error {
	buildPath := target
	serviceName := target

	if serviceName == "." || serviceName == "./" {
		serviceName = "app"
	}
	serviceName = filepath.Base(serviceName)

	// If the target doesn't look like a path, resolve it from the project's
	// component inventory. The inventory is enumerated from the REAL
	// sources (proto descriptor + owned worker/operator files + cmd/
	// binaries) — see
	// codegen.IntrospectComponents. Debug commands run at the project root,
	// consistent with mainPackageForService's relative-path resolution.
	isPath := strings.Contains(target, "/") || strings.Contains(target, ".")
	if !isPath {
		var svcPath string
		found := false
		for _, svc := range codegen.IntrospectComponents(".") {
			if svc.Name == target {
				svcPath = svc.Path
				found = true
				break
			}
		}
		// The lookup can legitimately miss — a target that is not a discovered
		// component (say a cobra subcommand the user registered in the owned
		// cmd/<bin>/cmd/commands.go) still belongs to the single
		// ./cmd/<binary>, so the project is debuggable anyway. Fall through to
		// mainPackageForService, which resolves the binary's main package.
		// Only error if we can't resolve a main package at all.
		mainPkg, err := mainPackageForService(target, svcPath)
		if err != nil {
			if !found {
				return fmt.Errorf("service %q not found in project config and no main package resolved; provide a path instead: %w", target, err)
			}
			return fmt.Errorf("resolving main package for service %q: %w", target, err)
		}
		buildPath = mainPkg
	}

	// Resolve configuration BEFORE the build: a missing secret or an unknown
	// workload is reported in a second, not after a minute of compiling.
	baseEnv, workload, err := resolveLaunchEnv(ctx, f.Debug, o, serviceName, isPath)
	if err != nil {
		return err
	}

	// Determine binary args + env so the launched process actually SERVES
	// instead of exiting 0 on a usage message: the service's own cobra
	// subcommand when the tree declares one, else the all-services `server`.
	// See serviceRunSpec. Its PORT is checked before the build too.
	binArgs, extraEnv := launchSpec(target, buildPath, o)
	childEnv := layerEnv(baseEnv, extraEnv)
	if err := checkAppPortFree(childEnv, o.appPort > 0); err != nil {
		return err
	}

	// Build with debug flags.
	outputBinary := filepath.Join(".forge", "debug", serviceName)
	if err := os.MkdirAll(filepath.Dir(outputBinary), 0o755); err != nil {
		return fmt.Errorf("creating debug output dir: %w", err)
	}

	fmt.Printf("Building %s with debug flags...\n", buildPath)
	// -trimpath=false overrides a -trimpath in GOFLAGS (the Reliant workspace
	// image makes it the toolchain default): `forge debug break` hands delve
	// an ABSOLUTE file path first, and a trimmed binary records only
	// module-relative ones.
	buildCmd := goexec.Graceful(exec.CommandContext(ctx, "go", "build", "-gcflags=all=-N -l", "-trimpath=false", "-o", outputBinary, buildPath))
	buildCmd.Env = goexec.Env()
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("building debug binary: %w", err)
	}

	absBinary, err := filepath.Abs(outputBinary)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}

	logPath := filepath.Join(".forge", "logs", o.env, "debug_"+serviceName+".log")
	d := dbgsvc.NewDelveDebugger()
	if err := d.Launch(ctx, dbgsvc.LaunchOptions{
		Binary: absBinary, Args: binArgs, Env: childEnv, ListenPort: o.port,
		OutputPath: logPath, StopOnStart: !o.run,
	}); err != nil {
		return fmt.Errorf("starting Delve: %w", err)
	}

	session := &dbgsvc.SessionInfo{
		Type:   "delve",
		Addr:   d.Addr(),
		PID:    d.PID(),
		DlvPID: d.DlvPID(),
		Binary: absBinary,
		// forge launched this process — `stop` may kill it.
		Owned:   true,
		Resumed: o.run,
		LogPath: logPath,
		Started: time.Now(),
	}
	if o.run {
		// A service that dies on boot (a missing config field, a port in
		// use) must say so now, not at the first `continue`.
		if err := checkLaunchSurvived(d.Addr(), logPath); err != nil {
			stopLaunched(ctx, session)
			return err
		}
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	if o.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(session)
		return nil
	}
	fmt.Printf("Debug session started for %s\n", serviceName)
	if workload != "" {
		fmt.Printf("Configuration: workload %q of env %q\n", workload, o.env)
	}
	if p := envValue(childEnv, "PORT"); p != "" {
		fmt.Printf("Service PORT: %s\n", p)
	}
	fmt.Printf("Delve listening at %s\n", d.Addr())
	fmt.Printf("Program output: %s\n", logPath)
	printRunState(!o.run, "at its entry point")
	return nil
}

// resolveLaunchEnv is the base environment of a launched service: the
// shell's, with --workload's declared configuration for --env on top (what
// `forge env exec` gives its child). Without --env/--workload it degrades to
// the shell environment when the project declares no such env, and names the
// workload it used.
func resolveLaunchEnv(ctx context.Context, api factory.DebugAPI, o startOptions, service string, isPath bool) ([]string, string, error) {
	if o.noEnv || api.WorkloadEnv == nil {
		return nil, "", nil
	}
	projectDir, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", o.env)); err != nil {
		if o.envSet {
			return nil, "", fmt.Errorf("env %q: deploy/kcl/%s/ not found (pass --no-env to launch with the shell environment)", o.env, o.env)
		}
		fmt.Fprintf(os.Stderr, "note: no deploy/kcl/%s/; launching with the shell environment only\n", o.env)
		return nil, "", nil
	}
	workload := o.workload
	if workload == "" {
		names, err := api.WorkloadNames(ctx, projectDir, o.env)
		if err != nil {
			return nil, "", fmt.Errorf("reading env %q: %w (pass --no-env to launch with the shell environment)", o.env, err)
		}
		workload = defaultWorkload(names, service, isPath)
		if workload == "" {
			return nil, "", fmt.Errorf("env %q declares no workload named %q or \"api\" (it declares: %s); pass --workload <name>, or --no-env", o.env, service, strings.Join(names, ", "))
		}
	}
	env, err := api.WorkloadEnv(ctx, projectDir, o.env, workload, os.Environ())
	if err != nil {
		return nil, "", fmt.Errorf("resolving workload %q of env %q: %w (pass --no-env to launch with the shell environment)", workload, o.env, err)
	}
	return env, workload, nil
}

// defaultWorkload picks the workload whose configuration a service launches
// with: the one named after the service, else the scaffold's "api" (the
// all-services server). A path target only takes "api".
func defaultWorkload(names []string, service string, isPath bool) string {
	for _, want := range []string{service, "api"} {
		if isPath && want == service {
			continue
		}
		for _, n := range names {
			if strings.EqualFold(n, want) {
				return n
			}
		}
	}
	return ""
}

// layerEnv returns base (nil = forge's own environment) with extra on top.
// OTEL_SERVICE_NAME from extra only fills a gap: a workload that declares its
// own telemetry identity keeps it.
func layerEnv(base, extra []string) []string {
	if base == nil {
		base = os.Environ()
	}
	out := append([]string(nil), base...)
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if k == "OTEL_SERVICE_NAME" && envValue(out, k) != "" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envValue is the effective value of key in env (the last entry wins, as in
// os/exec).
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// checkAppPortFree refuses to launch a service whose PORT is already bound:
// it would die on boot, and the usual holder is `forge env up` serving the
// same workload.
func checkAppPortFree(env []string, explicit bool) error {
	p := envValue(env, "PORT")
	if p == "" {
		return nil
	}
	l, err := net.Listen("tcp", ":"+p)
	if err != nil {
		if explicit {
			return fmt.Errorf("--app-port %s is in use: %w", p, err)
		}
		return fmt.Errorf("the service's PORT %s is in use (is 'forge env up' serving it?): pass --app-port <free port>, or attach to the running process with 'forge debug start --attach <pid>'", p)
	}
	_ = l.Close()
	return nil
}

// launchSettle is how long start watches a just-launched service for an
// early exit. A var so tests can shrink it.
var launchSettle = 1500 * time.Millisecond

// checkLaunchSurvived reports a launched target that exited on boot, with
// the tail of its output.
func checkLaunchSurvived(addr, logPath string) error {
	time.Sleep(launchSettle)
	d := dbgsvc.NewDelveDebugger()
	if err := d.Connect(addr); err != nil {
		return fmt.Errorf("connecting to debugger at %s: %w", addr, err)
	}
	defer d.Disconnect()
	if st, err := d.State(); st == dbgsvc.TargetExited {
		return fmt.Errorf("the service exited during startup (%v); its output (%s):\n%s", err, logPath, tailFile(logPath, 20))
	}
	return nil
}

// stopLaunched tears down a launch that failed after dlv started.
func stopLaunched(ctx context.Context, s *dbgsvc.SessionInfo) {
	if d := dbgsvc.NewDelveDebugger(); d.Connect(s.Addr) == nil {
		_ = d.Stop()
	}
	reapDlv(ctx, s)
}

// tailFile returns the last n lines of path ("" when unreadable).
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// dockerReadyTimeout bounds how long --docker waits for the app-debug
// container's Delve to accept connections. A var so tests can shrink it.
var dockerReadyTimeout = 90 * time.Second

func runDebugStartDocker(ctx context.Context) error {
	// The project's own docker-compose.yml declares app-debug (behind the
	// `debug` profile, as `task dev-up-debug` starts it). Pin the file and
	// project directory so compose resolves the same project name the
	// Taskfile and `forge env status` do, whatever the caller's cwd tricks.
	startCmd := exec.CommandContext(ctx, "docker", composeArgs("--profile", "debug", "up", "-d", "app-debug")...)
	startCmd.Stdout = os.Stdout
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("starting debug container: %w", err)
	}

	// Poll until Delve's port is published AND answers, instead of a fixed
	// sleep that was both too long and too short.
	fmt.Println("Waiting for debug container...")
	addr, err := waitForDockerDelve(ctx, dockerReadyTimeout)
	if err != nil {
		return err
	}
	fmt.Printf("Delve listening at %s\n", addr)

	// Save session.
	session := &dbgsvc.SessionInfo{
		Type:    "delve",
		Addr:    addr,
		Docker:  true,
		Owned:   true, // forge started this container — stop may tear it down.
		Started: time.Now(),
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	fmt.Println("Docker debug session started")
	return nil
}

// composeArgs is `compose -f docker-compose.yml --project-directory .`
// followed by args.
func composeArgs(args ...string) []string {
	return append([]string{"compose", "-f", "docker-compose.yml", "--project-directory", "."}, args...)
}

// waitForDockerDelve polls for app-debug's published Delve port and a
// working RPC connection to it.
func waitForDockerDelve(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		addr, err := discoverDelvePort(ctx)
		if err == nil && addr != "" {
			d := dbgsvc.NewDelveDebugger()
			if err = d.Connect(addr); err == nil {
				d.Disconnect()
				return addr, nil
			}
		}
		last = err
		if time.Now().After(deadline) {
			return "", fmt.Errorf("delve in app-debug not reachable after %s: %v (check 'docker compose logs app-debug')", timeout, last)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func discoverDelvePort(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", composeArgs("port", "app-debug", "2345")...).Output()
	if err != nil {
		return "", fmt.Errorf("docker compose port: %w", err)
	}
	addr := strings.TrimSpace(string(out))
	// Normalize 0.0.0.0 to 127.0.0.1
	addr = strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1)
	return addr, nil
}

// mainPackageForService resolves the importable main-package path that
// `go build -o <file>` must compile for a service — never a wildcard.
//
// The historical bug: a multi-binary repo fell back to `./cmd/...`, and
// `go build -o <file> ./cmd/...` fails with "cannot write multiple packages
// to non-directory". This resolver always returns a single package dir.
//
// Resolution order (first hit wins):
//  1. <svcPath>/cmd/server     — per-service cmd/server layout
//  2. <svcPath>/cmd/<service>  — per-service cmd/<name> layout
//  3. cmd/<service>            — top-level cmd/<service> (matches forge's
//     ./cmd/<name> GoBuild default)
//  4. the SINGLE cmd/* main package — forge repos dispatch every service
//     through one cobra binary (`<bin> <service>`), so a service name with
//     no own main package maps to that one binary.
//  5. <svcPath>                — last resort when svcPath is itself a main pkg
func mainPackageForService(service, svcPath string) (string, error) {
	candidates := []string{}
	if svcPath != "" {
		candidates = append(candidates,
			filepath.Join(svcPath, "cmd", "server"),
			filepath.Join(svcPath, "cmd", service),
		)
	}
	candidates = append(candidates, filepath.Join("cmd", service))
	for _, c := range candidates {
		if isMainPackageDir(c) {
			return "./" + filepath.ToSlash(c), nil
		}
	}

	// Shared-binary fallback: find every cmd/* directory holding a main
	// package. A unique one is unambiguous; multiple need disambiguation.
	mains := mainPackagesUnderCmd()
	switch len(mains) {
	case 1:
		return "./" + filepath.ToSlash(mains[0]), nil
	case 0:
		// fall through to svcPath
	default:
		// Several binaries. A forge service name that isn't itself a binary
		// dir (handled above) is a cobra subcommand of the multi-service
		// binary — the one cmd/* whose tree declares a `server` cobra
		// subcommand. Standalone binaries (a thin proxy, an operator main)
		// have no `server` subcommand. When exactly one binary is a
		// multi-service dispatcher, that's the answer.
		var dispatchers []string
		for _, m := range mains {
			if hasSubcommand("./"+filepath.ToSlash(m), "server") {
				dispatchers = append(dispatchers, m)
			}
		}
		if len(dispatchers) == 1 {
			return "./" + filepath.ToSlash(dispatchers[0]), nil
		}
		return "", fmt.Errorf(
			"service %q maps to no single main package; cmd/ holds %d binaries (%s) — pass an explicit path (e.g. forge debug start ./%s)",
			service, len(mains), strings.Join(mains, ", "), mains[0])
	}

	if svcPath != "" && isMainPackageDir(svcPath) {
		return "./" + filepath.ToSlash(svcPath), nil
	}
	return "", fmt.Errorf("no main package found for service %q (looked under cmd/ and %q)", service, svcPath)
}

// isMainPackageDir reports whether dir contains at least one non-test .go
// file declaring `package main`.
func isMainPackageDir(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "package main" {
				return true
			}
		}
	}
	return false
}

// mainPackagesUnderCmd returns every cmd/<x> directory that is a main
// package, sorted for determinism.
func mainPackagesUnderCmd() []string {
	entries, err := os.ReadDir("cmd")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("cmd", e.Name())
		if isMainPackageDir(dir) {
			out = append(out, dir)
		}
	}
	return out
}

// launchSpec is the argv and extra env of a launched service. --app-port
// sets PORT whatever the binary looks like, a cobra service or a plain main
// package. Without it, --port still sets the PORT of a cobra-launched service,
// as it did before --app-port existed: deprecated, since dlv listens on that
// port too.
func launchSpec(target, buildPath string, o startOptions) (args, env []string) {
	legacyPort := 0
	if o.appPort == 0 {
		legacyPort = o.port
	}
	args, env = serviceRunSpec(target, buildPath, legacyPort)
	if o.appPort > 0 {
		return args, append(env, fmt.Sprintf("PORT=%d", o.appPort))
	}
	if legacyPort > 0 && envValue(env, "PORT") != "" {
		fmt.Fprintf(os.Stderr, "Deprecated: --port also sets the service's PORT=%d (Delve listens on it too); pass --app-port <port> to set the service's port\n", legacyPort)
	}
	return args, env
}

// serviceRunSpec returns the args + extra env a debugged service binary
// needs to actually SERVE rather than exit 0 on a usage message.
//
// The subcommand IS the selection. `<binary> <service>` is a first-class
// cobra command that mounts only that service (its RunE names the typed
// (*app.Components).Mount<Svc> method), so when the command tree declares it
// that is what we launch. A binary whose tree has no such command — a name
// the tree reserves (server/version/db/help/completion), or a composition
// root that never named it — falls back to `<binary> server`, the
// all-services process. A binary with neither (a single-purpose main) runs
// as-is with no args.
//
// No SERVICE_NAME. It selects NOTHING: the scaffolded config proto reserves
// the `service_name` field precisely because service identity is a build-time
// property (the generated ServiceName constant), and every rendered manifest
// sets SERVICE_NAME to the PROJECT name, not the component's. OTEL_SERVICE_NAME
// is the per-process identity knob — the same one the per-env main.k sets per
// component — and PORT binds the listener.
//
// port is the PORT a subcommand-launched service binds: what --port supplied
// before --app-port existed (launchSpec keeps that).
func serviceRunSpec(service, buildPath string, port int) (args []string, env []string) {
	switch {
	case hasSubcommand(buildPath, service):
		args = []string{service}
	case hasSubcommand(buildPath, "server"):
		args = []string{"server"}
	default:
		return nil, nil
	}
	env = []string{"OTEL_SERVICE_NAME=" + service}
	if port > 0 {
		env = append(env, fmt.Sprintf("PORT=%d", port))
	}
	return args, env
}

// hasSubcommand reports whether the binary built from buildPath declares a
// cobra command with `Use: "<use>"`. It walks the binary's package tree
// (cmd/<binary>/...) because the command tree is usually assembled from
// sibling files / subpackages, not the main package itself.
func hasSubcommand(buildPath, use string) bool {
	root := strings.TrimPrefix(buildPath, "./")
	if root == "" || root == "." {
		root = "."
	}
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if declaresUse(string(data), use) {
			found = true
		}
		return nil
	})
	return found
}

// declaresUse reports whether src contains a cobra command whose Use field's
// first word is want (the rest, if any, is the arg spec — e.g.
// `Use: "db <cmd>"`). Tolerant of gofmt alignment whitespace.
func declaresUse(src, want string) bool {
	if want == "" {
		return false
	}
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "Use:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, "Use:"))
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		// Extract the quoted value, then its first word.
		rest = rest[1:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			continue
		}
		use := rest[:end]
		first := use
		if sp := strings.IndexAny(use, " \t"); sp >= 0 {
			first = use[:sp]
		}
		if first == want {
			return true
		}
	}
	return false
}
