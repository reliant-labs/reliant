package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/imports"

	"github.com/reliant-labs/forge/internal/checksums"
)

// Exercise normal discovery/assembly, including the generated Operators node,
// rather than the owned-only reconciliation scope covered by #2185.
func TestGenerateGraph_DisownedComposeConstructorReach(t *testing.T) {
	if testing.Short() {
		t.Skip("normal graph assembly loads Go packages; runs in task test")
	}
	for _, mode := range []string{"unreachable", "reached", "lazy", "aggregate", "default"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, src string) {
				t.Helper()
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(src), 0644); err != nil {
					t.Fatal(err)
				}
			}
			forgeDir, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			write("go.mod", fmt.Sprintf("module example.com/test\n\ngo 1.27.0\n\nrequire github.com/reliant-labs/forge v1.7.0\nreplace github.com/reliant-labs/forge => %s\n", filepath.ToSlash(forgeDir)))
			write("pkg/config/config.go", "package config\ntype Config struct{}\n")
			write("internal/operators/fleet/operator.go", `package fleet
import "context"
type Deps struct{}
type Controller struct{}
func New(Deps) *Controller { return &Controller{} }
func (*Controller) Run(context.Context) error { return nil }
`)
			write("internal/operators/shared/helper.go", "package shared\ntype Controller struct{}\n")
			compose := `package app
import (
 "example.com/test/internal/operators/fleet"
)
func (g *Graph) newFleet() (*fleet.Controller, error) { return fleet.New(fleet.Deps{}), nil }
func (g *Graph) newRunner() (*Runner, error) { _ = g.Fleet(); return &Runner{}, nil }
func (g *Graph) newUserRoutes() (*UserRoutes, error) { return NewUserRoutes(UserRoutesDeps{Logger: g.Log}) }
`
			if mode == "reached" {
				compose += "func (g *Graph) newBad() (*Bad, error) { _ = g.Shared(); return &Bad{}, nil }\n"
			}
			if mode == "lazy" {
				compose += "func (g *Graph) newBad() (*Bad, error) { _ = g.Shared; return &Bad{}, nil }\n"
			}
			if mode != "reached" && mode != "lazy" {
				compose += "func (g *Graph) newBad() (*Bad, error) { return &Bad{}, nil }\n"
			}
			// Generation must preserve the owned expressions, not repair their AST.
			write("internal/app/compose.go", compose)
			t.Cleanup(func() {
				b, err := os.ReadFile(filepath.Join(dir, "internal/app/compose.go"))
				if err != nil || string(b) != compose {
					t.Errorf("disowned compose.go changed after generation: %v", err)
				}
			})
			decls := `package app
import "example.com/test/internal/operators/shared"
type Runner struct{}
type RunnerDeps struct{}
func NewRunner(RunnerDeps) *Runner { return nil }
type Bad struct{}
type BadDeps struct{}
func NewBad(BadDeps) *Bad { return nil }
var _ *shared.Controller
`
			switch mode {
			case "unreachable":
				decls += "//forge:root run\ntype RunRoot struct { Runner *Runner }\n"
			case "reached", "lazy":
				decls += "//forge:root run\ntype RunRoot struct { Bad *Bad }\n"
			case "aggregate":
				decls += "//forge:root run\ntype RunRoot struct { Operators *Operators }\n"
			}
			write("internal/app/roots.go", decls)
			ops, err := OperatorDataFromSpecs(DiscoverOperatorSpecs(dir), dir)
			if err != nil {
				t.Fatal(err)
			}
			in := GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/test", Checksums: &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{"internal/app/compose.go": {Reason: "owned composition"}}}}, Operators: ops, Warnings: &strings.Builder{}}
			err = GenerateGraph(in)
			if mode != "unreachable" {
				if err == nil || !strings.Contains(err.Error(), "internal/operators/shared") {
					t.Fatalf("selected malformed operator must fail: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unreachable helper blocked normal generation: %v", err)
			}
			read := func(name string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			graph := read("internal/app/graph_gen.go")
			for _, absent := range []string{"shared", "Shared", "type Operators struct"} {
				if strings.Contains(graph, absent) {
					t.Errorf("generated graph references omitted helper/aggregate %q", absent)
				}
			}
			for _, present := range []string{"g.newFleet", "Run: c.Run", "Name: \"fleet\", Run: true"} {
				if !strings.Contains(graph, present) {
					t.Errorf("valid reached operator lost %q", present)
				}
			}
			if err := GenerateGraph(in); err != nil {
				t.Fatal(err)
			}
			if read("internal/app/compose.go") != compose {
				t.Fatal("disowned compose.go rewritten")
			}
			if read("internal/app/graph_gen.go") != graph {
				t.Fatal("second generation did not converge")
			}
			// Typecheck the emitted graph with the real graphkit/lifecyclekit
			// APIs, after the normal pipeline's import-cleanup step.
			for _, name := range []string{"graph_gen.go", "routes_gen.go"} {
				clean, err := imports.Process(filepath.Join(dir, "internal/app", name), []byte(read("internal/app/"+name)), nil)
				if err != nil {
					t.Fatal(err)
				}
				write("internal/app/"+name, string(clean))
			}
			cmd := exec.Command("go", "build", "-mod=mod", "graph_gen.go", "routes_gen.go", "compose.go", "roots.go")
			cmd.Dir = filepath.Join(dir, "internal/app")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build emitted graph: %v\n%s", err, out)
			}
		})
	}
}
