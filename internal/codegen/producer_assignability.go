// File: internal/codegen/producer_assignability.go
//
// By-ASSIGNABILITY producer resolution for compose.go (inject_gen.go).
//
// ServiceKeyResolver matches a Deps field to a producer component only when
// the field names the producer's EXACT contract type (`stripe.Service`). That
// contradicts forge's own package-boundary rule — "declare interfaces where
// they are CONSUMED" — because the idiomatic consumer shape is a narrow local
// interface:
//
//	// internal/enrollment
//	type Payments interface {
//		CreateDepositIntent(...) (...)
//		GetPaymentIntent(...) (...)
//		UpsertCustomer(...) (...)
//	}
//	type Deps struct{ Payments Payments }
//
// which stripe.Service satisfies but does not NAME. The exact-key resolver
// found no producer, and generate failed with "Deps fields with no provider"
// (Bark Social forced every consumer back to `stripe.Service`). A type alias
// (`type Payments = stripe.Service`) failed the same way: the pretty-printed
// field type is the alias name, which is no producer's key.
//
// This pass closes that gap with go/types. For every Deps field the exact-key
// resolver leaves unresolved, it asks: which registered component's
// constructed value is ASSIGNABLE to the field's declared interface type?
//
//   - exactly one  → that component is the producer (a build_topo edge, and
//     compose.go emits its instance);
//   - more than one → a LOUD ambiguity error naming every candidate — forge
//     never picks one silently, since wiring the wrong collaborator compiles
//     and misbehaves at runtime;
//   - none         → the field falls through to the Infra / missing-provider
//     path exactly as before.
//
// An Infra field keeps precedence: when the owned *Infra struct declares a
// field with the Deps field's NAME, or any Infra field assignable to its type,
// the user bound it explicitly and inference stays out of the way.
//
// SINGLE TYPE UNIVERSE: every component package plus internal/app load in ONE
// packages.Load call — go/types identity is pointer identity, so a split load
// makes identical types look distinct (see deps_assignability.go). When the
// load fails or a type is Invalid (project mid-edit) the pass infers nothing
// for that field: the pre-existing resolution path, and its fail-loud policy,
// is unchanged.

package codegen

import (
	"fmt"
	"go/types"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// AmbiguousProvider records a Deps field whose interface type is satisfied by
// more than one registered component, so forge cannot choose a producer.
type AmbiguousProvider struct {
	// Component is the consuming component's FieldName.
	Component string
	// Field is the Deps field name.
	Field string
	// Type is the declared field type.
	Type string
	// Candidates are the FieldNames of every component whose constructed
	// value is assignable to Type, sorted.
	Candidates []string
}

// inferringResolver layers the go/types-inferred producers over the exact-key
// base resolver. The base always wins; inference only fills fields the base
// left unresolved, so an exact `stripe.Service` dep resolves identically.
type inferringResolver struct {
	base TypeResolver
	// inferred maps inferKey(consumer, depsType) → producer FieldName.
	inferred map[string]string
}

func inferKey(consumerField, depsType string) string { return consumerField + "|" + depsType }

// Resolve implements TypeResolver.
func (r *inferringResolver) Resolve(consumer BuildComponent, depsType string) string {
	if f := r.base.Resolve(consumer, depsType); f != "" {
		return f
	}
	return r.inferred[inferKey(consumer.FieldName, depsType)]
}

// inferInterfaceProducers returns the producer inferred for every Deps field
// that base leaves unresolved and whose declared type is a non-empty
// interface satisfied by exactly one other component, plus every field
// satisfied by more than one. infraFields is the AST-parsed Infra surface
// (an exact-name Infra field is an explicit binding and skips inference);
// store names the generated-store dep types the framework seam owns.
func inferInterfaceProducers(projectDir string, comps []BuildComponent, base TypeResolver, infraFields map[string]InfraField, store storeSeam) (map[string]string, []AmbiguousProvider) {
	type candidateDep struct {
		consumer BuildComponent
		field    DepsField
	}
	var pending []candidateDep
	for _, c := range comps {
		for _, df := range c.Deps {
			if !inferableDep(df, store) {
				continue
			}
			if _, explicit := infraFields[df.Name]; explicit {
				continue
			}
			if base.Resolve(c, df.Type) != "" {
				continue
			}
			pending = append(pending, candidateDep{consumer: c, field: df})
		}
	}
	if len(pending) == 0 {
		// Nothing to infer: skip the type-check load entirely, so a project
		// whose deps all name exact contract types pays nothing for this pass.
		return nil, nil
	}

	u := loadProducerUniverse(projectDir, comps)
	if u == nil {
		return nil, nil
	}
	inferred := map[string]string{}
	var ambiguous []AmbiguousProvider
	for _, pd := range pending {
		p := u.lookup(u.compDir(pd.consumer))
		if p == nil {
			continue
		}
		depsTypes, _ := collectValidStructFieldTypes(p, "Deps")
		want, ok := depsTypes[pd.field.Name]
		if !ok {
			continue
		}
		iface, ok := want.Underlying().(*types.Interface)
		if !ok || iface.Empty() {
			// Only an interface with methods expresses a need a producer can
			// be PROVEN to meet; `any` would match every component.
			continue
		}
		// An Infra field that satisfies the type is the user's explicit
		// binding — leave the field to the Infra path.
		infraBound := false
		for _, it := range u.infraTypes {
			if infraAssignable(it, want) {
				infraBound = true
				break
			}
		}
		if infraBound {
			continue
		}
		var cands []string
		for _, n := range u.producerNames {
			if n == pd.consumer.FieldName {
				continue
			}
			if infraAssignable(u.produced[n], want) {
				cands = append(cands, n)
			}
		}
		switch len(cands) {
		case 0:
		case 1:
			inferred[inferKey(pd.consumer.FieldName, pd.field.Type)] = cands[0]
		default:
			ambiguous = append(ambiguous, AmbiguousProvider{
				Component:  pd.consumer.FieldName,
				Field:      pd.field.Name,
				Type:       pd.field.Type,
				Candidates: cands,
			})
		}
	}
	return inferred, ambiguous
}

// producerUniverse is the one packages.Load over internal/app and every
// component, indexed for producer inference.
type producerUniverse struct {
	lookup        func(dir string) *packages.Package
	compDir       func(c BuildComponent) string
	produced      map[string]types.Type // producer FieldName -> constructed value type
	producerNames []string              // sorted keys of produced
	infraTypes    map[string]types.Type // valid Infra field types
}

// loadProducerUniverse loads internal/app plus every component in ONE
// packages.Load (single type universe), or returns nil when the load fails.
func loadProducerUniverse(projectDir string, comps []BuildComponent) *producerUniverse {
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return nil
	}
	patterns := []string{"./" + path.Join("internal", "app")}
	for _, c := range comps {
		patterns = append(patterns, "./"+path.Join(filepath.ToSlash(c.compRoleRoot), filepath.ToSlash(c.compImportLeaf)))
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedTypes |
			packages.NeedDeps | packages.NeedImports,
		Dir: absProject,
		Env: goexec.Env(),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil || len(pkgs) == 0 {
		return nil
	}
	byDir := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.Types != nil {
			byDir[filepath.Clean(packageDir(p))] = p
		}
	}
	lookup := func(dir string) *packages.Package {
		if p, ok := byDir[filepath.Clean(dir)]; ok {
			return p
		}
		for d, p := range byDir {
			if sameDir(d, dir) {
				return p
			}
		}
		return nil
	}
	compDir := func(c BuildComponent) string {
		return filepath.Join(absProject, filepath.FromSlash(c.compRoleRoot), filepath.FromSlash(c.compImportLeaf))
	}

	// The value each producer's construction yields — its constructor's first
	// result (the contract interface for a wrapped/interface-returning New, the
	// concrete *Service for a handler). That is the static type of the
	// expression compose.go hands a consumer.
	produced := map[string]types.Type{}
	for _, c := range comps {
		if c.ServiceTypeKey == "" {
			continue
		}
		p := lookup(compDir(c))
		if p == nil {
			continue
		}
		if t := constructedType(p.Types, c.compConstructor); t != nil {
			produced[c.FieldName] = t
		}
	}

	var infraTypes map[string]types.Type
	if app := lookup(filepath.Join(absProject, "internal", "app")); app != nil {
		infraTypes, _ = collectValidStructFieldTypes(app, "Infra")
	}

	producerNames := make([]string, 0, len(produced))
	for n := range produced {
		producerNames = append(producerNames, n)
	}
	sort.Strings(producerNames)

	return &producerUniverse{lookup: lookup, compDir: compDir, produced: produced, producerNames: producerNames, infraTypes: infraTypes}
}

// inferableDep reports whether df is a collaborator dep the inference pass
// may resolve: not a conventional Logger/Config, not a scalar config value,
// not a framework-seam type forge already provides.
func inferableDep(df DepsField, store storeSeam) bool {
	switch df.Name {
	case "Logger", "Config":
		return false
	}
	if zeroValueLiteral(df.Type) != "nil" {
		return false
	}
	switch df.Type {
	case frameworkClockType, frameworkIDGenType, frameworkHTTPClientType:
		return false
	}
	return !store.owns(df.Type)
}

// constructedType returns the first result type of the package-level func
// ctor (default "New"), or nil when it is absent or its type did not check.
func constructedType(pkg *types.Package, ctor string) types.Type {
	if ctor == "" {
		ctor = DefaultConstructorName
	}
	fn, ok := pkg.Scope().Lookup(ctor).(*types.Func)
	if !ok {
		return nil
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Results().Len() == 0 {
		return nil
	}
	t := sig.Results().At(0).Type()
	if t == nil || t == types.Typ[types.Invalid] {
		return nil
	}
	return t
}

// ambiguousProviderError renders the loud refusal for fields satisfied by
// more than one component.
func ambiguousProviderError(amb []AmbiguousProvider) error {
	sort.Slice(amb, func(i, j int) bool {
		if amb[i].Component != amb[j].Component {
			return amb[i].Component < amb[j].Component
		}
		return amb[i].Field < amb[j].Field
	})
	var b strings.Builder
	b.WriteString("the explicit component construction site (internal/app/compose.go) has Deps fields satisfied by MORE THAN ONE component, so forge cannot choose a provider.\n\n")
	for _, a := range amb {
		fmt.Fprintf(&b, "  - %s.Deps.%s (%s) is satisfied by: %s\n", a.Component, a.Field, a.Type, strings.Join(a.Candidates, ", "))
	}
	b.WriteString("\nPick one explicitly: type the field as that component's own contract (e.g. `<pkg>.Service`), " +
		"or narrow the interface until only one component satisfies it.\n")
	return fmt.Errorf("%s", b.String())
}
