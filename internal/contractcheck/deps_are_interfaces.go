// File: internal/contractcheck/deps_are_interfaces.go
//
// The forgeconv-deps-are-interfaces rule fails ANY package that
// declares a `Deps` struct field whose type is a concrete struct
// pointer (or a concrete struct value) rather than an interface.
//
// Why this exists, and why it is not gated on a marker
//
// A component earns its unit tests precisely because every dep is
// behind an interface: `New(Deps{...})` takes mocks, the test
// exercises the real subject, and nothing real is dragged in.
// `Deps: *db.PostgresRepository` defeats that — the test has to
// construct a real repository (or fork-and-mock the whole concrete
// type), so it stops being a unit test and starts needing a database.
//
// That property has nothing to do with what ROLE a package plays. It
// follows from `Deps` existing at all. The rule was once gated on a
// per-package role marker, and the gate is why it covered zero
// packages in forge's own tree and zero in control-plane — while
// control-plane carried 28 concrete-typed `Deps` fields, nine of them
// exactly `*db.PostgresRepository`. An opt-in rule is not a rule; it
// now applies wherever a `type Deps struct` exists.
//
// Detection
//
// 1. Scan internal/<pkg>/ for `type Deps struct { ... }`.
// 2. For each field, classify the type:
//      *T            (pointer to anything other than `interface{...}`) → fire
//      pkg.T         (selector type) → resolve it; interface → ok, else fire
//      stdlib.T      (any type from the standard library)              → ok
//      []T, map[K]T  → primitive element/key → ok; an interface or func
//                      element (a table of collaborators) → ok; else fire
//      func(...) ...  (any signature)                                   → ok
//      interface{}, named interfaces                                    → ok
//      Logger / Config singletons by field name                         → ok
//      a struct whose every method is a pure field accessor (DATA)      → ok
// 3. The `*slog.Logger`-style logger is the one allowed concrete
//    pointer (loggers are pre-configured singletons; mocking is rare
//    and hand-rolled). We allow it by matching the field name
//    `Logger` rather than the type, which is loose but pragmatic.
//
// Resolution is not optional, and that is why this is an ERROR
//
// A `pkg.T` selector is resolved for real — in THIS module by walking
// the source tree, in any other module by asking `go list` where that
// import path lives and parsing it there. Both answer one question: is
// T declared `type T interface`?
//
// That completeness is what earns error severity. The rule used to
// resolve same-module selectors only and assume every third-party
// `pkg.T` was concrete, "absorbing the false positives at warning
// severity". Measured on control-plane, that assumption produced 9 false
// findings out of 28 — `jetstream.JetStream` (7), `client.Client` and
// `audit.Store` — every one of which is declared an interface by the
// package that ships it. A rule that is wrong a third of the time cannot
// be promoted, and a rule nobody can promote is a rule authors scroll
// past; the two facts are the same fact. Resolving across module
// boundaries removes the class, and with it the hardcoded allow-list of
// forge's own interface types that existed to paper over the single most
// common instance (`orm.Context`, which forge's own CRUD generator
// writes into the Deps of every service it wires).
//
// Severity is error. The remaining findings are the real foot-gun —
// `Deps: *db.PostgresRepository`, a concrete type YOU own — and the fix
// is mechanical: declare a narrow interface at the consumer. It gates
// `forge lint`, NOT `forge generate`: preCodegenContractCheck runs the
// contract-NAMES rule alone, because that is the one whose violation
// makes generated code fail to compile. A concrete dep compiles fine; it
// just cannot be unit-tested, which is a lint verdict.

package contractcheck

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// goListTimeout caps a single `go list` lookup. The command reads the
// module graph and touches no network (GOPROXY=off), so on an idle machine
// it returns in well under a second; the bound exists so a wedged toolchain
// fails the lookup instead of hanging the linter. It is a hang guard, not a
// latency budget: on a shared 16-core box at load ~1500 one lookup of
// sigs.k8s.io/controller-runtime/pkg/client from control-plane took 9-15s
// alone, and the old 20s bound expired under `forge lint`'s concurrent
// lanes. A lookup that does time out fails the rule (see
// [depsLinter.externalPackageDir]); it never turns into a finding.
const goListTimeout = 2 * time.Minute

// lintDepsAreInterfaces walks rootDir/internal/ for packages declaring
// a `type Deps struct`, and fires on every concrete-typed field that
// isn't an interface. Returns findings in deterministic order (file,
// then line). A missing internal/ tree is not an error.
func lintDepsAreInterfaces(rootDir string) (forgeconv.Result, error) {
	internalDir := filepath.Join(rootDir, "internal")
	if _, err := os.Stat(internalDir); os.IsNotExist(err) {
		return forgeconv.Result{}, nil
	}

	lin := &depsLinter{
		rootDir:            rootDir,
		modulePath:         readModulePath(rootDir),
		declCache:          map[string]typeDecls{},
		dirCache:           map[string]string{},
		nameCache:          map[string]string{},
		purityCache:        map[string]bool{},
		funcPurityVisiting: map[string]bool{},
	}

	var pkgDirs []string
	err := filepath.WalkDir(internalDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "vendor" {
			return filepath.SkipDir
		}
		entries, readErr := os.ReadDir(p)
		if readErr != nil {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				pkgDirs = append(pkgDirs, p)
				break
			}
		}
		return nil
	})
	if err != nil {
		return forgeconv.Result{}, fmt.Errorf("walk %s: %w", internalDir, err)
	}
	sort.Strings(pkgDirs)

	var result forgeconv.Result
	for _, dir := range pkgDirs {
		findings, lintErr := lin.lintPkg(dir)
		if lintErr != nil {
			return forgeconv.Result{}, lintErr
		}
		result.Findings = append(result.Findings, findings...)
	}
	if lin.resolveErr != nil {
		return forgeconv.Result{}, fmt.Errorf("could not read every Deps field's type, so no findings rather than judge an unread type concrete: %w",
			lin.resolveErr)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// depsLinter carries the per-run state the rule needs to resolve a
// selector type (`pkg.T`) to a real declaration: the module path from
// go.mod, and caches keyed on package directory so a widely-imported dep
// is read once, not once per consumer.
type depsLinter struct {
	rootDir    string
	modulePath string
	// resolveErr is the first `go list` lookup that FAILED, as opposed to
	// one that answered "no such package". It fails the whole rule: every
	// unresolvable selector is judged concrete, so a lookup that merely
	// timed out would otherwise report correct code as an error.
	resolveErr error
	// declCache memoizes directory → the type facts parsed out of it
	// (interfaces, declared names, method counts).
	declCache map[string]typeDecls
	// dirCache memoizes import path → on-disk directory for packages
	// OUTSIDE this module, which cost a `go list` exec to locate. An
	// empty value is a cached "could not resolve", so an unresolvable
	// path is never re-execed.
	dirCache map[string]string
	// nameCache memoizes directory → package clause, for the qualifier
	// fallback in dirForQualifier.
	nameCache map[string]string
	// purityCache memoizes "is this package-level func pure", keyed by
	// purityKey(dir, name). funcPurityVisiting is the recursion guard for
	// the same keys: a function reached while it is still being evaluated
	// is part of a cycle, and a cycle is answered "impure" rather than
	// recursed into forever.
	purityCache        map[string]bool
	funcPurityVisiting map[string]bool
}

// purityKey identifies one package-level function for the purity caches.
// The directory (not the import path) is the identity, because that is what
// packageTypeDecls is keyed on and what a qualifier already resolves to.
func purityKey(dir, name string) string { return dir + "\x00" + name }

// lintPkg parses every non-test .go file in a package and checks the
// `type Deps struct` for non-interface fields. `Deps` is looked for
// across the whole package rather than in one named file because the
// scaffolds split it out of contract.go (service.go / adapter.go /
// client.go all declare it).
func (l *depsLinter) lintPkg(pkgDir string) ([]forgeconv.Finding, error) {
	rootDir := l.rootDir
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkgDir, err)
	}

	fset := token.NewFileSet()
	var (
		// fileForDeps holds the AST file containing `type Deps struct`
		// (if any) and its on-disk path so findings can point at the
		// right spot. Only the FIRST Deps struct in the package is
		// considered — multiple is unusual and likely a mistake the
		// user wants to learn about separately.
		fileForDeps *ast.File
		depsPath    string
		// files is every parsed non-test file, for seamAdvice to read how
		// the package uses a field the rule fires on.
		files []*ast.File
	)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			// Test files often declare local fake structs; not what
			// the rule targets.
			continue
		}
		fp := filepath.Join(pkgDir, e.Name())
		file, parseErr := parser.ParseFile(fset, fp, nil, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			continue
		}
		files = append(files, file)
		if fileForDeps == nil {
			if findDepsStruct(file) != nil {
				fileForDeps = file
				depsPath = fp
			}
		}
	}

	// No `type Deps struct` — nothing this rule can say. That is the
	// scope: a package that never opted into the forge composition
	// shape is not being judged on it.
	if fileForDeps == nil {
		return nil, nil
	}

	deps := findDepsStruct(fileForDeps)
	if deps == nil || deps.Fields == nil {
		return nil, nil
	}

	fileImports := importAliases(fileForDeps)

	rel, relErr := filepath.Rel(rootDir, depsPath)
	if relErr != nil {
		rel = depsPath
	}

	var findings []forgeconv.Finding
	for _, field := range deps.Fields.List {
		// Anonymous embedded fields (no Names) — skip; embedding a
		// concrete type is a different smell that this rule doesn't
		// own.
		if len(field.Names) == 0 {
			continue
		}
		// Allow the standard *slog.Logger field: it's always concrete
		// and projects rarely mock it.
		if isLoggerField(field) {
			continue
		}
		// An instantiated generic type (`hub.Subscriber[Event]`) is the
		// generic type itself as far as this rule is concerned: whether it
		// is an interface is a property of its declaration, and type
		// arguments never change that. Classify the instantiated type.
		if generic := genericBase(field.Type); generic != nil {
			if isLikelyInterfaceType(generic) || l.selectorResolvesToInterface(generic, fileImports) {
				continue
			}
		}
		if isLikelyInterfaceType(field.Type) {
			continue
		}
		// A FUNCTION-typed field is already a substitution seam, so there
		// is nothing for this rule to demand. See isFuncSeam.
		if isFuncSeam(field.Type) {
			continue
		}
		// A slice or map of interfaces (or funcs) is a table of
		// collaborators, each already a seam. See collectionOfSeams.
		if l.collectionOfSeams(field.Type, pkgDir, fileImports) {
			continue
		}
		// A selector type (`pkg.T`) is resolved for real, in this module or
		// any other: find the package on disk and ask whether T is an
		// interface. Without this the rule fires on every cross-package dep
		// — including the ones already declared as interfaces, which is the
		// shape a correct Deps is SUPPOSED to have. Both of the findings it
		// produced on forge's own tree before same-module resolution
		// (`codegen.Parser`, `contract.Service`) were interfaces, and 9 of
		// the 28 on control-plane before off-module resolution were too. A
		// rule whose hits are false positives is a rule authors mute.
		if l.selectorResolvesToInterface(field.Type, fileImports) {
			continue
		}
		// Standard-library types are out of scope. The foot-gun this rule
		// targets is a concrete type YOU own — `*db.PostgresRepository` —
		// because that is the one a narrow interface declared at the
		// consumer can replace. A stdlib boundary has its own substitution
		// seam that is not an interface (`httptest.NewServer().Client()`
		// for *http.Client, fstest.MapFS for a filesystem), and forge's own
		// adapter scaffold ships `HTTPClient *http.Client` with exactly
		// that seam exercised in the test born beside it. A rule that
		// flags the shape forge itself scaffolds is wrong on its first
		// contact with a new project.
		if isStdlibType(field.Type, fileImports) {
			continue
		}
		// Config-shaped collections of primitives are DATA (allow-lists,
		// feature-flag rosters, scalar limits-by-key) rather than
		// behavior this package calls. They have no meaningful interface
		// equivalent — `[]string` doesn't get easier to mock by hiding
		// behind an interface. Skip them at the rule level so the
		// warning stays focused on real foot-guns (concrete struct
		// pointers, concrete adapter selectors).
		if isPrimitiveConfigShape(field.Type) {
			continue
		}
		// A generated CONFIG BLOCK is the shape forge-config-deps demands.
		//
		// That rule rejects naked scalars on Deps (`BaseURL string`) and
		// tells the author to group them into a config message, because a
		// scalar has no type for the composition to resolve against and
		// gets silently wired to a zero value. Doing what it says produces
		// `Cfg *configv1.FooConfig` — which this rule then rejects, because
		// a protobuf message carries generated getters and so misses the
		// method-less-data exemption just above. Following either rule
		// violated the other, with no suppression available on this side.
		//
		// The config block is data by construction: its fields are the
		// scalars the other rule moved there, and its methods are protoc's
		// accessors, not behavior this package calls. There is nothing to
		// fake, so there is nothing to gain from an interface.
		if isGeneratedConfigType(field.Type, fileImports) {
			continue
		}
		// A concrete type whose every declared method is a PURE ACCESSOR
		// over its own fields is DATA, not a collaborator. There is
		// nothing behind it to fake, so the rule's remedy ("declare a
		// narrow interface naming the methods you call") produces a
		// wrapper around a map lookup. This is the same judgement
		// isPrimitiveConfigShape makes about `[]string`, applied to the
		// struct form: control-plane's `*config.WorkspaceConfig` is a
		// YAML-deserialized bag of storage defaults and probe shapes whose
		// two methods index its own maps, against `*db.PostgresRepository`
		// with 173 that reach a database. See concreteTypeIsData for what
		// the syntactic purity test can and cannot see, and which way it
		// errs when it cannot see.
		if l.concreteTypeIsData(field.Type, fileImports) {
			continue
		}
		remediation := depsRemediation
		if l.isValueStruct(field.Type, fileImports) {
			remediation = valueStructRemediation + remediation
		}
		for _, n := range field.Names {
			if advice := l.seamAdvice(fset, files, n.Name, field.Type, fileImports); advice != "" {
				remediation = advice + remediation
				break
			}
		}
		// Report each concrete field separately so users see every
		// site that needs an interface lift.
		for _, n := range field.Names {
			line := fset.Position(n.NamePos).Line
			findings = append(findings, forgeconv.Finding{
				Rule:     string(RuleDepsAreInterfaces),
				Severity: forgeconv.SeverityError,
				File:     rel,
				Line:     line,
				Message: fmt.Sprintf(
					"Deps field %q has concrete type %s; deps should be interfaces so this package is testable with all-mock deps",
					n.Name, exprString(field.Type)),
				Remediation: remediation,
			})
		}
	}
	return findings, nil
}

// depsRemediation is the fix every finding of this rule carries.
const depsRemediation = "type the field with an interface. Look for an existing one first — the dep's own contract.go " +
	"interface (already mocked in its mock_gen.go by `forge generate`), a stdlib one like http.Handler, or, " +
	"if this package only FORWARDS the dep, the interface the real consumer one frame down already declares. " +
	"Only mint a new one when none fits, and then name just the methods this package calls. " +
	"If the field is `// forge:optional-dep`, assign it CONDITIONALLY (`if x != nil { deps.X = x }`): " +
	"a nil concrete pointer stored in an interface field is not a nil interface, so an unconditional " +
	"assignment turns your `if deps.X == nil` guard into a nil-receiver panic. " +
	"Skill: forge skill load service-layer"

// valueStructRemediation leads the fix for a field typed as a struct VALUE.
//
// # Why a value struct with func-field seams is still a finding
//
// isFuncSeam accepts a func-typed field because a test replaces it in one
// line, and an unset one is nil: calling it panics, loudly, instead of
// reaching anything real. Bundling such fields into a value struct
// (`netguard.Policy{AllowPrivate bool; LookupIP func(...)}`) looks like the
// same seam, and would be if the struct's methods reached the world ONLY
// through those fields. That is not what such types do: the typical method
// falls back to the real thing when its func field is nil
// (`net.DefaultResolver` when LookupIP is unset), or builds a live handle
// itself (`Transport()` returns a dialing *http.Transport). A value field
// cannot be left unset, so its zero value — which is what a test that says
// `Deps{}` passes, and what the generated harness passes, since it cannot
// stub a struct — is LIVE: reliant's model_endpoint handler resolved real DNS
// in a unit test of an endpoint URL.
//
// So the field is reported, with the two fixes that fit this shape first.
const valueStructRemediation = "this field is a struct VALUE, and its zero value — what `Deps{}` and the generated " +
	"test harness pass — is live: a func field it falls back from when nil, or a handle a method builds, reaches the " +
	"real thing. When the value is DERIVED FROM CONFIGURATION (a policy, a limits table), drop the field, take the " +
	"config block it is derived from (a generated config message is accepted on Deps), and derive it in New " +
	"(`netguard.ForDeployment(deps.ControlPlane.GetUrl())`): the test then chooses it by choosing config. Otherwise "

// isValueStruct reports whether expr is `pkg.T` — no pointer — where pkg
// declares T as a struct.
func (l *depsLinter) isValueStruct(expr ast.Expr, imports map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	dir := l.dirForQualifier(pkgIdent.Name, imports)
	if dir == "" {
		return false
	}
	_, isStruct := l.packageTypeDecls(dir).structs[sel.Sel.Name]
	return isStruct
}

// genericBase returns the generic type an instantiation names —
// `pkg.T` for `pkg.T[A]` or `pkg.T[A, B]`, `T` for a same-package `T[A]` —
// or nil when expr is not an instantiation. A pointer to an instantiation
// is deliberately NOT unwrapped: `*pkg.T[A]` is a concrete pointer whatever
// T is, exactly as `*pkg.T` is.
func genericBase(expr ast.Expr) ast.Expr {
	switch t := expr.(type) {
	case *ast.IndexExpr:
		return t.X
	case *ast.IndexListExpr:
		return t.X
	}
	return nil
}

// findDepsStruct returns the *ast.StructType for the first
// `type Deps struct {...}` declaration in the file, or nil.
func findDepsStruct(f *ast.File) *ast.StructType {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Deps" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			return st
		}
	}
	return nil
}

// isLoggerField returns true for the canonical `Logger *slog.Logger`
// (or any field named exactly `Logger`). We carve loggers out because
// projects ship one shared *slog.Logger; mocking it is rare and
// usually hand-rolled rather than worth an interface. Config is treated
// the same way: bootstrap supplies one *config.Config singleton and
// tests typically construct an inline Config{...} value rather than
// fork an interface.
func isLoggerField(field *ast.Field) bool {
	for _, n := range field.Names {
		switch n.Name {
		case "Logger", "Config":
			return true
		}
	}
	return false
}

// isLikelyInterfaceType is a syntactic predicate: it returns true for
// expression shapes that the rule treats as "interface-like":
//
//   - `interface{...}` literal
//   - `any` (universe ident)
//   - bare ident type names (treated as same-package interface OR
//     domain marker; the rule errs on the side of permissive — a
//     concrete struct named locally would only be a violation if it's
//     a struct declared in this package, which is a flag-day decision
//     each project owns)
//
// Pointer types are explicitly NOT interface-like (the foot-gun
// case): `*adapter.Service` looks like an interface to the eye but is
// a concrete struct pointer.
//
// Selector types (`pkg.T`) are NOT interface-like: from the linter's
// vantage we can't always tell if `pkg.T` is `interface` or `struct`
// without resolving across packages. We default to "needs an
// interface lift" and rely on the warning's low severity to absorb
// the false-positive risk on legitimately-imported interfaces.
func isLikelyInterfaceType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.InterfaceType:
		return true
	case *ast.Ident:
		// `any` is universe-defined as `interface{}`.
		if t.Name == "any" {
			return true
		}
		// Same-package bare ident: treat as interface for permissive
		// scoring. A package that declares `type Source interface` in
		// contract.go and references `Source` in Deps is accepted; a
		// package that references a same-package struct here is a
		// false-negative, but the failure mode (tests can still mock
		// it) is exactly what the rule cares about. False-negative >
		// false-positive at warning severity.
		return true
	}
	return false
}

// isFuncSeam reports whether expr is a function type — `func() time.Time`,
// `func() string`, `func(context.Context, []byte) error`, any signature.
//
// A function value is ALREADY the substitution this rule asks for. The
// remedy the finding prescribes ("declare a narrow interface naming the
// methods you call") has no methods to name, and the interface it would
// mint — one method, one call site — is a wrapper around a value a test
// replaces in one line with a literal and no mock. That is the same
// judgement isPrimitiveConfigShape makes about `[]string` and
// concreteTypeIsData makes about a data struct: the field is not
// behaviour you cannot fake.
//
// It is also the shape forge itself wires. The Clock/IDGen seam
// (service-layer skill, "Deterministic time & IDs") is a Deps field typed
// exactly `func() time.Time` or `func() string`, filled BY TYPE in the
// generated compose.go (`Now: time.Now, // framework clock`) and defaulted
// again in the generated test harness. Firing on it made two forge
// mechanisms contradict each other, and the resolution a real run reached
// was to DELETE the seam from two packages to get a green gate — trading a
// testable clock for a passing lint. A rule that flags what forge's own
// codegen writes is wrong on its first contact with a scaffolded project.
//
// The exemption is the whole CLASS, not the two wired signatures: what
// makes a func field safe is that it is a func, not that forge knows how
// to fill it.
func isFuncSeam(expr ast.Expr) bool {
	_, ok := expr.(*ast.FuncType)
	return ok
}

// collectionOfSeams reports whether expr is a slice, array or map whose
// element type — through any nesting of those — is itself a seam: an
// interface (declared in this package, in any other, or written inline) or
// a func.
//
// Such a field is a TABLE of collaborators: reliant's trigger handler keys
// `map[string]IntegrationAccess` by integration name, each value an
// interface. Every element is substitutable on its own, so a test builds the
// table from fakes (`map[string]IntegrationAccess{"github": fake}`) with
// nothing to mock about the map itself, and the zero value is inert: a
// lookup in a nil map answers the zero value with ok=false and a range over
// it runs no iterations, exactly as an unset interface field is nil. Firing
// made the lane invent a one-method lookup interface plus an adapter type
// per map — an interface whose only implementation is a map index.
//
// A pointer or concrete element (`map[string]*store.Repo`) is still a table
// of concrete collaborators and still fires.
func (l *depsLinter) collectionOfSeams(expr ast.Expr, pkgDir string, imports map[string]string) bool {
	elem, wrapped := collectionElem(expr)
	if !wrapped {
		return false
	}
	if generic := genericBase(elem); generic != nil {
		elem = generic
	}
	switch t := elem.(type) {
	case *ast.InterfaceType, *ast.FuncType:
		return true
	case *ast.Ident:
		return t.Name == "any" || l.packageInterfaces(pkgDir)[t.Name]
	case *ast.SelectorExpr:
		return l.selectorResolvesToInterface(t, imports)
	}
	return false
}

// collectionElem peels slice, array and map-value wrappers off expr and
// returns the element type, with wrapped reporting whether there was one.
func collectionElem(expr ast.Expr) (elem ast.Expr, wrapped bool) {
	for {
		switch t := expr.(type) {
		case *ast.ArrayType:
			expr, wrapped = t.Elt, true
		case *ast.MapType:
			expr, wrapped = t.Value, true
		default:
			return expr, wrapped
		}
	}
}

// seamAdvice reads how the package USES Deps.<field> — every `deps.<field>`
// / `<x>.deps.<field>` in its non-test functions, and every local variable
// one is assigned to — and, when no use calls a method on it, names where
// the substitution point actually is. It returns "" when any use calls a
// method, reads a field other than a func, or is anything it cannot classify
// (the generic remediation then stands alone).
//
// Two shapes earn advice, because the generic remedy ("type the field with
// an interface naming the methods you call") has no methods to name:
//
//   - the package only HANDS the value on: reliant's catalog handler passed
//     *tools.ToolsFactory to a registry entry's func field —
//     `toolDef.Factory(s.deps.Tools)`, and `def.Factory(factory)` after
//     `factory := s.deps.Tools` — and the lane added a ToolFor method to the
//     factory just to have something to put in an interface. The seam is
//     that call;
//   - the package only calls a func-typed FIELD of it:
//     `s.deps.Hooks.OnSave(id)` reads a func off the struct. The seam is that
//     func, which is already the shape this rule accepts.
//
// A nil comparison (`deps.X == nil`) is not a use, and neither is a write.
func (l *depsLinter) seamAdvice(fset *token.FileSet, files []*ast.File, field string, typ ast.Expr, imports map[string]string) string {
	u := &seamUses{l: l, fset: fset, funcFields: l.structFuncFields(typ, imports)}
	isField := func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == field && isDepsReceiver(sel.X)
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// One level of local aliasing, within the function: the alias's
			// own uses are judged the same way, and an alias of the alias
			// is not chased (it reads as an unclassified use).
			for _, alias := range u.walk(fn.Body, isField, true) {
				u.walk(fn.Body, func(n ast.Node) bool {
					id, ok := n.(*ast.Ident)
					return ok && id.Name == alias
				}, false)
			}
		}
	}
	if u.generic || len(u.passed)+len(u.funcCalls) == 0 {
		return ""
	}
	var b strings.Builder
	if len(u.passed) > 0 {
		fmt.Fprintf(&b, "this package never calls a method on Deps.%s: it only hands it to %s. "+
			"That call is the seam — take a func field that makes it (a func-typed Deps field, which this rule accepts) "+
			"or a one-method interface naming it, and let the composition root close over the concrete value. ",
			field, strings.Join(u.passed, ", "))
	}
	if len(u.funcCalls) > 0 {
		fmt.Fprintf(&b, "this package only calls func-typed field(s) of Deps.%s — %s — never a method. "+
			"Those funcs are the seam: take each as a Deps field of its own func type, which this rule accepts. ",
			field, strings.Join(u.funcCalls, ", "))
	}
	b.WriteString("Otherwise, ")
	return b.String()
}

// seamUses accumulates what seamAdvice finds.
type seamUses struct {
	l          *depsLinter
	fset       *token.FileSet
	funcFields map[string]bool
	// passed is each call the value is handed to, funcCalls each func field
	// called on it, both as "<what> (<file:line>)".
	passed, funcCalls []string
	// generic is set by any use that is neither: a method call, a field
	// read, a return, a composite literal, anything unclassified.
	generic bool
}

// walk classifies every node in body that match selects. With wantAliases,
// a use assigned to a local identifier is returned as an alias to follow
// rather than counted; without it, such an assignment is unclassified.
func (u *seamUses) walk(body ast.Node, match func(ast.Node) bool, wantAliases bool) (aliases []string) {
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		defer func() { stack = append(stack, n) }()
		if !match(n) || len(stack) == 0 {
			return true
		}
		if alias := u.classify(stack, n.(ast.Expr)); alias != "" {
			if wantAliases {
				aliases = append(aliases, alias)
			} else {
				u.generic = true
			}
		}
		return true
	})
	return aliases
}

// classify records one use of the value, given the stack of its ancestors.
// It returns the local name the use is assigned to, if that is what it is.
func (u *seamUses) classify(stack []ast.Node, use ast.Expr) (alias string) {
	at := u.l.relPos(u.fset, use.Pos())
	switch parent := stack[len(stack)-1].(type) {
	case *ast.BinaryExpr:
		if !isNilIdent(parent.X) && !isNilIdent(parent.Y) {
			u.generic = true
		}
	case *ast.CallExpr:
		if parent.Fun == use {
			u.generic = true
			break
		}
		u.passed = append(u.passed, exprString(parent.Fun)+" ("+at+")")
	case *ast.SelectorExpr:
		if parent.X != use {
			return "" // the name of a field or method, not the value
		}
		call, isCall := grandparent(stack).(*ast.CallExpr)
		if isCall && call.Fun == parent && u.funcFields[parent.Sel.Name] {
			u.funcCalls = append(u.funcCalls, parent.Sel.Name+" ("+at+")")
			break
		}
		u.generic = true
	case *ast.AssignStmt:
		for i, lhs := range parent.Lhs {
			if lhs == use {
				return "" // a write, not a use
			}
			if i < len(parent.Rhs) && parent.Rhs[i] == use && len(parent.Lhs) == len(parent.Rhs) {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					return id.Name
				}
			}
		}
		u.generic = true
	case *ast.ValueSpec:
		for i, v := range parent.Values {
			if v == use && i < len(parent.Names) && len(parent.Names) == len(parent.Values) {
				return parent.Names[i].Name
			}
		}
		for _, name := range parent.Names {
			if name == use {
				return "" // the declaration itself
			}
		}
		u.generic = true
	default:
		u.generic = true
	}
	return ""
}

// structFuncFields returns the func-typed fields of the struct expr names
// (through an optional pointer), or nil when it is not a resolvable struct.
func (l *depsLinter) structFuncFields(expr ast.Expr, imports map[string]string) map[string]bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil
	}
	dir := l.dirForQualifier(pkgIdent.Name, imports)
	if dir == "" {
		return nil
	}
	st, ok := l.packageTypeDecls(dir).structs[sel.Sel.Name]
	if !ok || st.Fields == nil {
		return nil
	}
	out := map[string]bool{}
	for _, f := range st.Fields.List {
		if _, isFunc := f.Type.(*ast.FuncType); !isFunc {
			continue
		}
		for _, n := range f.Names {
			out[n.Name] = true
		}
	}
	return out
}

// isDepsReceiver reports whether expr is how a forge component reaches its
// Deps: the `deps` parameter, or a `deps` field (`s.deps`).
func isDepsReceiver(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "deps"
	case *ast.SelectorExpr:
		return t.Sel.Name == "deps"
	}
	return false
}

func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

// grandparent returns the node two above the top of an ast.Inspect stack,
// or nil.
func grandparent(stack []ast.Node) ast.Node {
	if len(stack) < 2 {
		return nil
	}
	return stack[len(stack)-2]
}

// relPos renders pos as a rootDir-relative file:line.
func (l *depsLinter) relPos(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	rel, err := filepath.Rel(l.rootDir, p.Filename)
	if err != nil {
		rel = p.Filename
	}
	return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), p.Line)
}

// isStdlibType reports whether expr names a type from the Go standard
// library, through any number of pointer / slice / map / array wrappers.
//
// The standard-library test is the canonical one: an import path whose
// FIRST segment contains no dot is stdlib ("net/http", "log/slog"), while
// everything with a hostname up front ("github.com/...", "example.com/...")
// is not.
func isStdlibType(expr ast.Expr, imports map[string]string) bool {
	sel := underlyingSelector(expr)
	if sel == nil {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	importPath, ok := imports[pkgIdent.Name]
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

// isGeneratedConfigType reports whether expr names a type from a project's
// GENERATED config package — `*configv1.IdpConfig` out of
// `<module>/gen/<project>/config/v1`, or the `pkg/config` alias over it.
//
// Matched on the import path rather than the type name so it cannot be
// spoofed by naming a repository `FooConfig`: the path is what marks it as
// forge's own projection of proto/<project>/config/v1, and those messages
// are exactly the config blocks forge-config-deps asks authors to create.
func isGeneratedConfigType(expr ast.Expr, imports map[string]string) bool {
	sel := underlyingSelector(expr)
	if sel == nil {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	importPath, ok := imports[pkgIdent.Name]
	if !ok {
		return false
	}
	return (strings.Contains(importPath, "/gen/") && strings.HasSuffix(importPath, "/config/v1")) ||
		strings.HasSuffix(importPath, "/pkg/config")
}

// underlyingSelector peels pointer / slice / array / map-value wrappers off
// expr and returns the selector underneath, or nil when there isn't one.
func underlyingSelector(expr ast.Expr) *ast.SelectorExpr {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ArrayType:
			expr = t.Elt
		case *ast.MapType:
			expr = t.Value
		case *ast.SelectorExpr:
			return t
		default:
			return nil
		}
	}
}

// readModulePath returns the module path from rootDir/go.mod, or "" when
// there is no parseable go.mod. An unresolvable module path degrades the
// selector check to the conservative default (fire), which is the same
// answer the rule gave before resolution existed.
func readModulePath(rootDir string) string {
	b, err := os.ReadFile(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// importAliases maps the local package name used in selector expressions
// (explicit alias, else the last path segment) to its import path, for one
// file.
func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		p := strings.Trim(imp.Path.Value, `"`)
		alias := ""
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			alias = p
			if i := strings.LastIndex(alias, "/"); i >= 0 {
				alias = alias[i+1:]
			}
		}
		if alias == "_" || alias == "." {
			continue
		}
		out[alias] = p
	}
	return out
}

// selectorResolvesToInterface reports whether expr is a bare selector
// `pkg.T` naming a type declared as an interface — in this module or any
// other. It deliberately does not follow pointers: `*pkg.T` is a concrete
// pointer whatever T is (a pointer to an interface is a mistake, not a
// seam), so those keep firing.
//
// Same-module paths are answered from the rootDir walk with no exec. Every
// other import path is located with [depsLinter.externalPackageDir] and
// then read the same way, because "is this an interface" has one answer and
// a module boundary is not part of it. Guessing "concrete" for anything
// off-module is what made this rule wrong on `jetstream.JetStream`,
// `client.Client` and `audit.Store` — all three declared `type … interface`
// by the package that ships them.
func (l *depsLinter) selectorResolvesToInterface(expr ast.Expr, imports map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	dir := l.dirForQualifier(pkgIdent.Name, imports)
	if dir == "" {
		return false
	}
	return l.packageInterfaces(dir)[sel.Sel.Name]
}

// dirForQualifier maps the qualifier in `pkg.T` to the directory holding
// that package's sources.
//
// The fast path is the import map, which keys on the explicit alias or the
// import path's last segment. That is right almost always and wrong exactly
// when a module's directory name differs from its package clause —
// `github.com/nats-io/nats.go` is package `nats`, so `nats.Conn` finds
// nothing under the key "nats.go". On a miss, resolve each of the file's
// imports and ask the package itself what it is called. Without the
// fallback an unresolved qualifier defaults to "concrete", which at error
// severity is a build-gate failure on code that is already correct.
func (l *depsLinter) dirForQualifier(qualifier string, imports map[string]string) string {
	if importPath, ok := imports[qualifier]; ok {
		if dir := l.packageDir(importPath); dir != "" {
			return dir
		}
	}
	for alias, importPath := range imports {
		if alias == qualifier {
			continue // already tried above
		}
		// An explicit alias IS the qualifier by definition; if it did not
		// match, the package clause underneath is irrelevant. Only paths
		// whose last segment supplied the key can be lying about their name.
		if alias != path.Base(importPath) {
			continue
		}
		dir := l.packageDir(importPath)
		if dir != "" && l.packageName(dir) == qualifier {
			return dir
		}
	}
	return ""
}

// concreteTypeIsData reports whether expr names a type — through an
// optional pointer — that its own package declares as DATA rather than as a
// collaborator.
//
// # Why purity and not a method count
//
// The question this rule actually needs answered is not "how many methods"
// but "is there anything here a test would have to fake". A struct parsed
// out of YAML that grows two convenience accessors over its OWN fields is
// still data: an interface over `StorageSizeForTier(string) string` asserts
// nothing and mocks nothing, and the remedy the finding prescribes produces
// a wrapper around a map index. Conversely a type with ONE method that
// opens a connection is a collaborator whatever it is named, and a count
// threshold would wave it through.
//
// The predecessor of this function tested for zero methods, and named
// control-plane's `*config.WorkspaceConfig` in its own comment as the case
// it existed to cover. That config then grew two tier-lookup accessors and
// the exemption stopped covering the case it was written for — which is the
// evidence that a count was never the distinction.
//
// So: T is data when it embeds nothing (an embedded field promotes a whole
// foreign method set that no declaration in this package can see) and EVERY
// method it declares is a pure value accessor — accessor-shaped in its
// signature (methodSignatureIsAccessorShaped) and, unless the type's fields
// are provably inert, pure in its body (methodBodyIsPure). A zero-method
// struct satisfies that vacuously, so the case the old test covered is
// still covered.
//
// # What the syntactic test CANNOT see, and which way it errs
//
// It has no type information. It cannot tell that `c.Store.Get(k)` reads a
// map rather than a database, so it sees a call and refuses. It cannot
// follow a call into a helper to prove the helper is pure, so it refuses
// there too. Both answer "collaborator" for something that may be data,
// and that is the SAFE direction: the author sees a finding on a config
// struct — the status quo before this exemption existed — rather than a
// repository slipping past the rule.
//
// The unsafe direction, vouching for a real collaborator, requires a type
// whose every method reads its own fields and calls nothing at all. That is
// the definition of data, so there is no shape to spoof: a repository
// cannot escape by being named `Settings`, by declaring few methods, or by
// living in a package called `config`.
func (l *depsLinter) concreteTypeIsData(expr ast.Expr, imports map[string]string) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	dir := l.dirForQualifier(pkgIdent.Name, imports)
	if dir == "" {
		return false // unresolvable: keep the conservative answer
	}
	decls := l.packageTypeDecls(dir)
	if !decls.declared[sel.Sel.Name] {
		return false // not declared there; do not vouch for it
	}
	// An embedded field PROMOTES the embedded type's whole method set, so
	// `type Wrapper struct{ *sql.DB }` declares no methods and carries
	// hundreds. No purity check over this package's own declarations can
	// see them, so a struct that embeds anything is never data.
	if decls.embeds[sel.Sel.Name] {
		return false
	}
	// Every method must be accessor-SHAPED in its signature, whatever else
	// is true: a method taking a context, returning an error, or returning
	// nothing is an operation, and a type with one is a collaborator. That
	// clause alone is what keeps `Save(ctx, id) error` and a stubbed
	// `Close()` on the collaborator side no matter what the type holds.
	for _, m := range decls.methods[sel.Sel.Name] {
		if !methodSignatureIsAccessorShaped(m, decls) {
			return false
		}
	}
	// With the signatures vouched for, there are two independent ways to be
	// data, and a type needs only one:
	//
	//  1. Its FIELDS are inert AND no method reaches package-level state —
	//     it holds nothing that could be faked and it reads no singleton, so
	//     what the accessors compute over their own values is irrelevant.
	//  2. Its METHOD BODIES are fully pure — the original test, and still
	//     the answer for a type whose fields this rule cannot classify.
	//
	// Neither subsumes the other. A config that derives a value through a
	// third-party parser fails (2) and passes (1); a type holding something
	// unresolvable whose accessors plainly touch nothing fails (1) and
	// passes (2).
	//
	// Route 1 keeps the package-state clause because inert fields say what a
	// type HOLDS and say nothing about what it REACHES. A struct of one
	// string whose accessor indexes a package-level map is holding its
	// collaborator at package scope instead of in a field, and that is the
	// one way an inert-field type can still be a collaborator.
	if l.structFieldsAreInert(decls, sel.Sel.Name, 0) &&
		l.methodsAvoidPackageState(decls, sel.Sel.Name) {
		return true
	}
	// Route 2 additionally requires that the type not HOLD a collaborator.
	//
	// Pure method bodies alone were never sufficient, and this closes a hole
	// that predates the inert-fields work: a struct holding a *sql.DB whose
	// only declared method is `func (p *Pool) Name() string { return "pool" }`
	// passed the purity walk outright, because the walk reads the methods
	// that ARE declared and says nothing about the handle sitting in a field
	// that none of them happens to touch. Testing the fields — now that
	// there is a test for it — closes that from the other side.
	//
	// A type whose fields cannot be classified at all still reaches this
	// route, which is its purpose: it is the answer for a struct this rule
	// cannot see into, judged on the only evidence available.
	if l.structHoldsCollaborator(decls, sel.Sel.Name) {
		return false
	}
	for _, m := range decls.methods[sel.Sel.Name] {
		if !l.methodBodyIsPure(m, decls) {
			return false
		}
	}
	return true
}

// structHoldsCollaborator reports whether the named struct has a field that
// is UNAMBIGUOUSLY a collaborator: a pointer, a func, a channel, or an
// interface. Each is a way of holding something a test would want to
// substitute.
//
// This is the narrow converse of structFieldsAreInert, not its negation.
// Inert asks "is every field provably harmless" and answers no for anything
// unrecognised; this asks "is any field provably a handle" and answers no for
// anything unrecognised. A type that is neither — one holding a value type
// this rule cannot classify — falls through to the method-purity walk, which
// is the right treatment for a shape it cannot see into.
func (l *depsLinter) structHoldsCollaborator(decls typeDecls, name string) bool {
	st, ok := decls.structs[name]
	if !ok || st.Fields == nil {
		return false
	}
	for _, f := range st.Fields.List {
		switch ft := f.Type.(type) {
		case *ast.StarExpr, *ast.FuncType, *ast.ChanType, *ast.InterfaceType:
			return true
		case *ast.Ident:
			// A nested struct from this package: follow one level, so a
			// handle does not hide behind a wrapper field.
			if _, isStruct := decls.structs[ft.Name]; isStruct && ft.Name != name {
				if l.structHoldsCollaborator(decls, ft.Name) {
					return true
				}
			}
		}
	}
	return false
}

// methodsAvoidPackageState reports whether no method on the named type
// reaches package-level VAR state, goroutines, channels, or an I/O-boundary
// package — directly or through a package-level helper it calls.
//
// This is the companion clause to structFieldsAreInert. Inert fields say what
// a type HOLDS; they say nothing about what it REACHES. A struct of one
// string whose accessor indexes a package-level map is holding its
// collaborator at package scope rather than in a field, and it would
// otherwise walk straight through the inert-fields route.
//
// It is deliberately WEAKER than methodBodyIsPure: a call to a resolvable
// pure helper is fine, and so is a call into a third-party value parser like
// resource.ParseQuantity, which is the whole reason the inert-fields route
// exists. What it refuses is the set of constructs that mean "this reaches
// something outside its own values".
func (l *depsLinter) methodsAvoidPackageState(decls typeDecls, name string) bool {
	for _, m := range decls.methods[name] {
		if !l.bodyAvoidsPackageState(m, decls, 0) {
			return false
		}
	}
	return true
}

// helperFollowDepth bounds how far bodyAvoidsPackageState follows a
// package-level helper. Config helpers chain a step or two; deeper than this
// is not a shape to vouch for.
const helperFollowDepth = 3

// bodyAvoidsPackageState is methodsAvoidPackageState's per-body walk. See
// that function for what it refuses and why it is weaker than
// methodBodyIsPure.
func (l *depsLinter) bodyAvoidsPackageState(m methodDecl, decls typeDecls, depth int) bool {
	if m.body == nil {
		return false
	}
	if depth > helperFollowDepth {
		return false
	}
	ok := true
	ast.Inspect(m.body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch t := n.(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.SelectStmt, *ast.SendStmt:
			ok = false
		case *ast.UnaryExpr:
			if t.Op == token.ARROW {
				ok = false
			}
		case *ast.Ident:
			// Package-level VAR: mutable shared state, the singleton route.
			if decls.vars[t.Name] {
				ok = false
			}
		case *ast.CallExpr:
			switch fun := t.Fun.(type) {
			case *ast.Ident:
				// A local package-level helper: follow it, so state reached
				// one frame down is caught too.
				if decls.funcs[fun.Name] {
					if callee, found := decls.funcDecls[fun.Name]; found {
						if !l.bodyAvoidsPackageState(callee, decls, depth+1) {
							ok = false
						}
					} else {
						ok = false
					}
				}
			case *ast.SelectorExpr:
				// A call into an I/O-boundary package is out regardless of
				// what it looks like. A qualified call into anything else
				// (a value parser, a formatter) is allowed — that is the
				// point of this route.
				if pkgIdent, isIdent := fun.X.(*ast.Ident); isIdent {
					if importPath, known := m.imports[pkgIdent.Name]; known && packageIsIOBoundary(importPath) {
						ok = false
					}
				}
			}
		}
		return ok
	})
	return ok
}

// inertFieldDepth bounds how far structFieldsAreInert follows a struct-typed
// field into its own package. Config nests a level or two (a probe block, a
// tier block); anything deeper is not a shape worth vouching for blind.
const inertFieldDepth = 3

// structFieldsAreInert reports whether every field of the named struct is
// INERT: a primitive, a stdlib value type, or a collection or nested struct
// built out of those. Nothing a test could want to substitute.
//
// # Why a second route to the data verdict
//
// The per-method purity walk asks "do these methods do anything". That is the
// right question for a type that might hold a collaborator, and it is the
// wrong one for a type that demonstrably cannot. A struct of eighteen
// scalars, two probe blocks and a couple of string maps has no field that
// could hold a database, a client, or a queue — so there is nothing behind it
// to fake, whatever arithmetic its accessors perform on the way out.
//
// Measured on control-plane's `*config.WorkspaceConfig`, the type this
// exemption was written for and named after. Its accessors were rewritten to
// derive the docker ladder from the data ladder, which routes through
// `resource.ParseQuantity` — a pure value parser in k8s apimachinery. No
// purity walk short of type-checking a third-party module can clear that, and
// chasing it would mean following calls into arbitrary dependencies. Asking
// what the struct HOLDS answers the question in one step and does not depend
// on how far a helper chain reaches.
//
// # Why it is not a hole
//
// A collaborator's defining property is that it holds something: a *sql.DB, a
// client, a connection pool, a channel, a func. Any of those makes a field
// non-inert and the exemption does not apply, regardless of naming, package,
// or method count. The checks are deliberately narrow:
//
//   - an embedded field is never inert (it promotes a foreign method set)
//   - a pointer field is never inert — a *T is how a struct holds a shared
//     mutable thing, and the parse-free way to tell "*Tier" from "*sql.DB" is
//     not available here
//   - an interface, func, or chan field is never inert
//   - a selector type is inert only for a KNOWN-VALUE stdlib set (time.Time,
//     time.Duration, url.URL, big.Int …). Every other qualified type,
//     including anything from a third-party module, is not.
//   - a nested struct from the same package is followed, to inertFieldDepth,
//     and must itself be inert
//
// So a repository cannot reach this shape: it has to hold its handle
// somewhere, and every way of holding one fails the test.
func (l *depsLinter) structFieldsAreInert(decls typeDecls, name string, depth int) bool {
	if depth > inertFieldDepth {
		return false
	}
	st, ok := decls.structs[name]
	if !ok || st.Fields == nil {
		return false
	}
	if decls.embeds[name] {
		return false
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return false // embedded
		}
		if !l.typeIsInert(decls, f.Type, depth) {
			return false
		}
	}
	return true
}

// typeIsInert is structFieldsAreInert's per-field test. See that function for
// what counts and why the unknown cases answer "not inert".
func (l *depsLinter) typeIsInert(decls typeDecls, expr ast.Expr, depth int) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		if isPrimitiveType(t) {
			return true
		}
		// A struct declared in this same package: follow it.
		if _, isStruct := decls.structs[t.Name]; isStruct {
			return l.structFieldsAreInert(decls, t.Name, depth+1)
		}
		// A named type that is not a struct here (an interface, or an alias
		// to something unresolved) is not vouched for.
		return false
	case *ast.ArrayType:
		return l.typeIsInert(decls, t.Elt, depth)
	case *ast.MapType:
		return l.typeIsInert(decls, t.Key, depth) && l.typeIsInert(decls, t.Value, depth)
	case *ast.SelectorExpr:
		return isInertStdlibValueType(t)
	}
	// Pointers, funcs, chans, interfaces, and anything unrecognised: a
	// collaborator is held through exactly these, so none of them is inert.
	return false
}

// inertStdlibValueTypes are the stdlib types that are VALUES — carrying data,
// substituting for nothing. Deliberately a closed list rather than "any
// stdlib type": *os.File, net.Conn and sql.DB are stdlib too, and each is a
// live handle.
var inertStdlibValueTypes = map[string]bool{
	"time.Time": true, "time.Duration": true, "time.Month": true,
	"url.URL": true, "net.IP": true, "net.IPNet": true,
	"big.Int": true, "big.Float": true, "big.Rat": true,
	"regexp.Regexp": true, "json.RawMessage": true,
}

// isInertStdlibValueType reports whether sel names one of the value types
// above, matched on the qualifier's spelling. A local package aliased to
// `time` could spoof this; it would also have to declare a type named `Time`
// that holds a collaborator, which is not a shape that occurs by accident.
func isInertStdlibValueType(sel *ast.SelectorExpr) bool {
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return inertStdlibValueTypes[pkgIdent.Name+"."+sel.Sel.Name]
}

// methodSignatureIsAccessorShaped applies the three signature tests that
// separate a value lookup from an operation. All must hold:
//
//  1. It RETURNS at least one value. A pure computation over the
//     receiver's fields that yields nothing did nothing; the method is
//     there to mutate state or to perform an effect. This is the clause
//     that keeps a stubbed-out `Close()` on the collaborator side.
//  2. It takes no context.Context. A context is the marker of an
//     operation that can block or be cancelled — the defining property
//     of the I/O this rule exists to keep behind an interface.
//  3. It returns no error. A lookup over your own fields cannot fail: the
//     miss case returns a default, as both of control-plane's tier
//     accessors do. An error result means there was something out there
//     to go wrong with.
//
// Each is individually easy to satisfy by accident; the AND of the three,
// applied to EVERY method on the type, is not. A repository cannot reach
// this shape while remaining a repository.
func methodSignatureIsAccessorShaped(m methodDecl, decls typeDecls) bool {
	if m.sig == nil || m.sig.Results == nil || len(m.sig.Results.List) == 0 {
		return false
	}
	if m.sig.Params != nil {
		for _, p := range m.sig.Params.List {
			if isContextType(p.Type, m.imports) {
				return false
			}
		}
	}
	for _, r := range m.sig.Results.List {
		if isErrorType(r.Type, decls) {
			return false
		}
	}
	return true
}

// isContextType reports whether expr names context.Context, resolved
// through the file's import aliases so an aliased import still matches and
// a local package that happens to be called `context` does not.
func isContextType(expr ast.Expr, imports map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Context" {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return imports[pkgIdent.Name] == "context"
}

// isErrorType reports whether expr is the universe `error`, or a named
// type the declaring package itself declares that is not shadowing it.
// A custom error type is usually returned as `error`; the bare-ident test
// is what matters, and anything ambiguous is left to the body check.
func isErrorType(expr ast.Expr, decls typeDecls) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "error" && !decls.declared["error"]
}

// methodBodyIsPure reports whether m's body computes from the receiver's
// own fields and nothing else.
//
// Impure is the default for anything the walk cannot account for. The
// rejected constructs are the ones that mean "this method does work":
//
//   - no body at all (assembly, or //go:linkname) — nothing to inspect
//   - go / defer / select / channel send or receive
//   - a call that is neither a builtin, a type conversion, nor a
//     package-level func whose own body passes this same walk
//   - a reference to a package-level var of the declaring package, or to a
//     package-level func used as a VALUE rather than called
//
// The CALL is the load-bearing one, and the distinction is WHAT it reaches.
// `c.pool.Query(q)` goes through a FIELD, and no syntax distinguishes a
// field holding a map from one holding a database, so any call through a
// field is impure. A call to a package-level func reaches exactly one
// declaration, which is right there to be read — so it is read, recursively,
// by funcIsPure. Refusing to read it is what made this rule fire on a config
// struct whose accessor merely called a named helper, and pass on the same
// struct once the helper was inlined.
//
// A CONVERSION is syntactically a call (`time.Duration(n)`, `string(b)`)
// and is not one, so callIsPure resolves it: a qualified callee is a
// conversion when the package it names declares that identifier as a type.
// Without this, a config that renders a duration would be judged a
// collaborator — the same false positive this exemption exists to prevent,
// one refactor later.
//
// Package-level CONSTS are allowed; vars are not. A config method's
// fallback is usually a const in its own package (`return
// defaultStorageSize`), and a const is a compile-time value with no state
// to fake. A package-level var is mutable shared state, and reading one is
// how a method reaches a singleton client.
func (l *depsLinter) methodBodyIsPure(m methodDecl, decls typeDecls) bool {
	if m.body == nil {
		return false
	}
	// Callee identifiers are collected up front so the Ident arm below can
	// tell `helper(x)` — already judged on its body by callIsPure — from a
	// bare `helper` used as a value, which nothing here can follow.
	calleeIdents := map[*ast.Ident]bool{}
	ast.Inspect(m.body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				calleeIdents[id] = true
			}
		}
		return true
	})
	pure := true
	ast.Inspect(m.body, func(n ast.Node) bool {
		if !pure {
			return false
		}
		switch t := n.(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.SelectStmt, *ast.SendStmt:
			pure = false
		case *ast.UnaryExpr:
			if t.Op == token.ARROW { // <-ch
				pure = false
			}
		case *ast.CallExpr:
			if !l.callIsPure(t.Fun, m.imports, decls) {
				pure = false
			}
		case *ast.Ident:
			// A bare name the declaring package binds to a VAR is mutable
			// shared state — how a method reaches a singleton — so it is
			// still rejected. Locals, params, fields, consts and type
			// names are not state, and a local that SHADOWS a package-level
			// name is misread as one, which errs toward "collaborator".
			//
			// A package-level FUNC is deliberately NOT rejected here. The
			// CallExpr arm above already judged it on its body via
			// callIsPure; rejecting the callee identifier as well would
			// undo that verdict for every call, since a call's callee is
			// also visited as an Ident by this same walk.
			if decls.vars[t.Name] {
				pure = false
			}
			if decls.funcs[t.Name] && !calleeIdents[t] {
				// The func is referenced as a VALUE, not called — passed
				// somewhere, or assigned. Nothing here evaluates what the
				// receiver of that value will do with it, so the
				// conservative answer stands.
				pure = false
			}
		}
		return pure
	})
	return pure
}

// callIsPure reports whether a call expression's callee is something with
// no behaviour behind it: a Go builtin, or a type conversion.
func (l *depsLinter) callIsPure(fun ast.Expr, imports map[string]string, decls typeDecls) bool {
	switch t := fun.(type) {
	case *ast.ParenExpr:
		return l.callIsPure(t.X, imports, decls)
	case *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.InterfaceType, *ast.StructType, *ast.ChanType, *ast.FuncType:
		// `[]byte(s)`, `map[string]string(m)`, `(*T)(p)` — a type literal
		// in callee position is always a conversion.
		return true
	case *ast.Ident:
		if pureBuiltins[t.Name] || isPredeclaredTypeName(t.Name) {
			return true
		}
		// A type declared by the package under inspection: a conversion.
		if decls.declared[t.Name] && !decls.funcs[t.Name] {
			return true
		}
		// A package-level FUNC of the declaring package: look at what it
		// actually does. See funcIsPure for why this is not a hole.
		if decls.funcs[t.Name] {
			return l.funcIsPure(decls, t.Name)
		}
		return false
	case *ast.SelectorExpr:
		// `pkg.X(v)` is a conversion when pkg declares X as a TYPE
		// (`time.Duration(n)`) and a call when it declares it as a func
		// (`time.Now()`). Resolve the package and ask — the same
		// resolution selectorResolvesToInterface already does for `pkg.T`.
		pkgIdent, ok := t.X.(*ast.Ident)
		if !ok {
			return false // a method call on a value: never a conversion
		}
		dir := l.dirForQualifier(pkgIdent.Name, imports)
		if dir == "" {
			return false // unresolvable, or a method on a local: conservative
		}
		other := l.packageTypeDecls(dir)
		if other.declared[t.Sel.Name] && !other.funcs[t.Sel.Name] {
			return true
		}
		// A func in ANOTHER package of this module (or a resolvable
		// dependency) gets the same body-based judgement as a local one —
		// but only when that package is not itself an I/O boundary. See
		// funcIsPure and packageIsIOBoundary.
		if other.funcs[t.Sel.Name] {
			if importPath, ok := imports[pkgIdent.Name]; ok && packageIsIOBoundary(importPath) {
				return false
			}
			return l.funcIsPure(other, t.Sel.Name)
		}
		return false
	}
	return false
}

// funcIsPure reports whether the package-level function named fn, declared
// in decls, computes from its arguments and package consts and nothing else.
//
// # Why this exists
//
// Calling a package-level helper used to disqualify a type from the
// data-struct exemption outright, because the walk could not prove the
// helper was pure and "cannot see" was answered "collaborator". That is
// right for a call THROUGH A FIELD — `c.pool.Query(q)` reaches whatever the
// field holds, and no syntax distinguishes a map from a database. It is
// wrong for a package-level func, because that call reaches exactly one
// declaration and it is right there to be read.
//
// Measured on control-plane: `*config.WorkspaceConfig` is a
// YAML-deserialized bag of storage defaults whose accessors index its own
// maps — the exact case the exemption's own comment names. It fired anyway,
// solely because one accessor called a package-level tier helper, and
// INLINING that helper made the finding disappear. A rule whose verdict
// flips on whether a pure expression was given a name is not measuring
// anything about the type.
//
// # Why it is not a hole
//
// The helper's body gets the SAME walk as a method body — no I/O, no
// goroutines, no channels, no package-level vars, and every call it makes
// recursively judged the same way. A helper that reaches a database fails
// that walk exactly as an inlined copy of it would, so naming a statement
// cannot launder it. Three further guards:
//
//   - A function whose body is unavailable (assembly, //go:linkname, or a
//     package that did not parse) is impure: the default remains "cannot
//     see means collaborator".
//   - Recursion is answered impure via funcPurityVisiting rather than
//     recursed into. A cycle is not a shape a config accessor has.
//   - A call into a package that is an I/O boundary by construction (net,
//     os, database/sql …) is rejected at the call site without reading it,
//     because those bodies are pure-looking wrappers over syscalls. See
//     packageIsIOBoundary.
//
// What remains exempt is a type whose every method computes over its own
// fields, possibly by way of named helpers that do the same. That is data,
// whether or not its author factored it into functions.
func (l *depsLinter) funcIsPure(decls typeDecls, fn string) bool {
	decl, ok := decls.funcDecls[fn]
	if !ok || decl.body == nil {
		return false // no body to read: conservative
	}
	key := purityKey(decls.dir, fn)
	if got, ok := l.purityCache[key]; ok {
		return got
	}
	if l.funcPurityVisiting[key] {
		return false // recursive: not a config accessor's shape
	}
	l.funcPurityVisiting[key] = true
	defer delete(l.funcPurityVisiting, key)

	pure := l.methodBodyIsPure(decl, decls)
	l.purityCache[key] = pure
	return pure
}

// packageIsIOBoundary reports whether importPath names a package that
// performs I/O by construction, so a call into it is rejected without
// reading the body.
//
// Reading them would be worse than useless: `os.Getenv` and `net.Dial` are
// thin wrappers whose Go-level bodies look pure, and `time.Now` reaches the
// clock through a runtime linkname with no body at all. Any of those inside
// a "config accessor" means the type is reaching outside itself, which is
// the thing this rule exists to catch.
//
// Matched on the import path's stdlib root, plus any path segment naming a
// well-known I/O concern, so a third-party client package is caught too.
func packageIsIOBoundary(importPath string) bool {
	root, _, _ := strings.Cut(importPath, "/")
	switch root {
	case "os", "net", "io", "syscall", "database", "bufio", "log", "time", "crypto", "runtime", "os/exec":
		return true
	}
	for _, seg := range strings.Split(importPath, "/") {
		switch seg {
		case "http", "sql", "grpc", "client", "db", "redis", "nats", "kafka", "s3":
			return true
		}
	}
	return false
}

// pureBuiltins are the Go builtins a data accessor may use. Each computes
// over values already in hand and reaches nothing outside the call.
var pureBuiltins = map[string]bool{
	"len": true, "cap": true, "append": true, "copy": true,
	"make": true, "new": true, "delete": true, "clear": true,
	"min": true, "max": true,
	"complex": true, "real": true, "imag": true,
}

// isPredeclaredTypeName reports whether name is a universe-block type, so
// `string(b)` / `int64(n)` in callee position read as conversions rather
// than as calls.
func isPredeclaredTypeName(name string) bool {
	switch name {
	case "string", "bool", "error", "any",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune",
		"float32", "float64", "complex64", "complex128":
		return true
	}
	return false
}

// packageName returns the package clause of dir's first parseable non-test
// .go file, memoized per run. Empty when dir holds no readable Go source.
func (l *depsLinter) packageName(dir string) string {
	if got, ok := l.nameCache[dir]; ok {
		return got
	}
	l.nameCache[dir] = ""
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
		if parseErr != nil || f.Name == nil {
			continue
		}
		l.nameCache[dir] = f.Name.Name
		return f.Name.Name
	}
	return ""
}

// packageDir returns the on-disk directory holding importPath's sources, or
// "" when it cannot be located. A path inside this module is derived from
// the module path with no subprocess; anything else goes to
// externalPackageDir.
func (l *depsLinter) packageDir(importPath string) string {
	if l.modulePath != "" {
		if rel, ok := strings.CutPrefix(importPath, l.modulePath+"/"); ok {
			return filepath.Join(l.rootDir, filepath.FromSlash(rel))
		}
		if importPath == l.modulePath {
			return l.rootDir
		}
	}
	return l.externalPackageDir(importPath)
}

// externalPackageDir asks the go command where an off-module import path
// lives — the module cache, a `replace` directory, or a go.work member.
// The result is memoized per run (including the failure), so a widely
// imported dep costs one exec no matter how many Deps structs name it.
//
// GOPROXY=off makes this hermetic: a package that is not already on disk
// resolves to "", which falls back to the conservative "not an interface"
// answer rather than reaching for the network from inside a linter. -e
// keeps go list from failing the whole invocation over one bad path.
//
// Because of -e, "no such package" is an empty Dir with exit 0. A non-nil
// error therefore means the LOOKUP failed — the timeout, a missing or
// broken go command — and the answer is unknown, not "concrete". That is
// recorded in resolveErr and fails the rule. It used to return "" like a
// missing package, so a `go list` that timed out on a loaded machine
// reported every `client.Client` Deps field as a concrete type, at error
// severity, and a rerun of the same tree passed.
func (l *depsLinter) externalPackageDir(importPath string) string {
	if got, ok := l.dirCache[importPath]; ok {
		return got
	}
	l.dirCache[importPath] = "" // cache the failure up front

	ctx, cancel := context.WithTimeout(context.Background(), goListTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f", "{{.Dir}}", "--", importPath)
	cmd.Dir = l.rootDir
	cmd.Env = goexec.Env("GOPROXY=off")
	out, err := cmd.Output()
	if err != nil {
		if l.resolveErr == nil {
			var stderr string
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				stderr = strings.TrimSpace(string(exitErr.Stderr))
			}
			if ctx.Err() != nil {
				err = fmt.Errorf("%w after %s (%v)", ctx.Err(), goListTimeout, err)
			}
			l.resolveErr = fmt.Errorf("go list %s: %w", importPath, err)
			if stderr != "" {
				l.resolveErr = fmt.Errorf("%w\n%s", l.resolveErr, stderr)
			}
		}
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	l.dirCache[importPath] = dir
	return dir
}

// packageInterfaces returns the set of interface type names declared in
// dir's non-test .go files, memoized per run. A directory that does not
// parse yields an empty (cached) set — the rule then falls back to firing,
// which is the safe direction for a warning.
func (l *depsLinter) packageInterfaces(dir string) map[string]bool {
	return l.packageTypeDecls(dir).interfaces
}

// typeDecls is what one package directory tells the rule about its own
// type names: which are interfaces, which it declares at all, and the
// methods each declared type carries.
type typeDecls struct {
	interfaces map[string]bool
	declared   map[string]bool
	// methods maps a declared type name to the methods declared on it here,
	// each carrying enough context to judge its purity.
	methods map[string][]methodDecl
	// embeds marks struct types with at least one anonymous field, whose
	// promoted method set no declaration in this package can see.
	embeds map[string]bool
	// funcs and vars are the package's top-level function and variable
	// names. A method body that names one is reaching beyond its own
	// fields. Consts are deliberately absent: a const is a compile-time
	// value with no state to fake, and a config accessor's fallback is
	// routinely one.
	funcs map[string]bool
	vars  map[string]bool
	// structs maps a declared struct type name to its AST, so the rule can
	// ask what a type HOLDS rather than only what its methods do. See
	// structFieldsAreInert.
	structs map[string]*ast.StructType
	// funcDecls carries the BODY of each package-level func, so a call to
	// one can be judged on what it actually does instead of being assumed
	// impure. See funcIsPure.
	funcDecls map[string]methodDecl
	// dir is the directory these decls were parsed from, so a recursive
	// purity check knows which package it is resolving names against.
	dir string
}

// methodDecl is one method declared on a type, carried with the import
// aliases of the FILE it was declared in so a qualified name in its body
// resolves against the right import set. Methods on one type can be spread
// across files with different imports.
type methodDecl struct {
	name    string
	sig     *ast.FuncType
	body    *ast.BlockStmt
	imports map[string]string
}

// packageTypeDecls parses dir's non-test .go files once and memoizes the
// answer, so a widely imported dep is read once per run rather than once
// per consumer. A directory that does not parse yields an empty (cached)
// result — every caller then falls back to firing, which is the safe
// direction.
func (l *depsLinter) packageTypeDecls(dir string) typeDecls {
	if got, ok := l.declCache[dir]; ok {
		return got
	}
	out := typeDecls{
		interfaces: map[string]bool{},
		declared:   map[string]bool{},
		methods:    map[string][]methodDecl{},
		embeds:     map[string]bool{},
		funcs:      map[string]bool{},
		vars:       map[string]bool{},
		funcDecls:  map[string]methodDecl{},
		structs:    map[string]*ast.StructType{},
		dir:        dir,
	}
	l.declCache[dir] = out

	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if parseErr != nil {
			continue
		}
		fileImports := importAliases(f)
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						out.declared[ts.Name.Name] = true
						if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
							out.interfaces[ts.Name.Name] = true
						}
						if st, isStruct := ts.Type.(*ast.StructType); isStruct {
							if hasEmbeddedField(st) {
								out.embeds[ts.Name.Name] = true
							}
							out.structs[ts.Name.Name] = st
						}
					}
				case token.VAR:
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, n := range vs.Names {
							out.vars[n.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if name := receiverTypeName(d); name != "" {
					out.methods[name] = append(out.methods[name], methodDecl{
						name:    d.Name.Name,
						sig:     d.Type,
						body:    d.Body,
						imports: fileImports,
					})
					continue
				}
				out.funcs[d.Name.Name] = true
				out.funcDecls[d.Name.Name] = methodDecl{
					name:    d.Name.Name,
					sig:     d.Type,
					body:    d.Body,
					imports: fileImports,
				}
			}
		}
	}
	return out
}

// hasEmbeddedField reports whether st declares at least one anonymous
// (embedded) field.
func hasEmbeddedField(st *ast.StructType) bool {
	if st.Fields == nil {
		return false
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return true
		}
	}
	return false
}

// receiverTypeName returns the bare type name a method is declared on
// (peeling the pointer and any generic type parameters), or "" when fn is
// a plain function rather than a method.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr: // Recv[T]
			expr = t.X
		case *ast.IndexListExpr: // Recv[T, U]
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// isPrimitiveConfigShape returns true for slice/map types whose element
// (and key, for maps) is a Go built-in primitive — the canonical shape
// for config DATA on a Deps struct. Examples that pass:
//
//	[]string, []int, []float64, []bool, [][]byte
//	map[string]string, map[string]int, ...
//
// Examples that don't (the rule still fires):
//
//	[]Source                 // slice of an interface type
//	[]*adapter.Client        // slice of concrete pointer
//	map[string]*config.Tier  // map to concrete pointer
//
// Rationale: these primitives have no meaningful interface equivalent;
// hiding `[]string` behind an interface adds friction without unlocking
// any test power. Recognizing the shape keeps the rule from training
// users to ignore noisy warnings on routine config fields.
func isPrimitiveConfigShape(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.ArrayType:
		// []byte is `*ast.ArrayType{Elt: Ident{"byte"}}`; [][]byte nests.
		return isPrimitiveType(t.Elt) || isByteSlice(t.Elt)
	case *ast.MapType:
		return isPrimitiveType(t.Key) && (isPrimitiveType(t.Value) || isByteSlice(t.Value))
	}
	return false
}

// isPrimitiveType returns true for Go built-in scalar types that are
// universally safe to ship by value on a Deps struct.
func isPrimitiveType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	switch id.Name {
	case "string", "bool",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune",
		"float32", "float64",
		"complex64", "complex128":
		return true
	}
	return false
}

// isByteSlice returns true for the `[]byte` shape — common as an
// inline secret/key/seed, treated as primitive for our purposes.
func isByteSlice(expr ast.Expr) bool {
	at, ok := expr.(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "byte"
}

// exprString returns a short human-readable rendering of an
// expression for inclusion in the lint message. Avoids dragging in
// go/printer for what is fundamentally a one-line tag.
func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + exprString(t.Value)
	case *ast.IndexExpr:
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	case *ast.IndexListExpr:
		args := make([]string, len(t.Indices))
		for i, ix := range t.Indices {
			args[i] = exprString(ix)
		}
		return exprString(t.X) + "[" + strings.Join(args, ", ") + "]"
	default:
		// Never print a Go AST node name (`*ast.IndexExpr`) at the author:
		// render the source text of the type when it is anything else.
		var b strings.Builder
		if err := printer.Fprint(&b, token.NewFileSet(), expr); err == nil {
			return b.String()
		}
		return "this type"
	}
}
