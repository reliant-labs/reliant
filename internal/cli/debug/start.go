package debug

import (
	"context"
	"encoding/json"
	"fmt"
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

func newStartCmd(f *factory.Factory) *cobra.Command {
	var (
		attachPID  int
		port       int
		jsonOutput bool
		dockerMode bool
	)

	cmd := &cobra.Command{
		Use:   "start <service>",
		Short: "Start a debug session for a service",
		Long: `Start a debug session for a Go service.

The binary is built with debug flags (-gcflags=all=-N -l) and launched under Delve.

If the argument contains "/" or ".", it is treated as a direct path.
Otherwise it is looked up by name in forge.yaml.

Examples:
  forge debug start api-gateway
  forge debug start --attach 12345
  forge debug start --port 2345 api-gateway
  forge debug start ./cmd/server`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dockerMode {
				return runDebugStartDocker(cmd.Context())
			}
			if attachPID > 0 {
				return runDebugStartAttach(cmd.Context(), attachPID, jsonOutput)
			}
			if len(args) == 0 {
				return fmt.Errorf("provide a service name or path, or use --attach <pid>")
			}
			return runDebugStartService(cmd.Context(), f, args[0], port, jsonOutput)
		},
	}

	cmd.Flags().IntVar(&attachPID, "attach", 0, "Attach to an existing process by PID")
	cmd.Flags().IntVar(&port, "port", 0, "Debugger listen port (0 = auto)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&dockerMode, "docker", false, "Start debug session in Docker container")

	return cmd
}

func runDebugStartAttach(ctx context.Context, pid int, jsonOutput bool) error {
	d := dbgsvc.NewDelveDebugger()
	if err := d.StartAttach(ctx, pid); err != nil {
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
		Started: time.Now(),
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(session)
	} else {
		fmt.Printf("Attached to PID %d (forge does not own this process; 'stop' will detach, not kill)\n", pid)
		fmt.Printf("Delve listening at %s\n", d.Addr())
	}
	return nil
}

func runDebugStartService(ctx context.Context, f *factory.Factory, target string, port int, jsonOutput bool) error {
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
	if !strings.Contains(target, "/") && !strings.Contains(target, ".") {
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

	// Build with debug flags.
	outputBinary := filepath.Join(".forge", "debug", serviceName)
	if err := os.MkdirAll(filepath.Dir(outputBinary), 0o755); err != nil {
		return fmt.Errorf("creating debug output dir: %w", err)
	}

	fmt.Printf("Building %s with debug flags...\n", buildPath)
	// -trimpath=false overrides a -trimpath in GOFLAGS (the Reliant workspace
	// image makes it the toolchain default): `forge debug break` hands delve
	// an ABSOLUTE file path, and a trimmed binary records only module-relative
	// ones, so every breakpoint would be "could not find file".
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

	// Determine binary args + env so the launched process actually SERVES
	// instead of exiting 0 on a usage message: the service's own cobra
	// subcommand when the tree declares one, else the all-services `server`.
	// See serviceRunSpec.
	binArgs, binEnv := serviceRunSpec(target, buildPath, port)

	d := dbgsvc.NewDelveDebugger()
	if err := d.StartWithEnv(ctx, absBinary, binArgs, binEnv, port); err != nil {
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
		Started: time.Now(),
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(session)
	} else {
		fmt.Printf("Debug session started for %s\n", serviceName)
		fmt.Printf("Delve listening at %s\n", d.Addr())
	}
	return nil
}

func runDebugStartDocker(ctx context.Context) error {
	// Start the debug container.
	startCmd := exec.CommandContext(ctx, "docker", "compose", "--profile", "debug", "up", "-d", "app-debug")
	startCmd.Stdout = os.Stdout
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("starting debug container: %w", err)
	}

	// Wait for container to be running.
	fmt.Println("Waiting for debug container...")
	time.Sleep(5 * time.Second)

	// Discover Delve port.
	addr, err := discoverDelvePort(ctx)
	if err != nil {
		return fmt.Errorf("discovering Delve port: %w", err)
	}
	fmt.Printf("Delve listening at %s\n", addr)

	// Connect to verify the debugger is alive, then disconnect so the
	// TCP connection doesn't go stale when forge exits.
	d := dbgsvc.NewDelveDebugger()
	if err := d.Connect(addr); err != nil {
		return fmt.Errorf("connecting to Delve: %w", err)
	}
	d.Disconnect()

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

func discoverDelvePort(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "compose", "port", "app-debug", "2345").Output()
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
