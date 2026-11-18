package codegen

import (
	"strings"
	"testing"
)

// reachFixture is a graph with one declared root over a listener, and three
// nodes no root reaches: a handler whose Deps nothing fills, a provider that
// takes that handler, and a provider whose Deps resolve.
func reachFixture() (*graphModel, *graphResolver, []graphRoot) {
	prov := func(name, typ string, deps ...DepsField) *graphNode {
		keys := map[string]bool{}
		for _, d := range deps {
			keys[d.Name] = true
		}
		return &graphNode{
			Name: name, LogName: logName(name), Doc: "the " + name + " provider", Kind: nodeProvider, Type: typ,
			Ctor: "New" + name, DepsType: name + "Deps", Fallible: true, Deps: deps, DepsKeys: keys, DepsFound: true,
			SrcImports: map[string]string{"alpha": "example.com/p/internal/handlers/alpha"},
			File:       "internal/app/providers.go", Line: 10,
		}
	}
	store := prov("Store", "*Store")
	listener := prov("Listener", "*Listener", DepsField{Name: "Store", Type: "*Store"})
	listener.Lifecycle = lifecycle{Run: "Run"}
	alpha := &graphNode{
		Name: "Alpha", LogName: "alpha", Doc: "the alpha handler (internal/handlers/alpha)", Kind: nodeComponent, Role: "internal/handlers",
		Type: "*alpha.Service", Imports: map[string]string{"alpha": "example.com/p/internal/handlers/alpha"},
		Ctor: "alpha.New", DepsType: "alpha.Deps", DepsImport: "example.com/p/internal/handlers/alpha", Fallible: true,
		Deps:     []DepsField{{Name: "Logger", Type: "*slog.Logger"}, {Name: "SQL", Type: "*sql.DB"}, {Name: "Temporal", Type: "*Temporal"}},
		DepsKeys: map[string]bool{"Logger": true, "SQL": true, "Temporal": true}, DepsFound: true,
		Comp: &BuildComponent{Alias: "alpha", ImportPath: "internal/handlers/alpha", compImports: map[string]string{"sql": "database/sql"}},
	}
	beta := prov("Beta", "*Beta", DepsField{Name: "Alpha", Type: "*alpha.Service"})
	gamma := prov("Gamma", "*Gamma", DepsField{Name: "Store", Type: "*Store"})
	nodes := []*graphNode{alpha, store, listener, beta, gamma}
	m := &graphModel{byName: map[string]*graphNode{}, compNode: map[string]*graphNode{"Alpha": alpha}, u: loadGraphUniverse("/nonexistent", nil)}
	for _, n := range nodes {
		m.nodes = append(m.nodes, n)
		m.byName[n.Name] = n
	}
	r := &graphResolver{module: "example.com/p", nodes: nodes, byName: m.byName, u: m.u}
	roots := []graphRoot{{Name: "server", TypeName: "Server", Command: []string{"server"}, File: "internal/app/roots.go", Line: 3,
		Fields: []graphRootField{{Name: "Listener", Type: "*Listener"}}}}
	return m, r, roots
}

// A node no root reaches does not have to be constructible: generate leaves
// it out (and every node that takes it) instead of failing, keeps an
// unreached node that resolves, and `forge lint --graph` reports both kinds.
func TestReach_AnUnreachedNodeWhoseDepsDoNotResolveIsLeftOut(t *testing.T) {
	m, r, roots := reachFixture()
	in := GraphGenInput{GenContext: GenContext{ProjectDir: t.TempDir(), ModulePath: "example.com/p"}, Warnings: &strings.Builder{}}
	if err := m.wire(in, r, roots); err != nil {
		t.Fatalf("a root that reaches only constructible nodes must generate: %v", err)
	}

	if got := strings.Join(graphSortedKeys(m.unbuilt), ","); got != "Alpha,Beta" {
		t.Fatalf("left out = %s, want Alpha (its Deps do not resolve) and Beta (it takes Alpha)", got)
	}
	if m.unbuilt["Beta"].Via != "Alpha" || len(m.unbuilt["Alpha"].Problems) != 2 {
		t.Fatalf("why = %+v", m.unbuilt)
	}
	for _, want := range []string{"func (g *Graph) newListener()", "func (g *Graph) newStore()", "func (g *Graph) newGamma()"} {
		if !strings.Contains(m.compose, want) {
			t.Errorf("compose.go lacks %q:\n%s", want, m.compose)
		}
	}
	for _, gone := range []string{"newAlpha", "newBeta", "handlers/alpha"} {
		if strings.Contains(m.compose, gone) {
			t.Errorf("compose.go constructs a node forge cannot build (%s):\n%s", gone, m.compose)
		}
	}
	var built []string
	for _, n := range m.built() {
		built = append(built, n.Name)
	}
	if got := strings.Join(built, ","); got != "Store,Listener,Gamma" {
		t.Fatalf("graph_gen.go's nodes = %s", got)
	}
	gv := graphGenViewOf("example.com/p", m.built(), m.rootViews, nil, nil)
	for _, imp := range gv.Imports {
		if strings.Contains(imp.Path, "handlers/alpha") {
			t.Errorf("graph_gen.go imports a left-out node's package: %s", imp.Path)
		}
	}
	if got := strings.Join(m.rootViews[0].Reach, ","); got != "Store,Listener" {
		t.Fatalf("server reaches %s", got)
	}
	if w := m.unbuiltWarning(); !strings.Contains(w, "2 node(s) no root reaches are left out") || !strings.Contains(w, "Alpha, Beta") {
		t.Errorf("generate's warning: %s", w)
	}

	got := m.unreachableFindings()
	want := "forge-graph-unreachable/error@internal/handlers/alpha, forge-graph-unreachable/error@internal/app/providers.go, forge-graph-unreachable/error@internal/app/providers.go"
	if rulesOf(got) != want {
		t.Fatalf("findings = %s\nwant       %s", rulesOf(got), want)
	}
	byMsg := map[string]GraphFinding{}
	for _, f := range got {
		byMsg[f.Message] = f
	}
	var sawAlpha, sawBeta, sawGamma bool
	for msg := range byMsg {
		switch {
		case strings.Contains(msg, "alpha handler"):
			sawAlpha = strings.Contains(msg, "no node produces Deps.SQL (*sql.DB), no node produces Deps.Temporal (*Temporal)") && strings.Contains(msg, "left out of graph_gen.go")
		case strings.Contains(msg, "NewBeta"):
			sawBeta = strings.Contains(msg, "it takes g.Alpha(), which forge cannot construct")
		case strings.Contains(msg, "NewGamma"):
			sawGamma = strings.Contains(msg, "nothing takes its value") // resolvable: today's unreachable finding
		}
	}
	if !sawAlpha || !sawBeta || !sawGamma {
		t.Fatalf("messages:\n%s", strings.Join(graphSortedKeys(byMsg), "\n"))
	}
}

// A Deps field forge cannot fill on a node a root REACHES still fails
// generate — that process would boot with a nil — with the message generate
// always gave, plus a count of the unreached ones left out.
func TestReach_AReachedNodeWithAMissingProviderStillFailsGenerate(t *testing.T) {
	m, r, roots := reachFixture()
	listener := m.byName["Listener"]
	listener.Deps = append(listener.Deps, DepsField{Name: "Cache", Type: "*Cache"})
	listener.DepsKeys["Cache"] = true
	in := GraphGenInput{GenContext: GenContext{ProjectDir: t.TempDir(), ModulePath: "example.com/p"}, Warnings: &strings.Builder{}}
	err := m.wire(in, r, roots)
	if err == nil {
		t.Fatal("a reached node with a missing provider generated")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Listener.Deps.Cache (*Cache) has no provider") || !strings.Contains(msg, "func NewCache(d CacheDeps) (*Cache, error)") {
		t.Fatalf("error does not name the reached field and the provider to write:\n%s", msg)
	}
	if strings.Contains(msg, "Alpha.Deps") {
		t.Errorf("the error names an unreached node's fields:\n%s", msg)
	}
	if !strings.Contains(msg, "1 more node(s) no root reaches") {
		t.Errorf("the error does not count the unreached nodes left out:\n%s", msg)
	}
}

// With no root declared, the default root is every runnable node nothing
// depends on — so a runnable node with a missing provider is reached, and
// still fails generate exactly as before.
func TestReach_TheDefaultRootReachesEveryRunnableSink(t *testing.T) {
	m, r, _ := reachFixture()
	m.byName["Alpha"].Lifecycle = lifecycle{Run: "Run"}
	m.nodes = []*graphNode{m.byName["Alpha"], m.byName["Store"], m.byName["Listener"]} // nothing takes Alpha
	r.nodes = m.nodes
	in := GraphGenInput{GenContext: GenContext{ProjectDir: t.TempDir(), ModulePath: "example.com/p"}, Warnings: &strings.Builder{}}
	err := m.wire(in, r, nil)
	if err == nil || !strings.Contains(err.Error(), "Alpha.Deps.SQL (*sql.DB) has no provider") {
		t.Fatalf("a runnable sink is in the default root, so its missing provider fails generate: %v", err)
	}
}

// A graph whose every Deps field resolves never takes the reach path:
// nothing is left out and generate warns about nothing.
func TestReach_AResolvableGraphLeavesNothingOut(t *testing.T) {
	m, r, roots := reachFixture()
	m.nodes = []*graphNode{m.byName["Store"], m.byName["Listener"], m.byName["Gamma"]}
	r.nodes = m.nodes
	in := GraphGenInput{GenContext: GenContext{ProjectDir: t.TempDir(), ModulePath: "example.com/p"}, Warnings: &strings.Builder{}}
	if err := m.wire(in, r, roots); err != nil {
		t.Fatal(err)
	}
	if m.unbuilt != nil || m.unbuiltWarning() != "" || len(m.built()) != 3 {
		t.Fatalf("unbuilt = %+v", m.unbuilt)
	}
}
