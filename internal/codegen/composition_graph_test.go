package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// squash collapses runs of spaces, so an assertion does not depend on how
// gofmt aligned a composite literal.
func squash(s string) string {
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func writeAppFile(t *testing.T, dir, name, src string) {
	t.Helper()
	app := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGraphComposition_LegacyIsDetectedByItsDeclarations(t *testing.T) {
	dir := t.TempDir()
	if !GraphComposition(dir) {
		t.Fatal("a project with no internal/app is not in graph mode")
	}
	writeAppFile(t, dir, "providers.go", "package app\n\ntype DB struct{}\n\ntype DBDeps struct{}\n\nfunc NewDB(DBDeps) (*DB, error) { return nil, nil }\n")
	if !GraphComposition(dir) {
		t.Fatal("provider funcs are graph mode")
	}
	writeAppFile(t, dir, "providers.go", "package app\n\ntype Infra struct{}\n")
	if GraphComposition(dir) {
		t.Fatal("type Infra struct is the legacy composition")
	}
	writeAppFile(t, dir, "providers.go", "package app\n")
	writeAppFile(t, dir, "compose.go", "package app\n\ntype Components struct{}\n\nfunc NewComponents() (*Components, error) { return nil, nil }\n")
	if GraphComposition(dir) {
		t.Fatal("func NewComponents is the legacy composition")
	}
}

func TestDiscoverAppDecls_ProvidersAndRoots(t *testing.T) {
	dir := t.TempDir()
	writeAppFile(t, dir, "providers.go", `package app

import (
	"net/http"

	"example.com/x/internal/workers/tick"
)

type DB struct{}
type DBDeps struct {
	Logger *slog.Logger // forge:optional-dep
}
func NewDB(d DBDeps) (*DB, error) { return nil, nil }

type HTTPClientDeps struct{}
func NewHTTPClient(HTTPClientDeps) *http.Client { return nil }

// NewNotAProvider takes something other than its Deps.
func NewNotAProvider(x int) (*DB, error) { return nil, nil }

//forge:root worker
type Worker struct {
	Tick *tick.Worker
}

// API is the user root.
//
//forge:root
type APIRoot struct{ Listener *APIListener }
`)
	providers, roots, err := discoverAppDecls(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0].Name != "DB" || !providers[0].Fallible || providers[1].Name != "HTTPClient" || providers[1].Fallible {
		t.Fatalf("providers = %+v", providers)
	}
	if providers[0].Deps[0].Name != "Logger" || !providers[0].Deps[0].Optional {
		t.Fatalf("DB deps = %+v", providers[0].Deps)
	}
	if imps, unknown := typeExprImports(providers[1].Result, providers[1].Imports); imps["http"] != "net/http" || len(unknown) != 0 {
		t.Fatalf("result imports = %v %v", imps, unknown)
	}
	if len(roots) != 2 || roots[0].Name != "worker" || roots[0].TypeName != "Worker" || roots[0].Fields[0].Type != "*tick.Worker" ||
		roots[1].Name != "api-root" || roots[1].Fields[0].Name != "Listener" {
		t.Fatalf("roots = %+v", roots)
	}
}

// lifecycleFixture type-checks src (package fx, stdlib imports only) and
// returns the named type's pointer.
func lifecycleFixture(t *testing.T, src, name string) types.Type {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fx.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A stand-in "context" package: the detection compares the parameter's
	// qualified type, so the real one's export data is not needed.
	cf, err := parser.ParseFile(fset, "context.go", "package context\n\ntype Context interface{ Done() <-chan struct{} }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctxPkg, err := (&types.Config{}).Check("context", fset, []*ast.File{cf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	imp := importerFunc(func(path string) (*types.Package, error) {
		if path == "context" {
			return ctxPkg, nil
		}
		return nil, os.ErrNotExist
	})
	pkg, err := (&types.Config{Importer: imp}).Check("fx", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return types.NewPointer(pkg.Scope().Lookup(name).Type())
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func TestLifecycleOf_ReadsTheFourShapesOffTheType(t *testing.T) {
	src := `package fx

import "context"

type Listener struct{}
func (*Listener) Run(context.Context) error   { return nil }
func (*Listener) Drain(context.Context) error { return nil }
func (*Listener) Close(context.Context) error { return nil }
func (*Listener) Ready(context.Context) error { return nil }

type Pool struct{}
func (*Pool) Close() error                  { return nil } // not the lifecycle shape
func (*Pool) Ready(context.Context) error   { return nil }

type Worker struct{}
func (*Worker) Name() string                 { return "w" }
func (*Worker) Start(context.Context) error  { return nil }
func (*Worker) Stop(context.Context) error   { return nil }

type CtxWorker struct{ Worker }
func (*CtxWorker) RunContext(context.Context) error { return nil }

type Handler struct{}
func (*Handler) Start(context.Context) error { return nil } // not a Worker: no Name/Stop
`
	cases := map[string]lifecycle{
		"Listener":  {Run: "Run", Drain: "Drain", Close: "Close", Ready: "Ready"},
		"Pool":      {Ready: "Ready"},
		"Worker":    {Run: "Start", Close: "Stop"},
		"CtxWorker": {Run: "RunContext", Close: "Stop"},
		"Handler":   {},
	}
	for name, want := range cases {
		if got := lifecycleOf(lifecycleFixture(t, src, name)); got != want {
			t.Errorf("%s: lifecycle %+v, want %+v", name, got, want)
		}
	}
}

// graphFixture is a set of nodes with no type information: resolution runs
// on the written-type fallback, the path a project mid-edit takes.
func graphFixture() ([]*graphNode, *graphResolver) {
	alpha := &graphNode{
		Name: "Alpha", Doc: "the alpha handler", Kind: nodeComponent,
		Type: "*alpha.Service", Imports: map[string]string{"alpha": "example.com/p/internal/handlers/alpha"},
		Ctor: "alpha.New", DepsType: "alpha.Deps", DepsImport: "example.com/p/internal/handlers/alpha", Fallible: true,
		Deps:     []DepsField{{Name: "Logger", Type: "*slog.Logger"}, {Name: "Store", Type: "*app.Store"}, {Name: "Clock", Type: "func() time.Time"}},
		DepsKeys: map[string]bool{"Logger": true, "Store": true, "Clock": true}, DepsFound: true,
		Comp: &BuildComponent{Alias: "alpha", compImports: map[string]string{"app": "example.com/p/internal/app"}},
	}
	store := &graphNode{
		Name: "Store", Doc: "the store provider", Kind: nodeProvider, Type: "*Store",
		Ctor: "NewStore", DepsType: "StoreDeps", Fallible: true,
		Deps:     []DepsField{{Name: "Ctx", Type: "context.Context"}},
		DepsKeys: map[string]bool{"Ctx": true}, DepsFound: true, SrcImports: map[string]string{},
	}
	nodes := []*graphNode{alpha, store}
	r := &graphResolver{module: "example.com/p", nodes: nodes, u: loadGraphUniverse("/nonexistent", nil)}
	return nodes, r
}

func TestReconcileGraphCompose_FreshFileResolvesEveryKey(t *testing.T) {
	nodes, r := graphFixture()
	// The consumer writes the provider's type from package app's view; the
	// fallback compares import-path-qualified spellings, so make the
	// provider's type comparable.
	nodes[0].Deps[1].Type = "*Store"
	out, problems, err := reconcileGraphCompose("", nodes, r)
	if err != nil || len(problems) > 0 {
		t.Fatalf("err=%v problems=%+v", err, problems)
	}
	for _, want := range []string{
		"func (g *Graph) newAlpha() (*alpha.Service, error) {",
		"return alpha.New(alpha.Deps{",
		"Logger: g.Log,",
		"Store:  g.Store(),",
		"Clock:  time.Now, // framework clock",
		"func (g *Graph) newStore() (*Store, error) {",
		"Ctx: g.Ctx,",
		`"example.com/p/internal/handlers/alpha"`,
		`"time"`,
	} {
		if !strings.Contains(squash(out), squash(want)) {
			t.Errorf("compose.go lacks %q:\n%s", want, out)
		}
	}
}

func TestReconcileGraphCompose_OwnedValuesAreNeverTouched(t *testing.T) {
	nodes, r := graphFixture()
	nodes[0].Deps[1].Type = "*Store"
	nodes[0].DepsKeys = map[string]bool{"Logger": true, "Store": true, "Clock": true}
	existing := graphComposeHeader + `
import (
	"log/slog"

	"example.com/p/internal/handlers/alpha"
)

// newAlpha is mine.
func (g *Graph) newAlpha() (*alpha.Service, error) {
	return alpha.New(alpha.Deps{Logger: slog.Default(), Gone: 1})
}

// newStore is mine too, with no Deps literal of its type.
func (g *Graph) newStore() (*Store, error) {
	return &Store{}, nil
}
`
	out, problems, err := reconcileGraphCompose(existing, nodes, r)
	if err != nil || len(problems) > 0 {
		t.Fatalf("err=%v problems=%+v", err, problems)
	}
	for _, want := range []string{"Logger: slog.Default(),", "Store:  g.Store(),", "Clock:  time.Now,", "return &Store{}, nil"} {
		if !strings.Contains(squash(out), squash(want)) {
			t.Errorf("compose.go lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Gone") {
		t.Errorf("the stale key survived:\n%s", out)
	}
	if strings.Count(out, "func (g *Graph) newStore()") != 1 {
		t.Errorf("newStore was rewritten:\n%s", out)
	}
}

func TestReconcileGraphCompose_AmbiguityIsAnErrorUntilTheFileChooses(t *testing.T) {
	nodes, r := graphFixture()
	nodes[0].Deps[1].Type = "*Store"
	other := &graphNode{Name: "OtherStore", Kind: nodeProvider, Type: "*Store", Ctor: "NewOtherStore", DepsType: "OtherStoreDeps", DepsFound: true, DepsKeys: map[string]bool{}, SrcImports: map[string]string{}}
	nodes = append(nodes, other)
	r.nodes = nodes
	_, problems, err := reconcileGraphCompose("", nodes, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].Field != "Store" || strings.Join(problems[0].Candidates, ",") != "g.OtherStore(),g.Store()" {
		t.Fatalf("problems = %+v", problems)
	}
	msg := graphProblemsError(problems).Error()
	if !strings.Contains(msg, "Alpha.Deps.Store (*Store) could be g.OtherStore() or g.Store()") {
		t.Fatalf("error does not name the choice:\n%s", msg)
	}

	// The author picks in compose.go; forge keeps it and fills the rest.
	chosen := graphComposeHeader + `
import "example.com/p/internal/handlers/alpha"

func (g *Graph) newAlpha() (*alpha.Service, error) {
	return alpha.New(alpha.Deps{
		Store: g.OtherStore(),
	})
}
`
	out, problems, err := reconcileGraphCompose(chosen, nodes, r)
	if err != nil || len(problems) > 0 {
		t.Fatalf("err=%v problems=%+v", err, problems)
	}
	if !strings.Contains(squash(out), "Store: g.OtherStore(),") || !strings.Contains(squash(out), "Logger: g.Log,") {
		t.Fatalf("compose.go:\n%s", out)
	}
}

// An optional key forge filled with its "no provider" placeholder is forge's,
// not the author's: once a producer exists it is resolved, while a nil the
// author wrote (no placeholder comment) stays nil.
func TestReconcileGraphCompose_AnOptionalPlaceholderIsFilledOnceAProducerExists(t *testing.T) {
	nodes, r := graphFixture()
	nodes[0].Deps[1].Type = "*Store"
	nodes[0].Deps[1].Optional = true
	nodes[0].Deps = append(nodes[0].Deps, DepsField{Name: "Mine", Type: "*Store", Optional: true})
	nodes[0].DepsKeys["Mine"] = true
	existing := graphComposeHeader + `
import "example.com/p/internal/handlers/alpha"

func (g *Graph) newAlpha() (*alpha.Service, error) {
	return alpha.New(alpha.Deps{
		Logger: g.Log,
		Store:  nil, // optional — no provider
		Clock:  nil, // optional — no provider
		Mine:   nil,
	})
}
`
	out, problems, err := reconcileGraphCompose(existing, nodes, r)
	if err != nil || len(problems) > 0 {
		t.Fatalf("err=%v problems=%+v", err, problems)
	}
	for _, want := range []string{"Store: g.Store(),", "Clock: time.Now, // framework clock", "Mine: nil,"} {
		if !strings.Contains(squash(out), squash(want)) {
			t.Errorf("compose.go lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no provider") {
		t.Errorf("a placeholder survived its producer:\n%s", out)
	}

	// With still no producer, the placeholder is left exactly as it was.
	nodes[1].Type = "*Other"
	again, _, err := reconcileGraphCompose(existing, nodes, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(squash(again), squash("Store:  nil, // optional — no provider")) {
		t.Errorf("a placeholder with nothing to resolve to was rewritten:\n%s", again)
	}
}

func TestReconcileGraphCompose_MissingProviderNamesTheFuncToWrite(t *testing.T) {
	nodes, r := graphFixture()
	nodes[0].Deps[1].Type = "*Cache"
	_, problems, err := reconcileGraphCompose("", nodes, r)
	if err != nil {
		t.Fatal(err)
	}
	msg := graphProblemsError(problems).Error()
	if !strings.Contains(msg, "Alpha.Deps.Store (*Cache) has no provider") || !strings.Contains(msg, "func NewStore(d StoreDeps) (*Cache, error)") {
		t.Fatalf("error:\n%s", msg)
	}
}

func TestGraphCycle_NamesTheEdges(t *testing.T) {
	nodes := []*graphNode{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	src := `package app

func (g *Graph) newA() (*A, error) { return NewA(ADeps{B: g.B()}) }
func (g *Graph) newB() (*B, error) { return NewB(BDeps{C: g.C()}) }
func (g *Graph) newC() (*C, error) { return NewC(CDeps{A: g.A(), Log: g.Log}) }
`
	edges := graphComposeEdges(src, nodes)
	cycle := graphCycle(nodes, edges)
	if strings.Join(cycle, " → ") != "A → B → C → A" {
		t.Fatalf("cycle = %v (edges %v)", cycle, edges)
	}
	msg := graphCycleError(cycle).Error()
	for _, want := range []string{"A → B → C → A", "newC (compose.go) calls g.A()"} {
		if !strings.Contains(msg, want) {
			t.Errorf("cycle error lacks %q:\n%s", want, msg)
		}
	}
	delete(edges, "C")
	if c := graphCycle(nodes, edges); c != nil {
		t.Fatalf("acyclic graph reported %v", c)
	}
}

func TestDefaultGraphRoot_RunnableSinksInStopOrder(t *testing.T) {
	listener := &graphNode{Name: "APIListener", Kind: nodeProvider, Pos: 2, Type: "*APIListener", Lifecycle: lifecycle{Run: "Run"}}
	pprof := &graphNode{Name: "Pprof", Kind: nodeProvider, Pos: 3, Type: "*serverkit.Pprof", Lifecycle: lifecycle{Run: "Run"}}
	db := &graphNode{Name: "DB", Kind: nodeProvider, Pos: 1, Type: "*DB", Lifecycle: lifecycle{Close: "Close"}}
	loop := &graphNode{Name: "Loop", Kind: nodeProvider, Pos: 4, Type: "*Loop", Lifecycle: lifecycle{Run: "Run"}}
	tick := &graphNode{Name: "Tick", Kind: nodeComponent, Role: "internal/workers", Type: "*tick.Worker", Lifecycle: lifecycle{Run: "Start"}}
	ops := &graphNode{Name: "Operators", Kind: nodeOperators, Type: "*Operators", Lifecycle: lifecycle{Run: "Run"}}
	alpha := &graphNode{Name: "Alpha", Kind: nodeComponent, Role: "internal/handlers", Type: "*alpha.Service"}
	nodes := []*graphNode{alpha, tick, db, listener, pprof, loop, ops}
	edges := map[string][]string{"APIListener": {"Alpha", "DB"}, "Alpha": {"Loop"}}
	r := defaultGraphRoot(nodes, edges)
	var names []string
	for _, f := range r.Fields {
		names = append(names, f.Name)
	}
	// Loop runs but Alpha consumes it, so the API listener's closure reaches
	// it; DB does not run. The first field stops first.
	if got := strings.Join(names, ","); got != "APIListener,Pprof,Tick,Operators" {
		t.Fatalf("default root fields %s", got)
	}
	v := rootView(&r, edges)
	if v.Plan != "operators → tick → pprof → loop → alpha → db → api_listener" {
		t.Fatalf("plan %q", v.Plan)
	}
}

// A field typed func() T is a LAZY edge: forge wires the accessor's method
// value, which breaks a construction cycle, and the root's Build still
// constructs the lazily-used node at boot.
func TestLazyEdge_BreaksTheCycleAndIsBuiltAtBoot(t *testing.T) {
	alpha := &graphNode{Name: "Alpha", Kind: nodeProvider, Type: "*Alpha", Ctor: "NewAlpha", DepsType: "AlphaDeps", Fallible: true,
		Deps: []DepsField{{Name: "Beta", Type: "func() *Beta"}}, DepsKeys: map[string]bool{"Beta": true}, DepsFound: true, SrcImports: map[string]string{}}
	beta := &graphNode{Name: "Beta", Kind: nodeProvider, Type: "*Beta", Ctor: "NewBeta", DepsType: "BetaDeps", Fallible: true,
		Deps: []DepsField{{Name: "Alpha", Type: "*Alpha"}}, DepsKeys: map[string]bool{"Alpha": true}, DepsFound: true, SrcImports: map[string]string{}}
	listener := &graphNode{Name: "Listener", Kind: nodeProvider, Type: "*Listener", Ctor: "NewListener", DepsType: "ListenerDeps", Fallible: true,
		Deps: []DepsField{{Name: "Alpha", Type: "*Alpha"}}, DepsKeys: map[string]bool{"Alpha": true}, DepsFound: true, SrcImports: map[string]string{},
		Lifecycle: lifecycle{Run: "Run"}}
	nodes := []*graphNode{alpha, beta, listener}
	r := &graphResolver{module: "example.com/p", nodes: nodes, u: loadGraphUniverse("/nonexistent", nil)}
	out, problems, err := reconcileGraphCompose("", nodes, r)
	if err != nil || len(problems) > 0 {
		t.Fatalf("err=%v problems=%+v", err, problems)
	}
	if !strings.Contains(squash(out), "Beta: g.Beta, // lazy") {
		t.Fatalf("compose.go:\n%s", out)
	}
	edges, lazy := graphComposeEdgesLazy(out, nodes)
	if c := graphCycle(nodes, edges); c != nil {
		t.Fatalf("a lazy edge was counted as a construction edge: cycle %v", c)
	}
	root := defaultGraphRoot(nodes, edges)
	v := rootViewLazy(&root, edges, lazy)
	if strings.Join(v.Lazy, ",") != "Beta" {
		t.Fatalf("Build does not force the lazily-used Beta: %+v", v)
	}
}

// TestReconcileGraphComposeFile_DropsStaleKeyOfAConstructionNoLongerANode is
// #747 in graph mode. compose.go is owned and only ever added to, so a
// newAlpaca method outlives alpaca's node when the package is marked
// //forge:external-component. The reconcile measured only the nodes'
// literals, so a Deps field removed afterwards left a key naming nothing, and
// every generate failed its validate build and rolled back:
//
//	internal/app/compose.go:108:3: unknown field Region in struct literal of type alpaca.Deps
//
// The dead key goes, the author's construction and its other values stay,
// and the construction is reported with the graph's remedy.
func TestReconcileGraphComposeFile_DropsStaleKeyOfAConstructionNoLongerANode(t *testing.T) {
	cases := []struct {
		name, header, warning string
	}{
		{
			name:    "external-component",
			header:  "//forge:external-component\n",
			warning: "internal/app/compose.go still constructs internal/alpaca, which is now //forge:external-component — delete its new<X> method and import, and supply the instance from a New<X>(<X>Deps) provider func in internal/app/providers.go",
		},
		{
			name:    "not-a-component",
			header:  "//forge:not-a-component: the CLI builds it\n",
			warning: "internal/app/compose.go still constructs internal/alpaca, which is now //forge:not-a-component — delete its new<X> method and import: the server does not have it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newInjectProject(t)
			writeContract(t, dir, "internal/alpaca", tc.header+"package alpaca\n\nimport \"log/slog\"\n\ntype Service interface{ Do() }\n\n"+
				"type Deps struct {\n\tLogger *slog.Logger\n}\n\nfunc New(d Deps) (Service, error) { return nil, nil }\n")
			writeAppFile(t, dir, "compose.go", `package app

import "example.com/proj/internal/alpaca"

// newAlpaca constructs the alpaca package (internal/alpaca).
func (g *Graph) newAlpaca() (alpaca.Service, error) {
	return alpaca.New(alpaca.Deps{
		Logger: g.AuditLog,
		Region: "us-east-1",
	})
}
`)
			var warnings strings.Builder
			in := GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"}, Warnings: &warnings}
			f, err := reconcileGraphComposeFile(in, nil, &graphResolver{module: "example.com/proj"}, nil)
			if err != nil || f.disowned {
				t.Fatalf("reconcileGraphComposeFile: disowned=%v err=%v", f.disowned, err)
			}
			out := f.compose
			if strings.Contains(out, "Region") {
				t.Fatalf("compose.go still keys the removed Region field — the project cannot build:\n%s", out)
			}
			if !strings.Contains(squash(out), "Logger: g.AuditLog,") || !strings.Contains(out, "alpaca.New(alpaca.Deps{") {
				t.Fatalf("the construction and its surviving value are the author's and must be kept:\n%s", out)
			}
			if got := strings.TrimSpace(warnings.String()); got != "⚠️  "+tc.warning {
				t.Errorf("warning:\n got: %q\nwant: %q", got, "⚠️  "+tc.warning)
			}
		})
	}
}

// An existing new<X> method follows its node's instrument decision: a node
// that became instrumented is wrapped in its decorator (both constructor
// shapes), and one that opted out is unwrapped — while the literal pass
// still reconciles the Deps keys in the same run.
func TestReconcileGraphCompose_SyncsTheDecoratorWrap(t *testing.T) {
	existingFor := func(body string) string {
		return graphComposeHeader + `
import (
	"log/slog"

	"example.com/p/internal/handlers/alpha"
)

func (g *Graph) newAlpha() (*alpha.Service, error) {
` + body + `
}

func (g *Graph) newStore() (*Store, error) {
	return NewStore(StoreDeps{Ctx: g.Ctx})
}
`
	}
	cases := []struct {
		name     string
		wrapped  bool
		fallible bool
		body     string
		want     []string
		absent   string
	}{
		{
			name: "fallible becomes wrapped", wrapped: true, fallible: true,
			body: "\treturn alpha.New(alpha.Deps{Logger: slog.Default()})",
			want: []string{"svc, err := alpha.New(alpha.Deps{", "Store: g.Store(),", "return alpha.NewServiceWithForgeMiddleware(svc), nil"},
		},
		{
			name: "infallible becomes wrapped", wrapped: true, fallible: false,
			body: "\treturn alpha.New(alpha.Deps{Logger: slog.Default()}), nil",
			want: []string{"return alpha.NewServiceWithForgeMiddleware(alpha.New(alpha.Deps{", "})), nil"},
		},
		{
			name: "opted out is unwrapped", wrapped: false, fallible: true,
			body:   "\tsvc, err := alpha.New(alpha.Deps{Logger: slog.Default()})\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn alpha.NewServiceWithForgeMiddleware(svc), nil",
			want:   []string{"return svc, nil"},
			absent: "WithForgeMiddleware",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, r := graphFixture()
			nodes[0].Deps[1].Type = "*Store"
			nodes[0].Fallible = tc.fallible
			nodes[0].Wrapped = tc.wrapped
			nodes[0].MiddlewareCtor = "NewServiceWithForgeMiddleware"
			out, problems, err := reconcileGraphCompose(existingFor(tc.body), nodes, r)
			if err != nil || len(problems) > 0 {
				t.Fatalf("err=%v problems=%+v", err, problems)
			}
			for _, want := range tc.want {
				if !strings.Contains(squash(out), squash(want)) {
					t.Errorf("compose.go lacks %q:\n%s", want, out)
				}
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("compose.go still has %q:\n%s", tc.absent, out)
			}
			again, _, err := reconcileGraphCompose(out, nodes, r)
			if err != nil || again != out {
				t.Errorf("not converged after one pass (err=%v):\n%s", err, again)
			}
		})
	}
}
