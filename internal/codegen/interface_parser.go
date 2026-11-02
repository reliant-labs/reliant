package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocalInterface describes one interface type declared in a handler
// (or package) directory. Used by the testing.go auto-stub generator
// to synthesize zero-value implementations for service-owned Deps
// fields whose type is locally declared.
//
// Only interfaces DECLARED in the package are listed (a Deps field typed
// `pkg.Iface` goes through ResolveCrossPkgInterface instead), but their
// method sets are complete: an embedded interface contributes its methods
// whether it is declared here, in another package (`worktreesweep.Store`,
// resolved through go/types), or is the predeclared `error`.
type LocalInterface struct {
	// Name is the interface type name as declared (e.g. "Repository").
	Name string
	// Methods enumerates the interface's method set, with embedded
	// interfaces flattened so callers get a single list to walk.
	Methods []InterfaceMethod
	// Unresolved names each embedded interface whose methods could not be
	// read — `pkg.Store` whose package does not type-check, has an
	// unexported method, or an embed of a shape the parser does not know.
	// Methods is then missing what that embed contributes, and a stub
	// built from it would not compile, so the harness leaves such a field
	// to the test (computeAutoStubs reports it unresolved).
	Unresolved []string
	// ExtraImports lists every package the flattened method signatures
	// reference by qualifier, resolved through the import block of the
	// file each method was declared in.
	//
	// A LOCAL interface can still have CROSS-PACKAGE signatures — the
	// interface type itself is in this package, but its parameter and
	// result types need not be. control-plane's `proxy_authz` handler
	// is the canonical case:
	//
	//	type AccessDecider interface {
	//	    Authorize(ctx context.Context, in proxyauthz.Input) proxyauthz.Decision
	//	}
	//
	// The synthesized stub renders that signature verbatim, so the
	// generated helper file must import internal/proxyauthz or it does
	// not compile. Only qualifiers that actually appear in a rendered
	// signature are listed: blanket-importing the declaring file's whole
	// import block would produce UNUSED imports, which fails the build
	// just as hard as a missing one.
	ExtraImports []ExtraImport
}

// InterfaceMethod is one method on a LocalInterface, in a shape
// directly consumable by the testing.go template's stub emitter.
type InterfaceMethod struct {
	// Name is the method name as declared (e.g. "GetByID").
	Name string
	// Params is the rendered parameter list, TYPES ONLY, e.g.
	// "context.Context, string". Empty when the method takes no
	// parameters.
	//
	// A stub's body never reads its parameters, so it does not name them.
	// Keeping the interface's names put them in one scope with everything
	// the body spells: an interface method has no body, so a parameter
	// there may be called `testkit`, `new`, a local type's name, or the
	// package its result type comes from — legal in the interface, a
	// build failure in the stub (`func (s) Get(user string) (user.Profile,
	// error) { return *new(user.Profile), nil }`).
	Params string
	// Results is the rendered result list with parens when there are
	// multiple, e.g. "(*db.User, error)". Empty when the method
	// returns nothing.
	Results string
	// ReturnStatement is what the stub returns when the test has not
	// programmed the method: "return <zeroes>" as the builders render it,
	// or empty when the method returns nothing. programStubMethods then
	// replaces a trailing error's nil with testkit.StubNotConfigured.
	ReturnStatement string

	// ParamTypes is Params one entry per parameter (a variadic last one
	// spelled `...T`), and ResultTypes / ResultZeros are the result types
	// and the zero-value expression of each. The rendered strings above
	// cannot be split back apart — a func-typed parameter has commas of
	// its own — and the programmable stub needs each one: it names every
	// parameter to forward it, and swaps the trailing error's zero.
	ParamTypes  []string
	ResultTypes []string
	ResultZeros []string

	// The fields below are the programmable stub's rendering of the
	// method, filled by programStubMethods. Receiver and the parameter
	// names are chosen per method so neither can shadow a package the
	// body spells (see InterfaceMethod.Params).
	Receiver    string
	NamedParams string // "a0 context.Context, a1 ...string"
	CallArgs    string // "a0, a1..." — forwards to <Name>Func
	RecordArgs  string // "a0, a1"   — what Recorder.Record captures
	FuncType    string // "func(context.Context, ...string) error"
}

// ParseLocalInterfaces returns every interface type declared in non-
// test .go files under dir. The set is keyed by interface name so
// callers can index by the Deps field's pretty-printed type.
//
// Returns an empty map (never nil) and no error if dir doesn't exist
// — callers treat the absent case the same as "no local interfaces
// to stub" and fall back to nil for that field.
func ParseLocalInterfaces(dir string) (map[string]LocalInterface, error) {
	out := make(map[string]LocalInterface)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}

	fset := token.NewFileSet()
	// Two-pass: first collect interface AST entries (so embeds can
	// resolve in either declaration order), then flatten methods.
	entries2, ownPkg := collectInterfaceDecls(fset, dir, entries)

	// Every interface type name declared in this package, known before
	// any method is rendered. A method result naming one of these must
	// return nil, not `T{}` — a composite literal is legal only for a
	// struct. The self-returning `WithTx(...) T` on generated stores is
	// the shape that made this load-bearing.
	//
	// Only bare local names are decidable here: this path has the AST
	// and no type information, so a result typed `pkg.Iface` is left to
	// the cross-package resolver, which has real types.
	localIfaceNames := make(map[string]bool, len(entries2))
	for _, e := range entries2 {
		localIfaceNames[e.name] = true
	}

	// Build raw method maps + embed maps, then flatten.
	directMethods := map[string][]InterfaceMethod{}
	embeds := map[string][]string{}
	// foreign holds each interface's cross-package embeds, and
	// unknownEmbeds the embeds no resolver can read (a generic
	// instantiation, an alias the declaring file does not import).
	foreign := map[string][]foreignEmbed{}
	unknownEmbeds := map[string][]string{}
	foreignPaths := map[string]bool{}
	// declFileImports records which file each interface was declared in,
	// so a flattened embed's signatures resolve through the EMBEDDED
	// interface's imports rather than the embedder's.
	declFileImports := map[string]map[string]string{}
	for _, e := range entries2 {
		declFileImports[e.name] = e.fileImports
		if e.ifaceAt.Methods == nil {
			continue
		}
		for _, field := range e.ifaceAt.Methods.List {
			switch ft := field.Type.(type) {
			case *ast.FuncType:
				for _, n := range field.Names {
					m := buildInterfaceMethod(fset, n.Name, ft, localIfaceNames)
					directMethods[e.name] = append(directMethods[e.name], m)
				}
			case *ast.Ident:
				switch {
				case localIfaceNames[ft.Name]:
					// Embedded same-package interface.
					embeds[e.name] = append(embeds[e.name], ft.Name)
				case ft.Name == "error":
					// The predeclared error contributes Error() string.
					directMethods[e.name] = append(directMethods[e.name], errorMethod())
				case ft.Name == "any" || ft.Name == "comparable":
					// No methods.
				default:
					unknownEmbeds[e.name] = append(unknownEmbeds[e.name], ft.Name)
				}
			case *ast.SelectorExpr:
				// Cross-package embed (`worktreesweep.StorageStore`). Its
				// methods are read from the type-checked package below —
				// go/types flattens whatever IT embeds, at any depth.
				alias, ok := ft.X.(*ast.Ident)
				path := ""
				if ok {
					path = e.fileImports[alias.Name]
				}
				if path == "" {
					unknownEmbeds[e.name] = append(unknownEmbeds[e.name], printType(fset, ft))
					continue
				}
				foreign[e.name] = append(foreign[e.name], foreignEmbed{
					expr: printType(fset, ft), importPath: path, typeName: ft.Sel.Name,
					fileImports: e.fileImports,
				})
				foreignPaths[path] = true
			default:
				unknownEmbeds[e.name] = append(unknownEmbeds[e.name], printType(fset, field.Type))
			}
		}
	}

	// needed accumulates import path -> alias for the signatures walked
	// so far. It is filled per declaring interface, so an embedded
	// interface contributes its OWN file's import bindings. The walk
	// covers same-package embeds; the cross-package ones it reaches are
	// gathered into pending and rendered after it (below), once every
	// local signature has claimed its alias.
	var resolve func(name string, visited map[string]bool, needed map[string]string, pending *[]foreignEmbed, unresolved *[]string) []InterfaceMethod
	resolve = func(name string, visited map[string]bool, needed map[string]string, pending *[]foreignEmbed, unresolved *[]string) []InterfaceMethod {
		if visited[name] {
			return nil
		}
		visited[name] = true
		methods := append([]InterfaceMethod{}, directMethods[name]...)
		collectSignatureImports(directMethods[name], declFileImports[name], needed)
		*pending = append(*pending, foreign[name]...)
		*unresolved = append(*unresolved, unknownEmbeds[name]...)
		for _, em := range embeds[name] {
			methods = append(methods, resolve(em, visited, needed, pending, unresolved)...)
		}
		return methods
	}

	resolver := newTypesResolver(dir, sortedPathSet(foreignPaths))
	for _, e := range entries2 {
		needed := map[string]string{}
		var pending []foreignEmbed
		var unresolved []string
		methods := resolve(e.name, map[string]bool{}, needed, &pending, &unresolved)
		for _, fe := range pending {
			iface, ok := resolver.lookup(fe.importPath, fe.typeName)
			if !ok {
				unresolved = append(unresolved, fe.expr)
				continue
			}
			embedded, ok := typedInterfaceMethods(iface, embedQualifier(needed, fe.fileImports, ownPkg))
			if !ok {
				unresolved = append(unresolved, fe.expr)
				continue
			}
			methods = append(methods, embedded...)
		}
		out[e.name] = LocalInterface{
			Name: e.name,
			// Go accepts one method reached through two embeds (identical
			// signatures, since 1.14); a stub declaring it twice does not
			// compile.
			Methods:      dedupeMethods(methods),
			Unresolved:   unresolved,
			ExtraImports: SortedNeededImports(needed),
		}
	}
	return out, nil
}

// ifaceDecl is one interface type declaration ParseLocalInterfaces found.
type ifaceDecl struct {
	name    string
	ifaceAt *ast.InterfaceType
	// fileImports is the declaring file's alias -> path map, used to
	// resolve the package qualifiers appearing in this interface's
	// method signatures. Import scope is per-FILE in Go, so this must
	// be tracked per declaration, not per directory: two files in the
	// same package can bind the same alias to different paths.
	fileImports map[string]string
}

// collectInterfaceDecls is ParseLocalInterfaces' first pass: every interface
// type declared in dir's non-test, non-generated .go files, plus the package
// clause. ownPkg is the package clause: an import alias equal to it is taken
// for the package's own import and dropped from the stub's file (see
// extraImportsExcluding), so a cross-package embed must not choose it.
func collectInterfaceDecls(fset *token.FileSet, dir string, entries []os.DirEntry) (decls []ifaceDecl, ownPkg string) {
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		// Skip generated files — they re-declare nothing useful and
		// can transiently fail to parse. Same convention as ParseContract.
		if strings.HasSuffix(e.Name(), "_gen.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if ownPkg == "" && file.Name != nil {
			ownPkg = file.Name.Name
		}
		fileImports := fileImportMap(file)
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				decls = append(decls, ifaceDecl{name: ts.Name.Name, ifaceAt: it, fileImports: fileImports})
			}
		}
	}
	return decls, ownPkg
}

// foreignEmbed is an interface embedded from another package, as the
// embedding file spells it.
type foreignEmbed struct {
	expr        string // "worktreesweep.StorageStore"
	importPath  string
	typeName    string
	fileImports map[string]string // the embedding file's alias -> path
}

// embedQualifier names each package a cross-package embed's signatures
// reference, recording it in needed (path -> alias). A path some signature
// already claimed keeps that alias; otherwise the embedding file's own alias
// for it is preferred, then the package's declared name. An alias already
// bound to a DIFFERENT path, or equal to the package's own name, gets a
// numeric suffix — the import is emitted with its alias, so any spelling
// compiles.
func embedQualifier(needed, fileImports map[string]string, ownPkg string) types.Qualifier {
	return func(p *types.Package) string {
		path := p.Path()
		if alias, ok := needed[path]; ok {
			return alias
		}
		base := p.Name()
		for alias, imported := range fileImports {
			if imported == path {
				base = alias
				break
			}
		}
		taken := func(alias string) bool {
			if alias == ownPkg {
				return true
			}
			for other, a := range needed {
				if a == alias && other != path {
					return true
				}
			}
			return false
		}
		alias := base
		for n := 2; taken(alias); n++ {
			alias = fmt.Sprintf("%s%d", base, n)
		}
		needed[path] = alias
		return alias
	}
}

// errorMethod is the method the predeclared error interface contributes.
func errorMethod() InterfaceMethod {
	return InterfaceMethod{
		Name:            "Error",
		Results:         "string",
		ResultTypes:     []string{"string"},
		ResultZeros:     []string{`""`},
		ReturnStatement: `return ""`,
	}
}

// dedupeMethods keeps the first method of each name, in order.
func dedupeMethods(methods []InterfaceMethod) []InterfaceMethod {
	seen := make(map[string]bool, len(methods))
	out := methods[:0:0]
	for _, m := range methods {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		out = append(out, m)
	}
	return out
}

func sortedPathSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fileImportMap renders one file's import block as alias -> path, where
// the alias is the name the file's own code must use: the explicit alias
// when present, otherwise the path's last segment.
//
// The leaf-segment fallback is a heuristic — a package's declared name may
// differ from its directory (`package v1` under `.../userv1`). That is fine
// here because we only ever look up qualifiers we OBSERVED in a signature
// that this same file compiles, and we re-emit the import under exactly the
// alias we matched. Where the two disagree, the file must already carry an
// explicit alias, which we read directly.
//
// Dot- and blank-imports are skipped: a dot-import contributes no qualifier
// to match on, and a blank import can never be referenced.
func fileImportMap(file *ast.File) map[string]string {
	out := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if path == "" {
			continue
		}
		alias := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			alias = path[i+1:]
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				continue
			}
			alias = spec.Name.Name
		}
		out[alias] = path
	}
	return out
}

// collectSignatureImports records, into needed (path -> alias), every
// import the RENDERED method signatures actually reference.
//
// It reads the rendered Params/Results strings rather than re-walking the
// AST deliberately: those strings are verbatim what the stub template
// emits, so matching on them cannot drift from what the generated file
// says. Emitting an import the signature does not reference would be an
// unused import — as fatal to the build as the missing one this fixes —
// so a qualifier that resolves to nothing in fileImports is skipped.
func collectSignatureImports(methods []InterfaceMethod, fileImports map[string]string, needed map[string]string) {
	if len(fileImports) == 0 {
		return
	}
	for _, m := range methods {
		for _, sig := range []string{m.Params, m.Results} {
			for alias := range qualifiersIn(sig) {
				if path, ok := fileImports[alias]; ok {
					needed[path] = alias
				}
			}
		}
	}
}

// qualifiersIn returns the set of package qualifiers appearing as the
// `pkg` in a `pkg.Type` selector inside a rendered type expression.
//
// The scan is lexical: walk identifier runs, and take a run as a
// qualifier when the character immediately after it is '.'. Decoration
// (`*`, `[]`, `map[`, `chan `, parens, commas) is naturally skipped
// because none of it is an identifier character. Parameter NAMES cannot
// be mistaken for qualifiers — a name is always followed by a space, not
// a dot.
func qualifiersIn(sig string) map[string]struct{} {
	out := map[string]struct{}{}
	i := 0
	for i < len(sig) {
		if !isIdentStart(sig[i]) {
			i++
			continue
		}
		j := i
		for j < len(sig) && isIdentPart(sig[j]) {
			j++
		}
		if j < len(sig) && sig[j] == '.' {
			out[sig[i:j]] = struct{}{}
		}
		// Skip the whole run (plus a trailing '.' and the selected name)
		// so `pkg.Type` never re-enters with `Type` as a candidate.
		i = j + 1
	}
	return out
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// buildInterfaceMethod renders a single interface method into its
// stub-template-ready form: pretty-printed params, results, and a
// "return <zeroes>" body the template can drop into the generated
// stub method.
//
// localIfaceNames is the set of interface type names declared in this
// package, used to emit nil rather than an invalid `T{}` for a result
// typed as one of them. May be nil (treated as empty).
func buildInterfaceMethod(fset *token.FileSet, name string, ft *ast.FuncType, localIfaceNames map[string]bool) InterfaceMethod {
	m := InterfaceMethod{Name: name}

	// Params: types only, one per parameter. The stub's body never reads a
	// parameter, and a name could only collide with something the body
	// does spell — testkit, a result type's package qualifier, a local
	// type's composite literal, `new` (see InterfaceMethod.Params).
	if ft.Params != nil && len(ft.Params.List) > 0 {
		var parts []string
		for _, field := range ft.Params.List {
			tStr := printType(fset, field.Type)
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				parts = append(parts, tStr)
			}
		}
		m.Params = strings.Join(parts, ", ")
		m.ParamTypes = parts
	}

	// Results: collect into a flat list of type expressions. The
	// template wraps them in parens when len > 1.
	var resultTypes []string
	if ft.Results != nil {
		for _, field := range ft.Results.List {
			tStr := printType(fset, field.Type)
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				resultTypes = append(resultTypes, tStr)
			}
		}
	}
	m.ResultTypes = resultTypes
	for _, t := range resultTypes {
		m.ResultZeros = append(m.ResultZeros, zeroValueForResultType(t, localIfaceNames[strings.TrimSpace(t)]))
	}
	switch len(resultTypes) {
	case 0:
		m.Results = ""
		m.ReturnStatement = ""
	case 1:
		m.Results = resultTypes[0]
		m.ReturnStatement = "return " + m.ResultZeros[0]
	default:
		m.Results = "(" + strings.Join(resultTypes, ", ") + ")"
		m.ReturnStatement = "return " + strings.Join(m.ResultZeros, ", ")
	}

	return m
}

// zeroValueForResultType returns the Go literal for the zero value of
// a stub method's result, given whether the CALLER has proven that
// result type to be an interface.
//
// The isInterface flag is the whole point, and it must come from real
// type information — never from the shape of the type string. A
// composite literal is legal Go only for a struct, so `T{}` where T is
// an interface does not compile, and nothing about the rendered text
// "store.CrewStore" distinguishes it from "store.Snapshot".
//
// This was not a corner case: every generated store carries
// `WithTx(orm.Context) T`, returning the interface itself, so any
// db.*Store on a handler's Deps emitted an uncompilable
// helpers_gen_test.go — and that file is Tier-1 regenerated with a
// forge:hash, so the user could not hand-roll around it. The failure
// surfaced far from its cause, as `packages failed to load` from the
// contract linter and `[build failed]` from the test run.
//
// Both call sites can prove this cheaply: ResolveCrossPkgInterface
// holds the types.Type (types.IsInterface), and ParseLocalInterfaces
// knows every interface name declared in the package.
func zeroValueForResultType(t string, isInterface bool) string {
	if isInterface {
		return "nil"
	}
	return zeroValueForType(t)
}

// zeroValueForType returns a Go EXPRESSION evaluating to the zero
// value of the given pretty-printed type expression. Mirrors the
// contract package's zeroValue but lives here so the codegen package
// doesn't take an import-cycle on internal/generator/contract.
//
// The recognized cases below are emitted as their idiomatic literals
// (`""`, `0`, `false`, `nil`) because the generated stub is read by
// humans. Everything else — every named type — falls through to a
// universal form rather than a guessed literal; see the comment on
// that branch for why.
//
// Callers that can prove a result is an interface should still go
// through zeroValueForResultType: `nil` is correct AND reads better
// than the universal form, though both compile.
func zeroValueForType(t string) string {
	t = strings.TrimSpace(t)
	switch t {
	case "bool":
		return "false"
	case "string":
		return `""`
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "complex64", "complex128",
		"byte", "rune", "uintptr":
		return "0"
	case "error":
		return "nil"
	case "any", "interface{}":
		return "nil"
	}
	if strings.HasPrefix(t, "*") ||
		strings.HasPrefix(t, "[]") ||
		strings.HasPrefix(t, "map[") ||
		strings.HasPrefix(t, "chan ") ||
		strings.HasPrefix(t, "<-chan ") ||
		strings.HasPrefix(t, "chan<- ") ||
		strings.HasPrefix(t, "func(") ||
		strings.HasPrefix(t, "interface{") ||
		strings.HasPrefix(t, "interface ") {
		return "nil"
	}
	// Anything else is a NAMED type, and from rendered text alone we
	// cannot tell which family it belongs to — `policy.Role` reads
	// identically whether it is a struct, a defined string, a defined
	// int, or a named pointer. So do not guess: emit a form that is
	// correct for every one of them.
	//
	// `*new(T)` is that form. new(T) is legal for any type whatsoever
	// and yields *T; dereferencing gives T's zero value. It is an
	// EXPRESSION, which matters here because the caller joins several
	// of these with commas into a single `return a, b, c` — a
	// statement form like `var zero T` would need a multi-statement
	// body, distinct names per result, and would have to dodge
	// collisions with the stub's own parameter names (parameters share
	// the body's scope, so a method taking `zero string` would not
	// compile). The expression form has none of that surface.
	//
	// This branch previously returned `t + "{}"`, which is a composite
	// literal — legal ONLY for a struct, array, slice or map. That
	// guess was wrong twice: first for interface results (fixed above
	// via zeroValueForResultType, which still short-circuits here and
	// keeps the more readable `nil`), and then for `type Role string`
	// on a policy service, emitting `policy.Role{}` → "invalid
	// composite literal type policy.Role". Named scalars, named
	// pointers and defined func types were all in the same hole. Rather
	// than add a third special case, the fallback no longer needs to
	// know what the type is.
	return "*new(" + t + ")"
}

// IsLocallyDeclaredInterface reports whether typeExpr (as printed by
// printType — a bare identifier or selector) names an interface
// declared in locals. The Deps field type is matched ignoring
// surrounding pointer/array decoration: only fields with type "T"
// where "T" is a local interface name are auto-stubbable. Pointer-to-
// interface (`*Repository`) is not idiomatic and is left to the
// hand-roll path.
func IsLocallyDeclaredInterface(typeExpr string, locals map[string]LocalInterface) bool {
	if locals == nil {
		return false
	}
	_, ok := locals[strings.TrimSpace(typeExpr)]
	return ok
}

// printerInterface is a tiny indirection so the package-level
// printType lives in deps_parser.go without needing re-export.
var _ = printer.CommentedNode{}
