package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// seedCmdTree writes the pieces wireServiceIntoTree checks for: the generated
// services subcommand file (its presence is what says "there is something to
// wire") and a composition-root main.go.
func seedCmdTree(t *testing.T, root, bin, mainBody string, subcommands ...string) {
	t.Helper()
	svcDir := filepath.Join(root, "cmd", bin, "cmd", "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	for _, sub := range subcommands {
		if err := os.WriteFile(filepath.Join(svcDir, sub+".go"), []byte("package services\n"), 0o644); err != nil {
			t.Fatalf("write %s.go: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", bin, "main.go"), []byte(mainBody), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
}

const wiringMain = `package main

import (
	"example.com/testproj/cmd/testproj/cmd"
)

func main() {
	cmd.Execute()
}
`

// TestWireServiceIntoTree_EditsPristineMain is the end-to-end of the fix: the
// scaffold verb's wiring step actually edits main.go rather than printing an
// instruction the caller has to carry out by hand.
func TestWireServiceIntoTree_EditsPristineMain(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)
	markLegacyComposition(t, dir)
	seedCmdTree(t, dir, "testproj", wiringMain, "orders")

	cfg := readTestConfig(t, dir)
	wireServiceIntoTree(cfg, dir, "orders")

	out := readTestFile(t, filepath.Join(dir, "cmd", "testproj", "main.go"))
	if !strings.Contains(out, "services.NewOrdersCmd") {
		t.Errorf("main.go was not wired:\n%s", out)
	}
	if !strings.Contains(out, `"example.com/testproj/cmd/testproj/cmd/services"`) {
		t.Errorf("main.go did not gain the services import:\n%s", out)
	}
}

// TestWireServiceIntoTree_LeavesUnrecognizedMainAlone is the ownership floor
// at the verb level: a main.go forge cannot append to must come back byte-
// identical, with the user left to make the edit themselves.
func TestWireServiceIntoTree_LeavesUnrecognizedMainAlone(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	const handRolled = `package main

// I build the tree myself.
func main() {
	buildMyOwnTree().Run()
}
`
	seedCmdTree(t, dir, "testproj", handRolled, "orders")

	cfg := readTestConfig(t, dir)
	wireServiceIntoTree(cfg, dir, "orders")

	if out := readTestFile(t, filepath.Join(dir, "cmd", "testproj", "main.go")); out != handRolled {
		t.Errorf("forge rewrote a main.go it does not understand:\ngot:\n%s\nwant:\n%s", out, handRolled)
	}
}

// TestWireServiceIntoTree_ReservedNameIsSkipped: a service whose runtime name
// collides with a built-in (server/version/db/…) gets no subcommand at all, so
// there is nothing to wire and main.go must not be touched.
func TestWireServiceIntoTree_ReservedNameIsSkipped(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)
	markLegacyComposition(t, dir)
	seedCmdTree(t, dir, "testproj", wiringMain)

	cfg := readTestConfig(t, dir)
	wireServiceIntoTree(cfg, dir, "db")

	if out := readTestFile(t, filepath.Join(dir, "cmd", "testproj", "main.go")); out != wiringMain {
		t.Errorf("a reserved-name service still edited main.go:\n%s", out)
	}
}

// TestWireServiceIntoTree_IsIdempotent covers the --resume/--force re-run: a
// second wiring pass must not append a duplicate argument.
func TestWireServiceIntoTree_IsIdempotent(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)
	markLegacyComposition(t, dir)
	seedCmdTree(t, dir, "testproj", wiringMain, "orders")

	cfg := readTestConfig(t, dir)
	mainPath := filepath.Join(dir, "cmd", "testproj", "main.go")

	wireServiceIntoTree(cfg, dir, "orders")
	once := readTestFile(t, mainPath)
	wireServiceIntoTree(cfg, dir, "orders")
	twice := readTestFile(t, mainPath)

	if once != twice {
		t.Errorf("second wiring pass changed main.go:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
	if n := strings.Count(twice, "services.NewOrdersCmd"); n != 1 {
		t.Errorf("constructor appears %d times, want exactly 1:\n%s", n, twice)
	}
}

func readTestFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func readTestConfig(t *testing.T, root string) *config.ProjectConfig {
	t.Helper()
	cfg, err := generator.ReadProjectConfig(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	return cfg
}

// markLegacyComposition gives dir the legacy internal/app composition — the
// mode whose per-service subcommands main.go names. A project without it
// composes with the graph and has no per-service subcommands to wire.
func markLegacyComposition(t *testing.T, dir string) {
	t.Helper()
	app := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "providers.go"), []byte("package app\n\ntype Infra struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
