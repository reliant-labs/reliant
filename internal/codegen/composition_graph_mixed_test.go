package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/imports"

	"github.com/reliant-labs/forge/internal/checksums"
)

const legacyProviders = "package app\n\ntype Infra struct{}\n\nfunc OpenInfra() (*Infra, error) { return &Infra{}, nil }\n"

// forge#2285: a legacy project is mixed only when a root is marked adopt —
// an explicit, per-root opt-in. A root without it stays inert beside the
// legacy composition, as it was.
func TestProjectComposition_AdoptIsThePerRootOptIn(t *testing.T) {
	dir := t.TempDir()
	if ProjectComposition(dir) != CompositionGraph || !GraphComposition(dir) {
		t.Fatal("no internal/app is graph mode")
	}
	writeAppFile(t, dir, "providers.go", legacyProviders)
	writeAppFile(t, dir, "roots.go", "package app\n\n//forge:root tick\ntype TickRoot struct{ Clock *Clock }\n")
	if ProjectComposition(dir) != CompositionLegacy || GraphComposition(dir) {
		t.Fatal("a root that is not adopted leaves a legacy project legacy")
	}
	writeAppFile(t, dir, "roots.go", "package app\n\n//forge:root tick command=tick adopt\ntype TickRoot struct{ Clock *Clock }\n")
	if ProjectComposition(dir) != CompositionMixed {
		t.Fatal("an adopted root makes a legacy project mixed")
	}
	if GraphComposition(dir) {
		t.Fatal("a mixed project is not in graph mode: everything else keeps the legacy behaviour")
	}
	_, roots, err := discoverAppDecls(dir)
	if err != nil || len(roots) != 1 || !roots[0].Adopt || strings.Join(roots[0].Command, " ") != "tick" {
		t.Fatalf("roots = %+v, %v", roots, err)
	}
	// Beside the legacy composition, graph_compose.go is the adopted roots'
	// compose.go: never read for declarations.
	writeAppFile(t, dir, MixedComposeFile, "package app\n\ntype ShadowDeps struct{}\n\nfunc NewShadow(ShadowDeps) (*int, error) { return nil, nil }\n")
	if providers, _, _ := discoverAppDecls(dir); len(providers) != 0 {
		t.Fatalf("%s was read for providers: %+v", MixedComposeFile, providers)
	}
}

func TestNamesLegacyComposition(t *testing.T) {
	for src, want := range map[string]string{
		"func (g *Graph) newA() (*A, error) { i, _ := OpenInfra(); _ = i; return nil, nil }": "OpenInfra",
		"func NewA(d ADeps) (*A, error) { var c *Components; _ = c; return nil, nil }":       "Components",
		"func NewA(d ADeps) (*A, error) { return &A{Infra: d.Infra.Name}, nil }":             "", // fields named Infra are not the composition
		"func (g *Graph) newA() (*A, error) { return a.New(a.Deps{Logger: g.Log}), nil }":    "",
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package app\n"+src, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := namesLegacyComposition(f.Decls[0].(*ast.FuncDecl))
		if got != want {
			t.Errorf("%s: %q, want %q", src, got, want)
		}
	}
}

// forge#2285 review: the legacy check follows the construction's whole call
// closure inside internal/app — a helper, a package-level alias, a type
// alias, another Graph method — not only the one body.
func TestLegacyReach_FollowsIndirectConstruction(t *testing.T) {
	const legacy = `package app

type Infra struct{}
type Components struct{}
type Graph struct{}

func OpenInfra() (*Infra, error) { return &Infra{}, nil }
func NewComponents(*Infra) (*Components, error) { return &Components{}, nil }
`
	const providers = `package app

type A struct{}
type ADeps struct{}

func openShared() error { _, err := OpenInfra(); return err }
func viaHelper() error { return openShared() }
func NewHelper(ADeps) (*A, error) { return &A{}, viaHelper() }

var mk = NewComponents
func NewAliased(ADeps) (*A, error) { _, err := mk(nil); return &A{}, err }

type C = Components
func NewTypeAlias(ADeps) (*A, error) { var c *C; _ = c; return &A{}, nil }

func ping() error { return pong() }
func pong() error { return ping() }
func NewClean(ADeps) (*A, error) { return &A{}, ping() }

func (g *Graph) buildOnInfra() error { return openShared() }
`
	const draft = `package app

func (g *Graph) newDirect() (*A, error) { return NewHelper(ADeps{}) }
func (g *Graph) newViaMethod() (*A, error) { return &A{}, g.buildOnInfra() }
func (g *Graph) newViaDraft() (*A, error) { return g.newDirect() }
func (g *Graph) newClean() (*A, error) { return NewClean(ADeps{}) }
`
	fset := token.NewFileSet()
	var files []*ast.File
	for name, src := range map[string]string{"legacy.go": legacy, "providers.go": providers} {
		f, err := parser.ParseFile(fset, filepath.Join("internal", "app", name), src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{}).Check("example.com/test/internal/app", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	df, err := parser.ParseFile(token.NewFileSet(), MixedComposeFile, draft, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := newLegacyReach(fset, files, pkg, info, graphComposeMethods(df))
	for name, want := range map[string]string{
		"NewHelper":    "viaHelper → openShared → OpenInfra",
		"NewAliased":   "mk → NewComponents",
		"NewTypeAlias": "C → Components",
		"NewClean":     "",
	} {
		if got := r.provider(name); got != want {
			t.Errorf("provider %s: %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"newDirect":    "NewHelper → viaHelper → openShared → OpenInfra",
		"newViaMethod": "g.buildOnInfra → openShared → OpenInfra",
		"newViaDraft":  "g.newDirect → NewHelper → viaHelper → openShared → OpenInfra",
		"newClean":     "",
	} {
		if got := r.method(name); got != want {
			t.Errorf("method %s: %q, want %q", name, got, want)
		}
	}
}

// An adopted root's graph holds exactly what it reaches: the rest of the
// project is the legacy composition's.
func TestOutOfMixedReach_LeavesOutEverythingUnreached(t *testing.T) {
	nodes, r := graphFixture()
	m := &graphModel{nodes: nodes, byName: map[string]*graphNode{}, u: r.u}
	for _, n := range nodes {
		m.byName[n.Name] = n
	}
	root := graphRoot{Name: "store", TypeName: "StoreRoot", Adopt: true, Fields: []graphRootField{{Name: "Store", Type: "*Store"}}}
	unbuilt, err := m.outOfMixedReach([]graphRoot{root}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := unbuilt["Alpha"]; !out {
		t.Errorf("Alpha, which the root does not reach, was kept: %v", unbuilt)
	}
	if _, out := unbuilt["Store"]; out {
		t.Errorf("Store, the root's own field, was left out: %v", unbuilt)
	}
	if root.Fields[0].Node != nil {
		t.Error("the draft resolved the declared root's fields in place")
	}
	// A problem on a reached node fails, as it does in graph mode.
	if _, err := m.outOfMixedReach([]graphRoot{root}, "", []graphProblem{{Node: "Store", Field: "Ctx", Type: "context.Context"}}); err == nil {
		t.Error("a reached node's missing provider passed")
	}
}

// End to end: a legacy project with one adopted root gets that root's graph
// beside the legacy composition — its constructor in graph_compose.go, a
// graph of only what it reaches, compiling — and the legacy files stay
// untouched. A root whose closure builds the legacy composition is refused.
func TestGenerateGraph_MixedBuildsOnlyTheAdoptedRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("graph assembly loads Go packages and the test builds the emitted graph; runs in task test")
	}
	for _, mode := range []string{"clean", "reaches-legacy", "reaches-legacy-indirectly"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, src string) {
				t.Helper()
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			forgeDir, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			write("go.mod", fmt.Sprintf("module example.com/test\n\ngo 1.27.0\n\nrequire github.com/reliant-labs/forge v1.7.0\nreplace github.com/reliant-labs/forge => %s\n", filepath.ToSlash(forgeDir)))
			write("pkg/config/config.go", "package config\ntype Config struct{}\n")
			write("internal/workers/ticker/worker.go", `package ticker
import "context"
type Deps struct{}
type Worker struct{}
func New(Deps) *Worker { return &Worker{} }
func (*Worker) Run(context.Context) error { return nil }
`)
			write("internal/workers/other/worker.go", `package other
import "context"
type Deps struct{}
type Worker struct{}
func New(Deps) *Worker { return &Worker{} }
func (*Worker) Run(context.Context) error { return nil }
`)
			legacy := "package app\n\nimport \"example.com/test/internal/workers/ticker\"\n\ntype Infra struct{}\n\ntype Components struct{ Ticker *ticker.Worker }\n\nfunc NewComponents(infra *Infra) (*Components, error) { return &Components{Ticker: ticker.New(ticker.Deps{})}, nil }\n"
			write("internal/app/compose.go", legacy)
			clock := "package app\n\ntype Clock struct{}\ntype ClockDeps struct{}\n\nfunc NewClock(ClockDeps) (*Clock, error) { return &Clock{}, nil }\n"
			if mode == "reaches-legacy" {
				clock = "package app\n\ntype Clock struct{}\ntype ClockDeps struct{}\n\nfunc NewClock(ClockDeps) (*Clock, error) { _, err := NewComponents(&Infra{}); return &Clock{}, err }\n"
			}
			if mode == "reaches-legacy-indirectly" {
				// Through a helper and a package-level alias: no legacy name in NewClock itself.
				clock = "package app\n\ntype Clock struct{}\ntype ClockDeps struct{}\n\nvar build = NewComponents\n\nfunc shared() error { _, err := build(&Infra{}); return err }\n\nfunc NewClock(ClockDeps) (*Clock, error) { return &Clock{}, shared() }\n"
			}
			write("internal/app/clock.go", clock)
			write("internal/app/roots.go", `package app

import "example.com/test/internal/workers/ticker"

// TickRoot runs the ticker on the graph.
//
//forge:root tick command=tick adopt
type TickRoot struct {
	Clock  *Clock
	Ticker *ticker.Worker
}
`)
			if ProjectComposition(dir) != CompositionMixed {
				t.Fatal("fixture is not mixed")
			}
			workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
			if err != nil {
				t.Fatal(err)
			}
			in := GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/test", Checksums: &checksums.FileChecksums{}}, Workers: workers, Warnings: &strings.Builder{}, Mixed: true}
			err = GenerateGraph(in)
			if mode != "clean" {
				if err == nil || !strings.Contains(err.Error(), "root tick reaches Clock") || !strings.Contains(err.Error(), "NewComponents") {
					t.Fatalf("a root that builds the legacy composition must be refused, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			read := func(name string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			if read("internal/app/compose.go") != legacy {
				t.Fatal("the legacy compose.go was rewritten")
			}
			mixed := read("internal/app/" + MixedComposeFile)
			for _, want := range []string{"func (g *Graph) newTicker() (*ticker.Worker, error)", "func (g *Graph) newClock() (*Clock, error)"} {
				if !strings.Contains(mixed, want) {
					t.Errorf("%s lacks %q:\n%s", MixedComposeFile, want, mixed)
				}
			}
			graph := read("internal/app/graph_gen.go")
			if !strings.Contains(graph, "RunTickRoot") {
				t.Errorf("graph_gen.go does not run the adopted root:\n%s", graph)
			}
			if strings.Contains(graph, "other.") || strings.Contains(mixed, "other.") || strings.Contains(graph, "type Server struct") {
				t.Errorf("the graph holds what the adopted root does not reach (or a default root):\n%s\n%s", graph, mixed)
			}
			// Typecheck the whole package: legacy and graph side by side.
			for _, name := range []string{"graph_gen.go", "routes_gen.go"} {
				clean, err := imports.Process(filepath.Join(dir, "internal/app", name), []byte(read("internal/app/"+name)), nil)
				if err != nil {
					t.Fatal(err)
				}
				write("internal/app/"+name, string(clean))
			}
			cmd := exec.Command("go", "build", "-mod=mod", "./internal/app/")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build the mixed package: %v\n%s", err, out)
			}
			// A second run converges.
			if err := GenerateGraph(in); err != nil {
				t.Fatal(err)
			}
			if read("internal/app/"+MixedComposeFile) != mixed {
				t.Error("a second generate changed graph_compose.go")
			}
		})
	}
}
