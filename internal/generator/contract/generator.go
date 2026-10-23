// Package contract parses Go interface definitions from contract.go files
// and generates mock_gen.go (function-field mock pattern) in the same
// directory.
//
// Earlier versions of this package also emitted middleware_gen.go,
// tracing_gen.go and metrics_gen.go — per-method wrappers around every
// contract.go interface. Those were removed in favour of Connect
// interceptors at the handler boundary (forge/pkg/observe) plus opt-in
// helpers (observe.LogCall, observe.TraceCall, observe.NewCallMetrics)
// for users who want internal-package observability. The mock stays
// codegen because the per-method MockX struct is a real grep target —
// "show me MockUserService's methods" is a tight feedback loop that
// generic reflection can't replace.
package contract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// InterfaceDef represents a parsed Go interface.
type InterfaceDef struct {
	Name    string
	Methods []MethodDef
	// Qualifier is the package name to reach this interface by when it is
	// declared in ANOTHER package of the module (e.g. "db" for a
	// db.EstimateStore named in a Deps field). Empty for the ordinary
	// case: an interface declared in this package's own contract.go.
	//
	// The mock TYPE is always unqualified — MockEstimateStore, in the
	// consuming package — because the mock is generated here. Only the
	// compile-time check refers back to the original, as
	// `var _ db.EstimateStore = (*MockEstimateStore)(nil)`.
	Qualifier string
	// Collides is set when this foreign interface's bare name is also
	// declared locally, so MockName must disambiguate. See MockName.
	Collides bool
}

// QualifiedName is how the interface must be written inside the package
// being generated: bare when it is local, package-qualified when it was
// discovered through a Deps field pointing at another package.
func (i InterfaceDef) QualifiedName() string {
	if i.Qualifier == "" {
		return i.Name
	}
	return i.Qualifier + "." + i.Name
}

// MockName is the generated mock's type name. A foreign interface whose
// bare name collides with something the package already declares is
// prefixed with its qualifier — forge's own internal/docs is the live
// case: it declares Service AND depends on contract.Service, so the
// plain name would emit two MockService types and the package would not
// build. Disambiguate only on collision, so the overwhelmingly common
// case keeps the name the scaffolded comment promises (MockEstimateStore,
// not MockDbEstimateStore).
func (i InterfaceDef) MockName() string {
	if i.Collides && i.Qualifier != "" {
		return "Mock" + capitalizeASCII(i.Qualifier) + i.Name
	}
	return "Mock" + i.Name
}

// capitalizeASCII upper-cases the first byte. Go package names are ASCII
// by convention, so this avoids strings.Title (deprecated) and the
// unicode casing rules it would drag in.
func capitalizeASCII(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-('a'-'A')) + s[1:]
	}
	return s
}

// MethodDef represents a single method on an interface.
type MethodDef struct {
	Name    string
	Params  []ParamDef
	Results []ParamDef
	// SkipObserve is true when the interface method's doc / inline comment
	// carries the `// forge:no-observe` marker. The generated observability
	// decorator still wraps the type, but THIS method delegates DIRECTLY to the
	// inner implementation without routing through the observe chain. It is the
	// method-scoped twin of the package-level `// forge:no-observe` opt-out.
	SkipObserve bool
}

// ParamDef represents a method parameter or return value.
type ParamDef struct {
	Name     string
	TypeExpr string // rendered Go type expression, e.g. "context.Context", "*sql.Rows", "...any"
	Variadic bool
}

// File holds everything extracted from a single contract.go.
type File struct {
	Package string
	// Source is the base name of the file the interfaces were read from —
	// contract.go, or a package's deps.go — which mock_gen.go's header names
	// as the file to edit.
	Source  string
	Imports map[string]string // alias/name → import path (e.g. "sql" → "database/sql")
	// ExplicitImports records aliases that were written explicitly in
	// contract.go (for example `controlplanev1 ".../v1"`). Keeping that
	// bit lets the mock renderer preserve aliases whose spelling cannot be
	// recovered from the import path alone.
	ExplicitImports map[string]bool
	Interfaces      []InterfaceDef
	// InterfaceNames is the set of interface type names defined in this file.
	// Used by the zero-value generator to emit "nil" for interface-typed
	// returns instead of the invalid composite literal "T{}".
	InterfaceNames map[string]bool
	// PrimitiveAliases maps a named type (e.g. "BalanceCapReason") to its
	// underlying primitive kind (e.g. "string"). Populated by scanning
	// contract.go and its sibling .go files for `type X <primitive>`
	// declarations. Used by the zero-value generator to emit the
	// underlying primitive's zero (`""`, `0`, `false`) instead of the
	// invalid composite literal `BalanceCapReason{}`.
	PrimitiveAliases map[string]string
	// ForeignImports are import paths required by interfaces discovered
	// through the Deps struct rather than through contract.go. They are
	// held separately because collectImports only sees contract.go's own
	// import block, and a db.EstimateStore's signatures routinely mention
	// packages contract.go never imports (orm, the db package itself).
	ForeignImports map[string]ImportDef
}

// Options controls optional aspects of mock generation. The zero value is
// equivalent to plain Generate(contractPath) — every field is opt-in and
// backwards compatible.
type Options struct {
	// ExtraInterfaceTypes is a project-supplied allow-list of cross-package
	// interface types the mock generator should treat as mockable. The
	// rendered type expression of a method's return (e.g.
	// "billing.MeterClient") is matched against this set during zero-value
	// emission; matches produce "nil" instead of the invalid composite
	// literal "T{}".
	//
	// Sourced from forge.yaml's `contracts.interface_types` at the CLI
	// layer; nil / empty is the no-op default.
	ExtraInterfaceTypes map[string]bool

	// ProjectRoot + Checksums route the mock write through the
	// checksums.WriteGeneratedFile chokepoint so the emitted
	// internal/<pkg>/mock_gen.go is recorded in the manifest AND in
	// the per-run WrittenThisRun set. Without this, the stale-artifact
	// sweep saw every manifest-tracked mock_gen.go as "tracked but not
	// re-emitted this run" and flagged it for deletion on every
	// `forge generate` — and the sweep would delete live mocks
	// (kalshi-trader FORGE_BACKLOG #15).
	//
	// ProjectRoot is the project root (the directory containing .forge/);
	// relative manifest paths are computed against it. Both fields nil
	// /empty preserve the legacy raw-os.WriteFile behavior for callers
	// without a manifest in scope (e.g. the `forge scaffold package` stub
	// emit — its mock is adopted into the manifest on the next full
	// generate).
	ProjectRoot string
	Checksums   *checksums.FileChecksums

	// Resolver answers what each import is called and what each foreign
	// Deps interface's method set is, from the Go toolchain. A caller
	// generating many contracts (the `forge generate` contracts step)
	// builds ONE with NewResolver over all of them, so the whole run costs
	// two go/packages loads. nil resolves this contract alone.
	Resolver *Resolver
}

// Generate parses contractPath and writes mock_gen.go next to it.
//
// In addition to (re)writing mock_gen.go, Generate sweeps any stale
// observability wrappers (middleware_gen.go, tracing_gen.go,
// metrics_gen.go) from the same directory — these were emitted by
// previous forge versions and are now superseded by the
// forge/pkg/observe Connect interceptors. Removing them here (rather
// than relying on the audit "orphan" report) keeps `forge generate`
// idempotent and gives the user a clear signal in the build output:
// either the file is present and current, or it's gone.
func Generate(contractPath string) error {
	return GenerateWithOptions(contractPath, Options{})
}

// GenerateWithOptions is Generate with project-level extension hooks
// (currently: ExtraInterfaceTypes). New plumbing should go through this
// entry point; the bare Generate is preserved for call sites that don't
// need the extension surface.
func GenerateWithOptions(contractPath string, opts Options) error {
	cf, err := ParseContractWith(contractPath, opts.Resolver)
	if err != nil {
		return fmt.Errorf("parse contract: %w", err)
	}

	dir := filepath.Dir(contractPath)

	if err := writeMock(cf, dir, opts); err != nil {
		return fmt.Errorf("generate mock: %w", err)
	}

	if err := removeLegacyWrappers(dir); err != nil {
		return fmt.Errorf("remove legacy wrappers: %w", err)
	}

	return nil
}

// removeLegacyWrappers deletes middleware_gen.go, tracing_gen.go and
// metrics_gen.go from dir if present. These were emitted by earlier
// forge versions; they're now replaced by observe.* libraries. Missing
// files are not an error — the function is safe to call on freshly
// scaffolded packages.
//
// Each delete is journaled first (RecordPreWriteAbs) so a run that aborts
// later rewinds the file back. A delete that skipped the journal recorded
// the path as "absent pre-run" at the NEXT write, which made the rewind
// delete it — see removeJournaled.
func removeLegacyWrappers(dir string) error {
	for _, name := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		if err := removeJournaled(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// removeJournaled deletes a forge-owned path after capturing its pre-run
// bytes in the rollback journal, so an aborted `forge generate` restores it.
//
// This is the delete-side twin of the WriteGeneratedFile chokepoint. The
// journal is first-write-wins and keyed on the path, so capturing here is
// correct whether the file is only deleted this run or deleted and then
// rewritten: the capture at the delete holds the TRUE pre-run bytes, and the
// rewrite's own capture is a no-op.
//
// Without it the ordering silently inverts the rewind's meaning. Deleting
// first left the path uncaptured; the subsequent write captured it as
// existed=false ("absent before this run"), which RestoreRollback honours by
// DELETING. A validation failure in an unrelated proto therefore removed a
// package's middleware_gen.go and broke `go build ./...` across the whole
// checkout, attributing the damage to a package that had nothing to do with
// the failure. A missing file is not an error.
func removeJournaled(path string) error {
	checksums.RecordPreWriteAbs(path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// ParseContract parses a contract.go file and extracts all interface definitions.
//
// Sibling .go files in the same package directory are also scanned (parse-only,
// no method extraction) to populate InterfaceNames so the mock generator can
// emit "nil" for interface-typed returns whose declaration lives outside
// contract.go (e.g. internal/debug defines Service in contract.go and
// Debugger in debugger.go).
//
// Import names and foreign Deps interfaces are resolved through the Go
// toolchain by a Resolver scoped to this one contract; callers parsing many
// contracts share one via ParseContractWith.
func ParseContract(path string) (*File, error) {
	return ParseContractWith(path, nil)
}

// ParseContractWith is ParseContract against a shared Resolver (nil builds
// one for this contract alone).
//
// It is a single-pass parser: each statement consumes one part of the
// contract file in order. Splitting a sequential parse into helpers trades a
// long function for hidden state passed between them.
//
//nolint:funlen // A single-pass parser: each statement consumes one part
func ParseContractWith(path string, r *Resolver) (*File, error) {
	if r == nil {
		r = NewResolver(path)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse file: %w", err)
	}

	cf := &File{
		Package:          file.Name.Name,
		Source:           filepath.Base(path),
		Imports:          make(map[string]string),
		ExplicitImports:  make(map[string]bool),
		InterfaceNames:   make(map[string]bool),
		PrimitiveAliases: make(map[string]string),
	}

	// Scan sibling .go files in the same package directory and record any
	// interface type names. This lets zeroValue recognize interfaces whose
	// declarations are split across multiple files in the package (common
	// pattern: contract.go has Service, debugger.go has Debugger).
	dir := filepath.Dir(path)
	if entries, dirErr := os.ReadDir(dir); dirErr == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			// Skip generated files — they re-declare nothing useful and
			// can transiently fail to parse (e.g. an in-progress edit).
			if strings.HasSuffix(entry.Name(), "_gen.go") {
				continue
			}
			siblingPath := filepath.Join(dir, entry.Name())
			if siblingPath == path {
				continue
			}
			siblingFile, sErr := parser.ParseFile(fset, siblingPath, nil, parser.SkipObjectResolution)
			if sErr != nil {
				continue // best-effort: skip unparseable siblings
			}
			collectInterfaceNames(siblingFile, cf.InterfaceNames)
			collectPrimitiveAliases(siblingFile, cf.PrimitiveAliases)
		}
	}

	// Also collect primitive aliases declared in contract.go itself, so the
	// zero-value generator handles `type X string` defined alongside the
	// Service interface in the same file.
	collectPrimitiveAliases(file, cf.PrimitiveAliases)

	// Collect imports: build a map from local name → import path. An
	// unaliased import binds its package's DECLARED name, which is not
	// always its last path element (github.com/nats-io/nats.go binds
	// `nats`) — the resolver asks the toolchain.
	for _, imp := range file.Imports {
		name, path, explicit := r.importLocalName(imp)
		if name == "_" || name == "." {
			continue
		}
		if explicit {
			cf.ExplicitImports[name] = true
		}
		cf.Imports[name] = path
	}

	// First pass: collect all interface AST nodes by name.
	type ifaceEntry struct {
		name      string
		ifaceType *ast.InterfaceType
	}
	var ifaceEntries []ifaceEntry
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			ifaceEntries = append(ifaceEntries, ifaceEntry{
				name:      typeSpec.Name.Name,
				ifaceType: ifaceType,
			})
			cf.InterfaceNames[typeSpec.Name.Name] = true
		}
	}

	// Build a map of interface name → direct methods (no embedding resolved yet).
	ifaceMethodMap := make(map[string][]MethodDef)
	ifaceEmbeds := make(map[string][]string) // interface name → embedded interface names
	// Embeds from other packages contribute their methods from the
	// type-checked package. A mock or decorator missing them fails its own
	// compile-time `var _ Service = ...` check.
	embedAliases := newImportAliases(cf.Imports, cf.ExplicitImports, r)
	selfPkg, selfPath := r.selfPackage(dir)
	embedAliases.self = selfPath
	for _, entry := range ifaceEntries {
		if entry.ifaceType.Methods != nil {
			for _, field := range entry.ifaceType.Methods.List {
				switch ft := field.Type.(type) {
				case *ast.FuncType:
					// A method-level `// forge:no-observe` on the field's doc or
					// inline comment routes this method AROUND the observe chain
					// in the generated decorator (direct delegation).
					skip := commentGroupsHaveNoObserve(field.Doc, field.Comment)
					for _, name := range field.Names {
						md := extractMethod(name.Name, ft, fset)
						md.SkipObserve = skip
						ifaceMethodMap[entry.name] = append(ifaceMethodMap[entry.name], md)
					}
				case *ast.Ident:
					if ft.Name == "error" {
						// The predeclared error contributes Error() string.
						ifaceMethodMap[entry.name] = append(ifaceMethodMap[entry.name],
							MethodDef{Name: "Error", Results: []ParamDef{{TypeExpr: "string"}}})
						continue
					}
					// Embedded interface from same package, e.g. "CommandPublisher"
					ifaceEmbeds[entry.name] = append(ifaceEmbeds[entry.name], ft.Name)
				case *ast.SelectorExpr:
					// Embedded interface from another package: its whole method
					// set, flattened by go/types at every depth.
					ifaceMethodMap[entry.name] = append(ifaceMethodMap[entry.name],
						embeddedMethods(ft, cf.Imports, r, embedAliases)...)
				}
			}
		}
	}

	// Resolve embedded interfaces: collect all methods including those from embeds.
	//
	// An embed this file does not declare is declared in another file of
	// the package (a deps.go role embedding a role from roles.go): its
	// method set comes from the type-checked package, which NewResolver
	// loaded for exactly this (embedsSiblingInterface), rendered with the
	// package's own types unqualified. Without it the mock silently lacked
	// those methods and generate's build validation failed with "does not
	// implement".
	declaredHere := make(map[string]bool, len(ifaceEntries))
	for _, entry := range ifaceEntries {
		declaredHere[entry.name] = true
	}
	var resolveMethods func(name string, visited map[string]bool) []MethodDef
	resolveMethods = func(name string, visited map[string]bool) []MethodDef {
		if visited[name] {
			return nil
		}
		visited[name] = true
		if !declaredHere[name] {
			return siblingMethods(selfPkg, name, embedAliases)
		}
		methods := append([]MethodDef{}, ifaceMethodMap[name]...)
		for _, embedded := range ifaceEmbeds[name] {
			methods = append(methods, resolveMethods(embedded, visited)...)
		}
		return methods
	}

	// Second pass: build InterfaceDefs with resolved methods. Go accepts one
	// method reached through two embeds (identical signatures, since 1.14);
	// a mock declaring it twice does not compile, so the first one stands.
	for _, entry := range ifaceEntries {
		iface := InterfaceDef{
			Name:    entry.name,
			Methods: dedupeMethodDefs(resolveMethods(entry.name, make(map[string]bool))),
		}
		cf.Interfaces = append(cf.Interfaces, iface)
	}
	// The packages those embedded signatures reference — another package's,
	// or a sibling file's (resolved just above) — join contract.go's
	// import set, so collectImports finds them for the mock and the
	// decorator alike.
	for _, imp := range embedAliases.neededImports() {
		if _, ok := cf.Imports[imp.Name]; ok {
			continue
		}
		cf.Imports[imp.Name] = imp.Path
		if imp.Explicit {
			cf.ExplicitImports[imp.Name] = true
		}
	}

	// Interfaces this package DEPENDS on but does not declare — the
	// db.*Store shape forge tells every user to put in Deps. Without
	// these the scaffolded contract_test.go's promise of a
	// "pipeline.MockStore" is false, and users hand-roll the fake that
	// same comment forbids. See deps_stores.go.
	foreign, foreignImports := foreignInterfaces(dir, fset, cf.Imports, cf.ExplicitImports, r)
	localNames := make(map[string]bool, len(cf.Interfaces))
	for _, iface := range cf.Interfaces {
		localNames[iface.Name] = true
	}
	for _, iface := range foreign {
		// Two foreign interfaces from different packages can share a bare
		// name too, so the collision test is against everything already
		// claimed, not just contract.go's own.
		if localNames[iface.Name] {
			iface.Collides = true
		}
		localNames[iface.Name] = true
		// Registered under the QUALIFIED name because that is the form
		// the zero-value generator sees in a rendered signature: a
		// WithTx returning db.EstimateStore must collapse to nil, not to
		// the invalid composite literal db.EstimateStore{}.
		cf.InterfaceNames[iface.QualifiedName()] = true
		cf.Interfaces = append(cf.Interfaces, iface)
	}
	cf.ForeignImports = foreignImports

	return cf, nil
}

// collectInterfaceNames records the name of every interface type declared in
// file into the names set. Used to build a package-wide set of interface
// names so the zero-value generator can emit "nil" for interface-typed
// returns rather than the invalid composite literal "T{}".
func collectInterfaceNames(file *ast.File, names map[string]bool) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				names[typeSpec.Name.Name] = true
			}
		}
	}
}

// primitiveKinds enumerates the underlying Go primitive identifiers the
// zero-value generator can resolve for a named alias. Limited to types
// whose zero value is unambiguous and renders to a Go literal — pointers,
// slices, maps, channels, funcs, etc. already have their own branches in
// zeroValue and are not alias targets here.
var primitiveKinds = map[string]bool{
	"bool":    true,
	"string":  true,
	"int":     true,
	"int8":    true,
	"int16":   true,
	"int32":   true,
	"int64":   true,
	"uint":    true,
	"uint8":   true,
	"uint16":  true,
	"uint32":  true,
	"uint64":  true,
	"uintptr": true,
	"float32": true,
	"float64": true,
	"byte":    true,
	"rune":    true,
}

// collectPrimitiveAliases records any `type X <primitive>` declarations
// from file into the aliases map (name → underlying primitive kind). Used
// so the zero-value generator emits the underlying primitive's zero
// (e.g. `""` for `type BalanceCapReason string`) rather than the invalid
// composite literal `BalanceCapReason{}`.
func collectPrimitiveAliases(file *ast.File, aliases map[string]string) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			ident, ok := typeSpec.Type.(*ast.Ident)
			if !ok {
				continue
			}
			if primitiveKinds[ident.Name] {
				aliases[typeSpec.Name.Name] = ident.Name
			}
		}
	}
}

// primitiveZero returns the zero-value literal for a primitive kind name.
// Returns ("", false) if kind is not a recognized primitive.
func primitiveZero(kind string) (string, bool) {
	switch kind {
	case "bool":
		return "false", true
	case "string":
		return `""`, true
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune":
		return "0", true
	case "float32", "float64":
		return "0", true
	}
	return "", false
}

// noObserveDirective is the method-level opt-out marker recognized on an
// interface method's doc / inline comment. Kept in sync with the package-level
// directive spelling in internal/codegen (directiveNoObserve).
const noObserveDirective = "forge:no-observe"

// commentGroupsHaveNoObserve reports whether any of the given comment groups
// carries a comment whose whole text (after stripping comment syntax +
// whitespace) equals the `forge:no-observe` marker. Both the spaced
// (`// forge:no-observe`) and unspaced (`//forge:no-observe`) forms match; the
// marker must be the whole comment line so prose that merely mentions it is
// never a false positive.
func commentGroupsHaveNoObserve(groups ...*ast.CommentGroup) bool {
	for _, cg := range groups {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			if trimContractCommentMarkers(c.Text) == noObserveDirective {
				return true
			}
		}
	}
	return false
}

// trimContractCommentMarkers strips `//`, `/* */`, and surrounding whitespace
// from a raw comment so a directive can be exact-matched. Local twin of
// codegen.trimCommentMarkers (kept here to avoid a codegen import cycle).
func trimContractCommentMarkers(text string) string {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, "//"):
		text = strings.TrimPrefix(text, "//")
	case strings.HasPrefix(text, "/*"):
		text = strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/")
	}
	return strings.TrimSpace(text)
}

// extractMethod builds a MethodDef from a *ast.FuncType.
func extractMethod(name string, ft *ast.FuncType, fset *token.FileSet) MethodDef {
	md := MethodDef{Name: name}

	if ft.Params != nil {
		for _, field := range ft.Params.List {
			typeStr := renderExpr(field.Type, fset)
			variadic := false
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				variadic = true
			}
			if len(field.Names) == 0 {
				md.Params = append(md.Params, ParamDef{TypeExpr: typeStr, Variadic: variadic})
			} else {
				for _, n := range field.Names {
					md.Params = append(md.Params, ParamDef{Name: n.Name, TypeExpr: typeStr, Variadic: variadic})
				}
			}
		}
	}

	if ft.Results != nil {
		for _, field := range ft.Results.List {
			typeStr := renderExpr(field.Type, fset)
			if len(field.Names) == 0 {
				md.Results = append(md.Results, ParamDef{TypeExpr: typeStr})
			} else {
				for _, n := range field.Names {
					md.Results = append(md.Results, ParamDef{Name: n.Name, TypeExpr: typeStr})
				}
			}
		}
	}

	return md
}

// renderExpr converts an ast.Expr back into its Go source representation.
func renderExpr(expr ast.Expr, fset *token.FileSet) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, expr); err != nil {
		return fmt.Sprintf("/* renderExpr error: %v */", err)
	}
	return buf.String()
}

// collectImports determines which imports from the source file are needed
// by the generated code for the given interfaces.
func collectImports(cf *File, ifaces []InterfaceDef) []ImportDef {
	needed := make(map[string]ImportDef)
	for _, iface := range ifaces {
		for _, m := range iface.Methods {
			for _, p := range m.Params {
				collectFromTypeExpr(p.TypeExpr, cf.Imports, cf.ExplicitImports, needed)
			}
			for _, r := range m.Results {
				collectFromTypeExpr(r.TypeExpr, cf.Imports, cf.ExplicitImports, needed)
			}
		}
	}

	var imports []ImportDef
	for _, imp := range needed {
		imports = append(imports, imp)
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
	return imports
}

// collectFromTypeExpr scans a type expression string for package references
// and adds the corresponding import paths to the needed set.
func collectFromTypeExpr(typeExpr string, importMap map[string]string, explicit map[string]bool, needed map[string]ImportDef) {
	for _, qualifier := range typeExprQualifiers(typeExpr) {
		if path, ok := importMap[qualifier]; ok {
			needed[path] = ImportDef{Path: path, Name: qualifier, Explicit: explicit[qualifier]}
		}
	}
}

// typeExprQualifiers returns every identifier in a rendered type expression
// that is immediately followed by a "." — the package qualifiers in
// "context.Context", "*sql.Rows", "func([]byte) (*nats.Msg, error)". It
// matches whole identifiers: a substring test made an explicit `v1` alias
// match inside "controlplanev1.Plan" and emitted an import the mock never
// used, which does not compile.
func typeExprQualifiers(typeExpr string) []string {
	var out []string
	isIdent := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	for i := 0; i < len(typeExpr); {
		if !isIdent(typeExpr[i]) {
			i++
			continue
		}
		start := i
		for i < len(typeExpr) && isIdent(typeExpr[i]) {
			i++
		}
		// A selector's right-hand side (the "Msg" of nats.Msg) is
		// preceded by "."; only the left-hand side is a qualifier.
		if i < len(typeExpr) && typeExpr[i] == '.' && (start == 0 || typeExpr[start-1] != '.') {
			out = append(out, typeExpr[start:i])
		}
	}
	return out
}

// renderMock renders the mock_gen.go body for cf — the pure half of
// writeMock, with no filesystem in it. Split out so the retirement
// sweep (retire.go) can ask "are these on-disk bytes forge's own current
// render?" without writing anything.
//
// opts.ExtraInterfaceTypes is unioned into the set of interface names the
// template consults during zero-value emission. The union lets projects
// declare additional cross-package interface types in forge.yaml without
// requiring a forge fork; an entry like "billing.MeterClient" flips the
// zero value for that type from the (invalid) "billing.MeterClient{}" to
// the canonical "nil".
func renderMock(cf *File, opts Options) ([]byte, error) {
	imports := collectImports(cf, cf.Interfaces)
	// The mock embeds contractkit.Recorder and uses contractkit.MockNotSet
	// for error-returning methods. The Recorder embed alone requires the
	// import even for interfaces that have zero methods, so include it
	// whenever the file declares at least one interface.
	if len(cf.Interfaces) > 0 {
		addImport(&imports, ImportDef{Path: contractkitImport, Name: "contractkit"})
	}

	// Foreign dep interfaces bring their own imports: collectImports only
	// consults contract.go's import block, which knows nothing about the
	// packages a db.EstimateStore's signatures mention.
	for _, imp := range cf.ForeignImports {
		addImport(&imports, imp)
	}

	// Union the project-supplied extras into a fresh copy of the contract's
	// own interface name set. The template uses this combined set to decide
	// which return types collapse to "nil" instead of "T{}".
	interfaceNames := make(map[string]bool, len(cf.InterfaceNames)+len(opts.ExtraInterfaceTypes))
	for k, v := range cf.InterfaceNames {
		interfaceNames[k] = v
	}
	for k, v := range opts.ExtraInterfaceTypes {
		if v {
			interfaceNames[k] = true
		}
	}

	// Each mock method is rendered from a copy whose parameter names cannot
	// shadow what the template's body spells: the receiver and the
	// contractkit qualifier (see withBodyScopedNames). cf.Interfaces itself
	// keeps the contract's names; the decorator renders from it too.
	ifaces := make([]InterfaceDef, len(cf.Interfaces))
	for i, iface := range cf.Interfaces {
		ifaces[i] = iface
		ifaces[i].Methods = make([]MethodDef, len(iface.Methods))
		for j, m := range iface.Methods {
			ifaces[i].Methods[j] = m.withBodyScopedNames(mockReceiver, "contractkit")
		}
	}

	data := templateData{
		Package:          cf.Package,
		Source:           cf.sourceName(),
		Receiver:         mockReceiver,
		Imports:          imports,
		Interfaces:       ifaces,
		InterfaceNames:   interfaceNames,
		PrimitiveAliases: cf.PrimitiveAliases,
	}

	var buf bytes.Buffer
	if err := mockTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute mock template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt mock output: %w\n---\n%s", err, buf.String())
	}
	return formatted, nil
}

// writeMock renders and writes mock_gen.go in dir.
func writeMock(cf *File, dir string, opts Options) error {
	formatted, err := renderMock(cf, opts)
	if err != nil {
		return err
	}

	mockPath := filepath.Join(dir, mockFileName)

	// Manifest-aware path: when the caller supplied a project root, the
	// write MUST go through checksums.WriteGeneratedFile so the path
	// lands in WrittenThisRun (stale-sweep immunity) and honors
	// disowned entries (user-owned files are never stomped).
	// force=true matches the file's Tier-1 contract: mock_gen.go is
	// regenerated unconditionally from contract.go every run.
	if opts.ProjectRoot != "" {
		rel, relErr := filepath.Rel(opts.ProjectRoot, mockPath)
		if relErr != nil {
			return fmt.Errorf("compute project-relative mock path: %w", relErr)
		}
		if _, werr := checksums.WriteDerivedFileOutcome(opts.ProjectRoot, rel, formatted, opts.Checksums, true); werr != nil {
			return fmt.Errorf("write %s: %w", rel, werr)
		}
		return nil
	}

	// Legacy path: no manifest in scope (e.g. `forge scaffold package` stub
	// emit). Raw write; the file is adopted into the manifest by the
	// next full `forge generate`.
	return atomicfile.WriteFile(mockPath, formatted, 0644)
}

// addImport adds an import path if not already present.
func addImport(imports *[]ImportDef, imp ImportDef) {
	for _, existing := range *imports {
		if existing.Path == imp.Path {
			return
		}
	}
	*imports = append(*imports, imp)
	sort.Slice(*imports, func(i, j int) bool { return (*imports)[i].Path < (*imports)[j].Path })
}

// ParamSignature returns the Go parameter list for a method, e.g. "ctx context.Context, id string".
func (m MethodDef) ParamSignature() string {
	var parts []string
	for i, p := range m.Params {
		parts = append(parts, concreteParamName(p, i)+" "+p.TypeExpr)
	}
	return strings.Join(parts, ", ")
}

// concreteParamName returns a usable identifier for a parameter on the
// generated concrete mock method. Interface declarations may omit names (as
// protoc-gen-connect-go does) or spell them `_`; concrete method bodies cannot
// record or forward either form, so Forge supplies stable positional names.
func concreteParamName(p ParamDef, index int) string {
	if p.Name == "" || p.Name == "_" {
		return fmt.Sprintf("p%d", index)
	}
	return p.Name
}

// withBodyScopedNames returns a copy of m that is safe to render as a method
// WITH a body, given the identifiers that body references (bodyIdents).
//
// contract.go declares interface methods, which have no body, so a parameter
// there may be named anything: `m`, `ctx`, `err`, `contractkit`, or the
// package a sibling parameter's type comes from (`user user.User`). A
// generated mock or decorator method puts those parameters in ONE scope with
// its receiver, its locals, and every package qualifier and predeclared
// identifier its body spells. A parameter that shadows one of them either
// fails to compile (`m redeclared`, `user.User is not a type`) or, worse,
// compiles and forwards the wrong value. So every such parameter is renamed
// to a positional pN, the name an unnamed or blank parameter already gets.
// Renaming is invisible to callers: a method's parameter names are not part
// of its type.
//
// Result names are dropped for the same reason. No generated body assigns a
// named result, so a name there can only collide (a result named `m` with
// the receiver, `r0` with the decorator's capture variable).
func (m MethodDef) withBodyScopedNames(bodyIdents ...string) MethodDef {
	reserved := make(map[string]bool, len(goUniverse)+len(bodyIdents))
	for _, id := range goUniverse {
		reserved[id] = true
	}
	for _, id := range bodyIdents {
		reserved[id] = true
	}
	// Every identifier the method's own types spell: package qualifiers
	// (zero values and closure signatures name the result types), local
	// type names (`Result{}`), and predeclared ones.
	for _, list := range [][]ParamDef{m.Params, m.Results} {
		for _, p := range list {
			for _, id := range typeExprIdents(p.TypeExpr) {
				reserved[id] = true
			}
		}
	}

	out := m
	out.Params = make([]ParamDef, len(m.Params))
	copy(out.Params, m.Params)
	out.Results = make([]ParamDef, len(m.Results))
	for i, r := range m.Results {
		out.Results[i] = ParamDef{TypeExpr: r.TypeExpr, Variadic: r.Variadic}
	}

	// Keep every contract name that is free, then give each remaining
	// parameter the first positional name nothing else holds. Two passes, so
	// a kept name can never be handed out again as some later pN.
	used := map[string]bool{}
	rename := make([]bool, len(out.Params))
	for i, p := range out.Params {
		if p.Name == "" || p.Name == "_" || reserved[p.Name] {
			rename[i] = true
			continue
		}
		used[p.Name] = true
	}
	for i := range out.Params {
		if !rename[i] {
			continue
		}
		name := fmt.Sprintf("p%d", i)
		for used[name] || reserved[name] {
			name += "_"
		}
		used[name] = true
		out.Params[i].Name = name
	}
	return out
}

// goUniverse is Go's universe scope: every predeclared identifier. A
// parameter may legally take any of these names, and a generated body may
// spell any of them (`nil`, `false`, `*new(pkg.T)`, `func(...) error`).
var goUniverse = []string{
	"any", "bool", "byte", "comparable", "complex64", "complex128", "error",
	"float32", "float64", "int", "int8", "int16", "int32", "int64", "rune",
	"string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"true", "false", "iota", "nil",
	"append", "cap", "clear", "close", "complex", "copy", "delete", "imag",
	"len", "make", "max", "min", "new", "panic", "print", "println", "real",
	"recover",
}

// typeExprIdents returns every identifier in a rendered type expression:
// package qualifiers, type names, and the names inside a func type's own
// parameter list. Over-collecting is harmless — it can only rename a
// parameter that did not strictly need it.
func typeExprIdents(typeExpr string) []string {
	return strings.FieldsFunc(typeExpr, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	})
}

// ResultSignature returns the Go result type list, e.g. "(string, error)" or "error".
func (m MethodDef) ResultSignature() string {
	if len(m.Results) == 0 {
		return ""
	}
	var parts []string
	hasNames := false
	for _, r := range m.Results {
		if r.Name != "" {
			hasNames = true
			parts = append(parts, r.Name+" "+r.TypeExpr)
		} else {
			parts = append(parts, r.TypeExpr)
		}
	}
	sig := strings.Join(parts, ", ")
	if len(m.Results) > 1 || hasNames {
		return "(" + sig + ")"
	}
	return sig
}

// RecordArgs returns the argument list to pass to the embedded
// contractkit.Recorder.Record. Variadic params are passed verbatim
// (no "..." suffix) so the recorder receives the slice as a single
// any value — this is what callers want when asserting on captured
// arguments. If the method has no parameters, returns the empty
// string and the template emits Record("Method") with no extra args.
func (m MethodDef) RecordArgs() string {
	var parts []string
	for i, p := range m.Params {
		name := concreteParamName(p, i)
		// For variadic params, pass the slice as a single value rather
		// than spreading it — Recorder.Record uses ...any internally,
		// so spreading would scatter the elements across multiple
		// captured args, which is rarely what the test assertion wants.
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// CallArgs returns the argument list for delegating to the inner implementation,
// e.g. "ctx, id" or "ctx, query, args...".
func (m MethodDef) CallArgs() string {
	var parts []string
	for i, p := range m.Params {
		name := concreteParamName(p, i)
		if p.Variadic {
			parts = append(parts, name+"...")
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

// FuncFieldType returns the func type for mock function fields,
// e.g. "func(context.Context, string) (string, error)".
func (m MethodDef) FuncFieldType() string {
	var params []string
	for _, p := range m.Params {
		params = append(params, p.TypeExpr)
	}
	paramStr := strings.Join(params, ", ")

	result := m.ResultSignature()
	if result == "" {
		return "func(" + paramStr + ")"
	}
	return "func(" + paramStr + ") " + result
}

// ZeroResults returns the zero-value expression for the result types,
// for use in mock fallback returns. E.g. "nil, contractkit.MockNotSet(...)".
//
// interfaceNames is the set of locally-defined interface names from the
// parsed contract; passed through to zeroValue so interface-typed returns
// emit "nil" rather than the invalid composite literal "T{}".
//
// primitiveAliases maps locally-declared named primitive types (e.g.
// `type BalanceCapReason string`) to their underlying kind; passed
// through so the mock emits the primitive's zero value ("") rather than
// the invalid composite literal `BalanceCapReason{}`.
//
// The trailing error result is rendered as contractkit.MockNotSet so the
// canonical "Mock<Iface>.<Method>Func not set" error string lives in the
// library; bumping the format is now a one-place change.
func (m MethodDef) ZeroResults(mockName string, interfaceNames map[string]bool, primitiveAliases map[string]string) string {
	if len(m.Results) == 0 {
		return ""
	}
	var parts []string
	for i, r := range m.Results {
		// Last result that is "error" delegates to contractkit.MockNotSet.
		if i == len(m.Results)-1 && r.TypeExpr == "error" {
			parts = append(parts, fmt.Sprintf(`contractkit.MockNotSet(%q, %q)`, mockName, m.Name))
		} else {
			parts = append(parts, zeroValue(r.TypeExpr, interfaceNames, primitiveAliases))
		}
	}
	return strings.Join(parts, ", ")
}

// ResultNames returns placeholder variable names for capturing results,
// e.g. "r0, r1" for two return values.
func (m MethodDef) ResultNames() string {
	var parts []string
	for i := range m.Results {
		parts = append(parts, fmt.Sprintf("r%d", i))
	}
	return strings.Join(parts, ", ")
}

// HasResults reports whether the method returns anything.
func (m MethodDef) HasResults() bool {
	return len(m.Results) > 0
}

// LastResultIsError returns true if the last result type is "error".
func (m MethodDef) LastResultIsError() bool {
	if len(m.Results) == 0 {
		return false
	}
	return m.Results[len(m.Results)-1].TypeExpr == "error"
}

// HasContext returns true if the method has a context.Context parameter.
func (m MethodDef) HasContext() bool {
	for _, p := range m.Params {
		if p.TypeExpr == "context.Context" {
			return true
		}
	}
	return false
}

// ContextParamName returns the name of the context.Context parameter, or empty string.
func (m MethodDef) ContextParamName() string {
	for i, p := range m.Params {
		if p.TypeExpr == "context.Context" {
			return concreteParamName(p, i)
		}
	}
	return ""
}

// ErrorResultName returns the placeholder name for the error result (last result).
func (m MethodDef) ErrorResultName() string {
	if !m.LastResultIsError() {
		return ""
	}
	return fmt.Sprintf("r%d", len(m.Results)-1)
}

// localNamedTypeRe matches an unqualified exported identifier — e.g. "CheckResult".
// These are almost always struct types defined in the same package as the
// contract, so the safe zero value is a composite literal "CheckResult{}".
var localNamedTypeRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

// qualifiedNamedTypeRe matches "pkg.Type" — a type from another package.
// Heuristically we emit "pkg.Type{}". This is wrong for interface types
// (e.g. sql.Result), but the previous "nil" default was wrong for struct
// types. Structs are far more common as direct return values; interfaces
// are usually returned via pointer or wrapped in an error return.
var qualifiedNamedTypeRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*\.[A-Z][A-Za-z0-9_]*$`)

// crossPackageInterfaces is a small allow-list of well-known cross-package
// interface types. The zero-value generator emits "nil" for these instead
// of the invalid composite literal "pkg.T{}".
//
// Local interfaces defined in the same contract.go are detected
// automatically via File.InterfaceNames — this list only needs
// to enumerate types that come from other packages and that contract
// methods commonly return. Extend as needed; an unrecognized cross-
// package interface still produces a build error in the generated mock.
var crossPackageInterfaces = map[string]bool{
	"context.Context":     true,
	"io.Reader":           true,
	"io.Writer":           true,
	"io.Closer":           true,
	"io.ReadWriter":       true,
	"io.ReadCloser":       true,
	"io.WriteCloser":      true,
	"io.ReadWriteCloser":  true,
	"io.Seeker":           true,
	"io.ReaderAt":         true,
	"io.WriterAt":         true,
	"net.Conn":            true,
	"net.Listener":        true,
	"net.Addr":            true,
	"http.Handler":        true,
	"http.ResponseWriter": true,
	"http.RoundTripper":   true,
	"http.CookieJar":      true,
	"sql.Result":          true,
	"driver.Conn":         true,
	"driver.Driver":       true,
	"driver.Result":       true,
	"driver.Stmt":         true,
	"driver.Tx":           true,
	"fmt.Stringer":        true,
	"error":               true,
	// Connect-RPC framework interfaces. Contract methods that return
	// interceptors / interceptor funcs / streaming-handler interceptors
	// would otherwise emit `connect.Interceptor{}` which doesn't
	// compile. The v1 cp-forge migration repro'd this on every audit /
	// auth interceptor port.
	"connect.Interceptor":                 true,
	"connect.UnaryInterceptorFunc":        true,
	"connect.StreamingHandlerInterceptor": true,
	"connect.StreamingClientInterceptor":  true,
	"connect.AnyRequest":                  true,
	"connect.AnyResponse":                 true,
}

// isInterfaceType reports whether typeExpr names an interface — either a
// local interface defined in the contract file (interfaceNames) or one of
// the well-known cross-package interfaces listed in crossPackageInterfaces.
func isInterfaceType(typeExpr string, interfaceNames map[string]bool) bool {
	if interfaceNames[typeExpr] {
		return true
	}
	if crossPackageInterfaces[typeExpr] {
		return true
	}
	return false
}

// zeroValue returns the zero value literal for a Go type expression.
//
// interfaceNames is the set of locally-defined interface type names from
// the parsed File; types in that set (or in the cross-package
// allow-list) emit "nil" instead of "T{}" because composite literals are
// not valid for interface types.
//
// primitiveAliases maps locally-declared named primitive types (e.g.
// `type BalanceCapReason string`) to their underlying kind. Types in
// this map emit the underlying primitive's zero value (`""`, `0`,
// `false`) instead of the invalid composite literal `T{}`.
func zeroValue(typeExpr string, interfaceNames map[string]bool, primitiveAliases map[string]string) string {
	switch {
	case typeExpr == "bool":
		return "false"
	case typeExpr == "string":
		return `""`
	case typeExpr == "int", typeExpr == "int8", typeExpr == "int16",
		typeExpr == "int32", typeExpr == "int64",
		typeExpr == "uint", typeExpr == "uint8", typeExpr == "uint16",
		typeExpr == "uint32", typeExpr == "uint64",
		typeExpr == "float32", typeExpr == "float64",
		typeExpr == "complex64", typeExpr == "complex128",
		typeExpr == "byte", typeExpr == "rune", typeExpr == "uintptr":
		return "0"
	case typeExpr == "error":
		return "nil"
	case typeExpr == "any", typeExpr == "interface{}":
		return "nil"
	case strings.HasPrefix(typeExpr, "*"),
		strings.HasPrefix(typeExpr, "[]"),
		strings.HasPrefix(typeExpr, "map["),
		strings.HasPrefix(typeExpr, "chan "),
		strings.HasPrefix(typeExpr, "<-chan "),
		strings.HasPrefix(typeExpr, "chan<- "),
		strings.HasPrefix(typeExpr, "func("),
		strings.HasPrefix(typeExpr, "interface{"),
		strings.HasPrefix(typeExpr, "interface "):
		return "nil"
	case isInterfaceType(typeExpr, interfaceNames):
		// Named interface type (local or well-known cross-package). A
		// composite literal "T{}" is invalid for interfaces, so emit
		// "nil" — the typed-nil-interface zero value.
		return "nil"
	case primitiveAliases[typeExpr] != "":
		// Named primitive alias declared in the package (e.g.
		// `type BalanceCapReason string`). A composite literal `T{}`
		// is invalid for primitive-underlying named types, so emit the
		// underlying primitive's zero value.
		if z, ok := primitiveZero(primitiveAliases[typeExpr]); ok {
			return z
		}
		return typeExpr + "{}"
	case localNamedTypeRe.MatchString(typeExpr):
		// Local named type that is neither a known interface nor a
		// primitive alias (both are resolved from this package's own
		// declarations above), so it is a struct/array/slice/map —
		// and the composite literal "T{}" is valid and reads best.
		return typeExpr + "{}"
	case qualifiedNamedTypeRe.MatchString(typeExpr):
		// Cross-package named type. From the rendered text alone its
		// family is unknowable: `time.Duration` (a defined int64),
		// `billing.Tier` (a defined string), `store.Snapshot` (a struct)
		// and an unlisted interface all read the same. "pkg.T{}" is
		// valid only for the struct case and was emitted here until
		// `time.Duration{}` broke a real contract's mock_gen.go.
		//
		// `*new(pkg.T)` is the zero value of EVERY type and is an
		// expression, so it drops into a multi-value return unchanged.
		// Same form internal/codegen.zeroValueForType settled on for the
		// identical problem in handler stubs.
		return "*new(" + typeExpr + ")"
	default:
		// Anything else (generics like "Result[T]", arrays "[N]T", etc.)
		// — fall back to nil. This is wrong for some shapes but matches
		// the long-standing behavior.
		return "nil"
	}
}

// sourceName is the file the mock header names: Source, or contract.go for
// a File built without a path.
func (cf *File) sourceName() string {
	if cf.Source == "" {
		return "contract.go"
	}
	return cf.Source
}
