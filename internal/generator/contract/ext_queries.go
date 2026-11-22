// Query sets collected from an ORM package's <entity>_repo_ext.go seam.
//
// derive.go turns a marked type's exported METHOD set into an interface. The
// ORM's hand-written queries are not methods: the seam forge scaffolds tells
// the author to write package functions of the delegates' own shape,
//
//	func GetPlanByThreadID(ctx context.Context, db orm.Context, threadID string) (*core.Plan, error)
//
// and a function is not a type, so no Deps interface could name one without a
// hand-written passthrough adapter — the layer the generated <Entity>Store
// exists to delete. CollectExtQueries reads those functions so the ORM
// generator can bind the handle the way <Entity>Store does: one interface
// method per query, minus the orm.Context.
//
// It works on syntax alone, like derive.go, and shares its import resolution
// (fileImports, the lazy Resolver) and doc copying (methodDocLines): an ext
// file is user code mid-edit as often as not, and generate must not need it to
// type-check.
package contract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/contractsrc"
)

// ORMImportPath is the import path of forge's ORM runtime, whose Context is
// the handle every ext query takes second.
const ORMImportPath = "github.com/reliant-labs/forge/pkg/orm"

// ExtQuerySet is the collectable query functions of one ext file.
type ExtQuerySet struct {
	// Package is the file's package name.
	Package string
	// ORMName is the name the file binds forge's orm package to (`orm`,
	// unless the file aliases it). A rendering that names orm.Context
	// spells it with this qualifier, which Imports carries.
	ORMName string
	// Queries are the collected functions, sorted by name.
	Queries []ExtQuery
	// Imports are the imports the collected signatures need, the orm
	// package always among them, sorted by path. Aliases are the file's.
	Imports []ImportDef
	// Skipped are the exported functions that look like queries — they take
	// a context.Context or an orm.Context — but cannot be collected, sorted
	// by name.
	Skipped []ExtSkip
}

// ExtQuery is one collected function, its orm.Context parameter removed.
type ExtQuery struct {
	Name string
	// Doc is the function's doc comment, line by line, `//go:` directives
	// dropped.
	Doc []string
	// Params are the parameters after the handle is removed: the context
	// first. Every one is named (an unnamed or `_` parameter gets a
	// synthesized name), so a forwarding call can pass it on.
	Params []ExtParam
	// Results are the results as declared; Name is "" when unnamed.
	Results []ExtParam
}

// ExtParam is one parameter or result: its name and its type, spelled as
// the source file spells it.
type ExtParam struct {
	Name     string
	Type     string // "...string" for a variadic parameter
	Variadic bool
}

// ExtSkip is an exported function CollectExtQueries left out, and why.
type ExtSkip struct {
	Name   string
	Reason string
}

// ErrExtFileUnusable wraps the reasons a whole ext file is not collected
// (it does not parse, has a dot import, or build constraints leave it out of
// some builds). The caller reports it and generates nothing from the file.
type ErrExtFileUnusable struct {
	Path   string
	Reason string
}

func (e *ErrExtFileUnusable) Error() string { return e.Path + ": " + e.Reason }

// CollectExtQueries reads the ext file at path and returns its exported
// package functions of the form `func F(ctx context.Context, db orm.Context,
// …)` — orm being forge's pkg/orm under whatever name the file imports it.
//
// Collected: any further parameters (variadic included) and any results.
// Skipped, each with its reason: a generic function, one whose signature
// names an unexported type (an interface naming it could not be implemented
// or mocked outside the package), one whose qualifier no import binds, and one
// named WithTx (the store's own method). Methods, unexported functions and
// exported functions taking neither handle are not queries and are not
// reported.
func CollectExtQueries(path string) (ExtQuerySet, error) {
	return new(ExtQueryCollector).Collect(path)
}

// ExtQueryCollector collects multiple ext files in one generation run. It
// indexes package declarations once per directory, even if it reads many seams.
// The zero value is ready to use; do not reuse across edits or concurrently.
type ExtQueryCollector struct {
	localTypes map[[2]string]map[string]bool // (directory, package) → package-local type names
}

// Collect reads the query declarations of one seam.
func (batch *ExtQueryCollector) Collect(path string) (ExtQuerySet, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return ExtQuerySet{}, &ErrExtFileUnusable{Path: path, Reason: "does not parse: " + err.Error()}
	}
	fi := newFileImports(file)
	if fi.dot {
		return ExtQuerySet{}, &ErrExtFileUnusable{Path: path, Reason: "has a dot import, so its package's types cannot be told from the imported ones"}
	}
	if contractsrc.BuildConstrained(filepath.Dir(path), filepath.Base(path), file) {
		return ExtQuerySet{}, &ErrExtFileUnusable{Path: path, Reason: "is left out of some builds by build constraints"}
	}
	c := &extCollector{
		path: path, fset: fset, fi: fi, set: ExtQuerySet{Package: file.Name.Name},
		needed: map[string]ImportDef{}, localTypes: batch.packageLocalTypes(path, file),
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() {
			continue
		}
		c.collect(fn)
	}
	if len(c.set.Queries) > 0 {
		imports := make([]ImportDef, 0, len(c.needed))
		for _, imp := range c.needed {
			imports = append(imports, imp)
		}
		sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
		c.set.Imports = imports
	}
	sort.Slice(c.set.Queries, func(i, j int) bool { return c.set.Queries[i].Name < c.set.Queries[j].Name })
	sort.Slice(c.set.Skipped, func(i, j int) bool { return c.set.Skipped[i].Name < c.set.Skipped[j].Name })
	return c.set, nil
}

// packageLocalTypes finds declarations which can hide a universe type. Read
// across build variants: the unconditional generated interface must not expose
// a private type in any supported build. Test files are never part of it.
func (batch *ExtQueryCollector) packageLocalTypes(path string, file *ast.File) map[string]bool {
	dir := filepath.Dir(path)
	key := [2]string{dir, file.Name.Name}
	if names, ok := batch.localTypes[key]; ok {
		return names
	}
	names := map[string]bool{}
	addTypes := func(f *ast.File) {
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						names[ts.Name.Name] = true
					}
				}
			}
		}
	}
	addTypes(file)
	entries, _ := os.ReadDir(dir)
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == filepath.Base(path) || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The parser can provide usable declarations even if a function body
		// is mid-edit. No package type checking or toolchain load is needed.
		if f, _ := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution); f != nil && f.Name.Name == file.Name.Name {
			addTypes(f)
		}
	}
	if batch.localTypes == nil {
		batch.localTypes = map[[2]string]map[string]bool{}
	}
	batch.localTypes[key] = names
	return names
}

type extCollector struct {
	path       string
	fset       *token.FileSet
	fi         *fileImports
	r          *Resolver
	set        ExtQuerySet
	needed     map[string]ImportDef // local name → import
	localTypes map[string]bool
}

// resolve returns the import a qualifier binds, building the toolchain
// Resolver only when the Go convention cannot name an unaliased import (as
// derive.go does).
func (c *extCollector) resolve(q string) (ImportDef, bool) {
	if imp, ok := c.fi.resolve(q, c.r); ok {
		return imp, true
	}
	if c.r == nil && len(c.fi.unnamed) > 0 {
		c.r = NewResolver(c.path)
		return c.fi.resolve(q, c.r)
	}
	return ImportDef{}, false
}

// isSelector reports whether expr is `q.Sel` with q binding importPath.
func (c *extCollector) isSelector(expr ast.Expr, importPath, sel string) (ImportDef, bool) {
	s, ok := expr.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return ImportDef{}, false
	}
	id, ok := s.X.(*ast.Ident)
	if !ok {
		return ImportDef{}, false
	}
	// Convention alone: both handles' packages are named as their paths
	// say, so recognising them never needs the toolchain.
	imp, ok := c.fi.resolve(id.Name, nil)
	if !ok || imp.Path != importPath {
		return ImportDef{}, false
	}
	return imp, true
}

// handles reports whether fn takes a context.Context or an orm.Context
// anywhere — the mark of a query, collected or not.
func (c *extCollector) handles(fn *ast.FuncDecl) bool {
	for _, f := range fn.Type.Params.List {
		if _, ok := c.isSelector(f.Type, "context", "Context"); ok {
			return true
		}
		if _, ok := c.isSelector(f.Type, ORMImportPath, "Context"); ok {
			return true
		}
	}
	return false
}

func (c *extCollector) skip(fn *ast.FuncDecl, reason string) {
	c.set.Skipped = append(c.set.Skipped, ExtSkip{Name: fn.Name.Name, Reason: reason})
}

// collect folds one exported package function into the set.
func (c *extCollector) collect(fn *ast.FuncDecl) {
	if !c.handles(fn) {
		return
	}
	params := flattenFields(fn.Type.Params)
	if len(params) < 2 {
		c.skip(fn, "not func(ctx context.Context, db orm.Context, …)")
		return
	}
	if _, ok := c.isSelector(params[0].typ, "context", "Context"); !ok {
		c.skip(fn, "not func(ctx context.Context, db orm.Context, …)")
		return
	}
	ormImp, ok := c.isSelector(params[1].typ, ORMImportPath, "Context")
	if !ok {
		c.skip(fn, "not func(ctx context.Context, db orm.Context, …)")
		return
	}
	if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0 {
		c.skip(fn, "generic")
		return
	}
	if fn.Name.Name == "WithTx" {
		c.skip(fn, "named WithTx, which the store declares itself")
		return
	}
	results := flattenFields(fn.Type.Results)
	kept := append(append([]field(nil), params[0]), params[2:]...)
	for _, f := range append(append([]field(nil), kept...), results...) {
		if name := unexportedLocalType(f.typ, c.localTypes); name != "" {
			c.skip(fn, "names the unexported type "+name)
			return
		}
	}
	// Every qualifier the kept signature spells must bind an import.
	uses := map[string]ImportDef{}
	for _, f := range append(append([]field(nil), kept...), results...) {
		for _, q := range qualifiers(f.typ) {
			imp, ok := c.resolve(q)
			if !ok {
				c.skip(fn, "names "+q+"., which no import of the file binds")
				return
			}
			uses[q] = imp
		}
	}

	doc := methodDocLines(fn.Doc)
	// A dropped directive leaves its blank `//` separator behind.
	for len(doc) > 0 && strings.TrimSpace(strings.TrimPrefix(doc[len(doc)-1], "//")) == "" {
		doc = doc[:len(doc)-1]
	}
	q := ExtQuery{Name: fn.Name.Name, Doc: doc}
	taken := map[string]bool{}
	for _, f := range append(append([]field(nil), kept...), results...) {
		if f.name != "" && f.name != "_" {
			taken[f.name] = true
		}
	}
	for i, f := range kept {
		name := f.name
		if name == "" || name == "_" {
			name = freshName(fmt.Sprintf("p%d", i), taken)
		}
		_, variadic := f.typ.(*ast.Ellipsis)
		q.Params = append(q.Params, ExtParam{Name: name, Type: c.render(f.typ), Variadic: variadic})
	}
	for _, f := range results {
		name := f.name
		if name == "_" {
			name = ""
		}
		q.Results = append(q.Results, ExtParam{Name: name, Type: c.render(f.typ)})
	}
	// A result list mixing `_` with names would not parse once `_` is
	// dropped; keep names only when every result has one.
	for _, r := range q.Results {
		if r.Name == "" {
			for i := range q.Results {
				q.Results[i].Name = ""
			}
			break
		}
	}

	for name, imp := range uses {
		c.needed[name] = imp
	}
	ormName := ormImp.Name
	if c.set.ORMName == "" {
		c.set.ORMName = ormName
		c.needed[ormName] = ormImp
	} else if c.set.ORMName != ormName {
		// Unreachable in one file — an import binds one name — but a
		// rendering that spelled orm two ways would not compile.
		c.skip(fn, "spells orm.Context as "+ormName+".Context, unlike the file's other queries")
		return
	}
	c.set.Queries = append(c.set.Queries, q)
}

// render spells a type expression exactly as the source does.
func (c *extCollector) render(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, c.fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

// field is one name of a flattened field list: `a, b string` is two.
type field struct {
	name string
	typ  ast.Expr
}

func flattenFields(fl *ast.FieldList) []field {
	if fl == nil {
		return nil
	}
	var out []field
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			out = append(out, field{typ: f.Type})
			continue
		}
		for _, n := range f.Names {
			out = append(out, field{name: n.Name, typ: f.Type})
		}
	}
	return out
}

func freshName(base string, taken map[string]bool) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	taken[name] = true
	return name
}

// unexportedLocalType returns the first unexported type a signature names,
// or "" if it names none. A package-local declaration takes precedence over
// a predeclared type. Only type positions are walked: a struct's field names
// and an interface's method names are not types.
func unexportedLocalType(expr ast.Expr, localTypes map[string]bool) string {
	var found string
	var walk func(ast.Expr)
	walkFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			walk(f.Type)
		}
	}
	walk = func(e ast.Expr) {
		if found != "" || e == nil {
			return
		}
		switch t := e.(type) {
		case *ast.Ident:
			_, predeclared := types.Universe.Lookup(t.Name).(*types.TypeName)
			if !t.IsExported() && (!predeclared || localTypes[t.Name]) {
				found = t.Name
			}
		case *ast.SelectorExpr:
			// pkg.T: exported by construction.
		case *ast.StarExpr:
			walk(t.X)
		case *ast.ParenExpr:
			walk(t.X)
		case *ast.Ellipsis:
			walk(t.Elt)
		case *ast.ArrayType:
			walk(t.Elt)
		case *ast.MapType:
			walk(t.Key)
			walk(t.Value)
		case *ast.ChanType:
			walk(t.Value)
		case *ast.IndexExpr:
			walk(t.X)
			walk(t.Index)
		case *ast.IndexListExpr:
			walk(t.X)
			for _, i := range t.Indices {
				walk(i)
			}
		case *ast.FuncType:
			walkFields(t.Params)
			walkFields(t.Results)
		case *ast.StructType:
			walkFields(t.Fields)
		case *ast.InterfaceType:
			walkFields(t.Methods)
		}
	}
	walk(expr)
	return found
}

// qualifiers returns the package qualifiers a type expression spells, in
// order of appearance.
func qualifiers(expr ast.Expr) []string {
	var out []string
	ast.Inspect(expr, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
		return false
	})
	return out
}

// String renders the query's interface method signature — `(ctx
// context.Context, id string) (*Plan, error)` — the FuncType minus `func`.
func (q ExtQuery) String() string {
	var b strings.Builder
	b.WriteString("(")
	for i, p := range q.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name + " " + p.Type)
	}
	b.WriteString(")")
	switch {
	case len(q.Results) == 0:
	case len(q.Results) == 1 && q.Results[0].Name == "":
		b.WriteString(" " + q.Results[0].Type)
	default:
		b.WriteString(" (")
		for i, r := range q.Results {
			if i > 0 {
				b.WriteString(", ")
			}
			if r.Name != "" {
				b.WriteString(r.Name + " ")
			}
			b.WriteString(r.Type)
		}
		b.WriteString(")")
	}
	return b.String()
}
