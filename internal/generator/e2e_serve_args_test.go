package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The e2e harness starts the service with the SAME subcommand the scaffolded
// CLI declares for it.
//
// It used to run `<bin> server <svc>`. The generated tree has no such form:
// each service is a TOP-LEVEL `<bin> <svc>` command, and `server` takes no
// service argument (it mounts all of them). Every scaffolded e2e suite
// therefore failed to start its service (houndersclub PR #8). The argv now
// comes from codegen.CmdServiceCommand — the function that names the cobra
// command — so the two cannot drift. TestE2EScaffoldE2EHarnessCommand (e2e
// tag) runs that argv against a real scaffold's binary.
//
// That holds for a legacy Components project only. A project composed by the
// graph — every new one — has no per-component subcommands, and the harness
// ran `<bin> <svc>` there too (`unknown command "api-gateway"`) until
// TestE2EHarnessServesAGraphProjectWithServer pinned it.
func TestE2EHarnessServesWithTheCLISubcommand(t *testing.T) {
	for _, c := range []struct {
		service string
		want    string
	}{
		{"membership", `"./bin/demo", "membership")`},
		{"api-gateway", `"./bin/demo", "api-gateway")`},
		{"AdminServerService", `"./bin/demo", "admin-server")`},
		// A name the built-in tree already claims gets no subcommand of
		// its own; `server` mounts every service, this one included.
		{"version", `"./bin/demo", "server")`},
	} {
		t.Run(c.service, func(t *testing.T) {
			dir := legacyComponentsProject(t)
			if err := GenerateE2ETests(dir, c.service, "example.com/demo", "demo", nil); err != nil {
				t.Fatalf("GenerateE2ETests: %v", err)
			}
			pkg := strings.ReplaceAll(strings.ToLower(c.service), "-", "_")
			main, err := os.ReadFile(filepath.Join(dir, "e2e", pkg, "main_test.go"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(main)
			if !strings.Contains(src, c.want) {
				t.Errorf("harness does not start %q with %s", c.service, c.want)
			}
			if cmd, _, ok := codegen.CmdServiceCommand(c.service); ok && !strings.Contains(src, `"`+cmd+`")`) {
				t.Errorf("harness argv disagrees with the CLI command %q", cmd)
			}
			if strings.Contains(src, `"server", "`) {
				t.Error("harness passes a service name to `server`, which takes none")
			}
			for _, want := range []string{"DATABASE_URL=postgres://test:test@localhost:15432/demo_test", "AUTO_MIGRATE=true"} {
				if !strings.Contains(src, want) {
					t.Errorf("harness does not set %s for the service it starts", want)
				}
			}
		})
	}
}

// legacyComponentsProject is a project whose internal/app still composes with
// NewComponents, which codegen.GraphComposition reads as the legacy shape.
func legacyComponentsProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	app := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package app\n\ntype Components struct{}\n\nfunc NewComponents() (*Components, error) { return &Components{}, nil }\n"
	if err := os.WriteFile(filepath.Join(app, "compose.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if codegen.GraphComposition(dir) {
		t.Fatal("precondition: a NewComponents project must read as legacy")
	}
	return dir
}

// TestE2EHarnessServesAGraphProjectWithServer: a graph project's CLI has no
// `<bin> <svc>` command — `server` runs the default root, which serves every
// service — so that is the argv its harness must start.
func TestE2EHarnessServesAGraphProjectWithServer(t *testing.T) {
	for _, service := range []string{"membership", "api-gateway"} {
		t.Run(service, func(t *testing.T) {
			dir := t.TempDir()
			if !codegen.GraphComposition(dir) {
				t.Fatal("precondition: a fresh project composes with the graph")
			}
			if err := GenerateE2ETests(dir, service, "example.com/demo", "demo", nil); err != nil {
				t.Fatalf("GenerateE2ETests: %v", err)
			}
			pkg := strings.ReplaceAll(service, "-", "_")
			main, err := os.ReadFile(filepath.Join(dir, "e2e", pkg, "main_test.go"))
			if err != nil {
				t.Fatal(err)
			}
			if want := `exec.CommandContext(ctx, "./bin/demo", "server")`; !strings.Contains(string(main), want) {
				t.Errorf("graph project's harness must start %s:\n%s", want, main)
			}
		})
	}
}
