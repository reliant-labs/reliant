package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Mocks for dep interfaces that live in ANOTHER package of this module.
//
// The mock generator's original input set was one file: the package's own
// contract.go. That is the right default for interfaces a package
// declares, and it is exactly wrong for the one dep shape forge tells
// every user to write. The service-layer skill says "do NOT hand-write a
// Store interface plus a passthrough adapter over the ORM … just declare
// the field", with `Deps { Estimates db.EstimateStore }` as its own
// example — and the db.*Store interfaces that field names are generated
// into internal/db/store_gen.go, which the contract parser never looked
// at. So forge mandated a shape and then supplied no way to test it,
// while the scaffolded contract_test.go went on promising a
// `pipeline.MockStore` that could not exist.
//
// Discovery starts from the Deps struct rather than from internal/db,
// which is what keeps the output proportionate: a package naming one
// store gets one mock, not one per table. The aggregate db.Store is the
// exception that proves it — its accessors RETURN the per-entity stores,
// so those come along transitively or MockStore is unusable.

// depsTypeName is the struct whose fields name a package's dependencies.
// Forge scaffolds it in service.go and resolves it by type at
// composition; it is the one declaration that knows what a package
// depends on.
const depsTypeName = "Deps"

// foreignInterfaces finds every interface reachable from the Deps struct
// in dir and returns them ready to render into the package named by cf:
// method signatures re-qualified into the consuming package's namespace,
// plus the import paths that qualification requires.
//
// Errors are deliberately swallowed into "found nothing". A package with
// no Deps, an unresolvable import, or a sibling mid-edit must still get
// its own contract.go mocks — mock generation runs on every `forge
// generate`, so a hard failure here would block codegen for the whole
// project over a dep that is merely not understood yet.
func foreignInterfaces(dir string, fset *token.FileSet, consumerImports map[string]string, consumerExplicit map[string]bool, r *Resolver) ([]InterfaceDef, map[string]ImportDef) {
	fields := depsFieldTypes(dir, fset, r)
	if len(fields) == 0 {
		return nil, nil
	}

	moduleRoot, modulePath := findModule(dir)
	if moduleRoot == "" {
		return nil, nil
	}

	// Group the named types by the package they came from, so each
	// foreign package is parsed once however many stores are named.
	type packageRequest struct {
		qualifier string
		names     []string
	}
	byImport := make(map[string]*packageRequest)
	for _, f := range fields {
		req := byImport[f.importPath]
		if req == nil {
			req = &packageRequest{qualifier: f.qualifier}
			byImport[f.importPath] = req
		}
		req.names = append(req.names, f.typeName)
	}

	aliases := newImportAliases(consumerImports, consumerExplicit, r)
	var defs []InterfaceDef
	var importPaths []string
	for importPath := range byImport {
		importPaths = append(importPaths, importPath)
	}
	sort.Strings(importPaths)
	for _, importPath := range importPaths {
		req := byImport[importPath]
		pkgDir, ok := packageDir(moduleRoot, modulePath, importPath)
		if !ok {
			continue
		}
		typed := r.foreignPackage(importPath)
		// The syntax view is the fallback for interfaces the toolchain
		// could not type. Parsed lazily: with a healthy load it is never
		// needed.
		var syntax *foreignPackage
		syntaxOK := true
		parseSyntax := func() *foreignPackage {
			if syntax == nil && syntaxOK {
				syntax, syntaxOK = parseForeignPackage(pkgDir, fset, r)
			}
			return syntax
		}
		if typed == nil && parseSyntax() == nil {
			continue
		}
		qualifier := aliases.use(importPath, req.qualifier, true)

		// Transitive closure over the NAMED interfaces only. An accessor
		// returning another interface of the same package (db.Store's
		// Estimates() EstimateStore) pulls that one in, because a mock
		// whose accessor cannot be scripted is not a usable mock.
		seen := map[string]bool{}
		var queue []string
		queue = append(queue, req.names...)
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			if seen[name] {
				continue
			}
			if typed != nil {
				def, reachable, isIface, ok := typedInterface(typed, name, aliases)
				if ok {
					if !isIface {
						// A Deps field can name a struct (a row type, a
						// config). Not every named type is mockable, and a
						// mock for a struct would not compile.
						continue
					}
					seen[name] = true
					def.Qualifier = qualifier
					defs = append(defs, def)
					queue = append(queue, reachable...)
					continue
				}
			}
			pkg := parseSyntax()
			if pkg == nil {
				continue
			}
			iface, isIface := pkg.interfaces[name]
			if !isIface {
				continue
			}
			seen[name] = true

			def, reachable := pkg.methodsOf(name, iface, qualifier, aliases, fset)
			defs = append(defs, def)
			queue = append(queue, reachable...)
		}
	}

	// Deterministic order: mock_gen.go is regenerated on every run and a
	// map-ordered render would churn the file (and its checksum) with no
	// change of meaning.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })

	return defs, aliases.neededImports()
}

// ImportDef is one import required by generated mock code. Name is the
// qualifier used in rendered signatures; Explicit says the declaration must
// retain that spelling instead of relying on the imported package's name.
type ImportDef struct {
	Path     string
	Name     string
	Explicit bool
}

// Declaration renders one import spec, preserving explicit or synthesized
// aliases. The path is quoted here so the template cannot accidentally quote
// the whole `alias "path"` declaration.
func (i ImportDef) Declaration() string {
	if i.Explicit {
		return i.Name + " " + strconv.Quote(i.Path)
	}
	return strconv.Quote(i.Path)
}

type importAliases struct {
	byPath map[string]ImportDef
	byName map[string]string
	needed map[string]bool
	// resolver supplies each path's declared package name, which decides
	// whether a chosen qualifier needs an explicit alias in the import
	// block. declared holds names learned exactly from type-checked
	// packages during this render, and wins over the resolver.
	resolver *Resolver
	declared map[string]string
	// self is the import path of the package the mock is rendered INTO.
	// Its types are written unqualified and it is never imported: a mock
	// importing its own package is an import cycle.
	self string
}

// packageName is the name an unaliased import of path binds.
func (r *importAliases) packageName(path string) string {
	if name, ok := r.declared[path]; ok {
		return name
	}
	return r.resolver.packageName(path)
}

func newImportAliases(imports map[string]string, explicit map[string]bool, resolver *Resolver) *importAliases {
	r := &importAliases{
		byPath:   make(map[string]ImportDef),
		byName:   make(map[string]string),
		needed:   make(map[string]bool),
		resolver: resolver,
		declared: make(map[string]string),
	}
	var names []string
	for name := range imports {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := imports[name]
		r.byPath[path] = ImportDef{Path: path, Name: name, Explicit: explicit[name]}
		r.byName[name] = path
	}
	return r
}

// use returns one collision-free qualifier for path and marks the import as
// required by a foreign mock signature. Existing aliases from contract.go win
// so a foreign `v1.Type` is rewritten to (for example)
// `controlplanev1.Type` instead of trying to import one path twice.
func (r *importAliases) use(path, preferred string, explicit bool) string {
	if existing, ok := r.byPath[path]; ok {
		r.needed[path] = true
		return existing.Name
	}
	if preferred == "" {
		preferred = r.packageName(path)
	}
	name := preferred
	if occupied, ok := r.byName[name]; ok && occupied != path {
		for suffix := 2; ; suffix++ {
			candidate := fmt.Sprintf("%s%d", preferred, suffix)
			if _, taken := r.byName[candidate]; !taken {
				name = candidate
				explicit = true
				break
			}
		}
	}
	// An import binds its package's DECLARED name; any other qualifier
	// must be spelled out in the import block.
	if name != r.packageName(path) {
		explicit = true
	}
	imp := ImportDef{Path: path, Name: name, Explicit: explicit}
	r.byPath[path] = imp
	r.byName[name] = path
	r.needed[path] = true
	return name
}

// useNamed is use for a package whose declared name is known exactly (a
// type-checked *types.Package): that name is preferred, and an alias is
// written only when it collides with one already taken.
func (r *importAliases) useNamed(path, pkgName string) string {
	if path != "" && path == r.self {
		return ""
	}
	r.declared[path] = pkgName
	return r.use(path, pkgName, false)
}

func (r *importAliases) neededImports() map[string]ImportDef {
	imports := make(map[string]ImportDef, len(r.needed))
	for path := range r.needed {
		imports[path] = r.byPath[path]
	}
	return imports
}

// depsField is one Deps field that names a type from another package.
type depsField struct {
	importPath string
	qualifier  string
	typeName   string
}

// depsFieldTypes reads the Deps struct from the package in dir and
// returns the qualified types its fields name, resolved through that
// file's own import block.
//
// Only `pkg.Type` fields are considered. An unqualified field names a
// type this package already declares — contract.go's own interfaces
// already cover those — and a field typed as a pointer, slice or func is
// not an interface to mock.
func depsFieldTypes(dir string, fset *token.FileSet, r *Resolver) []depsField {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var out []depsField
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_gen.go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		imports := importRefs(file, r)
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != depsTypeName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok || structType.Fields == nil {
					continue
				}
				for _, field := range structType.Fields.List {
					sel, ok := field.Type.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					pkgIdent, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					ref, ok := imports[pkgIdent.Name]
					if !ok {
						continue
					}
					out = append(out, depsField{importPath: ref.path, qualifier: pkgIdent.Name, typeName: sel.Sel.Name})
				}
			}
		}
	}
	return out
}

// foreignPackage is a parsed package other than the one being generated:
// its declared type names (needed to qualify identifiers) and its
// interfaces (the mock candidates).
type foreignPackage struct {
	// declaredTypes is every type name the package declares. An
	// identifier in a method signature that appears here is the foreign
	// package's own type and MUST be qualified when rendered into the
	// consuming package — an unqualified *Estimate in package pipeline
	// refers to nothing, and only the compiler catches it.
	declaredTypes map[string]bool
	interfaces    map[string]foreignInterface
}

type foreignInterface struct {
	typeNode *ast.InterfaceType
	imports  map[string]importRef
}

func parseForeignPackage(dir string, fset *token.FileSet, r *Resolver) (*foreignPackage, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}

	pkg := &foreignPackage{
		declaredTypes: map[string]bool{},
		interfaces:    map[string]foreignInterface{},
	}

	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			continue
		}
		found = true
		fileImports := importRefs(file, r)
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
				pkg.declaredTypes[typeSpec.Name.Name] = true
				if iface, ok := typeSpec.Type.(*ast.InterfaceType); ok {
					pkg.interfaces[typeSpec.Name.Name] = foreignInterface{typeNode: iface, imports: fileImports}
				}
			}
		}
	}
	if !found {
		return nil, false
	}
	return pkg, true
}

// methodsOf builds the InterfaceDef for one foreign interface, with every
// signature re-qualified into the consuming package's namespace. It also
// reports the import paths the rendered signatures need, and the names of
// same-package interfaces the signatures reach (so the caller can pull
// those in too).
func (p *foreignPackage) methodsOf(name string, iface foreignInterface, qualifier string, aliases *importAliases, fset *token.FileSet) (InterfaceDef, []string) {
	def := InterfaceDef{Name: name, Qualifier: qualifier}

	var reachable []string

	noteType := func(expr ast.Expr) {
		ast.Inspect(expr, func(n ast.Node) bool {
			switch t := n.(type) {
			case *ast.Ident:
				if _, ok := p.interfaces[t.Name]; ok {
					reachable = append(reachable, t.Name)
				}
			}
			return true
		})
	}

	if iface.typeNode.Methods == nil {
		return def, nil
	}
	for _, field := range iface.typeNode.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			// Embedded interfaces in a generated store are not a shape
			// forge emits; skipping keeps this from silently producing a
			// mock that fails the compile-time interface check.
			continue
		}
		for _, mName := range field.Names {
			md := MethodDef{Name: mName.Name}
			if ft.Params != nil {
				for _, param := range ft.Params.List {
					noteType(param.Type)
					typeStr := p.qualify(param.Type, qualifier, iface.imports, aliases, fset)
					_, variadic := param.Type.(*ast.Ellipsis)
					if len(param.Names) == 0 {
						md.Params = append(md.Params, ParamDef{TypeExpr: typeStr, Variadic: variadic})
						continue
					}
					for _, n := range param.Names {
						md.Params = append(md.Params, ParamDef{Name: n.Name, TypeExpr: typeStr, Variadic: variadic})
					}
				}
			}
			if ft.Results != nil {
				for _, res := range ft.Results.List {
					noteType(res.Type)
					typeStr := p.qualify(res.Type, qualifier, iface.imports, aliases, fset)
					if len(res.Names) == 0 {
						md.Results = append(md.Results, ParamDef{TypeExpr: typeStr})
						continue
					}
					for _, n := range res.Names {
						md.Results = append(md.Results, ParamDef{Name: n.Name, TypeExpr: typeStr})
					}
				}
			}
			def.Methods = append(def.Methods, md)
		}
	}

	return def, reachable
}

// qualify renders a type expression from the foreign package as it must
// read inside the consuming package: identifiers the foreign package
// declares gain its qualifier, everything else is untouched.
//
// This is the whole reason foreign interfaces cannot reuse the ordinary
// render path. The methods are parsed in db's namespace, where *Estimate
// is correct, and rendered into pipeline's, where it names nothing.
func (p *foreignPackage) qualify(expr ast.Expr, qualifier string, imports map[string]importRef, aliases *importAliases, fset *token.FileSet) string {
	switch t := expr.(type) {
	case *ast.Ident:
		if p.declaredTypes[t.Name] {
			return qualifier + "." + t.Name
		}
		return t.Name
	case *ast.StarExpr:
		return "*" + p.qualify(t.X, qualifier, imports, aliases, fset)
	case *ast.Ellipsis:
		return "..." + p.qualify(t.Elt, qualifier, imports, aliases, fset)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + p.qualify(t.Elt, qualifier, imports, aliases, fset)
		}
		return "[" + renderExpr(t.Len, fset) + "]" + p.qualify(t.Elt, qualifier, imports, aliases, fset)
	case *ast.MapType:
		return "map[" + p.qualify(t.Key, qualifier, imports, aliases, fset) + "]" + p.qualify(t.Value, qualifier, imports, aliases, fset)
	case *ast.ChanType:
		switch t.Dir {
		case ast.RECV:
			return "<-chan " + p.qualify(t.Value, qualifier, imports, aliases, fset)
		case ast.SEND:
			return "chan<- " + p.qualify(t.Value, qualifier, imports, aliases, fset)
		default:
			return "chan " + p.qualify(t.Value, qualifier, imports, aliases, fset)
		}
	case *ast.FuncType:
		var params []string
		if t.Params != nil {
			for _, f := range t.Params.List {
				rendered := p.qualify(f.Type, qualifier, imports, aliases, fset)
				count := len(f.Names)
				if count == 0 {
					count = 1
				}
				for i := 0; i < count; i++ {
					params = append(params, rendered)
				}
			}
		}
		var results []string
		if t.Results != nil {
			for _, f := range t.Results.List {
				rendered := p.qualify(f.Type, qualifier, imports, aliases, fset)
				count := len(f.Names)
				if count == 0 {
					count = 1
				}
				for i := 0; i < count; i++ {
					results = append(results, rendered)
				}
			}
		}
		sig := "func(" + strings.Join(params, ", ") + ")"
		switch len(results) {
		case 0:
			return sig
		case 1:
			return sig + " " + results[0]
		default:
			return sig + " (" + strings.Join(results, ", ") + ")"
		}
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if ref, found := imports[id.Name]; found {
				name := aliases.use(ref.path, id.Name, ref.explicit)
				return name + "." + t.Sel.Name
			}
		}
		return p.qualify(t.X, qualifier, imports, aliases, fset) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return p.qualify(t.X, qualifier, imports, aliases, fset) + "[" + p.qualify(t.Index, qualifier, imports, aliases, fset) + "]"
	case *ast.IndexListExpr:
		indices := make([]string, 0, len(t.Indices))
		for _, index := range t.Indices {
			indices = append(indices, p.qualify(index, qualifier, imports, aliases, fset))
		}
		return p.qualify(t.X, qualifier, imports, aliases, fset) + "[" + strings.Join(indices, ", ") + "]"
	case *ast.ParenExpr:
		return "(" + p.qualify(t.X, qualifier, imports, aliases, fset) + ")"
	default:
		return renderExpr(expr, fset)
	}
}

// importRef retains both an import's path and whether its local name was
// explicit. The latter matters when a package intentionally uses a short alias
// such as `v1` even though its declared package name is `controlplanev1`.
type importRef struct {
	path     string
	explicit bool
}

// importRefs maps each identifier file's imports bind to its import. An
// unaliased import binds its package's declared name, which r resolves.
func importRefs(file *ast.File, r *Resolver) map[string]importRef {
	out := make(map[string]importRef, len(file.Imports))
	for _, imp := range file.Imports {
		name, path, explicit := r.importLocalName(imp)
		if name == "_" || name == "." {
			continue
		}
		out[name] = importRef{path: path, explicit: explicit}
	}
	return out
}

// findModule walks up from dir to the nearest go.mod and returns its
// directory and declared module path.
func findModule(dir string) (string, string) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		data, err := os.ReadFile(filepath.Join(current, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return current, strings.TrimSpace(path)
				}
			}
			return current, ""
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ""
		}
		current = parent
	}
}

// packageDir maps an import path to a directory inside this module.
//
// In-module only, by design: a dep from a third-party module is not
// forge's to mock, and resolving one would mean a full package load on
// every generate for a mock nobody asked for.
func packageDir(moduleRoot, modulePath, importPath string) (string, bool) {
	if modulePath == "" || importPath == modulePath {
		return "", false
	}
	rel, ok := strings.CutPrefix(importPath, modulePath+"/")
	if !ok {
		return "", false
	}
	dir := filepath.Join(moduleRoot, filepath.FromSlash(rel))
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}
