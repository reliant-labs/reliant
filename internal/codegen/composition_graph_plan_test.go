package codegen

import (
	"strings"
	"testing"
)

// Plan<Root> lists the root's closure in Build's order with each node's
// lifecycle, the probe listener first for a root that serves no probes — so
// its supervised stop order begins with the root's first field.
func TestGraphGen_PlanIsBuildOrderWithLifecycles(t *testing.T) {
	listener := &graphNode{Name: "APIListener", LogName: "api_listener", Kind: nodeProvider, Type: "*APIListener", Lifecycle: lifecycle{Run: "Run", Drain: "Drain", Ready: "Ready"}}
	db := &graphNode{Name: "DB", LogName: "db", Kind: nodeProvider, Type: "*DB", Lifecycle: lifecycle{Close: "Close", Ready: "Ping"}}
	alpha := &graphNode{Name: "Alpha", LogName: "alpha", Kind: nodeComponent, Type: "*alpha.Service"}
	tick := &graphNode{Name: "Tick", LogName: "tick", Kind: nodeComponent, Type: "*tick.Worker", Lifecycle: lifecycle{Run: "Start"}}
	nodes := []*graphNode{alpha, tick, db, listener}
	edges := map[string][]string{"APIListener": {"Alpha", "DB"}, "Alpha": {"DB"}, "Tick": {"DB"}}

	server := graphRoot{Name: "server", TypeName: "Server", Fields: []graphRootField{{Name: "API", Node: listener}, {Name: "Tick", Node: tick}}}
	worker := graphRoot{Name: "worker", TypeName: "Worker", Fields: []graphRootField{{Name: "Tick", Node: tick}}}
	sv, wv := rootView(&server, edges), rootView(&worker, edges)
	sv.ServesProbes = true
	gv := graphGenViewOf("example.com/demo", nodes, []graphRootView{sv, wv}, nil, nil)

	var got []string
	for _, n := range gv.Roots[0].PlanNodes {
		got = append(got, n.Name)
	}
	// Fields last to first, each after its dependencies: the plan string.
	if strings.Join(got, " → ") != sv.Plan {
		t.Fatalf("plan nodes %v, want the start order %q", got, sv.Plan)
	}
	src, err := renderGoTemplate("graph_gen.go.tmpl", gv)
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(string(src)), " ")
	for _, want := range []string{
		`func PlanServer() graphkit.Plan { return graphkit.Plan{ {Name: "db", Close: true, Ready: true}, {Name: "tick", Run: true}, {Name: "alpha"}, {Name: "api_listener", Run: true, Drain: true, Ready: true}, } }`,
		`func PlanWorker() graphkit.Plan { return graphkit.Plan{ {Name: "probe_listener", Run: true, Drain: true, Close: true, Ready: true}, {Name: "db", Close: true, Ready: true}, {Name: "tick", Run: true}, } }`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("graph_gen.go lacks %q:\n%s", want, src)
		}
	}
	if gv.Roots[0].PlanNodes == nil || sv.PlanNodes != nil {
		t.Error("the plan is graph_gen.go's view of a root; the root views themselves are left as assembled")
	}
}
