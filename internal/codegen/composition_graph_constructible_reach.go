package codegen

import "path/filepath"

func (m *graphModel) constructorNode(p unconstructibleComponent) *graphNode {
	for _, n := range m.nodes {
		if n.Comp != nil && filepath.ToSlash(filepath.Join(n.Comp.compRoleRoot, n.Comp.compImportLeaf)) == p.Dir {
			return n
		}
	}
	return nil
}

func (m *graphModel) constructorGraphProblems() []graphProblem {
	var problems []graphProblem
	for _, p := range m.constructorProblems {
		if n := m.constructorNode(p); n != nil {
			problems = append(problems, graphProblem{Node: n.Name, Field: "constructor", Type: "a callable component constructor"})
		}
	}
	return problems
}

func (m *graphModel) checkConstructorReach(declared []graphRoot, draft string) error {
	if len(m.constructorProblems) == 0 {
		return nil
	}
	// No explicit roots retains the historical/default-root contract: every
	// declared component must have a callable constructor.
	if len(declared) == 0 {
		return unconstructibleError(m.constructorProblems)
	}
	edges, lazy := graphComposeEdgesLazy(draft, m.nodes)
	views, err := graphRootViews(declared, m.nodes, m.u, edges, lazy)
	if err != nil {
		return err
	}
	reached := map[string]bool{}
	for _, v := range views {
		for _, n := range v.Reach {
			reached[n] = true
		}
	}
	var bad []unconstructibleComponent
	for _, p := range m.constructorProblems {
		if n := m.constructorNode(p); n != nil && reached[n.Name] {
			bad = append(bad, p)
		}
	}
	if len(bad) > 0 {
		return unconstructibleError(bad)
	}
	return nil
}

// validateReachedConstructors is the final constructor check, over the graph
// as generated: a component with no callable constructor may be neither built
// (generated code would call a constructor that does not exist) nor reached
// by a root through the FINAL compose.go. The unbuilt set was decided against
// a reconcile draft, and the draft the nodes left out produce can draw
// different edges, so reach is measured again here rather than trusted from
// that set.
func (m *graphModel) validateReachedConstructors(roots []graphRoot) error {
	var bad []unconstructibleComponent
	for _, p := range m.constructorProblems {
		if n := m.constructorNode(p); n != nil {
			if _, out := m.unbuilt[n.Name]; !out {
				bad = append(bad, p)
			}
		}
	}
	if len(bad) > 0 {
		return unconstructibleError(bad)
	}
	return m.checkConstructorReach(roots, m.compose)
}
