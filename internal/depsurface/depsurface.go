// Package depsurface reads what a component KNOWS through its Deps
// (docs/design/composition-graph.md §2, "least knowledge"): for every Deps
// field, the type it declares and the part of that type the package actually
// uses through it. go/types resolves every `s.deps.X` selection to the field
// it names, however the Deps value was reached, so a method called through
// the field counts and a same-named method on anything else does not.
//
// Three rules read it (internal/linter/forgeconv/least_knowledge.go):
//
//   - deps-surface: an interface field declares methods the package never
//     needs through it — never calls, and never passes the value on as an
//     interface that declares them;
//   - no-container-deps: a field holds a container — the composition root's
//     types, the command tree's Deps, a locator, `any`. (The root config
//     joins the list with K3, when the scaffold gives each component a
//     config block of its own: until then the scaffold itself writes
//     `Config *config.Config`, and a lint that flags forge's own output on
//     every fresh service is noise);
//   - deps-no-downcast: a type assertion turns a field back into a concrete
//     type, undoing the capability its interface granted.
//
// and `forge fix narrow-deps` (Narrow) writes the interface the observed
// calls describe.
package depsurface

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Package is one component package's Deps, as used.
type Package struct {
	// Dir is the package directory, project-relative and slash-separated.
	Dir string
	// Path is the import path.
	Path string
	// DepsFile is the project-relative file declaring Deps.
	DepsFile string
	Fields   []Field
	// Downcasts are type assertions from a Deps field to a concrete type.
	Downcasts []Downcast
	// EnvReads are the package's own reads of the process environment
	// (os.Getenv, os.LookupEnv, os.Environ) in non-test files: configuration
	// the composition never handed it, and a root's config reach cannot see.
	EnvReads []EnvRead

	pkg  *packages.Package
	deps *types.Struct
}

// Field is one Deps field.
type Field struct {
	Name string
	File string // project-relative
	Line int
	// Type is the field's type as the consumer would write it.
	Type string
	// Interface is true for an interface-typed field; Methods are its
	// exported methods, sorted.
	Interface bool
	Methods   []string
	// Foreign is true for a named interface declared in another package
	// (the consumer did not declare what it needs).
	Foreign bool
	// Used are the methods the package needs through the field: those it
	// calls, plus every method of each Role.
	Used map[string]bool
	// Roles are the interfaces the field's value is passed on as: a
	// parameter, a struct field (assigned or in a literal) or a conversion
	// whose declared type is an interface with methods. Whatever the
	// receiver does with the value, it does through that interface, so its
	// method set — as the receiver declares it — bounds what the receiver
	// needs, and is in Used.
	Roles []Role
	// Escapes is true when the value also leaves through something that
	// bounds nothing — `any`, a concrete type, a return value, a channel, a
	// map or slice element — so even Used is not the whole story.
	// EscapeAt is where it first does ("file:line").
	Escapes  bool
	EscapeAt string
	// Downcast is true when the package type-asserts the field to a
	// concrete type (Package.Downcasts says where).
	Downcast bool
	// Container names the container the field holds ("" for none).
	Container string
	// RootConfig is true for a field holding the project's root config
	// (pkg/config.Config) rather than the component's own block.
	RootConfig bool

	typ types.Type
}

// Unused are the declared methods the package never calls through the field.
func Unused(f Field) []string {
	var out []string
	for _, m := range f.Methods {
		if !f.Used[m] {
			out = append(out, m)
		}
	}
	return out
}

// Role is one interface a Deps field's value is passed on as.
type Role struct {
	// Type is the interface as the consumer would write it
	// ("threads.Repository"); for an interface literal or a type
	// parameter's constraint, its methods in braces.
	Type string
	// Methods are its exported methods, sorted.
	Methods []string
	// Embeddable is true when the consumer can name the interface (a named
	// interface it may refer to), so a narrowed interface can embed it.
	Embeddable bool

	typ types.Type
}

// EnvRead is one read of the process environment.
type EnvRead struct {
	File string
	Line int
	// Call is the function read through ("os.Getenv").
	Call string
}

// Downcast is one type assertion from a Deps field.
type Downcast struct {
	Field  string
	File   string
	Line   int
	Target string
}

// skipTop are internal/'s subtrees that hold no component: the composition
// root and the generated stores.
var skipTop = map[string]bool{"app": true, "db": true}

// FindComponentDirs lists the project-relative directories under internal/
// whose non-test files declare `type Deps struct`.
func FindComponentDirs(projectDir string) ([]string, error) {
	root := filepath.Join(projectDir, "internal")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		name := d.Name()
		if path != root {
			if name == "testdata" || name == "mocks" || strings.HasPrefix(name, ".") || filepath.Dir(path) == root && skipTop[name] {
				return filepath.SkipDir
			}
		}
		if declaresDeps(path) {
			rel, _ := filepath.Rel(projectDir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func declaresDeps(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				if ts := s.(*ast.TypeSpec); ts.Name.Name == "Deps" {
					if _, ok := ts.Type.(*ast.StructType); ok {
						return true
					}
				}
			}
		}
	}
	return false
}

// Analyze loads the component packages in dirs (project-relative) and reads
// each one's Deps. A package that does not type-check is skipped: the
// compiler reports it better than a lint can.
func Analyze(projectDir string, dirs []string) ([]*Package, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	module, err := modulePath(abs)
	if err != nil {
		return nil, err
	}
	patterns := make([]string, len(dirs))
	for i, d := range dirs {
		patterns[i] = "./" + d
	}
	cfg := &packages.Config{
		// No NeedDeps: dependencies come from export data, so only the
		// component packages themselves are parsed and type-checked.
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports,
		Dir: abs,
		Env: goexec.Env(),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load component packages: %w", err)
	}
	var out []*Package
	for _, p := range pkgs {
		if len(p.Errors) > 0 || p.Types == nil || p.TypesInfo == nil {
			continue
		}
		if r := analyzePackage(abs, module, p); r != nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

func modulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", fmt.Errorf("%s/go.mod declares no module", dir)
}

func analyzePackage(projectDir, module string, p *packages.Package) *Package {
	obj, ok := p.Types.Scope().Lookup("Deps").(*types.TypeName)
	if !ok {
		return nil
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	rel := func(pos token.Pos) (string, int) {
		position := p.Fset.Position(pos)
		r, err := filepath.Rel(projectDir, position.Filename)
		if err != nil {
			r = position.Filename
		}
		return filepath.ToSlash(r), position.Line
	}
	out := &Package{Path: p.PkgPath, pkg: p, deps: st}
	if len(p.GoFiles) > 0 {
		d, _ := filepath.Rel(projectDir, filepath.Dir(p.GoFiles[0]))
		out.Dir = filepath.ToSlash(d)
	}
	out.DepsFile, _ = rel(obj.Pos())
	qual := nameQualifier(p.Types)
	byVar := map[*types.Var]int{}
	for i := 0; i < st.NumFields(); i++ {
		v := st.Field(i)
		if v.Embedded() {
			continue
		}
		f := Field{Name: v.Name(), Type: types.TypeString(v.Type(), qual), Used: map[string]bool{}, typ: v.Type()}
		f.File, f.Line = rel(v.Pos())
		if iface, isIface := v.Type().Underlying().(*types.Interface); isIface {
			f.Interface = true
			for j := 0; j < iface.NumMethods(); j++ {
				if m := iface.Method(j); m.Exported() {
					f.Methods = append(f.Methods, m.Name())
				}
			}
			sort.Strings(f.Methods)
			// Through an alias too: `type Repository = db.Repository` is
			// still db's declaration, not the consumer's.
			if named, ok := types.Unalias(v.Type()).(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg() != p.Types {
				f.Foreign = true
			}
		}
		f.Container = containerKind(module, v.Type())
		f.RootConfig = isRootConfig(module, v.Type())
		byVar[v] = len(out.Fields)
		out.Fields = append(out.Fields, f)
	}
	readUses(p, byVar, out, rel)
	out.EnvReads = envReads(p, rel)
	return out
}

// isRootConfig reports whether t is the project's root config,
// <module>/pkg/config.Config, by value or pointer.
func isRootConfig(module string, t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == module+"/pkg/config" && named.Obj().Name() == "Config"
}

// envFuncs are the os functions that read the process environment.
var envFuncs = map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true}

// envReads lists the package's calls of os.Getenv / LookupEnv / Environ in
// non-test files, resolved by type so an alias of os or a local named os
// is told apart.
func envReads(p *packages.Package, rel func(token.Pos) (string, int)) []EnvRead {
	var out []EnvRead
	for _, f := range p.Syntax {
		if strings.HasSuffix(p.Fset.Position(f.Pos()).Filename, "_test.go") {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "os" || !envFuncs[fn.Name()] {
				return true
			}
			file, line := rel(call.Pos())
			out = append(out, EnvRead{File: file, Line: line, Call: "os." + fn.Name()})
			return true
		})
	}
	return out
}

// containerKind names the container t is, or "".
func containerKind(module string, t types.Type) string {
	if iface, ok := t.Underlying().(*types.Interface); ok && iface.Empty() {
		return "`any` (an untyped bag: the field could be anything, so it says nothing)"
	}
	base := t
	if p, ok := base.(*types.Pointer); ok {
		base = p.Elem()
	}
	if m, ok := base.Underlying().(*types.Map); ok {
		if k, ok := m.Key().Underlying().(*types.Basic); ok && k.Kind() == types.String {
			if iface, ok := m.Elem().Underlying().(*types.Interface); ok && iface.Empty() {
				return "a service locator (map[string]any)"
			}
		}
	}
	named, ok := base.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	path, name := named.Obj().Pkg().Path(), named.Obj().Name()
	switch {
	case path == module+"/internal/app":
		return fmt.Sprintf("the composition root's %s (internal/app)", name)
	case strings.HasPrefix(path, module+"/cmd/") && name == "Deps":
		return "the command tree's Deps (cmd.Deps)"
	}
	return ""
}

// readUses walks the package's non-test files for selections of a Deps
// field: a method called (or taken as a value) through it is used; a type
// assertion on it is a downcast; passing it on as an interface with methods
// is a Role, whose methods are used; anything else the value does — passed
// on as `any` or a concrete type, returned, sent — is an escape. A field
// assigned to a local is followed one step: the local's selections count as
// the field's.
func readUses(p *packages.Package, byVar map[*types.Var]int, out *Package, rel func(token.Pos) (string, int)) {
	r := &useReader{info: p.TypesInfo, byVar: byVar, alias: map[*types.Var]int{}, aliased: map[ast.Expr]bool{}, out: out, rel: rel}
	var files []*ast.File
	for _, f := range p.Syntax {
		if !strings.HasSuffix(p.Fset.Position(f.Pos()).Filename, "_test.go") {
			files = append(files, f)
		}
	}
	for _, f := range files {
		ast.Inspect(f, r.collectAlias)
	}
	for _, f := range files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if e, ok := n.(ast.Expr); ok && len(stack) > 0 {
				if i, ok := r.field(e); ok {
					r.classify(i, e, stack)
				}
			}
			stack = append(stack, n)
			return true
		})
	}
}

type useReader struct {
	info    *types.Info
	byVar   map[*types.Var]int // Deps field → index into out.Fields
	alias   map[*types.Var]int // local variable holding a field → index
	aliased map[ast.Expr]bool  // field selections consumed by an alias
	out     *Package
	rel     func(token.Pos) (string, int)
}

// field reports which Deps field e denotes: a selection of it, or a local
// that holds it.
func (r *useReader) field(e ast.Expr) (int, bool) {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		sel, ok := r.info.Selections[x]
		if !ok || sel.Kind() != types.FieldVal {
			return 0, false
		}
		v, ok := sel.Obj().(*types.Var)
		if !ok {
			return 0, false
		}
		i, ok := r.byVar[v]
		return i, ok
	case *ast.Ident:
		if v, ok := r.info.Uses[x].(*types.Var); ok {
			i, ok := r.alias[v]
			return i, ok
		}
	}
	return 0, false
}

// collectAlias records `x := s.deps.F` (and `var x = s.deps.F`).
func (r *useReader) collectAlias(n ast.Node) bool {
	bind := func(lhs *ast.Ident, rhs ast.Expr) {
		sel, ok := ast.Unparen(rhs).(*ast.SelectorExpr)
		if !ok || lhs == nil {
			return
		}
		i, ok := r.field(sel)
		if !ok {
			return
		}
		obj := r.info.Defs[lhs]
		if obj == nil {
			obj = r.info.Uses[lhs]
		}
		if v, ok := obj.(*types.Var); ok && !v.IsField() {
			r.alias[v] = i
			r.aliased[sel] = true
		}
	}
	switch x := n.(type) {
	case *ast.AssignStmt:
		if len(x.Lhs) == len(x.Rhs) {
			for k := range x.Lhs {
				id, _ := x.Lhs[k].(*ast.Ident)
				bind(id, x.Rhs[k])
			}
		}
	case *ast.ValueSpec:
		if len(x.Names) == len(x.Values) {
			for k := range x.Names {
				bind(x.Names[k], x.Values[k])
			}
		}
	}
	return true
}

// classify records what the expression e (field i) is used for, from its
// parent.
func (r *useReader) classify(i int, e ast.Expr, stack []ast.Node) {
	f := &r.out.Fields[i]
	if r.aliased[e] {
		return
	}
	k := len(stack) - 1
	for k > 0 {
		if _, paren := stack[k].(*ast.ParenExpr); !paren {
			break
		}
		k--
	}
	switch parent := stack[k].(type) {
	case *ast.SelectorExpr:
		if sel, ok := r.info.Selections[parent]; ok && ast.Unparen(parent.X) == e {
			if sel.Kind() == types.MethodVal {
				f.Used[parent.Sel.Name] = true
			}
			return
		}
	case *ast.TypeAssertExpr:
		if ast.Unparen(parent.X) != e {
			break
		}
		before := len(r.out.Downcasts)
		if parent.Type != nil {
			r.downcast(f.Name, parent, r.info.TypeOf(parent.Type))
		} else if k > 0 {
			r.typeSwitch(f.Name, stack[:k])
		}
		f.Downcast = f.Downcast || len(r.out.Downcasts) > before
		return
	case *ast.BinaryExpr:
		if isNil(r.info, parent.X) || isNil(r.info, parent.Y) {
			return
		}
	case *ast.CallExpr:
		if r.role(f, r.argType(parent, e)) {
			return
		}
	case *ast.KeyValueExpr:
		// A keyed struct literal: the key resolves to the field it fills
		// (a map literal's key is no field).
		if id, ok := parent.Key.(*ast.Ident); ok && ast.Unparen(parent.Value) == e {
			if v, ok := r.info.Uses[id].(*types.Var); ok && v.IsField() && r.role(f, v.Type()) {
				return
			}
		}
	case *ast.CompositeLit:
		if r.role(f, r.positionalFieldType(parent, e)) {
			return
		}
	case *ast.AssignStmt:
		// Filling the field (`s.deps.F = v`, a setter) is no use of its
		// value: the value assigned is the one the package goes on using.
		for _, lhs := range parent.Lhs {
			if ast.Unparen(lhs) == e {
				return
			}
		}
		if len(parent.Lhs) == len(parent.Rhs) {
			for idx, rhs := range parent.Rhs {
				if ast.Unparen(rhs) == e && r.role(f, r.info.TypeOf(parent.Lhs[idx])) {
					return
				}
			}
		}
	}
	if !f.Escapes {
		file, line := r.rel(e.Pos())
		f.EscapeAt = fmt.Sprintf("%s:%d", file, line)
	}
	f.Escapes = true
}

// role records dest as one of f's Roles, and its methods as used, when dest
// is an interface with methods: the receiver can do nothing with the value
// but call them. It reports false for anything that bounds nothing — no
// type, `any`, a concrete type, a constraint with type terms.
func (r *useReader) role(f *Field, dest types.Type) bool {
	if dest == nil {
		return false
	}
	// A type parameter's underlying type is its constraint.
	iface, ok := dest.Underlying().(*types.Interface)
	if !ok || !iface.IsMethodSet() || iface.NumMethods() == 0 {
		return false
	}
	var methods []string
	for j := 0; j < iface.NumMethods(); j++ {
		if m := iface.Method(j); m.Exported() {
			methods = append(methods, m.Name())
		}
	}
	sort.Strings(methods)
	own := r.out.pkg.Types
	role := Role{Methods: methods, typ: dest}
	_, isParam := types.Unalias(dest).(*types.TypeParam)
	if named, ok := types.Unalias(dest).(*types.Named); ok && !isParam {
		obj := named.Obj()
		// Store[T] in a generic callee's signature names the callee's T,
		// which the consumer cannot spell.
		role.Embeddable = (obj.Pkg() == own || obj.Exported()) && !mentionsTypeParam(named)
		role.Type = types.TypeString(dest, nameQualifier(own))
	}
	if role.Type == "" {
		role.Type = "interface{ " + strings.Join(methods, "; ") + " }"
	}
	for _, m := range methods {
		f.Used[m] = true
	}
	for _, have := range f.Roles {
		if have.Type == role.Type {
			return true
		}
	}
	f.Roles = append(f.Roles, role)
	return true
}

// mentionsTypeParam reports whether t's type arguments name a type
// parameter, anywhere a narrowed interface would have to repeat it. A
// composite type argument it does not walk counts as one, so the role is
// written out method by method rather than embedded wrongly.
func mentionsTypeParam(t types.Type) bool {
	switch x := types.Unalias(t).(type) {
	case *types.TypeParam:
		return true
	case *types.Named:
		args := x.TypeArgs()
		for i := 0; i < args.Len(); i++ {
			if mentionsTypeParam(args.At(i)) {
				return true
			}
		}
		return false
	case *types.Basic:
		return false
	case *types.Pointer:
		return mentionsTypeParam(x.Elem())
	case *types.Slice:
		return mentionsTypeParam(x.Elem())
	case *types.Array:
		return mentionsTypeParam(x.Elem())
	case *types.Map:
		return mentionsTypeParam(x.Key()) || mentionsTypeParam(x.Elem())
	case *types.Chan:
		return mentionsTypeParam(x.Elem())
	}
	return true
}

// argType is the type the callee declares for e, an argument of call — the
// parameter's type in the callee's own (uninstantiated) signature, so a
// type parameter answers with its constraint — or the target type of a
// conversion. It is nil for a builtin, and for an argument spread into a
// variadic parameter (`f(xs...)`).
func (r *useReader) argType(call *ast.CallExpr, e ast.Expr) types.Type {
	if tv, ok := r.info.Types[call.Fun]; ok && tv.IsType() {
		return tv.Type
	}
	idx := -1
	for i, a := range call.Args {
		if ast.Unparen(a) == e {
			idx = i
		}
	}
	sig := r.declaredSignature(call.Fun)
	if idx < 0 || sig == nil {
		return nil
	}
	params := sig.Params()
	if sig.Variadic() && idx >= params.Len()-1 {
		if call.Ellipsis.IsValid() {
			return nil
		}
		if s, ok := params.At(params.Len() - 1).Type().Underlying().(*types.Slice); ok {
			return s.Elem()
		}
		return nil
	}
	if idx >= params.Len() {
		return nil
	}
	return params.At(idx).Type()
}

// declaredSignature is the signature fun is declared with: a named
// function's or method's origin (its type parameters intact), or else the
// signature of fun's type. It is nil for a builtin.
func (r *useReader) declaredSignature(fun ast.Expr) *types.Signature {
	fun = ast.Unparen(fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = ast.Unparen(x.X)
	case *ast.IndexListExpr:
		fun = ast.Unparen(x.X)
	}
	var id *ast.Ident
	switch x := fun.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		// A method expression (T.M) takes the receiver first, which the
		// method's own signature does not list.
		if sel, ok := r.info.Selections[x]; !ok || sel.Kind() != types.MethodExpr {
			id = x.Sel
		}
	}
	if id != nil {
		switch obj := r.info.Uses[id].(type) {
		case *types.Builtin:
			return nil
		case *types.Func:
			sig, _ := obj.Origin().Type().(*types.Signature)
			return sig
		}
	}
	t := r.info.TypeOf(fun)
	if t == nil {
		return nil
	}
	sig, _ := t.Underlying().(*types.Signature)
	return sig
}

// positionalFieldType is the type of the struct field e fills in an
// unkeyed struct literal, or nil.
func (r *useReader) positionalFieldType(lit *ast.CompositeLit, e ast.Expr) types.Type {
	t := r.info.TypeOf(lit)
	if t == nil {
		return nil
	}
	u := t.Underlying()
	if p, ok := u.(*types.Pointer); ok {
		u = p.Elem().Underlying()
	}
	st, ok := u.(*types.Struct)
	if !ok {
		return nil
	}
	for i, el := range lit.Elts {
		if ast.Unparen(el) == e && i < st.NumFields() {
			return st.Field(i).Type()
		}
	}
	return nil
}

func (r *useReader) downcast(field string, at ast.Node, t types.Type) {
	if t == nil || types.IsInterface(t) {
		return
	}
	file, line := r.rel(at.Pos())
	r.out.Downcasts = append(r.out.Downcasts, Downcast{Field: field, File: file, Line: line, Target: types.TypeString(t, nameQualifier(r.out.pkg.Types))})
}

// typeSwitch reports the concrete cases of the type switch enclosing a field
// assertion (`switch x := s.deps.F.(type)`).
func (r *useReader) typeSwitch(field string, stack []ast.Node) {
	for k := len(stack) - 1; k >= 0; k-- {
		ts, ok := stack[k].(*ast.TypeSwitchStmt)
		if !ok {
			continue
		}
		for _, stmt := range ts.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				if t := r.info.TypeOf(e); t != nil && !isNil(r.info, e) {
					r.downcast(field, e, t)
				}
			}
		}
		return
	}
}

// nameQualifier writes a type as the consumer package's source would: its
// own types bare, every other package's by package name.
func nameQualifier(own *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == own {
			return ""
		}
		return p.Name()
	}
}

func isNil(info *types.Info, e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	_, isNil := info.Uses[id].(*types.Nil)
	return isNil
}
