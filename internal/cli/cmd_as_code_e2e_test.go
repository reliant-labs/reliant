//go:build e2e

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2ECmdAsCodeSubcommands drives the cmd-as-code surface end to end
// on a real scaffold:
//
//  1. A new project composes with the composition graph, so it has NO
//     per-service subcommand (design §9.5): `./<bin> server` runs the
//     generated server root — every service, worker and operator — and the
//     composition root cmd/<bin>/main.go is a bare cmd.Execute().
//  2. The user-owned cmd/commands.go extension point: a second binary
//     registered AS CODE (userCommands()) shows up on the root command,
//     runs, and SURVIVES regeneration (Tier-2: forge never overwrites).
//  3. `forge project audit --json` reports no phantom service.
func TestE2ECmdAsCodeSubcommands(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "cmdcode", "--mod", "example.com/cmdcode", "--service", "api")
	projectDir := filepath.Join(dir, "cmdcode")

	// Local-replace harness (same as the fixture corpus): appkit/
	// serverkit revisions are newer than any published forge/pkg
	// snapshot, so wire the scaffold to the in-repo sources.
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "generate")

	// The cmd layer is a cobra tree under cmd/<bin>/: the generated root,
	// the owned serve pipeline, and the user-owned extension point. There is
	// no services/ group — a process is a root, not a subcommand per service.
	cmdDir := filepath.Join(projectDir, "cmd", "cmdcode", "cmd")
	commandsPath := filepath.Join(cmdDir, "commands.go")
	assertPathExistsE2E(t, commandsPath)
	if _, err := os.Stat(filepath.Join(cmdDir, "services")); !os.IsNotExist(err) {
		t.Errorf("a graph-mode project has a cmd/cmdcode/cmd/services group (%v)", err)
	}
	if main := readFileE2E(t, filepath.Join(projectDir, "cmd", "cmdcode", "main.go")); !strings.Contains(main, "cmd.Execute()") {
		t.Errorf("main.go must be the bare composition root:\n%s", main)
	}
	// `server` is the default root's command, generated with every other
	// root's into roots_gen.go, and runs through serve.go's runRoot.
	assertPathNotExistsE2E(t, filepath.Join(cmdDir, "server.go"))
	rootsGo := readFileE2E(t, filepath.Join(cmdDir, "roots_gen.go"))
	if !regexp.MustCompile(`rootSpec\{\s*Name:\s*"server",`).MatchString(rootsGo) || !regexp.MustCompile(`\bRun:\s*\(\*app\.Graph\)\.RunServer,`).MatchString(rootsGo) {
		t.Errorf("`server` must run the graph's server root through runRoot:\n%s", rootsGo)
	}

	// (2) Register a second binary AS CODE: replace the scaffolded
	// userCommands with a self-contained subcommand (the workspace-proxy
	// shape — a process that is not a Connect service and needs no
	// parallel main()).
	customCommands := `// What extra subcommands this binary ships is code, not config.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func proxyToolCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "proxy-tool",
		Short: "Run the proxy tool (second binary registered as code)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("proxy-tool: code-registered second binary ran")
			return nil
		},
	}
}

func userCommands(deps Deps) []*cobra.Command {
	_ = deps
	return []*cobra.Command{proxyToolCmd()}
}
`
	if err := os.WriteFile(commandsPath, []byte(customCommands), 0644); err != nil {
		t.Fatal(err)
	}

	// Regenerate: the user-owned extension point must survive verbatim.
	runCmd(t, projectDir, forgeBin, "generate")
	if got := readFileE2E(t, commandsPath); got != customCommands {
		t.Fatalf("forge generate overwrote the user-owned cmd/<bin>/cmd/commands.go:\n%s", got)
	}

	bin := filepath.Join(projectDir, "cmdcode-bin")
	// `./cmd/...` matches the main package AND its cobra subpackages, which
	// `go build -o <file>` refuses; name the ONE main package.
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/cmdcode")

	// Root help advertises the server command and the code-registered second
	// binary — and no per-service subcommand.
	helpOut := runCmdOutput(t, projectDir, bin, "--help")
	for _, want := range []string{"proxy-tool", "server"} {
		if !strings.Contains(helpOut, want) {
			t.Errorf("root --help missing subcommand %q:\n%s", want, helpOut)
		}
	}
	if strings.Contains(helpOut, "Run only the api service") {
		t.Errorf("root --help still lists a per-service subcommand:\n%s", helpOut)
	}

	// The second binary runs through the shared root.
	toolOut := runCmdOutput(t, projectDir, bin, "proxy-tool")
	if !strings.Contains(toolOut, "proxy-tool: code-registered second binary ran") {
		t.Errorf("proxy-tool subcommand did not run its body:\n%s", toolOut)
	}

	// The scaffolded config declares database_url REQUIRED (config.checkRequired
	// fires before bind) and the DB provider PINGS the database while the root
	// is built, so
	// "no database" is not a bootable configuration for any service project.
	// One ephemeral database serves both boots below; it stays empty — the point
	// here is the command grammar, not the schema.
	bootDSN, dropDB, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer dropDB()

	// bootService starts `bin <args...>`, waits for /healthz, and asserts 200.
	bootService := func(label string, args ...string) {
		t.Helper()
		port := freePortE2E(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv := exec.CommandContext(ctx, bin, args...)
		srv.Dir = projectDir
		srv.Env = append(os.Environ(),
			fmt.Sprintf("PORT=%d", port),
			"DATABASE_URL="+bootDSN,
			// The scaffold's SetupAuth always builds a real JWT validator, so
			// the server boots in validate mode; this test only hits /healthz,
			// which is on the unauthenticated allow-list and needs no token.
			"ENVIRONMENT=development",
		)
		var srvOut strings.Builder
		srv.Stdout = &srvOut
		srv.Stderr = &srvOut
		if err := srv.Start(); err != nil {
			t.Fatalf("failed to start `cmdcode-bin %s`: %v", label, err)
		}
		defer func() {
			_ = srv.Process.Kill()
			_ = srv.Wait()
		}()
		addr := fmt.Sprintf("http://127.0.0.1:%d", port)
		if !waitForServer(t, addr+"/healthz", 10*time.Second) {
			t.Fatalf("`cmdcode-bin %s` did not become ready\noutput:\n%s", label, srvOut.String())
		}
		resp, err := http.Get(addr + "/healthz")
		if err != nil {
			t.Fatalf("health check failed (%s): %v", label, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 from /healthz (%s), got %d", label, resp.StatusCode)
		}
	}

	// `server` takes no args and runs the server root: every service on the
	// API listener plus every worker/operator, in one process.
	bootService("server", "server")

	// (3) No phantom service: audit's codegen category must not carry an
	// unregistered_services finding — every subcommand the binary
	// advertises is backed by a registration row.
	auditOut := runCmdStdout(t, projectDir, forgeBin, "project", "audit", "--json")
	var report struct {
		Categories map[string]struct {
			Details map[string]any `json:"details"`
		} `json:"categories"`
	}
	if err := json.Unmarshal([]byte(auditOut), &report); err != nil {
		t.Fatalf("parse audit JSON: %v\n%s", err, auditOut)
	}
	if findings, ok := report.Categories["codegen"].Details["unregistered_services"]; ok {
		t.Errorf("audit reports phantom (unregistered) services on a fully code-registered binary: %v", findings)
	}

	// (4) Owned-auth honesty compiles end to end. Auth is CODE now, not a
	// forge.yaml provider gate: a fresh project scaffolds the OWNED
	// internal/app/auth.go (SetupAuth), and the owned UserAuth provider calls
	// it and threads the validator into AuthDeps.Validate. Regenerate + build
	// must stay green with no auth block anywhere in forge.yaml.
	runCmd(t, projectDir, forgeBin, "generate")
	providersGo := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "providers.go"))
	if !strings.Contains(providersGo, "SetupAuth(d.Config)") {
		t.Fatalf("internal/app/providers.go's UserAuth must call the owned SetupAuth:\n%s", providersGo)
	}
	serveGo := readFileE2E(t, filepath.Join(cmdDir, "serve.go"))
	if strings.Contains(serveGo, "InstallGeneratedAuth") || strings.Contains(serveGo, "GeneratedAuthInterceptor") {
		t.Fatalf("cmd/<bin>/cmd/serve.go must not reference the retired generated-auth surface:\n%s", serveGo)
	}
	authSetup := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "auth.go"))
	if !strings.Contains(authSetup, "func SetupAuth(") {
		t.Fatalf("internal/app/auth.go must scaffold SetupAuth (owned):\n%s", authSetup)
	}
	forgeYaml := readFileE2E(t, filepath.Join(projectDir, "forge.yaml"))
	if strings.Contains(forgeYaml, "provider:") && strings.Contains(forgeYaml, "auth:") {
		t.Fatalf("fresh forge.yaml must carry no auth-provider block:\n%s", forgeYaml)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}
