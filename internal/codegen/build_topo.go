// File: internal/codegen/build_topo.go
//
// Type-topological ordering over component Deps structs — the reusable
// core of the owned composition-root model (FORGE_SHAPE_REDESIGN §2).
//
// The owned internal/app/build.go is a typed composition root: it must
// construct each registered component via its New(Deps) AFTER every
// collaborator that component depends on by TYPE has been constructed.
// "By TYPE, not by name" is the whole point — billing.Deps.Users typed
// user.Service means construct user before billing, regardless of the
// field's name. This file computes that order.
//
// Conventional deps (Logger / Config / DB / Repo) come from
// the shared Infra value, never another component, so they NEVER
// participate in a topo edge. Only a Deps field whose declared type
// resolves to another registered component's Service interface produces
// an edge consumer -> producer.
//
// Cycle handling is deliberately LOUD (spec: "detect a cycle and emit a
// clearly-commented post-construction setter stub the user completes — do
// NOT silently break"). Kahn's algorithm yields the order; any nodes that
// never reach in-degree zero are a residual cycle, reported with the
// back-edges so build.go can emit a `// forge:build-twophase` block and
// `forge project map`/`audit` can flag it as a guardrail finding.
//
// This is PURE over its inputs (the parsed component set + a type
// resolver) so it is unit-testable without a real project on disk.

package codegen

import (
	"sort"
	"strings"
)

// BuildComponent is one node in the construction graph: a registered
// service / worker / operator, its parsed Deps fields, and the metadata
// the build.go scaffold needs to emit its constructor call.
type BuildComponent struct {
	// Name is the runtime kebab name (display / inventory selection only —
	// never a construction key). Used for stable sort + diagnostics.
	Name string
	// FieldName is the exported Go field name on the *Services struct
	// (e.g. "Billing", "SvcBilling"). The collision-aware name shared
	// with the inventory.
	FieldName string
	// VarName is the lower-camel local variable name build.go binds the
	// constructed instance to (e.g. "billing").
	VarName string
	// Alias is the import alias for the component's package.
	Alias string
	// ImportPath is the module-relative import path
	// (e.g. "internal/handlers/billing").
	ImportPath string
	// ServiceTypeKey is the type-identity key this component PRODUCES —
	// the thing a consumer's Deps field type is matched against to draw an
	// edge. It is the FULL IMPORT-PATH-qualified Service interface
	// (e.g. "example.com/proj/internal/billing.Service"), NOT the bare
	// package clause: two packages may share a clause name (a domain
	// `internal/billing` and a handler `internal/handlers/billing` both
	// `package billing`), and keying by clause would collide them, mis-
	// wiring a consumer's domain dep to the handler instance. Import paths
	// are unique, so import-path keying gives each package a distinct
	// identity. Empty for components that expose no collaborator interface
	// (pure leaf workers).
	//
	// The clause-qualified form is retained as a FALLBACK lookup
	// (compPackageKey) for the unambiguous single-clause case + mid-edit
	// projects where the consumer's import block can't be parsed.
	ServiceTypeKey string
	// Deps are the parsed Deps fields, in declaration order.
	Deps []DepsField
	// DepsKeys is every field name the component's `Deps{…}` literal may
	// legally key — the full set including the embedded and unexported fields
	// Deps deliberately drops (see DepsLiteralKeys). DepsFound reports whether
	// a Deps struct was positively READ, so an empty DepsKeys can be told apart
	// from a package that failed to parse. Only the compose reconciler's
	// stale-key drop reads these, and only when DepsFound is true.
	DepsKeys  map[string]bool
	DepsFound bool

	// The following unexported fields carry the disk-resolved render
	// metadata the inject_gen pass needs (package clause, fallible
	// constructor, role root, on-disk import leaf). They are unexported
	// because the topo core itself never reads them — only GenerateInject
	// (same package) populates and consumes them. Keeping them off the
	// exported surface means the build_topo unit tests (which construct
	// BuildComponent literals) are unaffected.
	compPackage string // Go package clause (constructor selector)
	// compConstructor is the component's entry-point func name — `New`, or the
	// `// forge:constructor`-marked name the author chose instead. compose.go
	// emits `pkg.<compConstructor>(pkg.Deps{...})`, so detection and emission
	// read the same marker and a renamed constructor still wires.
	compConstructor string
	compFallible    bool   // the constructor returns (T, error)
	compRoleRoot    string // "internal/handlers" / "internal" / "internal/workers" / "internal/operators"
	compImportLeaf  string // on-disk dir leaf under the role root (matcher load key)
	// compPackageKey is the bare package-clause-qualified Service key
	// (e.g. "billing.Service"). Retained alongside the import-path-keyed
	// ServiceTypeKey as the resolver's unambiguous-clause fallback.
	compPackageKey string
	// compImports is the consumer's import block (alias -> full import
	// path), parsed once from the component's package dir. The resolver
	// uses it to turn a Deps field type "<alias>.Service" into the
	// producer's full import-path key, so same-clause packages resolve to
	// the correct distinct producer. Empty when the dir can't be parsed.
	compImports map[string]string
	// compFieldType is the alias-qualified type the component's New
	// produces (e.g. "*item.Service" for a handler struct, "user.Service"
	// for a contract interface). It is the Services-registry field type AND
	// the inject_gen local-var type, so both match the constructor exactly.
	// Empty falls back to "*<alias>.Service" (the bootstrap default).
	compFieldType string
	// compWrapped is true when the package declares the OWNED observability
	// wrapper (the generated middleware decorator — see observe_wrap.go) AND its
	// constructor returns the interface. compose.go then emits the construction
	// as `c.X = pkg.<compMiddlewareCtor>(pkg.New(...))`.
	compWrapped bool
	// compMiddlewareCtor is the generated middleware wrapper's exact constructor
	// name (codegen.ResolveMiddlewareWrapper — keyed off the constructor's
	// concrete return type, e.g. "NewServiceWithForgeMiddleware"). compose.go
	// emits `pkg.<compMiddlewareCtor>(...)`, matching the generator's declaration
	// byte-for-byte. Empty unless compWrapped.
	compMiddlewareCtor string
}

// BuildEdge records a resolved consumer -> producer dependency: the
// consumer's Deps field Field (typed Type) was matched to the producer
// component. Used both to order construction and to render each
// constructor call's collaborator assignments.
type BuildEdge struct {
	Consumer string // consumer FieldName
	Producer string // producer FieldName
	Field    string // the Deps field on the consumer that carries the dep
	Type     string // the declared field type (for comments / setter stubs)
}

// BuildPlan is the result of ordering the construction graph.
type BuildPlan struct {
	// Order is the topo-sorted construction order (producers first). When
	// a cycle exists, Order contains the acyclic prefix and Cycles names
	// the components that could not be ordered.
	Order []BuildComponent
	// Edges are every resolved consumer->producer collaborator edge,
	// including back-edges that participate in a cycle (see CycleEdges).
	Edges []BuildEdge
	// Cycles lists the FieldNames of components left unordered because
	// they sit on a dependency cycle. Empty for a clean DAG.
	Cycles []string
	// CycleEdges are the specific edges build.go must break with a
	// two-phase setter stub: each is a consumer->producer edge where both
	// endpoints are in Cycles. The user completes the setter after both
	// instances exist.
	CycleEdges []BuildEdge
}

// HasCycle reports whether the plan contains an unresolved dependency
// cycle. forge project map / audit use this as a guardrail signal.
func (p BuildPlan) HasCycle() bool { return len(p.Cycles) > 0 }

// reachesSelf reports whether start can reach itself by following
// producer edges restricted to the within set (the unplaced nodes). A
// node that can reach itself sits on a genuine dependency cycle; a node
// that is merely downstream of a cycle cannot. DFS from start over the
// consumer->producer relation.
func reachesSelf(start string, producers map[string]map[string]bool, within map[string]bool) bool {
	seen := map[string]bool{}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		for p := range producers[n] {
			if !within[p] {
				continue
			}
			if p == start {
				return true
			}
			if !seen[p] {
				seen[p] = true
				if dfs(p) {
					return true
				}
			}
		}
		return false
	}
	return dfs(start)
}

// TypeResolver maps a Deps field's declared type to the FieldName of the
// component that PRODUCES that type, or "" when no registered component
// produces it (the field is a conventional dep filled from Infra, or an
// external collaborator). Implementations resolve by TYPE — see
// depsTypeResolver for the production matcher and the test helpers for
// in-memory variants.
type TypeResolver interface {
	// Resolve returns the producing component's FieldName for a Deps
	// field of the given declared type, or "" if none. consumer is the
	// component that DECLARED the field — its import block disambiguates
	// the field's package-clause prefix to a full import path, so two
	// producers sharing a package clause resolve to the correct distinct
	// producer (import-path identity, not bare clause).
	Resolve(consumer BuildComponent, depsType string) string
}

// ServiceKeyResolver is the default TypeResolver: it resolves a Deps
// field type against the set of component-exposed service type keys
// (each producer's ServiceTypeKey, e.g. "user.Service"). The match is on
// the pretty-printed type string the Deps parser already produces,
// tolerating a leading pointer (`*user.Service`) since a consumer may
// hold either the interface value or a pointer to it.
//
// This is intentionally string-structural rather than go/types-based:
// it is pure, deterministic, and cheap, and the package-qualified
// Service interface name is unambiguous across a project (one `Service`
// per component package by the strict-contract-names convention). A narrow
// consumer-declared interface (or an alias) satisfied by another component
// is NOT this resolver's job: GenerateCompose layers the go/types
// assignability pass (producer_assignability.go) over it, which resolves a
// unique satisfier and refuses loudly when several components satisfy it.
type ServiceKeyResolver struct {
	// byPath maps the FULL import-path-qualified Service key
	// (e.g. "example.com/proj/internal/billing.Service") -> producer
	// FieldName. Import paths are unique, so this index never collides —
	// it is the primary, collision-proof resolution path.
	byPath map[string]string
	// byClause maps the bare package-clause-qualified key
	// (e.g. "billing.Service") -> producer FieldName, used ONLY as a
	// fallback when the consumer's import block can't disambiguate the
	// clause to a path. clauseAmbiguous marks every clause shared by >1
	// producer: an ambiguous clause is NOT resolvable by clause alone
	// (that is exactly the collision this fix closes), so it resolves to
	// "" rather than guessing — the field then falls to the Infra /
	// loud-missing path, matching the rest of the fail-loud stance.
	byClause        map[string]string
	clauseAmbiguous map[string]bool
}

// NewServiceKeyResolver indexes comps by their import-path key (primary)
// and their package-clause key (ambiguity-tracked fallback). Components
// with an empty ServiceTypeKey produce nothing and are skipped.
func NewServiceKeyResolver(comps []BuildComponent) *ServiceKeyResolver {
	byPath := make(map[string]string, len(comps))
	byClause := make(map[string]string, len(comps))
	clauseAmbiguous := make(map[string]bool)
	for _, c := range comps {
		if c.ServiceTypeKey == "" {
			continue
		}
		byPath[c.ServiceTypeKey] = c.FieldName
		if c.compPackageKey != "" {
			if _, seen := byClause[c.compPackageKey]; seen {
				clauseAmbiguous[c.compPackageKey] = true
			} else {
				byClause[c.compPackageKey] = c.FieldName
			}
		}
	}
	return &ServiceKeyResolver{byPath: byPath, byClause: byClause, clauseAmbiguous: clauseAmbiguous}
}

// Resolve matches a Deps field type to a producing component FieldName,
// disambiguating the field's package-clause prefix through the consumer's
// import block to a full import path (import-path identity). Falls back to
// the bare clause only when it is unambiguous and the import block didn't
// resolve it.
func (r *ServiceKeyResolver) Resolve(consumer BuildComponent, depsType string) string {
	t := depsType
	for len(t) > 0 && t[0] == '*' {
		t = t[1:]
	}
	// Split "<prefix>.<TypeName>" into the clause/alias and the trailing
	// type identifier. A type with no selector (e.g. a local "Repository")
	// is never a cross-package Service producer key.
	dot := strings.LastIndex(t, ".")
	if dot < 0 {
		return ""
	}
	alias, typeName := t[:dot], t[dot+1:]

	// PRIMARY: resolve the alias to a full import path via the consumer's
	// import block, then look up the import-path key. Unique by path.
	//
	// When the alias IS in the import block, the consumer was EXPLICIT about
	// which package it means: either it resolves to a producer, or it
	// doesn't (the package is external/hand-built on Infra, or simply not a
	// component). In the latter case we must NOT fall back to the bare
	// clause — falling back would mis-resolve to a DIFFERENT same-clause
	// producer (e.g. a handler `package billing`) when the consumer
	// explicitly imported the domain `internal/billing`. Return "" so the
	// field falls to the Infra / loud-missing path, which is correct for an
	// external collaborator.
	if consumer.compImports != nil {
		if path, ok := consumer.compImports[alias]; ok {
			if f, ok := r.byPath[path+"."+typeName]; ok {
				return f
			}
			return ""
		}
	}

	// FALLBACK: the alias wasn't in the import block (same-package ref,
	// unparseable dir, or a synthetic test component with no imports).
	// Resolve by bare clause ONLY when it is unambiguous — an ambiguous
	// clause is the collision this fix refuses to guess at.
	clauseKey := alias + "." + typeName
	if r.clauseAmbiguous[clauseKey] {
		return ""
	}
	if f, ok := r.byClause[clauseKey]; ok {
		return f
	}
	return ""
}

// ComputeBuildPlan orders comps so every component is constructed after
// the components it depends on by type. resolver decides, per Deps field,
// whether the field's type is produced by another registered component
// (an edge) or is a conventional/external dep (no edge).
//
// Determinism: comps are processed in a stable order (by FieldName) and
// Kahn's queue is kept sorted, so the emitted build.go is byte-stable
// across regenerates regardless of map iteration order upstream.
func ComputeBuildPlan(comps []BuildComponent, resolver TypeResolver) BuildPlan {
	// Stable input order.
	sorted := make([]BuildComponent, len(comps))
	copy(sorted, comps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FieldName < sorted[j].FieldName })

	byField := make(map[string]BuildComponent, len(sorted))
	for _, c := range sorted {
		byField[c.FieldName] = c
	}

	// Build edges. An edge consumer->producer means producer must be
	// constructed first; in Kahn terms the consumer has an in-edge.
	var edges []BuildEdge
	// producers[consumer] = set of distinct producer FieldNames it needs.
	producers := make(map[string]map[string]bool, len(sorted))
	// consumers[producer] = consumers that depend on it (for queue drain).
	consumers := make(map[string][]string, len(sorted))
	for _, c := range sorted {
		producers[c.FieldName] = map[string]bool{}
	}

	for _, c := range sorted {
		for _, df := range c.Deps {
			prod := resolver.Resolve(c, df.Type)
			if prod == "" || prod == c.FieldName {
				// No registered producer (conventional/external dep) or a
				// self-reference (degenerate) — not an ordering edge.
				continue
			}
			if _, known := byField[prod]; !known {
				continue
			}
			edges = append(edges, BuildEdge{
				Consumer: c.FieldName,
				Producer: prod,
				Field:    df.Name,
				Type:     df.Type,
			})
			if !producers[c.FieldName][prod] {
				producers[c.FieldName][prod] = true
				consumers[prod] = append(consumers[prod], c.FieldName)
			}
		}
	}

	// In-degree = number of distinct producers a consumer still waits on.
	indeg := make(map[string]int, len(sorted))
	for f, ps := range producers {
		indeg[f] = len(ps)
	}

	// Kahn with a sorted ready-queue for determinism.
	var ready []string
	for _, c := range sorted {
		if indeg[c.FieldName] == 0 {
			ready = append(ready, c.FieldName)
		}
	}
	sort.Strings(ready)

	var order []BuildComponent
	placed := map[string]bool{}
	for len(ready) > 0 {
		f := ready[0]
		ready = ready[1:]
		order = append(order, byField[f])
		placed[f] = true
		var newlyReady []string
		for _, consumer := range consumers[f] {
			indeg[consumer]--
			if indeg[consumer] == 0 {
				newlyReady = append(newlyReady, consumer)
			}
		}
		if len(newlyReady) > 0 {
			ready = append(ready, newlyReady...)
			sort.Strings(ready)
		}
	}

	plan := BuildPlan{Order: order, Edges: edges}

	// Residual cycle: anything not placed sits on a cycle. Append the
	// unplaced nodes (sorted) so build.go still constructs them — with the
	// back-edge field zeroed and a two-phase setter stub — rather than
	// dropping them.
	if len(order) < len(sorted) {
		var unplaced []string
		for _, c := range sorted {
			if !placed[c.FieldName] {
				unplaced = append(unplaced, c.FieldName)
			}
		}
		sort.Strings(unplaced)
		plan.Cycles = unplaced
		for _, c := range unplaced {
			plan.Order = append(plan.Order, byField[c])
		}

		// A node merely DOWNSTREAM of a cycle (e.g. Report -> User where
		// User<->Billing cycle) is also unplaceable by Kahn, but it sits on
		// no cycle of its own and needs no two-phase setter. Distinguish
		// TRUE cycle members — those that are mutually reachable through the
		// producer graph (an SCC of size > 1, or a self-loop) — from the
		// transitively-blocked tail. Only edges between true cycle members
		// become CycleEdges (the setters build.go must emit).
		unplacedSet := map[string]bool{}
		for _, f := range unplaced {
			unplacedSet[f] = true
		}
		// onCycle[f] iff f can reach itself following producer edges
		// restricted to unplaced nodes.
		onCycle := map[string]bool{}
		for _, start := range unplaced {
			if reachesSelf(start, producers, unplacedSet) {
				onCycle[start] = true
			}
		}
		for _, e := range edges {
			if onCycle[e.Consumer] && onCycle[e.Producer] {
				plan.CycleEdges = append(plan.CycleEdges, e)
			}
		}
		sort.Slice(plan.CycleEdges, func(i, j int) bool {
			if plan.CycleEdges[i].Consumer != plan.CycleEdges[j].Consumer {
				return plan.CycleEdges[i].Consumer < plan.CycleEdges[j].Consumer
			}
			return plan.CycleEdges[i].Field < plan.CycleEdges[j].Field
		})
	}

	return plan
}
