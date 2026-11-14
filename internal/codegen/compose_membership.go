// File: internal/codegen/compose_membership.go
//
// Which assembled components the server composition constructs, and what
// generate says when a package's membership and the graph disagree.
//
// Membership is DECLARED by the package (package_directives.go), never
// inferred: a package that no server component consumes looks exactly like a
// server package whose consumer is not written yet, so reachability is used
// only to word a hint (unconsumedPackages), never to drop a package from
// compose.go.

package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// composedComponents narrows the assembled component slice to the ones the
// server composition constructs, and returns the internal packages marked
// `//forge:not-a-component` separately. Two markers take a package out of the
// Build node set, and they mean different things (package_directives.go):
//
//   - `//forge:external-component` (alias `//forge:provided`): the server
//     HAS it, hand-constructed in providers.go / OpenInfra. The injector emits
//     no New(Deps) node for it, and a consumer of its Service interface
//     resolves to the Infra field the owner placed it on. Dropped here.
//   - `//forge:not-a-component`: the server does NOT have it — the CLI, a
//     daemon or another binary builds it. No node, no Infra field, its Deps
//     never resolved. Returned separately so GenerateCompose can refuse a
//     server component that requires it (notAComponentConsumers).
//
// Neither touches contract codegen: both keep their mock (a different walk
// entirely), and the decorator when they opt in.
//
// not-a-component is honoured on internal packages only. A handler, worker or
// operator is a server component by definition — it is mounted or supervised
// whether or not anything depends on it — and dropping one would leave
// mounts_services.go or lifecycle.go naming a field compose.go never declared.
// `forge lint` reports the marker there (forge-not-a-component-scope).
//
// This is a SELECTION predicate over the already-assembled component slice,
// applied post-enumeration — it deliberately does not touch how components
// are discovered. The component's on-disk package dir is reconstructed from
// the role root + import leaf the assembler already resolved.
func composedComponents(projectDir string, comps []BuildComponent) (composed, notComponents []BuildComponent) {
	composed = comps[:0:0]
	for _, c := range comps {
		dir := filepath.Join(projectDir, filepath.FromSlash(c.compRoleRoot), filepath.FromSlash(c.compImportLeaf))
		switch {
		case HasExternalComponentDirective(dir):
			continue
		case c.compRoleRoot == "internal" && HasNotAComponentDirective(dir):
			notComponents = append(notComponents, c)
			continue
		}
		composed = append(composed, c)
	}
	return composed, notComponents
}

// unconsumedPackages returns FieldName → import path for every internal
// package in comps that no service, worker or operator reaches through a
// chain of Deps edges. Those roles are the server's roots: a handler is
// mounted and a worker or operator is supervised whether or not anything
// depends on it, while an internal package is in the server only because
// something there needs it.
func unconsumedPackages(comps []BuildComponent, edges []BuildEdge) map[string]string {
	producers := map[string][]string{}
	for _, e := range edges {
		producers[e.Consumer] = append(producers[e.Consumer], e.Producer)
	}
	reached := map[string]bool{}
	var queue []string
	for _, c := range comps {
		if c.compRoleRoot != "internal" {
			reached[c.FieldName] = true
			queue = append(queue, c.FieldName)
		}
	}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, p := range producers[next] {
			if !reached[p] {
				reached[p] = true
				queue = append(queue, p)
			}
		}
	}
	out := map[string]string{}
	for _, c := range comps {
		if c.compRoleRoot == "internal" && !reached[c.FieldName] {
			out[c.FieldName] = c.ImportPath
		}
	}
	return out
}

// notAComponentConsumer is one required Deps field of a composed component
// whose type is the contract of a //forge:not-a-component package.
type notAComponentConsumer struct {
	Component string // consumer FieldName
	Field     string // the Deps field
	Type      string // its declared type
	Package   string // the not-a-component package's import path ("internal/cliauth")
	Dir       string // its on-disk directory, for the marker's reason
}

// notAComponentConsumers matches every REQUIRED Deps field of the composed
// components against the contracts of the not-a-component packages, with the
// same import-path-keyed resolver the Build graph uses — so a same-clause
// package elsewhere never matches. An optional field is left alone: it
// already accepts nil, which is exactly what a package the server does not
// have provides.
//
// Only an exact contract-type reference is caught. A consumer-declared narrow
// interface that a not-a-component package happens to satisfy is not; it
// falls through to the ordinary missing-provider path, which is still loud.
func notAComponentConsumers(composed, notComponents []BuildComponent) []notAComponentConsumer {
	if len(notComponents) == 0 {
		return nil
	}
	resolver := NewServiceKeyResolver(notComponents)
	byField := make(map[string]BuildComponent, len(notComponents))
	for _, nc := range notComponents {
		byField[nc.FieldName] = nc
	}
	var out []notAComponentConsumer
	for _, c := range composed {
		for _, df := range c.Deps {
			if df.Optional {
				continue
			}
			prod := resolver.Resolve(c, df.Type)
			if prod == "" {
				continue
			}
			nc := byField[prod]
			out = append(out, notAComponentConsumer{
				Component: c.FieldName,
				Field:     df.Name,
				Type:      df.Type,
				Package:   nc.ImportPath,
				Dir:       filepath.Join(filepath.FromSlash(nc.compRoleRoot), filepath.FromSlash(nc.compImportLeaf)),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// notAComponentConsumedError names every server component that requires a
// not-a-component package, and the three ways to make the tree truthful.
func notAComponentConsumedError(projectDir string, consumed []notAComponentConsumer) error {
	var b strings.Builder
	b.WriteString("the server composition (internal/app/compose.go) requires packages marked //forge:not-a-component.\n\n")
	b.WriteString("That marker says the server does not have the package, so no server component can require it:\n\n")
	for _, m := range consumed {
		reason := ""
		if marker, ok := FindNotAComponentMarker(filepath.Join(projectDir, m.Dir)); ok && marker.Reason != "" {
			reason = fmt.Sprintf(" (%q)", marker.Reason)
		}
		fmt.Fprintf(&b, "  - %s.Deps.%s (%s) is the contract of %s, marked //forge:not-a-component%s\n", m.Component, m.Field, m.Type, m.Package, reason)
	}
	b.WriteString("\nMake one of these true:\n")
	b.WriteString("  - the server needs it and forge can build it: delete the marker; compose.go constructs it like any other component.\n")
	b.WriteString("  - the server needs it but you build it by hand: replace the marker with //forge:external-component and put the instance on Infra (internal/app/providers.go).\n")
	b.WriteString("  - the server does not need it: remove the Deps field, or mark it //forge:optional-dep if the consumer copes with nil.\n")
	return fmt.Errorf("%s", b.String())
}
