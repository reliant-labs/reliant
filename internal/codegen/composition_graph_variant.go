// File: internal/codegen/composition_graph_variant.go
//
// Variant-only packages. `forge scaffold variant` writes an internal/app
// provider whose value is a wrapper type embedding a package's contract
// (`type SyncingChecks struct{ workflowcheck.Service }`). A package with no
// default configuration is constructed only through such wrappers: its own
// node is never taken by anything, and its Deps may not even resolve (so
// generate leaves the default node out), yet every process that needs it
// builds it, through the variants. Those variants ARE its nodes, so
// `forge lint --graph` does not report the package unreachable while a root
// reaches one of them.

package codegen

import "go/types"

// variantReached reports whether a provider node some root reaches produces
// a wrapper over n's value: a struct, or a pointer to one, embedding exactly
// n's produced type. Identity is go/types identity; an untyped node has none.
func (m *graphModel) variantReached(n *graphNode, reached map[string]bool) bool {
	if m.u == nil || n.Kind != nodeComponent {
		return false
	}
	inner := m.u.produced[n.Name]
	if inner == nil {
		return false
	}
	for _, p := range m.nodes {
		if p.Kind == nodeProvider && reached[p.Name] && embedsType(m.u.produced[p.Name], inner) {
			return true
		}
	}
	return false
}

// embedsType reports whether outer, or the struct it points to, has an
// embedded field of type inner.
func embedsType(outer, inner types.Type) bool {
	if outer == nil {
		return false
	}
	if ptr, ok := outer.Underlying().(*types.Pointer); ok {
		outer = ptr.Elem()
	}
	st, ok := outer.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		if f := st.Field(i); f.Embedded() && types.Identical(f.Type(), inner) {
			return true
		}
	}
	return false
}
