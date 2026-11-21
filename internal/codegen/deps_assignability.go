// File: internal/codegen/deps_assignability.go
//
// Type-aware Deps→AppExtras field matcher used by:
//
//   - inspectComponentDepsShape (Matcher A, generator.go) — decides
//     whether to emit `<DepsField>: app.<DepsField>` from the bootstrap
//     template.
//   - wireExpressionForApp / wireExpressionFor (Matcher B, wire_gen.go) —
//     decides whether `wireXxxDeps` should resolve a Deps field to
//     `app.<Field>`.
//
// Both matchers historically compared the pretty-printed type STRINGS
// produced by printType (go/printer). Pure string compare has two
// well-known failure modes:
//
//   1. False-negative — silent drop. AppExtras.Repo is *db.Repo (wide
//      concrete) and Deps.Repo is myservice.Repository (narrow
//      interface) that *db.Repo implements. The strings differ, the wire
//      is silently skipped, the package constructs with a nil Repo, the
//      feature no-ops in production.
//
//   2. False-positive — compile error. AppExtras.Foo is *foo.A and
//      Deps.Foo is *bar.A. Matcher B (wire_gen) sees a name match and
//      emits `app.Foo` regardless of type; the rendered code fails to
//      compile.
//
// The DepsAssignabilityMatcher uses go/types via x/tools/go/packages to
// answer the real question — "is AppExtras.<Field> assignable to
// Deps.<Field>'s declared type?".
//
// SINGLE TYPE UNIVERSE (kalshi-trader FORGE_BACKLOG #13): pkg/app and
// the component package MUST be loaded in ONE packages.Load call.
// go/types identity is pointer identity — the same named type loaded
// through two separate packages.Load invocations yields two distinct
// *types.Named (and two distinct method-signature parameter types like
// context.Context), so types.AssignableTo / types.Implements report
// false for types that are literally identical in source. The original
// implementation loaded pkg/app once and each component once, in
// separate calls, which made every worker-local interface satisfied by
// an AppExtras adapter look like a NameMismatch — silently un-wiring
// production collaborators. One Load per (pkg/app, component) pair
// keeps both sides in a shared universe where identity holds.
//
// DETERMINISTIC FAIL-LOUD POLICY: when a Deps field name-matches an
// App/AppExtras field but assignability cannot be PROVEN (project
// doesn't type-check mid-pipeline, load error, field invisible to the
// type checker), the matcher reports MatchUnavailable and BOTH
// consumers wire the name match anyway (`app.<Field>`). Rationale:
//
//   - The Go compiler is the final arbiter. A wrong wire fails the
//     build loudly, with the error pointing at the exact assignment.
//     Silently emitting nil instead un-wires a production collaborator
//     with NO signal — the disaster mode kalshi actually hit (the
//     settlement loop no-op'd).
//   - It preserves the pre-matcher behavior (name match → wire), so a
//     project mid-edit regenerates exactly as it did before the
//     matcher existed.
//   - It makes generate→generate deterministic. The old split policy
//     (Unavailable → legacy string compare, NameMismatch → drop) meant
//     wire_gen.go content depended on whether the project happened to
//     type-check at the instant the matcher loaded it — structural
//     regens (pkg/app transiently broken) wired `app.<Field>` while
//     the very next steady-state regen flipped the same field to nil.
//     Under the unified policy, an unproven name match and a proven
//     assignable name match emit the SAME wiring, so the transient and
//     steady-state paths converge.
//
// Only a PROVEN mismatch (both sides type-checked in one universe and
// AssignableTo/Implements still say no) drops the wire — and that drop
// is loud: typed-zero + TODO comment + UNRESOLVED header + wire-coverage
// lint finding, even for `forge:optional-dep` fields (a name-matched
// type conflict is a misconfiguration, not an intentional nil).
//
// Caching: one matcher per generate run. Each (pkg/app, component)
// pair is loaded once via packages.Load, then reused for every Deps
// field of that component. Loading pkg/app jointly per component costs
// one extra root per Load versus the old shared-app cache, but go/list
// caching makes the delta negligible — and correctness (shared
// universe) is non-negotiable.

package codegen

import (
	"go/types"
	"path"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// MatchKind classifies the result of one Deps-field → AppExtras-field
// match attempt. The two matchers (bootstrap and wire_gen) treat the
// kinds identically — see the policy block in the file header.
type MatchKind int

const (
	// MatchNoName — AppExtras has no field with this Deps field's name.
	// Both matchers treat this as "no wire, no error".
	MatchNoName MatchKind = iota
	// MatchExactString — the pretty-printed type strings are byte-equal.
	// Legacy fast path: no go/types load required. Both matchers wire.
	MatchExactString
	// MatchAssignable — both packages loaded in one shared type universe
	// and AppExtras.<F> is assignable to Deps.<F>'s declared type per
	// go/types (narrow-interface case). Both matchers wire.
	MatchAssignable
	// MatchNameMismatch — name matches but the types are PROVEN not
	// assignable (both sides type-checked in a single universe).
	// Bootstrap drops the wire and relies on the post-generate lint to
	// surface the gap; wire_gen drops the app.<Field> resolution and
	// falls through to typed-zero + loud unresolved hint so the silent
	// compile-error class becomes loud.
	MatchNameMismatch
	// MatchUnavailable — assignability could not be proven either way
	// (go/types load failed, project not buildable mid-pipeline, field
	// invisible to the type checker). Per the deterministic fail-loud
	// policy (file header), BOTH consumers treat this as "wire the name
	// match": the compiler arbitrates a wrong wire loudly, whereas
	// emitting nil would silently un-wire a live collaborator.
	MatchUnavailable
	// MatchUnprovenBackstop — the Infra-field matcher (infra_assignability.go)
	// found NO assignable Infra field AND could not PROVE one is absent because
	// the universe is mid-write (some relevant field type did not type-check —
	// e.g. internal/app references the not-yet-regenerated Build, or the
	// new component package is a fresh stub). This is the generate-ORDERING
	// fragility class: raising a generate-time MissingProvider here is a
	// FALSE NEGATIVE (the user's Infra field, named differently from the Deps
	// field, would prove assignable on a clean load). The injector emits the
	// compile-time backstop `infra.<DepsField>` and lets the Go compiler
	// arbitrate — loud if genuinely missing, silently correct once the next
	// clean generate proves the assignable match. It is ONLY used by the
	// Infra matcher; the Deps/AppExtras matcher never returns it.
	MatchUnprovenBackstop
)

// DepsAssignabilityMatcher answers "is AppExtras.<FieldName> assignable
// to Deps.<FieldName>?" for a given roleRoot (e.g. "internal",
// "handlers", "workers", "operators") and package directory under it.
//
// One instance per generate run. Methods are safe for concurrent use
// within a single generate (the cache mutex serializes loads).
//
// Construction is cheap: NewDepsAssignabilityMatcher only stores the
// project dir. Packages are loaded lazily on the first Match call that
// requires them. A project that's missing pkg/app or whose source
// doesn't type-check still constructs fine and reports MatchUnavailable
// (→ wire-the-name-match, the pre-matcher behavior).
type DepsAssignabilityMatcher struct {
	projectDir string

	mu sync.Mutex
	// universes caches one shared-universe load per component.
	// Key: roleRoot|pkgDir. Each entry holds pkg/app's field types AND
	// the component's Deps field types resolved from the SAME
	// packages.Load call, so go/types identity holds across them.
	universes map[string]*matcherUniverse
}

// matcherUniverse is the cached result of one joint (pkg/app +
// component) load. ok=false means assignability cannot be proven for
// any field of this component this run (load error or type errors on
// either side) — Match degrades to MatchUnavailable.
type matcherUniverse struct {
	ok         bool
	appFields  map[string]types.Type // App + AppExtras exported fields → declared type
	depsFields map[string]types.Type // component Deps exported fields → declared type
	// appPkgPath is the import path of pkg/app in this universe. Types
	// that live in pkg/app itself must render UNQUALIFIED in the emitted
	// pkg/app/interface_assertions_gen.go (a package cannot import
	// itself), so AssignablePairs strips this path's qualifier.
	appPkgPath string
}

// NewDepsAssignabilityMatcher returns a matcher rooted at projectDir.
// projectDir is the directory containing go.mod / pkg/app / handlers /
// workers / operators / internal.
func NewDepsAssignabilityMatcher(projectDir string) *DepsAssignabilityMatcher {
	return &DepsAssignabilityMatcher{
		projectDir: projectDir,
		universes:  map[string]*matcherUniverse{},
	}
}

// Match resolves whether the named Deps field should be wired from
// AppExtras. roleRoot is "internal" / "handlers" / "workers" /
// "operators"; pkgDir is the directory name under roleRoot (e.g.
// "audit", "billing", possibly nested like "mcp/database");
// depsFieldName is the Go field name on the package's Deps struct;
// appNameKnown reports whether AppExtras has a same-name field (as
// parsed by ParseAppFields — the cheap AST path); depsTypeStr /
// appTypeStr are the pretty-printed type strings the legacy matchers
// already had.
//
// The matcher uses the cheap inputs to avoid a go/types load when the
// answer is unambiguous (no name match, or byte-equal strings). It
// only loads packages when the strings differ AND a name match exists
// — exactly the case the legacy compare got wrong.
func (m *DepsAssignabilityMatcher) Match(roleRoot, pkgDir, depsFieldName, depsTypeStr, appTypeStr string, appNameKnown bool) MatchKind {
	if !appNameKnown {
		return MatchNoName
	}
	if depsTypeStr == appTypeStr {
		return MatchExactString
	}

	// Strings differ — we need the type checker to decide. Load
	// pkg/app + roleRoot/pkgDir jointly (one universe), lazily.
	m.mu.Lock()
	defer m.mu.Unlock()

	key := roleRoot + "|" + pkgDir
	u, loaded := m.universes[key]
	if !loaded {
		u = m.loadUniverseLocked(roleRoot, pkgDir)
		m.universes[key] = u
	}
	if !u.ok {
		return MatchUnavailable
	}
	appT, ok := u.appFields[depsFieldName]
	if !ok {
		// AST said the name was there but the type checker disagrees
		// (e.g. the field is on a promoted struct we didn't model).
		// Unproven — per the fail-loud policy the consumers wire the
		// name match and let the compiler arbitrate.
		return MatchUnavailable
	}
	depsT, ok := u.depsFields[depsFieldName]
	if !ok {
		return MatchUnavailable
	}

	if types.AssignableTo(appT, depsT) {
		return MatchAssignable
	}
	// Interface impl is the common other shape: depsT is an interface
	// and appT implements it (covered by AssignableTo above, but be
	// explicit in case of named-vs-underlying surprises).
	if iface, ok := depsT.Underlying().(*types.Interface); ok && types.Implements(appT, iface) {
		return MatchAssignable
	}
	return MatchNameMismatch
}

// InterfaceAssertion is one proven (concrete satisfies interface) pair,
// rendered ready for emission. Both type strings are qualified with the
// package selector form `<pkg>.<Name>` (via types.TypeString), and
// Imports lists every package path the two strings reference so the
// emitter can build a valid import block.
type InterfaceAssertion struct {
	// DepsField is the Deps field name the pair came from — used only for
	// a deterministic sort + a human-readable comment in the emitted file.
	DepsField string
	// Interface is the qualified interface type string the concrete type
	// satisfies (e.g. "user.Repository", "billing.PlanAssigner").
	Interface string
	// Concrete is the qualified concrete type string assigned to the
	// interface in wire_gen (e.g. "*db.PostgresRepository"). Always a
	// pointer or interface spelling — the matcher only emits assertions
	// for those (see AssignablePairs) so `(Concrete)(nil)` always compiles.
	Concrete string
	// Imports maps package path → package name for every package the two
	// type strings reference. The emitter dedupes across assertions.
	Imports map[string]string
}

// AssignablePairs returns the proven concrete→interface satisfaction
// pairs for one component, ready to emit as `var _ <Interface> =
// (<Concrete>)(nil)` assertions. For each Deps field of the component
// whose declared type is an INTERFACE and whose name-matched App/AppExtras
// field holds a POINTER-or-INTERFACE concrete that the type checker
// proves satisfies it, one assertion is produced.
//
// Why only interface Deps fields with pointer/interface concretes:
//   - The whole value of the assertion (FORGE_SHAPE_REDESIGN §6c) is
//     making "what implements <Interface>" greppable. That only applies
//     when Deps.<F> IS an interface and the wired concrete is a distinct
//     named type (the fat-repo case: *db.PostgresRepository satisfies a
//     dozen narrow per-service Repository interfaces with no assertion).
//   - `(<Concrete>)(nil)` is a guaranteed-valid zero only for pointer and
//     interface concretes. Value-typed collaborators (rare for the
//     repo/client shape this targets) are skipped rather than risk an
//     un-compilable zero expression.
//
// Pairs where the concrete and interface are identical (Deps field typed
// as the same interface the App field already holds) are skipped — the
// assertion would be a tautology.
//
// Returns nil (no assertions, no error) when the component's universe
// could not be loaded/type-checked — assertions are a best-effort
// greppability aid, never a generate-blocking gate.
func (m *DepsAssignabilityMatcher) AssignablePairs(roleRoot, pkgDir string) []InterfaceAssertion {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := roleRoot + "|" + pkgDir
	u, loaded := m.universes[key]
	if !loaded {
		u = m.loadUniverseLocked(roleRoot, pkgDir)
		m.universes[key] = u
	}
	if !u.ok {
		return nil
	}

	// qualifier renders every package as its own name (`pkg.Name`) and
	// records the path→name mapping so the emitter can build imports. The
	// closure mutates the per-call imports map.
	var out []InterfaceAssertion
	for depsName, depsT := range u.depsFields {
		appT, ok := u.appFields[depsName]
		if !ok {
			continue
		}
		iface, ok := depsT.Underlying().(*types.Interface)
		if !ok {
			continue // Deps field is not an interface — nothing to grep for.
		}
		// Concrete must be a pointer or interface so (Concrete)(nil)
		// compiles. (A named interface satisfying another interface is a
		// legitimate, greppable seam too — e.g. a client type.)
		switch appT.Underlying().(type) {
		case *types.Pointer, *types.Interface:
		default:
			continue
		}
		if !types.Implements(appT, iface) && !types.AssignableTo(appT, iface) {
			continue
		}
		imports := map[string]string{}
		q := func(p *types.Package) string {
			if p == nil {
				return ""
			}
			// Types declared in pkg/app itself render unqualified — the
			// emitted file IS package app and cannot import itself.
			if u.appPkgPath != "" && p.Path() == u.appPkgPath {
				return ""
			}
			imports[p.Path()] = p.Name()
			return p.Name()
		}
		ifaceStr := types.TypeString(depsT, q)
		concreteStr := types.TypeString(appT, q)
		if ifaceStr == concreteStr {
			continue // tautology — App field already typed as the interface.
		}
		out = append(out, InterfaceAssertion{
			DepsField: depsName,
			Interface: ifaceStr,
			Concrete:  concreteStr,
			Imports:   imports,
		})
	}
	// Deterministic order so the emitted file is stable across regens.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Interface != out[j].Interface {
			return out[i].Interface < out[j].Interface
		}
		return out[i].Concrete < out[j].Concrete
	})
	return out
}

// loadUniverseLocked runs ONE packages.Load covering both pkg/app and
// roleRoot/pkgDir, then indexes each side's fields. Caller holds m.mu.
//
// The joint load is the entire point (see file header): named types and
// their method-signature parameter types are only Identical when both
// packages come from the same loader universe. Splitting this into two
// Load calls reintroduces the cross-universe false-negative.
func (m *DepsAssignabilityMatcher) loadUniverseLocked(roleRoot, pkgDir string) *matcherUniverse {
	u := &matcherUniverse{}

	absProject, err := filepath.Abs(m.projectDir)
	if err != nil {
		return u
	}
	appDir := filepath.Join(absProject, "pkg", "app")
	compDir := filepath.Join(absProject, roleRoot, filepath.FromSlash(pkgDir))

	// Mode NeedName|NeedTypes|NeedTypesInfo|NeedDeps|NeedImports|NeedSyntax
	// has been battle-tested against forge's workspace setup. NeedFiles is
	// added so each returned package can be matched back to its on-disk
	// directory (the loader does not guarantee result order). cfg.Dir = the project root, so module
	// resolution honors the project's go.mod / go.work; patterns are
	// "./"-relative and always slash-separated (go list syntax).
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports |
			packages.NeedSyntax,
		Dir: absProject,
		Env: goexec.Env(),
	}
	appPattern := "./" + path.Join("pkg", "app")
	compPattern := "./" + path.Join(filepath.ToSlash(roleRoot), filepath.ToSlash(pkgDir))
	pkgs, err := packages.Load(cfg, appPattern, compPattern)
	if err != nil || len(pkgs) == 0 {
		return u
	}

	var appPkg, compPkg *packages.Package
	for _, p := range pkgs {
		dir := packageDir(p)
		switch {
		case sameDir(dir, appDir):
			appPkg = p
		case sameDir(dir, compDir):
			compPkg = p
		}
	}
	// Refuse partial type info on EITHER side: if a package didn't
	// type-check, AssignableTo over its (incomplete) types would prove
	// nothing — see ResolveCrossPkgInterface's identical rationale.
	// Unproven, not mismatched: the consumers wire the name match.
	if appPkg == nil || len(appPkg.Errors) > 0 || appPkg.Types == nil {
		return u
	}
	if compPkg == nil || len(compPkg.Errors) > 0 || compPkg.Types == nil {
		return u
	}

	u.ok = true
	u.appFields = collectAppFieldTypes(appPkg)
	u.depsFields = collectDepsFieldTypes(compPkg)
	u.appPkgPath = appPkg.PkgPath
	return u
}

// sameDir reports whether a and b name the same on-disk directory,
// tolerating symlink aliases. macOS is the motivating case: the go
// toolchain reports /private/var/... for source trees the caller knows
// as /var/folders/... (t.TempDir), and a plain string compare would
// fail to recognize the loaded package as ours — silently degrading
// every Match to Unavailable.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

// packageDir resolves a loaded package back to its on-disk directory
// using the first known source file. Empty when the package has no
// files (typical for a pattern that matched nothing — the caller
// treats that side as unavailable).
func packageDir(p *packages.Package) string {
	for _, list := range [][]string{p.GoFiles, p.CompiledGoFiles, p.OtherFiles} {
		if len(list) > 0 {
			return filepath.Dir(list[0])
		}
	}
	return ""
}

// collectAppFieldTypes walks pkg/app's loaded types and returns a map
// from field name → declared type for every exported field on the App
// struct AND every exported field on AppExtras (which App embeds as a
// pointer; Go's promotion makes those reachable as app.<Field>).
//
// Mirrors ParseAppFields's AST-level coverage but operates on
// go/types so the resulting Type values can feed AssignableTo /
// Implements.
func collectAppFieldTypes(pkg *packages.Package) map[string]types.Type {
	out := map[string]types.Type{}
	scope := pkg.Types.Scope()
	for _, name := range []string{"App", "AppExtras"} {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for f := range st.Fields() {
			if !f.Exported() {
				continue
			}
			if f.Anonymous() {
				// Embedded (most importantly *AppExtras on App). We
				// pick those fields up via the AppExtras pass above.
				continue
			}
			// First write wins so App's own fields shadow AppExtras
			// when they collide — matches Go's field-promotion rules.
			if _, exists := out[f.Name()]; exists {
				continue
			}
			out[f.Name()] = f.Type()
		}
	}
	return out
}

// collectDepsFieldTypes walks the loaded package's types scope for a
// type named "Deps" and returns its exported fields by name. Empty
// when no Deps struct is declared (legitimate for component packages
// that don't take deps yet).
func collectDepsFieldTypes(pkg *packages.Package) map[string]types.Type {
	out := map[string]types.Type{}
	obj := pkg.Types.Scope().Lookup("Deps")
	if obj == nil {
		return out
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return out
	}
	st, ok := tn.Type().Underlying().(*types.Struct)
	if !ok {
		return out
	}
	for f := range st.Fields() {
		if !f.Exported() {
			continue
		}
		out[f.Name()] = f.Type()
	}
	return out
}
