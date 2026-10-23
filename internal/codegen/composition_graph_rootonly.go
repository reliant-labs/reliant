// File: internal/codegen/composition_graph_rootonly.go
//
// Root-only nodes. A provider marked `//forge:root-only <root>[,<root>...]`
// is a root-specific variant of a component — reliant's worker builds a
// daemon router that may act on a trigger's delegated token, which its api
// must never hold. Giving the variant a type of its own is not enough on its
// own: resolution fills an interface-typed Deps field with whatever single
// node satisfies it, so the variant would be wired into a handler some other
// root serves. Two rules close that:
//
//   - resolution offers a root-only node only to a field of its very type
//     (graphUniverse.producersOf), never to an interface it satisfies;
//   - after the roots are resolved, a root-only node in the closure of a
//     root it does not name fails generate (checkRootOnlyReach), whichever
//     expression — forge's or the author's — drew the edge.

package codegen

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// checkRootOnlyReach fails when a root's closure holds a root-only node that
// does not name it, or when a root-only directive names no declared root.
func checkRootOnlyReach(views []graphRootView, nodes []*graphNode, edges map[string][]string) error {
	byName := make(map[string]*graphNode, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n
	}
	declared := map[string]bool{}
	var rootNames []string
	for _, v := range views {
		declared[v.Name] = true
		rootNames = append(rootNames, v.Name)
	}
	sort.Strings(rootNames)

	var problems []string
	for _, n := range nodes {
		for _, r := range n.RootOnly {
			if !declared[r] {
				problems = append(problems, fmt.Sprintf("%s:%d: New%s is //forge:root-only %s, but no root is named %q (the roots are %s)",
					n.File, n.Line, n.Name, strings.Join(n.RootOnly, ","), r, strings.Join(rootNames, ", ")))
			}
		}
	}
	for _, v := range views {
		reach := map[string]bool{}
		for _, name := range v.Reach {
			reach[name] = true
		}
		for _, name := range v.Reach {
			n := byName[name]
			if n == nil || len(n.RootOnly) == 0 || slices.Contains(n.RootOnly, v.Name) {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s:%d: New%s is //forge:root-only %s, but root %s reaches it through %s: give root %s a node of its own type, or name it in the directive",
				n.File, n.Line, n.Name, strings.Join(n.RootOnly, ","), v.Name, rootOnlyReachers(v, name, reach, edges), v.Name))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("the composition graph hands a root-only node to another root:\n  - %s", strings.Join(problems, "\n  - "))
}

// rootOnlyReachers names what in root v draws the edge to node: the root's
// fields holding it, and the nodes in its closure that depend on it.
func rootOnlyReachers(v graphRootView, node string, reach map[string]bool, edges map[string][]string) string {
	var out []string
	for _, f := range v.Fields {
		if f.Accessor == node {
			out = append(out, v.TypeName+"."+f.Name)
		}
	}
	for _, name := range v.Reach {
		if reach[name] && slices.Contains(edges[name], node) {
			out = append(out, "g."+name+"()")
		}
	}
	if len(out) == 0 {
		return "a lazy edge"
	}
	return strings.Join(out, ", ")
}
