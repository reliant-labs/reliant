package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// checkSource type-checks one import-free file as package reg and returns it
// with the named types the multi-implementation search would consider.
func checkSource(t *testing.T, src string) (*packages.Package, []*types.TypeName) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "registry.go", "package reg\n"+src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{}).Check("example.com/m/internal/reg", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []*types.TypeName
	for _, name := range pkg.Scope().Names() {
		if tn, ok := pkg.Scope().Lookup(name).(*types.TypeName); ok {
			if _, isIface := tn.Type().Underlying().(*types.Interface); !isIface {
				candidates = append(candidates, tn)
			}
		}
	}
	return &packages.Package{PkgPath: pkg.Path(), Types: pkg, TypesInfo: info, Syntax: []*ast.File{f}, Fset: fset}, candidates
}

// Two implementations of Source, registered in different ways.
const twoSources = `
type Source interface{ Load() (int, error) }
type Params map[string]any
type a struct{}
func (a) Load() (int, error) { return 1, nil }
type b struct{}
func (b) Load() (int, error) { return 2, nil }
`

// A registry that registers FACTORIES is the same strategy registry as one
// that registers instances. Red before: only a `Register…(Source)` parameter
// was recognised, so ranger's loadsource (`Register(name string, f
// Factory)`, `type Factory func(Params, Env) (Source, error)`) was refused
// as a multi-implementation contract and needed a lint-disable allowance.
func TestMultiImplInterface_FactoryRegistryIsExempt(t *testing.T) {
	cases := map[string]string{
		"instance":           `func Register(s Source) {}`,
		"named factory":      `type Factory func(p Params) (Source, error); func Register(name string, f Factory) {}`,
		"bare named factory": `type Factory func() Source; func Register(name string, f Factory) {}`,
		"literal factory":    `func Register(name string, f func(Params) (Source, error)) {}`,
		"aliased factory":    `type Factory = func(Params) (Source, error); func Register(name string, f Factory) {}`,
		"variadic":           `func Register(s ...Source) {}`,
		"registry method":    `type Registry struct{}; type Factory func(Params) (Source, error); func (*Registry) RegisterSource(name string, f Factory) {}`,
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			p, candidates := checkSource(t, twoSources+decl)
			if iface, impls, refused := multiImplInterface(p, candidates); refused {
				t.Errorf("strategy registry refused as multi-impl: %s %v", iface, impls)
			}
			if got := strategyRegistryInterface(p); got != "Source" {
				t.Errorf("strategyRegistryInterface = %q, want Source", got)
			}
		})
	}
}

// What is not a registry stays refused: the factory must return the
// interface (alone or with an error), and the func must be an exported
// Register*.
func TestMultiImplInterface_NotARegistryIsRefused(t *testing.T) {
	cases := map[string]string{
		"no registry":            ``,
		"factory returns int":    `func Register(name string, f func() (Source, int)) {}`,
		"factory returns struct": `func Register(name string, f func() a) {}`,
		"factory takes Source":   `func Register(name string, f func(Source)) {}`,
		"not named Register":     `type Factory func(Params) (Source, error); func Add(name string, f Factory) {}`,
		"unexported register":    `type Factory func(Params) (Source, error); func register(name string, f Factory) {}`,
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			p, candidates := checkSource(t, twoSources+decl)
			if iface, _, refused := multiImplInterface(p, candidates); !refused || iface != "Source" {
				t.Errorf("multiImplInterface refused=%v iface=%q, want Source refused", refused, iface)
			}
			if got := strategyRegistryInterface(p); got != "" {
				t.Errorf("strategyRegistryInterface = %q, want none", got)
			}
		})
	}
}

func TestFuncDisplayName(t *testing.T) {
	p, _ := checkSource(t, `
type T struct{}
type G[K any] struct{}
func (*T) Ptr() {}
func (T) Val() {}
func (g *G[K]) Gen() {}
func Free() {}
`)
	var got []string
	for _, decl := range p.Syntax[0].Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			got = append(got, funcDisplayName(fd))
		}
	}
	want := "(*T).Ptr (T).Val (*G).Gen Free"
	if strings.Join(got, " ") != want {
		t.Errorf("funcDisplayName = %q, want %q", strings.Join(got, " "), want)
	}
}

// End to end through the gate, on ranger's loadsource shape: a factory
// registry with //forge:exclude-contract, where ONE registered source makes
// an HTTP call. The multi-impl refusal no longer fires; the outbound-io one
// still does, and its remediation names the implementation and prescribes an
// adapter for that call while the registry KEEPS its exclusion. Red before:
// both refusals fired, and the outbound-io hint said to make the whole
// registry an adapter with a New(Deps) it does not have.
func TestExcludeGate_FactoryRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and type-checks a fixture module (net/http) via go/packages; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/reg\n\ngo 1.24\n",
		"internal/loadsource/doc.go": `// Package loadsource selects a load source by name.
//
//forge:exclude-contract: strategy registry — one factory per load source
package loadsource
`,
		"internal/loadsource/loadsource.go": `package loadsource

import "net/http"

type Source interface{ Load() (float64, error) }

type Env struct{ HTTPClient *http.Client }

type Factory func(params map[string]any, env Env) (Source, error)

var factories = map[string]Factory{}

func Register(name string, f Factory) { factories[name] = f }

type none struct{}

func (none) Load() (float64, error) { return 0, nil }

func init() { Register("none", func(map[string]any, Env) (Source, error) { return none{}, nil }) }
`,
		"internal/loadsource/prometheus.go": `package loadsource

import "net/http"

type prometheus struct{ hc *http.Client; url string }

func (s *prometheus) Load() (float64, error) { return s.fetch() }

func (s *prometheus) fetch() (float64, error) {
	req, err := http.NewRequest(http.MethodGet, s.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, err
	}
	return 1, resp.Body.Close()
}

func init() {
	Register("prometheus", func(p map[string]any, env Env) (Source, error) {
		return &prometheus{hc: env.HTTPClient}, nil
	})
}
`,
		// The same registry with no I/O: accepted outright.
		"internal/purereg/purereg.go": `// Package purereg is a factory registry with no I/O.
//
//forge:exclude-contract: strategy registry — one factory per algorithm
package purereg

type Algo interface{ Run(xs []float64) float64 }

type Factory func(params map[string]any) (Algo, error)

func Register(name string, f Factory) {}

type mean struct{}

func (mean) Run(xs []float64) float64 { return 0 }

type max struct{}

func (max) Run(xs []float64) float64 { return 0 }
`,
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := packages.Load(&packages.Config{
		Dir: dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule,
	}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if n := packages.PrintErrors(pkgs); n != 0 {
		t.Fatalf("fixture has %d load errors", n)
	}
	byPkg := map[string][]ExcludeFinding{}
	for _, f := range CheckExcludeDirectives(pkgs, ExcludeGateOptions{}) {
		byPkg[filepath.Base(filepath.Dir(f.File))] = append(byPkg[filepath.Base(filepath.Dir(f.File))], f)
	}
	if fs := byPkg["purereg"]; len(fs) != 0 {
		t.Errorf("a pure factory registry was refused: %+v", fs)
	}
	fs := byPkg["loadsource"]
	if len(fs) != 1 || fs[0].Rule != RuleExcludeOutboundIO {
		t.Fatalf("loadsource findings = %+v, want exactly one %s", fs, RuleExcludeOutboundIO)
	}
	if !strings.Contains(fs[0].Message, "in (*prometheus).fetch") {
		t.Errorf("the refusal must name the implementation making the call: %s", fs[0].Message)
	}
	for _, want := range []string{"keep `//forge:exclude-contract` on this Source registry", "forge scaffold adapter", "(*prometheus).fetch"} {
		if !strings.Contains(fs[0].FixHint, want) {
			t.Errorf("registry remediation lacks %q: %s", want, fs[0].FixHint)
		}
	}
	if strings.Contains(fs[0].FixHint, "remove `//forge:exclude-contract`") {
		t.Errorf("a registry must not be told to drop its exclusion: %s", fs[0].FixHint)
	}
}
