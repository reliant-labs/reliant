package templates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// TestScaffoldRenderSmoke is the merge tier's one proof that a fresh
// scaffold still renders: it scaffolds a service project, writes its
// deploy/kcl/config_gen.k and per-env config.k with the production writers
// `forge generate` calls, and renders dev and prod through the native KCL
// runtime — all under -short.
//
// It exists because every other scaffold→render test is full-mode only, so a
// template or codegen change that breaks the render first ran on main: on
// 2026-11-21/22 main went red three times that way, once because #2683 grew
// project/config-roots.k.tmpl a `.Process` field that a test's hand-built
// data lacked. Here the template's data comes from the writer generate uses
// (codegen.GenerateConfigNativeSharedRoots → graphRootsKCL → RootsKCLFor), so
// a template field the real data does not carry fails this test in the merge
// queue. It asserts a few structural facts and no more; the full-mode tests
// (scaffold_hosted_envs_test.go, scaffold_kcl_eval_test.go) own the detail.
//
// It is cache-sound and execs nothing. Every file it renders is written into
// t.TempDir() by this process: the scaffold (from go:embedded templates, with
// GoVersionOverride set so Generate never runs `go env GOVERSION`) and the
// config modules. kclrender evaluates in process, and supplies `import forge`
// from the KCL module embedded in this binary (internal/kclvendor), which it
// materializes once into a directory keyed on a hash of that embedded
// content — a cache of bytes the test binary already holds, never a
// repository file Go's test cache cannot see.
func TestScaffoldRenderSmoke(t *testing.T) {
	root := t.TempDir()
	const name = "smoke"
	g := generator.NewProjectGenerator(name, root, "example.com/"+name)
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	g.ServiceName = "orders"
	g.GoVersionOverride = "1.27.0"
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !codegen.GraphComposition(root) {
		t.Fatal("a fresh scaffold is not a composition-graph project; config_gen.k would carry no ROOTS block to render")
	}

	// The config set generate projects when no descriptor has been built
	// yet, which mirrors the scaffolded config.proto: PORT, and the
	// sensitive, required DATABASE_URL among them.
	fields := codegen.DefaultConfigMessages()[0].Fields
	kclDir := filepath.Join(root, "deploy", "kcl")
	if err := codegen.GenerateConfigNativeSharedRoots(fields, nil, name, root, kclDir, nil); err != nil {
		t.Fatalf("write config_gen.k: %v", err)
	}
	for _, env := range []string{"dev", "prod"} {
		if _, err := codegen.GenerateConfigKScaffold(fields, name, kclDir, env); err != nil {
			t.Fatalf("write %s/config.k: %v", env, err)
		}
	}
	if gen, err := os.ReadFile(filepath.Join(kclDir, "config_gen.k")); err != nil {
		t.Fatal(err)
	} else if !containsAll(string(gen), "ROOTS", "root_of", "workloadEnvMap") {
		t.Fatalf("config_gen.k carries no ROOTS block:\n%s", gen)
	}

	// The default `server` root's workload: bound through root_of, and its
	// env built by workloadEnvMap from what that root reaches.
	for _, env := range []string{"dev", "prod"} {
		api := findWorkload(t, renderEnvOutput(t, root, env), "api", env)
		spec, _ := api["spec"].(map[string]any)
		if args, _ := spec["args"].([]any); len(args) != 1 || args[0] != "server" {
			t.Errorf("%s: workload api runs %#v, want the server root's args [server]", env, spec["args"])
		}
		names := map[string]bool{}
		envList, _ := spec["env"].([]any)
		for _, e := range envList {
			if m, ok := e.(map[string]any); ok {
				n, _ := m["name"].(string)
				names[n] = true
			}
		}
		for _, want := range []string{"PORT", "DATABASE_URL"} {
			if !names[want] {
				t.Errorf("%s: workload api's env has no %s: %#v", env, want, spec["env"])
			}
		}
	}
}

// findWorkload returns the rendered workload called name, failing when the
// env rendered none.
func findWorkload(t *testing.T, out map[string]any, name, env string) map[string]any {
	t.Helper()
	ws, _ := out["workloads"].([]any)
	for _, w := range ws {
		if m, ok := w.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	t.Fatalf("%s rendered no %s workload: %#v", env, name, out["workloads"])
	return nil
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
