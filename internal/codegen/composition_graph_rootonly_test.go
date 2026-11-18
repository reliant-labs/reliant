package codegen

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// typedRouters type-checks a package with a Router interface and two
// implementations, and returns its scope.
func typedRouters(t *testing.T) *types.Scope {
	t.Helper()
	const src = `package p

type Router interface{ Route() }

type WorkerRouter struct{}

func (*WorkerRouter) Route() {}

type APIRouter struct{}

func (*APIRouter) Route() {}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pkg.Scope()
}

// A //forge:root-only node fills a field of its very type, never an
// interface it merely satisfies: a handler's Router must not be wired to the
// one root's privileged variant because it happens to be the only router.
func TestProducersOf_ARootOnlyNodeFillsOnlyItsOwnType(t *testing.T) {
	scope := typedRouters(t)
	ptr := func(name string) types.Type { return types.NewPointer(scope.Lookup(name).Type()) }
	iface := scope.Lookup("Router").Type()

	worker := &graphNode{Name: "WorkerRouter", RootOnly: []string{"worker"}}
	api := &graphNode{Name: "APIRouter"}
	u := &graphUniverse{produced: map[string]types.Type{"WorkerRouter": ptr("WorkerRouter"), "APIRouter": ptr("APIRouter")}}

	if got := u.producersOf(iface, "Handler", []*graphNode{worker}); len(got) != 0 {
		t.Fatalf("a root-only node filled an interface it satisfies: %v", names(got))
	}
	if got := u.producersOf(iface, "Handler", []*graphNode{worker, api}); names(got) != "APIRouter" {
		t.Fatalf("producers of Router = %s, want APIRouter alone (the root-only one is no candidate)", names(got))
	}
	if got := u.producersOf(ptr("WorkerRouter"), "Tools", []*graphNode{worker, api}); names(got) != "WorkerRouter" {
		t.Fatalf("producers of *WorkerRouter = %s: a field of the root-only node's own type still resolves to it", names(got))
	}
}

func names(nodes []*graphNode) string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return strings.Join(out, ",")
}

// rootOnlyFixture is two roots over a root-only router: the worker's tools
// take it (allowed); the api's handler takes it only when the author's
// compose.go says so.
func rootOnlyFixture() ([]*graphNode, []graphRootView, map[string][]string) {
	router := &graphNode{Name: "WorkerRouter", File: "internal/app/providers_worker.go", Line: 12, RootOnly: []string{"worker"}}
	tools := &graphNode{Name: "WorkerTools"}
	handler := &graphNode{Name: "Daemon"}
	nodes := []*graphNode{router, tools, handler}
	views := []graphRootView{
		{Name: "worker", TypeName: "Worker", Fields: []graphRootFieldView{{Name: "Tools", Accessor: "WorkerTools"}}, Reach: []string{"WorkerRouter", "WorkerTools"}},
		{Name: "api", TypeName: "API", Fields: []graphRootFieldView{{Name: "Daemon", Accessor: "Daemon"}}, Reach: []string{"Daemon"}},
	}
	edges := map[string][]string{"WorkerTools": {"WorkerRouter"}}
	return nodes, views, edges
}

func TestCheckRootOnlyReach(t *testing.T) {
	nodes, views, edges := rootOnlyFixture()
	if err := checkRootOnlyReach(views, nodes, edges); err != nil {
		t.Fatalf("the root a root-only node names may reach it: %v", err)
	}

	// The author wires the api's handler to the worker's router.
	edges["Daemon"] = []string{"WorkerRouter"}
	views[1].Reach = []string{"WorkerRouter", "Daemon"}
	err := checkRootOnlyReach(views, nodes, edges)
	if err == nil {
		t.Fatal("another root reached a root-only node and generate went on")
	}
	for _, want := range []string{
		"internal/app/providers_worker.go:12: NewWorkerRouter is //forge:root-only worker",
		"root api reaches it through g.Daemon()",
		"give root api a node of its own type",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}

	// As a root field, too.
	views[1].Fields = append(views[1].Fields, graphRootFieldView{Name: "Router", Accessor: "WorkerRouter"})
	if err := checkRootOnlyReach(views, nodes, edges); err == nil || !strings.Contains(err.Error(), "through API.Router, g.Daemon()") {
		t.Fatalf("error does not name the root field: %v", err)
	}
}

// A directive naming a root that does not exist is a typo, not a scope.
func TestCheckRootOnlyReach_UnknownRoot(t *testing.T) {
	nodes, views, edges := rootOnlyFixture()
	nodes[0].RootOnly = []string{"wroker"}
	views[0].Reach = nil
	err := checkRootOnlyReach(views, nodes, edges)
	if err == nil || !strings.Contains(err.Error(), `no root is named "wroker" (the roots are api, worker)`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRootOnlyDirective(t *testing.T) {
	doc := func(lines ...string) *ast.CommentGroup {
		g := &ast.CommentGroup{}
		for _, l := range lines {
			g.List = append(g.List, &ast.Comment{Text: l})
		}
		return g
	}
	for _, tc := range []struct {
		lines []string
		want  string
		err   bool
	}{
		{lines: []string{"// NewX builds X."}},
		{lines: []string{"// NewX builds X.", "//", "//forge:root-only worker"}, want: "worker"},
		{lines: []string{"// forge:root-only api, worker"}, want: "api,worker"},
		{lines: []string{"//forge:root-only"}, err: true},
		{lines: []string{"//forge:root-only worker,"}, err: true},
		{lines: []string{"//forge:root-only worker api"}, err: true},
	} {
		got, err := rootOnlyDirective(doc(tc.lines...))
		if (err != nil) != tc.err || strings.Join(got, ",") != tc.want {
			t.Errorf("%q: got %v, %v", tc.lines, got, err)
		}
	}
}
