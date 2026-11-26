package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// lintGraphFixture is a graph with one user listener over a user chain,
// given its own value types so listener and chain identity is read the way
// LintGraph reads it (embedding serverkit.Listener / serverkit.Chain).
type lintGraphFixture struct {
	m *graphModel
}

func newLintGraphFixture(t *testing.T) *lintGraphFixture {
	t.Helper()
	sk := types.NewPackage(serverkitPkg, "serverkit")
	named := func(pkg *types.Package, name string, embed types.Type) *types.Named {
		var fields []*types.Var
		if embed != nil {
			fields = append(fields, types.NewField(token.NoPos, pkg, "", embed, true))
		}
		return types.NewNamed(types.NewTypeName(token.NoPos, pkg, name, nil), types.NewStruct(fields, nil), nil)
	}
	listener := named(sk, "Listener", nil)
	chain := named(sk, "Chain", nil)
	app := types.NewPackage("example.com/x/internal/app", "app")
	ptr := types.NewPointer

	m := &graphModel{
		byName:   map[string]*graphNode{},
		compNode: map[string]*graphNode{},
		u:        &graphUniverse{produced: map[string]types.Type{}},
		edges:    map[string][]string{},
		lazy:     map[string][]string{},
	}
	add := func(n *graphNode, typ types.Type) {
		m.nodes = append(m.nodes, n)
		m.byName[n.Name] = n
		if typ != nil {
			m.u.produced[n.Name] = typ
		}
	}
	add(&graphNode{Name: "UserAuth", Kind: nodeProvider, Type: "*UserAuth", File: "internal/app/providers.go", Line: 10, AuthAudience: "user"}, nil)
	add(&graphNode{Name: "UserChain", Kind: nodeProvider, Type: "*UserChain", File: "internal/app/providers.go", Line: 20}, ptr(named(app, "UserChain", ptr(chain))))
	add(&graphNode{Name: "APIListener", Kind: nodeProvider, Type: "*APIListener", File: "internal/app/providers.go", Line: 30}, ptr(named(app, "APIListener", ptr(listener))))
	add(&graphNode{Name: "UserRoutes", Kind: nodeRoutes, Type: "*UserRoutes"}, nil)
	add(&graphNode{Name: "DaemonRoutes", Kind: nodeRoutes, Type: "*DaemonRoutes"}, nil)
	add(&graphNode{Name: "HTTPClient", Kind: nodeProvider, Type: "*http.Client", File: "internal/app/providers.go", Line: 40, Standby: true}, nil)
	add(&graphNode{Name: "Cache", Kind: nodeProvider, Type: "*Cache", File: "internal/app/providers.go", Line: 50}, nil)
	for _, svc := range []struct{ name, audience string }{{"Alpha", "user"}, {"Tools", "daemon"}} {
		n := &graphNode{Name: svc.name, Kind: nodeComponent, Role: "internal/handlers", Type: "*" + strings.ToLower(svc.name) + ".Service",
			Doc: "the " + strings.ToLower(svc.name) + " handler", Comp: &BuildComponent{ImportPath: "internal/handlers/" + strings.ToLower(svc.name)}}
		add(n, nil)
		m.compNode[svc.name] = n
		m.routesView.Services = append(m.routesView.Services, routesServiceView{InventoryServiceData: InventoryServiceData{FieldName: svc.name, Audience: svc.audience}})
	}
	m.edges["UserChain"] = []string{"UserAuth"}
	m.edges["UserRoutes"] = []string{"Alpha"}
	m.edges["DaemonRoutes"] = []string{"Tools"}
	m.edges["APIListener"] = []string{"UserChain", "UserRoutes"}
	m.roots = []graphRoot{{Name: "server", TypeName: "Server"}}
	m.rootViews = []graphRootView{{Name: "server", TypeName: "Server", Reach: []string{"UserAuth", "UserChain", "Alpha", "UserRoutes", "APIListener"}}}
	return &lintGraphFixture{m: m}
}

func rulesOf(fs []GraphFinding) string {
	var out []string
	for _, f := range fs {
		sev := "warn"
		if f.Error {
			sev = "error"
		}
		out = append(out, f.Rule+"/"+sev+"@"+f.File)
	}
	return strings.Join(out, ", ")
}

func TestLintGraph_UnreachableServiceAndProvider(t *testing.T) {
	m := newLintGraphFixture(t).m
	got := m.unreachableFindings()
	// The daemon-audience Tools service is reached by no root (no listener
	// serves DaemonRoutes), and Cache nothing takes; the standby HTTPClient
	// and the route sets are not findings.
	want := "forge-graph-unreachable/error@internal/app/providers.go, forge-graph-unreachable/error@internal/handlers/tools"
	if rulesOf(got) != want {
		t.Fatalf("findings = %s\nwant       %s", rulesOf(got), want)
	}
	if !strings.Contains(got[1].Fix, "DaemonRoutes") {
		t.Errorf("an unreachable service's fix names its audience's route set: %s", got[1].Fix)
	}
	if !strings.Contains(got[0].Message, "provider NewCache") {
		t.Errorf("an unreachable provider is named by its func: %s", got[0].Message)
	}
}

// An internal package nothing consumes yet — the state `forge scaffold
// package` always leaves — is a pending WARNING, so a fresh scaffold passes
// lint. The same package taken by a consumer no root reaches is an ERROR:
// a consumer exists, and no process constructs it.
func TestLintGraph_UnconsumedPackageIsPendingConsumedButUnreachedIsAnError(t *testing.T) {
	m := newLintGraphFixture(t).m
	pkg := &graphNode{Name: "Blobstore", Kind: nodeComponent, Role: "internal", Type: "blobstore.Service",
		Doc: "the blobstore package (internal/blobstore)", Comp: &BuildComponent{ImportPath: "internal/blobstore"}}
	m.nodes = append(m.nodes, pkg)
	m.byName[pkg.Name] = pkg

	got := m.unreachableFindings()
	want := "forge-graph-unreachable/error@internal/app/providers.go, forge-graph-unreachable/error@internal/handlers/tools, forge-graph-unreachable/warn@internal/blobstore"
	if rulesOf(got) != want {
		t.Fatalf("findings = %s\nwant       %s", rulesOf(got), want)
	}
	if msg := got[2].Message; !strings.HasPrefix(msg, "pending: nothing takes blobstore.Service") {
		t.Errorf("the pending finding must say what it waits on: %s", msg)
	}

	// Tools (no root reaches it) now takes the package: an error.
	m.edges["Tools"] = []string{"Blobstore"}
	if got := m.unreachableFindings(); rulesOf(got) != "forge-graph-unreachable/error@internal/app/providers.go, forge-graph-unreachable/error@internal/handlers/tools, forge-graph-unreachable/error@internal/blobstore" {
		t.Fatalf("a package taken by an unreached consumer must be an error: %s", rulesOf(got))
	}

	// A lazy edge (a method value) is a consumer too.
	m.edges["Tools"] = nil
	m.lazy["Tools"] = []string{"Blobstore"}
	if got := m.unreachableFindings(); !strings.HasSuffix(rulesOf(got), "error@internal/blobstore") {
		t.Fatalf("a lazily consumed package no root reaches must be an error: %s", rulesOf(got))
	}

	// Workers and operators have no consumer by design; they stay errors.
	pkg.Role = "internal/workers"
	m.lazy["Tools"] = nil
	if got := m.unreachableFindings(); !strings.HasSuffix(rulesOf(got), "error@internal/blobstore") {
		t.Fatalf("an unreached worker is an error whatever consumes it: %s", rulesOf(got))
	}
}

func TestLintGraph_AudienceMismatchThroughTheListener(t *testing.T) {
	m := newLintGraphFixture(t).m
	if got := m.audienceFindings(); len(got) != 0 {
		t.Fatalf("a user listener serving user routes: %s", rulesOf(got))
	}
	// Hand the user listener the daemon route set as well: a daemon
	// service behind the user chain.
	m.edges["APIListener"] = []string{"UserChain", "UserRoutes", "DaemonRoutes"}
	got := m.audienceFindings()
	if rulesOf(got) != "forge-graph-audience/error@internal/app/providers.go" {
		t.Fatalf("findings = %s", rulesOf(got))
	}
	if !strings.Contains(got[0].Message, `(audience "daemon")`) || !strings.Contains(got[0].Message, `UserAuth (audience "user")`) {
		t.Errorf("message = %s", got[0].Message)
	}
	// Reachability now holds: the listener reaches Tools.
	m.rootViews[0].Reach = append(m.rootViews[0].Reach, "DaemonRoutes", "Tools")
	if got := m.unreachableFindings(); rulesOf(got) != "forge-graph-unreachable/error@internal/app/providers.go" {
		t.Fatalf("only Cache stays unreachable: %s", rulesOf(got))
	}
}

// A connect-go v2 project's chain embeds serverkit.ChainV2; the audience is
// read through it exactly as through a v1 serverkit.Chain.
func TestLintGraph_AudienceReadThroughAConnectV2Chain(t *testing.T) {
	m := newLintGraphFixture(t).m
	sk := types.NewPackage(serverkitPkg, "serverkit")
	app := types.NewPackage("example.com/x/internal/app", "app")
	chainV2 := types.NewNamed(types.NewTypeName(token.NoPos, sk, "ChainV2", nil), types.NewStruct(nil, nil), nil)
	userChain := types.NewNamed(types.NewTypeName(token.NoPos, app, "UserChain", nil),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, app, "", types.NewPointer(chainV2), true)}, nil), nil)
	m.u.produced["UserChain"] = types.NewPointer(userChain)
	if got := m.audienceFindings(); len(got) != 0 {
		t.Fatalf("a user listener over a v2 user chain serving user routes: %s", rulesOf(got))
	}
}

func TestLintGraph_ChainWithNoAudienceMarkerIsAWarning(t *testing.T) {
	m := newLintGraphFixture(t).m
	m.byName["UserAuth"].AuthAudience = ""
	got := m.audienceFindings()
	if rulesOf(got) != "forge-graph-audience/warn@internal/app/providers.go" {
		t.Fatalf("findings = %s", rulesOf(got))
	}
}

func TestDiscoverAppDecls_ProviderDirectives(t *testing.T) {
	dir := t.TempDir()
	writeAppFile(t, dir, "providers.go", `package app

type Cache struct{}
type CacheDeps struct{}

// NewCache is kept for later.
//
//forge:standby
func NewCache(CacheDeps) (*Cache, error) { return nil, nil }

type DaemonAuth struct{}
type DaemonAuthDeps struct{}

//forge:audience daemon
func NewDaemonAuth(DaemonAuthDeps) (*DaemonAuth, error) { return nil, nil }
`)
	providers, _, err := discoverAppDecls(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]graphProvider{}
	for _, p := range providers {
		got[p.Name] = p
	}
	if !got["Cache"].Standby || got["Cache"].Audience != "" || got["Cache"].Line != 9 {
		t.Errorf("Cache = %+v", got["Cache"])
	}
	if got["DaemonAuth"].Standby || got["DaemonAuth"].Audience != "daemon" {
		t.Errorf("DaemonAuth = %+v", got["DaemonAuth"])
	}
}

// A package with no default configuration, constructed only by its variants
// (`forge scaffold variant`: `type <Name> struct{ <pkg>.Service }` and a
// provider New<Name>), is reached through any variant a root reaches — forge
// bug 65120c00: reliant's workflowcheck, built only by RecordOnlyChecks and
// SyncingChecks, was reported unreachable once //forge:provided was removed.
func TestLintGraph_PackageReachedThroughItsVariant(t *testing.T) {
	m := newLintGraphFixture(t).m
	wcPkg := types.NewPackage("example.com/x/internal/workflowcheck", "workflowcheck")
	svc := types.NewNamed(types.NewTypeName(token.NoPos, wcPkg, "Service", nil), types.NewInterfaceType(nil, nil), nil)
	app := types.NewPackage("example.com/x/internal/app", "app")
	// The variant's package and internal/app are separate loads in a real
	// project, so the embed is a distinct *types.Named of the same name.
	svcAgain := types.NewNamed(types.NewTypeName(token.NoPos, types.NewPackage(wcPkg.Path(), "workflowcheck"), "Service", nil), types.NewInterfaceType(nil, nil), nil)
	wrapper := types.NewPointer(types.NewNamed(types.NewTypeName(token.NoPos, app, "RecordOnlyChecks", nil),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, app, "Service", svcAgain, true)}, nil), nil))

	pkg := &graphNode{Name: "Workflowcheck", Kind: nodeComponent, Role: "internal", Type: "workflowcheck.Service",
		Doc: "the workflowcheck package (internal/workflowcheck)", Comp: &BuildComponent{ImportPath: "internal/workflowcheck", compConstructor: "New"}}
	variant := &graphNode{Name: "RecordOnlyChecks", Kind: nodeProvider, Type: "*RecordOnlyChecks", File: "internal/app/variant_record_only_checks.go", Line: 29}
	for _, n := range []*graphNode{pkg, variant} {
		m.nodes = append(m.nodes, n)
		m.byName[n.Name] = n
	}
	m.u.produced["Workflowcheck"] = svc
	m.u.produced["RecordOnlyChecks"] = wrapper
	// Forge cannot fill the package's own Deps (it has no default
	// configuration), so its node is left out of graph_gen.go.
	m.unbuilt = map[string]unbuiltNode{"Workflowcheck": {Problems: []graphProblem{{Node: "Workflowcheck", Field: "Store", Type: "workflowcheck.Store"}}}}

	const base = "forge-graph-unreachable/error@internal/app/providers.go, forge-graph-unreachable/error@internal/handlers/tools"

	// No root reaches the variant: both it and the package are reported.
	if got := rulesOf(m.unreachableFindings()); got != base+", forge-graph-unreachable/error@internal/workflowcheck, forge-graph-unreachable/error@internal/app/variant_record_only_checks.go" {
		t.Fatalf("an unreached variant reaches nothing: %s", got)
	}
	if w := m.unbuiltWarning(); !strings.Contains(w, "Workflowcheck") {
		t.Fatalf("generate's warning names the unreached package: %q", w)
	}

	setProvider := func(body string, calledPackage *types.Package) {
		t.Helper()
		file, err := parser.ParseFile(token.NewFileSet(), "variant.go", "package app; func NewRecordOnlyChecks() {"+body+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
				info.Uses[sel.Sel] = types.NewFunc(token.NoPos, calledPackage, "New", nil)
			}
			return true
		})
		m.u.app = &packages.Package{Syntax: []*ast.File{file}, TypesInfo: info}
	}

	m.rootViews[0].Reach = append(m.rootViews[0].Reach, "RecordOnlyChecks")
	// Embedding nil, retaining a supplied service, an uncalled closure and a
	// same-spelled constructor from another package are not evidence.
	for _, body := range []string{
		"return &RecordOnlyChecks{}",
		"return &RecordOnlyChecks{Service: supplied}",
		"workflowcheck.New(); return &RecordOnlyChecks{}",
		"svc, err := workflowcheck.New(); _ = svc; _ = err; return &RecordOnlyChecks{}",
		"svc, err := workflowcheck.New(); svc = supplied; return &RecordOnlyChecks{Service: svc}, err",
		"svc, err := workflowcheck.New(); return &RecordOnlyChecks{Service: supplied}, err",
		"if enabled { workflowcheck.New() }; return &RecordOnlyChecks{}",
		"f := func() { workflowcheck.New() }; _ = f",
	} {
		setProvider(body, wcPkg)
		if got := rulesOf(m.unreachableFindings()); got != base+", forge-graph-unreachable/error@internal/workflowcheck" {
			t.Fatalf("provider %q must not suppress the unbuilt finding: %s", body, got)
		}
		if w := m.unbuiltWarning(); !strings.Contains(w, "Workflowcheck") {
			t.Fatalf("provider %q must retain generate's warning: %q", body, w)
		}
	}
	setProvider("workflowcheck.New()", types.NewPackage("example.com/other", "workflowcheck"))
	if m.variantReached(m.reached())["Workflowcheck"] {
		t.Fatal("a constructor from a different package must not establish reach")
	}

	// The server root takes the variant, whose body calls the real constructor
	// (even with a different import alias).
	for _, body := range []string{
		"svc, err := checks.New(checks.Deps{}); if err != nil { return nil, err }; return &RecordOnlyChecks{Service: svc}, nil",
		"return &RecordOnlyChecks{Service: checks.New()}",
		"svc := checks.New(); alias := svc; return &RecordOnlyChecks{alias}",
	} {
		setProvider(body, wcPkg)
		if got := rulesOf(m.unreachableFindings()); got != base {
			t.Fatalf("provider %q wraps a constructed service: %s", body, got)
		}
		if w := m.unbuiltWarning(); w != "" {
			t.Fatalf("generate must not warn about a package a reached variant constructs: %q", w)
		}
	}

	// A wrapper that NAMES the Service in a field, rather than embedding
	// it, is not the variant shape: the package stays pending.
	m.unbuilt = nil
	m.u.produced["RecordOnlyChecks"] = types.NewPointer(types.NewNamed(types.NewTypeName(token.NoPos, app, "RecordOnlyChecks", nil),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, app, "Service", svcAgain, false)}, nil), nil))
	if got := rulesOf(m.unreachableFindings()); got != base+", forge-graph-unreachable/warn@internal/workflowcheck" {
		t.Fatalf("a named (not embedded) field is not the variant shape: %s", got)
	}
}
