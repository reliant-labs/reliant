package codegen

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
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
