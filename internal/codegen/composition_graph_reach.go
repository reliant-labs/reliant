// File: internal/codegen/composition_graph_reach.go
//
// Reach scopes resolution (docs/design/composition-graph.md §6: partial
// adoption needs no fork). A root's process builds exactly the closure its
// fields reach, so only a node some root reaches has to be constructible:
//
//   - a Deps field forge cannot fill (MISSING or AMBIGUOUS) on a node a root
//     reaches fails generate, as it always has: that process would otherwise
//     boot with a nil;
//   - a node NO root reaches whose Deps do not resolve is left out of the
//     emitted graph — no accessor in graph_gen.go, no constructor method
//     injected into compose.go (an owned one is left alone) — and so is
//     every node that takes it. `forge lint --graph` reports each as
//     forge-graph-unreachable, an error by default; a project declaring its
//     roots one at a time dials that rule to warn in forge.yaml's lint.rules;
//   - a node no root reaches whose Deps DO resolve keeps its accessor, for a
//     hand-written main or a test, as it always has.
//
// A project whose every Deps field resolves — every fresh scaffold — never
// takes this path, so its compose.go and graph_gen.go are what they were.

package codegen

import (
	"fmt"
	"strings"
)

// reachScope decides, from the draft compose.go the resolvable keys make,
// which nodes with problems to leave out: none a root reaches (that is the
// returned error).
type reachScope func(draft string, problems []graphProblem) (map[string]unbuiltNode, error)

// unbuiltNode is why a node is left out of the emitted graph: Deps keys forge
// cannot fill, or (Via) a node it takes that is left out itself.
type unbuiltNode struct {
	Problems []graphProblem
	Via      string
}

// outOfReach is the reachScope of assembleGraph: it reads the edges the draft
// draws, takes each declared root's closure (or the default root's), fails
// with the problems of every node a root reaches, and otherwise returns the
// unreached nodes with problems plus every node that takes one of them.
func (m *graphModel) outOfReach(declared []graphRoot, draft string, problems []graphProblem) (map[string]unbuiltNode, error) {
	edges, lazy := graphComposeEdgesLazy(draft, m.nodes)
	roots := make([]graphRoot, len(declared))
	for i, r := range declared {
		r.Fields = append([]graphRootField(nil), r.Fields...)
		roots[i] = r
	}
	if len(roots) == 0 {
		roots = []graphRoot{defaultGraphRoot(m.nodes, edges)}
	}
	views, err := graphRootViews(roots, m.nodes, m.u, edges, lazy)
	if err != nil {
		return nil, err
	}
	reached := map[string]bool{}
	for _, v := range views {
		for _, n := range v.Reach {
			reached[n] = true
		}
	}
	unbuilt := map[string]unbuiltNode{}
	var inReach []graphProblem
	for _, p := range problems {
		if reached[p.Node] {
			inReach = append(inReach, p)
			continue
		}
		u := unbuilt[p.Node]
		u.Problems = append(u.Problems, p)
		unbuilt[p.Node] = u
	}
	if len(inReach) > 0 {
		err := graphProblemsError(inReach)
		if len(unbuilt) > 0 {
			err = fmt.Errorf("%w\n\n  (%d more node(s) no root reaches have Deps forge cannot fill either: they are left out of the graph, and `forge lint --graph` lists them.)", err, len(unbuilt))
		}
		return nil, err
	}
	// A node that takes a left-out node cannot be built either. None of
	// them is reached: a reached node's dependencies are reached too.
	for changed := true; changed; {
		changed = false
		for _, n := range m.nodes {
			if _, out := unbuilt[n.Name]; out {
				continue
			}
			for _, d := range append(append([]string(nil), edges[n.Name]...), lazy[n.Name]...) {
				if _, out := unbuilt[d]; out {
					unbuilt[n.Name] = unbuiltNode{Via: d}
					changed = true
					break
				}
			}
		}
	}
	return unbuilt, nil
}

// built is every node the emitted graph holds, in discovery order.
func (m *graphModel) built() []*graphNode { return withoutNodes(m.nodes, m.unbuilt) }

// withoutNodes is nodes minus the named ones, order kept.
func withoutNodes(nodes []*graphNode, drop map[string]unbuiltNode) []*graphNode {
	if len(drop) == 0 {
		return nodes
	}
	out := make([]*graphNode, 0, len(nodes))
	for _, n := range nodes {
		if _, ok := drop[n.Name]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// unbuiltWarning is generate's one advisory line for the nodes left out, ""
// when there are none.
func (m *graphModel) unbuiltWarning() string {
	if len(m.unbuilt) == 0 {
		return ""
	}
	names := graphSortedKeys(m.unbuilt)
	return fmt.Sprintf("%d node(s) no root reaches are left out of the composition graph, because forge cannot fill their Deps: %s. "+
		"graph_gen.go has no accessor for them and compose.go gets no constructor until a root reaches one — then generate names every provider it lacks. "+
		"`forge lint --graph` reports each (%s)", len(names), capList(names, 8), GraphRuleUnreachable)
}

// describe says why a node is left out, in one clause.
func (u unbuiltNode) describe() string {
	if u.Via != "" {
		return fmt.Sprintf("it takes g.%s(), which forge cannot construct", u.Via)
	}
	parts := make([]string, 0, len(u.Problems))
	for _, p := range u.Problems {
		if len(p.Candidates) > 0 {
			parts = append(parts, fmt.Sprintf("Deps.%s (%s) could be %s", p.Field, p.Type, strings.Join(p.Candidates, " or ")))
		} else {
			parts = append(parts, fmt.Sprintf("no node produces Deps.%s (%s)", p.Field, p.Type))
		}
	}
	return capList(parts, 3)
}

// capList joins items, naming at most limit of them.
func capList(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:limit], ", "), len(items)-limit)
}
