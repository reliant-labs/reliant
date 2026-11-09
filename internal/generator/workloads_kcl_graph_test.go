package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// TestScaffoldWorkloadsKCL_GraphDeclaresNoServiceWorkload pins bug 42f7f65b:
// `forge project new --service orders --service payments` declared
// `args = ["orders"]` / `["payments"]` workloads, but a project on the
// composition graph has one command per root (`server`) and none per
// service, so a pod running one crash-looped on "unknown command". On the
// graph no service or worker is declared (every env's `_api` runs them);
// on the legacy composition, whose binary has a subcommand per component,
// each still is.
func TestScaffoldWorkloadsKCL_GraphDeclaresNoServiceWorkload(t *testing.T) {
	components := codegen.Inventory{
		{Name: "orders", Kind: config.ComponentKindServer},
		{Name: "payments", Kind: config.ComponentKindServer},
		{Name: "reaper", Kind: config.ComponentKindWorker},
	}

	graph := t.TempDir()
	if err := ScaffoldWorkloadsKCL(graph, "github.com/acme/demo", "demo", components, true); err != nil {
		t.Fatal(err)
	}
	got := readWorkloadsK(t, graph)
	declared := codegen.DeclaredWorkloads(got)
	for _, name := range []string{"orders", "payments", "reaper"} {
		if w, ok := declared[name]; ok {
			t.Errorf("graph project declares workload %q (args %v), a command its binary does not have", name, w.Args)
		}
	}
	if _, ok := declared["migrate"]; !ok || !strings.Contains(got, "ALL: [fw.Workload] = [migrate]") {
		t.Errorf("graph project lost its migrate job:\n%s", got)
	}
	if !strings.Contains(got, `config_gen.root_of("billing")`) {
		t.Errorf("graph workloads.k does not say how to split a service out as a root:\n%s", got)
	}

	legacy := t.TempDir()
	app := filepath.Join(legacy, "internal", "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "compose.go"), []byte("package app\n\ntype Infra struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ScaffoldWorkloadsKCL(legacy, "github.com/acme/demo", "demo", components, true); err != nil {
		t.Fatal(err)
	}
	declared = codegen.DeclaredWorkloads(readWorkloadsK(t, legacy))
	for _, name := range []string{"orders", "payments", "reaper"} {
		if w := declared[name]; len(w.Args) != 1 || w.Args[0] != name {
			t.Errorf("legacy project: workload %q = %+v, want args [%q] (its own subcommand)", name, w, name)
		}
	}
}

func readWorkloadsK(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, codegen.WorkloadsKCLRelPath))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
