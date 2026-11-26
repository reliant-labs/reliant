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
	"go/ast"
	"go/parser"
	"go/token"
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
	viaExpr := composeFieldSources(m.compose)
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
			for _, df := range m.fieldsFilledBy(consumer, p, viaExpr[consumer.Name]) {
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

// fieldsFilledBy is consumer's Deps fields p fills: those whose type p's
// value satisfies, and those whose compose.go value expression reads p's
// accessor (sources: field → nodes, from composeFieldSources). The second
// catches a value derived from p — a variant wrapper's inner client,
// `g.CallerControlPlane().Client()` — which is nil exactly when p is, though
// its type is not p's. Untyped (mid-edit) and unread, it is none: the edge
// alone cannot name the field.
func (m *graphModel) fieldsFilledBy(consumer, p *graphNode, sources map[string]map[string]bool) []DepsField {
	pt, typed := m.u.produced[p.Name]
	var out []DepsField
	for _, df := range consumer.Deps {
		if sources[df.Name][p.Name] {
			out = append(out, df)
			continue
		}
		if !typed {
			continue
		}
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

// composeFieldSources reads, for each new<X> method of compose.go, the
// accessors each keyed field of its composite literals calls: node X →
// field → accessor names. A local assigned from an accessor
// (`cp := g.CallerControlPlane()`) carries that accessor into every field
// that reads it. Unparseable source reads as none.
func composeFieldSources(src string) map[string]map[string]map[string]bool {
	out := map[string]map[string]map[string]bool{}
	if src == "" {
		return out
	}
	file, err := parser.ParseFile(token.NewFileSet(), "compose.go", src, parser.SkipObjectResolution)
	if err != nil {
		return out
	}
	for name, fd := range graphComposeMethods(file) {
		x, ok := strings.CutPrefix(name, "new")
		if !ok || len(fd.Recv.List[0].Names) != 1 {
			continue
		}
		recv := fd.Recv.List[0].Names[0].Name
		locals := map[string]map[string]bool{}
		reads := func(e ast.Node) map[string]bool {
			got := map[string]bool{}
			ast.Inspect(e, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := v.X.(*ast.Ident); ok && id.Name == recv {
						got[v.Sel.Name] = true
					}
				case *ast.Ident:
					for a := range locals[v.Name] {
						got[a] = true
					}
				}
				return true
			})
			return got
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				return true
			}
			src := reads(as.Rhs[0])
			if len(src) == 0 {
				return true
			}
			for _, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
					if locals[id.Name] == nil {
						locals[id.Name] = map[string]bool{}
					}
					for a := range src {
						locals[id.Name][a] = true
					}
				}
			}
			return true
		})
		fields := map[string]map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				for a := range reads(kv.Value) {
					if fields[key.Name] == nil {
						fields[key.Name] = map[string]bool{}
					}
					fields[key.Name][a] = true
				}
			}
			return true
		})
		out[x] = fields
	}
	return out
}
