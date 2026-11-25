// File: internal/codegen/composition_graph_optional.go
//
// Optional components: `//forge:optional-component: <why>` on a component
// package (or on a provider func's doc) declares a node some deployments do
// not have. Its constructor may return a nil value with a nil error — the
// control-plane client of a self-hosted install — and the graph treats that
// nil as absent:
//
//   - graphkit memoizes the nil like any value (a nil error is no failure);
//   - the generated lifecycle literal checks it first, so supervision skips
//     a nil node's Run/Drain/Close/Ready (NilGuard, forced on here);
//   - a wrapped (observed) component's compose.go constructor returns the nil
//     instead of wrapping it, so consumers never get a decorator over nothing;
//   - every consumer Deps field it fills must be `// forge:optional-dep`,
//     because the consumer must handle the nil: forge lint --graph's
//     forge-graph-optional-component.
//
// A node whose value cannot be nil (a struct) cannot be optional: generate
// says so.

package codegen

import (
	"fmt"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// GraphRuleOptionalComponent is a consumer field, not marked
// `// forge:optional-dep`, that an optional component fills.
const GraphRuleOptionalComponent = "forge-graph-optional-component"

// componentOptional reports the `//forge:optional-component: <why>` marker of
// c's own package. A bare marker (no reason) does not count; forge lint's
// go-markers lane reports it.
func componentOptional(projectDir string, c *BuildComponent) bool {
	m, ok := FindOptionalComponentMarker(filepath.Join(projectDir, filepath.FromSlash(c.compRoleRoot), filepath.FromSlash(c.compImportLeaf)))
	return ok && strings.TrimSpace(m.Reason) != ""
}

// checkOptionalNilable refuses an optional node whose value cannot be nil,
// and makes the lifecycle literal of every optional one nil-checked.
func (m *graphModel) checkOptionalNilable() error {
	for _, n := range m.nodes {
		if !n.Optional {
			continue
		}
		if t, ok := m.u.produced[n.Name]; ok && !nilable(t) {
			return fmt.Errorf("%s is //forge:optional-component, but its constructor returns %s, which cannot be nil: an optional component returns a pointer or an interface, nil when it is not deployed", n.Doc, types.TypeString(t, nil))
		}
		n.NilGuard = true
	}
	return nil
}

// optionalComponentFindings names every consumer Deps field an optional
// component fills (a compose.go edge to it) that is not optional.
func (m *graphModel) optionalComponentFindings() []GraphFinding {
	var out []GraphFinding
	for _, consumer := range m.nodes {
		var optional []*graphNode
		for _, d := range m.edges[consumer.Name] {
			if p := m.byName[d]; p != nil && p.Optional {
				optional = append(optional, p)
			}
		}
		if len(optional) == 0 {
			continue
		}
		for _, p := range optional {
			for _, df := range m.fieldsFilledBy(consumer, p) {
				if df.Optional {
					continue
				}
				file, line := consumer.File, consumer.Line
				if consumer.Comp != nil {
					file, line = consumer.Comp.ImportPath, 0
				}
				out = append(out, GraphFinding{
					Rule: GraphRuleOptionalComponent, Error: true, File: file, Line: line,
					Message: fmt.Sprintf("%s's Deps.%s is filled by %s, which is //forge:optional-component and may be nil when it is not deployed, but the field is not marked optional", consumer.LogName, df.Name, p.LogName),
					Fix:     fmt.Sprintf("mark Deps.%s `// forge:optional-dep` and handle its nil (`if s.deps.%s == nil { … }`)", df.Name, df.Name),
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Message < out[j].Message })
	return out
}

// fieldsFilledBy is consumer's Deps fields whose type p's value satisfies.
// Untyped (mid-edit), it is none: the edge alone cannot name the field.
func (m *graphModel) fieldsFilledBy(consumer, p *graphNode) []DepsField {
	pt, ok := m.u.produced[p.Name]
	if !ok {
		return nil
	}
	var out []DepsField
	for _, df := range consumer.Deps {
		want := m.u.deps[consumer.Name][df.Name]
		if want == nil || want == types.Typ[types.Invalid] {
			continue
		}
		if types.AssignableTo(pt, want) {
			out = append(out, df)
		}
	}
	return out
}
