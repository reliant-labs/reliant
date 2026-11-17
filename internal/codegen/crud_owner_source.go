// File: internal/codegen/crud_owner_source.go
//
// Which function an owner-scoping wrapper reads its owner from.
//
// # The defect
//
// handlers_crud.go is born with every scoped wrapper reading its owner off
// a placeholder, `owner := claims.UserID` under a FORGE_SCAFFOLD marker,
// and the author replaces it with the app's real mapping. growth replaced
// all of them with `owner, err := tenancy.TenantID(ctx)`. Later it deleted
// two hand-written List handlers, and `forge generate` appended scoped
// wrappers for them — with the placeholder, the one principal no other
// wrapper in the file used. A user id is never a tenant id, so the two
// Lists compiled, ran and scoped every query to rows that could not exist.
// growth's tests caught it; nothing in forge did.
//
// The file already said what the answer was: every sibling scoping the
// same column called the same resolver. An appended shim now reads that
// answer off the file (ownerSourceFor) instead of re-emitting the birth
// placeholder, and `forge lint` flags a wrapper whose owner source
// disagrees with its siblings' (forgeconv-crud-owner-resolver).
//
// # What counts as a source
//
// The scan is syntactic and reads only RPC handlers — `*Service` methods
// shaped `(ctx context.Context, req *connect.Request[T])` — so a helper
// that receives the owner as a parameter is not mistaken for a policy. In
// each, an owner USE is `orm.WhereEq("<col>", x)` or `e.<Field> = x`, and
// x is traced back to the statement that bound it:
//
//   - `x, err := pkg.F(ctx)` is a resolver call (tenancy.TenantID);
//   - `x := c.Field`, where `c, err := pkg.F(ctx)`, reads a claims field
//     (the birth placeholder is middleware.GetUser's UserID);
//   - anything else (a request field, a computed value) is unknown, and
//     an unknown sibling means forge cannot say what the policy is.
package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/naming"
)

// CRUDOwnerSourceKind classifies where a scoped wrapper's owner value
// comes from.
type CRUDOwnerSourceKind int

const (
	// CRUDOwnerFromResolver is `owner, err := pkg.F(ctx)`.
	CRUDOwnerFromResolver CRUDOwnerSourceKind = iota
	// CRUDOwnerFromClaims is `claims, err := pkg.F(ctx)` then
	// `owner := claims.Field`.
	CRUDOwnerFromClaims
	// CRUDOwnerUnknown is any other binding.
	CRUDOwnerUnknown
)

// CRUDOwnerSource is one way a wrapper obtains its owner value.
type CRUDOwnerSource struct {
	Kind CRUDOwnerSourceKind
	// Func is the called function as the file spells it ("tenancy.TenantID").
	Func string
	// Import is Func's package as an import spec ("path" or "alias path"),
	// "" when the file does not import it (a dot import, a local func).
	Import string
	// Field is the claims field read, for CRUDOwnerFromClaims.
	Field string
	// Expr is the binding's source text, for CRUDOwnerUnknown.
	Expr string
}

// String renders the source as a reader would recognise it in code.
func (s CRUDOwnerSource) String() string {
	switch s.Kind {
	case CRUDOwnerFromResolver:
		return s.Func + "(ctx)"
	case CRUDOwnerFromClaims:
		return s.Func + "(ctx)." + s.Field
	default:
		return s.Expr
	}
}

// identity is the function the source calls, independent of how one file
// spells it: the import PATH (never the alias, which each file chooses)
// plus the selected name and claims field. String is only a spelling — two
// handler files can bind `tenancy` to different packages — so evidence
// gathered across files is compared by identity. A source whose package
// the file does not import (a local value, a dot import) is bound in its
// own package, so its identity carries the directory it was read in.
func (s CRUDOwnerSource) identity(dir string) string {
	if s.Kind == CRUDOwnerUnknown {
		return "unknown\x00" + dir + "\x00" + s.Expr
	}
	pkg := "local:" + dir
	if s.Import != "" {
		f := strings.Fields(s.Import)
		pkg = "import:" + f[len(f)-1]
	}
	name := s.Func
	if i := strings.Index(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strconv.Itoa(int(s.Kind)) + "\x00" + pkg + "\x00" + name + "\x00" + s.Field
}

// isPlaceholder reports the birth scaffold's own owner expression: the
// auth seam's claims, read at UserID. It is forge's guess, not the app's
// decision, so it is never evidence of what the policy is.
func (s CRUDOwnerSource) isPlaceholder() bool {
	return s.Kind == CRUDOwnerFromClaims && s.Func == crudAuthSeam && s.Field == "UserID"
}

// IsPlaceholder is isPlaceholder for the linter.
func (s CRUDOwnerSource) IsPlaceholder() bool { return s.isPlaceholder() }

// CRUDOwnerUse is one RPC handler's owner source for one owner column.
type CRUDOwnerUse struct {
	// Method is the RPC handler's name.
	Method string
	// Entity is the entity the handler's name is about ("ListTenants" →
	// "Tenant"), "" for a verb forge does not recognise. Two tables can
	// share an owner column NAME and not its meaning — tenants.id is a
	// tenant, users.id a user — so evidence is weighed per entity first.
	Entity string
	// Read reports a read RPC (Get, List, Search, Count, Watch): reads and
	// writes may legitimately differ (a write that needs an admin), so the
	// two are compared within their own class first.
	Read bool
	// Column is the column a predicate names (`orm.WhereEq("tenant_id", …)`),
	// Field the entity field a stamp assigns (`e.TenantID = …`); one is set.
	Column string
	Field  string
	Source CRUDOwnerSource
	// Line is the handler's declaration line.
	Line int
	// Dir is the project-relative directory of the file the use was read
	// in, set for evidence gathered across files ("" within one file).
	Dir string
}

// matches reports whether the use scopes the owner column column, whose
// db.<Entity> field is field.
func (u CRUDOwnerUse) matches(column, field string) bool {
	return (u.Column != "" && u.Column == column) || (u.Field != "" && u.Field == field)
}

// ScanCRUDOwnerSources returns every owner use in one parsed Go file's RPC
// handlers, in declaration order. A handler that scopes the same column in
// more than one place (Fetch and Persist) reports each distinct source
// once.
func ScanCRUDOwnerSources(fset *token.FileSet, file *ast.File) []CRUDOwnerUse {
	imports := fileImportSpecs(file)
	entities := rpcEntities(file)
	var uses []CRUDOwnerUse
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ctxName, ok := rpcHandlerCtxParam(fn)
		if !ok {
			continue
		}
		binds := bindingsOf(fn.Body)
		seen := map[string]bool{}
		add := func(column, field string, ident *ast.Ident) {
			src := traceOwnerSource(ident, binds, ctxName, imports)
			key := column + "\x00" + field + "\x00" + src.String()
			if seen[key] {
				return
			}
			seen[key] = true
			uses = append(uses, CRUDOwnerUse{
				Method: fn.Name.Name,
				Entity: entities[fn.Name.Name],
				Read:   isReadRPCName(fn.Name.Name),
				Column: column,
				Field:  field,
				Source: src,
				Line:   fset.Position(fn.Pos()).Line,
			})
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if !isSelectorCall(n.Fun, "orm", "WhereEq") || len(n.Args) != 2 {
					return true
				}
				lit, ok := n.Args[0].(*ast.BasicLit)
				id, idOK := n.Args[1].(*ast.Ident)
				if !ok || lit.Kind != token.STRING || !idOK {
					return true
				}
				if col, err := strconv.Unquote(lit.Value); err == nil {
					add(col, "", id)
				}
			case *ast.AssignStmt:
				if n.Tok != token.ASSIGN || len(n.Lhs) != 1 || len(n.Rhs) != 1 {
					return true
				}
				sel, ok := n.Lhs[0].(*ast.SelectorExpr)
				id, idOK := n.Rhs[0].(*ast.Ident)
				if ok && idOK && binds[id.Name] != nil {
					add("", sel.Sel.Name, id)
				}
			}
			return true
		})
	}
	return uses
}

// ScanCRUDOpKeyColumns maps each RPC whose op a parsed
// handlers_crud_ops_gen.go wires to its entity's primary-key column, read
// off the generated op (`crud.ScopedGet(ctx, "<pk>", …)`, `PkColumnName:
// "<pk>"`): the schema's own key, which the handler file alone cannot
// tell apart from an owner column of the same name. A Create's op names no
// key, so it has no entry.
func ScanCRUDOpKeyColumns(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if !strings.HasPrefix(name, "crud") || !strings.HasSuffix(name, "Op") || len(name) <= len("crudOp") {
			continue
		}
		method := name[len("crud") : len(name)-len("Op")]
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			var lit ast.Expr
			switch n := n.(type) {
			case *ast.CallExpr:
				for _, f := range []string{"ScopedGet", "ScopedUpdate", "ScopedUpdateMasked", "ScopedDelete"} {
					if isSelectorCall(n.Fun, "crud", f) && len(n.Args) > 1 {
						lit = n.Args[1]
					}
				}
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && k.Name == "PkColumnName" {
					lit = n.Value
				}
			}
			if bl, ok := lit.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if col, err := strconv.Unquote(bl.Value); err == nil && col != "" {
					out[method] = col
					return false
				}
			}
			return true
		})
	}
	return out
}

// rpcHandlerCtxParam reports whether fn is a connect RPC handler — a method
// taking (ctx context.Context, req *connect.Request[T]) — and returns its
// context parameter's name.
func rpcHandlerCtxParam(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 2 {
		return "", false
	}
	ctxField, reqField := fn.Type.Params.List[0], fn.Type.Params.List[1]
	if len(ctxField.Names) != 1 || !isSelectorExpr(ctxField.Type, "context", "Context") {
		return "", false
	}
	star, ok := reqField.Type.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	idx, ok := star.X.(*ast.IndexExpr)
	if !ok || !isSelectorExpr(idx.X, "connect", "Request") {
		return "", false
	}
	return ctxField.Names[0].Name, true
}

// readVerbs and writeVerbs are the RPC verbs whose noun names an entity.
var (
	readVerbs  = []string{"Get", "List", "Search", "Count", "Watch"}
	writeVerbs = []string{"Create", "Update", "Delete"}
)

// isReadRPCName reports a read RPC by its verb.
func isReadRPCName(name string) bool {
	_, read, ok := splitRPCVerb(name)
	return ok && read
}

// splitRPCVerb splits "ListTenants" into ("Tenants", read=true).
func splitRPCVerb(name string) (noun string, read, ok bool) {
	for _, v := range readVerbs {
		if n := strings.TrimPrefix(name, v); n != name && n != "" && n[0] >= 'A' && n[0] <= 'Z' {
			return n, true, true
		}
	}
	for _, v := range writeVerbs {
		if n := strings.TrimPrefix(name, v); n != name && n != "" && n[0] >= 'A' && n[0] <= 'Z' {
			return n, false, true
		}
	}
	return "", false, false
}

// rpcEntities maps each RPC handler in file to its entity. A List's noun is
// plural; it resolves to the sibling noun that pluralizes to it (the way
// forge named the List from the entity), else to its singular.
func rpcEntities(file *ast.File) map[string]string {
	nouns := map[string]string{}
	singular := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := rpcHandlerCtxParam(fn); !ok {
			continue
		}
		noun, _, ok := splitRPCVerb(fn.Name.Name)
		if !ok {
			continue
		}
		nouns[fn.Name.Name] = noun
		if !strings.HasPrefix(fn.Name.Name, "List") {
			singular[noun] = true
		}
	}
	out := map[string]string{}
	for method, noun := range nouns {
		switch {
		case !strings.HasPrefix(method, "List") || singular[noun]:
			out[method] = noun
		default:
			out[method] = inflection.Singular(noun)
			for m := range singular {
				if naming.Pluralize(m) == noun {
					out[method] = m
					break
				}
			}
		}
	}
	return out
}

// binding is the statement that gave a local identifier its value: the
// expression assigned to it, and how many names that statement bound.
type binding struct {
	expr  ast.Expr
	arity int
}

// bindingsOf maps each identifier the function body's TOP-LEVEL statements
// bind to its binding. The owner is resolved before anything else in a
// wrapper, so nested scopes (where a shadowed `err` lives) are not read; a
// name bound twice keeps its first binding.
func bindingsOf(body *ast.BlockStmt) map[string]*binding {
	out := map[string]*binding{}
	bind := func(name string, b *binding) {
		if name == "_" || out[name] != nil {
			return
		}
		out[name] = b
	}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE {
				continue
			}
			switch {
			case len(s.Rhs) == 1 && len(s.Lhs) > 1:
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					bind(id.Name, &binding{expr: s.Rhs[0], arity: len(s.Lhs)})
				}
			case len(s.Rhs) == len(s.Lhs):
				for i, l := range s.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						bind(id.Name, &binding{expr: s.Rhs[i], arity: 1})
					}
				}
			}
		case *ast.DeclStmt:
			gd, ok := s.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				switch {
				case len(vs.Values) == 1 && len(vs.Names) > 1:
					bind(vs.Names[0].Name, &binding{expr: vs.Values[0], arity: len(vs.Names)})
				case len(vs.Values) == len(vs.Names):
					for i, n := range vs.Names {
						bind(n.Name, &binding{expr: vs.Values[i], arity: 1})
					}
				}
			}
		}
	}
	return out
}

// traceOwnerSource classifies how id got its value.
func traceOwnerSource(id *ast.Ident, binds map[string]*binding, ctxName string, imports map[string]string) CRUDOwnerSource {
	b := binds[id.Name]
	// Follow plain renames (`owner := tenantID`) a few hops.
	for hops := 0; b != nil && hops < 3; hops++ {
		next, ok := b.expr.(*ast.Ident)
		if !ok || binds[next.Name] == nil {
			break
		}
		b = binds[next.Name]
	}
	if b == nil {
		return CRUDOwnerSource{Kind: CRUDOwnerUnknown, Expr: id.Name}
	}
	if fn, ok := ctxResolverCall(b.expr, ctxName); ok && b.arity == 2 {
		return CRUDOwnerSource{Kind: CRUDOwnerFromResolver, Func: fn, Import: imports[strings.SplitN(fn, ".", 2)[0]]}
	}
	if sel, ok := b.expr.(*ast.SelectorExpr); ok {
		if recv, ok := sel.X.(*ast.Ident); ok {
			if cb := binds[recv.Name]; cb != nil && cb.arity == 2 {
				if fn, ok := ctxResolverCall(cb.expr, ctxName); ok {
					return CRUDOwnerSource{Kind: CRUDOwnerFromClaims, Func: fn, Field: sel.Sel.Name, Import: imports[strings.SplitN(fn, ".", 2)[0]]}
				}
			}
		}
	}
	return CRUDOwnerSource{Kind: CRUDOwnerUnknown, Expr: types.ExprString(b.expr)}
}

// ctxResolverCall matches `pkg.F(ctx)` — a package-qualified call whose
// only argument is the handler's context — and returns "pkg.F".
func ctxResolverCall(e ast.Expr, ctxName string) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	if !ok || arg.Name != ctxName {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return pkg.Name + "." + sel.Sel.Name, true
}

func isSelectorExpr(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func isSelectorCall(fun ast.Expr, pkg, name string) bool { return isSelectorExpr(fun, pkg, name) }

// fileImportSpecs maps the identifier each import binds to its import spec
// in ensureImportLine's form ("path", or "alias path" for a renamed one).
// An unrenamed import is assumed to bind its path's last element, minus a
// /vN major-version suffix.
func fileImportSpecs(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				out[imp.Name.Name] = imp.Name.Name + " " + p
			}
			continue
		}
		base := path.Base(p)
		if len(base) > 1 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
			base = path.Base(path.Dir(p))
		}
		out[base] = p
	}
	return out
}

// resolveAppendedOwners sets each scoped method's owner source before it is
// appended to the existing handlers_crud.go at shimRel (bytes existing).
//
// The file's own wrappers are the evidence first: they are the methods the
// new one sits beside, and the column's policy is whatever they already do.
// A column no wrapper in the file scopes falls back to the project's other
// handlers_crud.go files. No evidence anywhere keeps the birth placeholder.
// Every decision is printed, because a wrong one is an access-control bug
// and the author should read which principal forge picked.
//
// A file's birth never comes here: it has no siblings, and its scaffolded
// lifecycle test is written for the placeholder's owner.
func resolveAppendedOwners(projectDir, shimRel string, existing []byte, missing []CRUDMethodTemplateData) []CRUDMethodTemplateData {
	var fileUses []CRUDOwnerUse
	fset := token.NewFileSet()
	if f, err := parser.ParseFile(fset, shimRel, existing, parser.SkipObjectResolution); err == nil {
		fileUses = ScanCRUDOwnerSources(fset, f)
	}
	var projectUses []CRUDOwnerUse
	projectScanned := false

	out := make([]CRUDMethodTemplateData, len(missing))
	for i, m := range missing {
		out[i] = m
		if !m.Scoped || m.ShapeMismatch {
			continue
		}
		read := m.Operation == "get" || m.Operation == "list"
		entity := m.EntityPbType // method names spell the entity in pb casing
		if entity == "" {
			entity = m.EntityName
		}
		target := ownerTarget{entity: entity, column: m.OwnerColumn, field: m.OwnerField, read: read, isPK: m.OwnerIsPK}
		d, ok := ownerSourceFor(fileUses, target)
		where := "this file's"
		if !ok {
			if !projectScanned {
				projectUses = projectCRUDOwnerUses(projectDir, shimRel)
				projectScanned = true
			}
			d, ok = ownerSourceFor(projectUses, target)
			where = "the project's"
		}
		if !ok {
			continue
		}
		switch {
		case d.Undetermined != "":
			out[i].OwnerUndetermined = d.Undetermined
			fmt.Printf("  ⚠️  %s: scoped to %s, but %s — appended it to %s calling %s, which does not compile: choose its owner resolver there\n",
				m.MethodName, m.OwnerColumn, d.Undetermined, shimRel, out[i].OwnerTODOIdent())
		case d.Source != nil && d.Source.Kind == CRUDOwnerFromResolver:
			out[i].OwnerResolver = d.Source.Func
			out[i].OwnerImport = d.Source.Import
			fmt.Printf("  🔒 %s: scoped to %s via %s, the resolver %s other %s wrappers call\n",
				m.MethodName, m.OwnerColumn, d.Source, where, m.OwnerColumn)
		case d.Source != nil && d.Source.Kind == CRUDOwnerFromClaims:
			out[i].AuthSeam = d.Source.Func
			out[i].OwnerClaimsField = d.Source.Field
			out[i].OwnerImport = d.Source.Import
			fmt.Printf("  🔒 %s: scoped to %s via %s, the mapping %s other %s wrappers use\n",
				m.MethodName, m.OwnerColumn, d.Source, where, m.OwnerColumn)
		}
	}
	return out
}

// projectCRUDOwnerUses scans every handlers_crud.go under the project's
// internal/handlers except skip (project-relative) for owner uses.
func projectCRUDOwnerUses(projectDir, skip string) []CRUDOwnerUse {
	var uses []CRUDOwnerUse
	root := filepath.Join(projectDir, "internal", "handlers")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is no evidence
		}
		if d.IsDir() || d.Name() != "handlers_crud.go" {
			return nil
		}
		if rel, rerr := filepath.Rel(projectDir, p); rerr == nil && filepath.Clean(rel) == filepath.Clean(skip) {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil //nolint:nilerr // a file that does not parse is no evidence
		}
		dir := filepath.Dir(p)
		if rel, rerr := filepath.Rel(projectDir, dir); rerr == nil {
			dir = filepath.ToSlash(rel)
		}
		for _, u := range ScanCRUDOwnerSources(fset, f) {
			u.Dir = dir
			uses = append(uses, u)
		}
		return nil
	})
	return uses
}

// crudOwnerDecision is what an appended scoped shim reads its owner from.
type crudOwnerDecision struct {
	// Source is the sibling source to reuse; nil keeps the birth
	// placeholder (no evidence, or only placeholders).
	Source *CRUDOwnerSource
	// Undetermined is set when the evidence does not name one source; it
	// says why, and the shim is rendered so it does not compile.
	Undetermined string
}

// ownerTarget is the new scoped handler an owner source is decided for.
type ownerTarget struct {
	entity, column, field string
	read                  bool
	// isPK reports an owner column that is the table's key: its name
	// ("id") then means a different principal on every table, so only the
	// entity's own wrappers are evidence.
	isPK bool
}

// ownerSourceFor decides the owner source for target from uses, the owner
// uses of the handlers that already exist. ok is false when no use is
// evidence for it at all.
//
// The entity's own wrappers on the column are the evidence when there are
// any; otherwise other entities' wrappers on a column of the same name
// (never for a key-owner column). Within that evidence:
//
//   - An unknown sibling source makes the policy unreadable: undetermined.
//   - Placeholders are forge's guess, not evidence; they are set aside.
//   - The new handler's own class (read or write) is consulted first, then
//     every sibling: exactly one distinct source is reused, more than one
//     is undetermined (forge will not pick a principal for an access check).
//   - Only placeholders: keep the placeholder, as the siblings do.
//
// Evidence borrowed from other entities is held to more: every one of
// their wrappers, of either class, must agree on one decided source. A
// placeholder beside a resolver, or reads that agree while writes do not,
// is undetermined — the column may not mean the same principal on every
// table, and only an unanimous project says it does.
func ownerSourceFor(uses []CRUDOwnerUse, target ownerTarget) (crudOwnerDecision, bool) {
	var sameEntity, sameColumn []CRUDOwnerUse
	for _, u := range uses {
		if u.Entity == "" || !u.matches(target.column, target.field) {
			continue
		}
		if u.Entity == target.entity {
			sameEntity = append(sameEntity, u)
		} else if !target.isPK {
			sameColumn = append(sameColumn, u)
		}
	}
	relevant := sameEntity
	borrowed := false
	if len(relevant) == 0 {
		relevant = sameColumn
		borrowed = true
	}
	if len(relevant) == 0 {
		return crudOwnerDecision{}, false
	}
	column := target.column
	var decided []CRUDOwnerUse
	placeholders := 0
	for _, u := range relevant {
		if u.Source.Kind == CRUDOwnerUnknown {
			return crudOwnerDecision{Undetermined: "the " + column + " wrapper " + u.Method +
				" reads its owner from `" + u.Source.Expr + "`, which forge cannot trace to a resolver"}, true
		}
		if u.Source.isPlaceholder() {
			placeholders++
		} else {
			decided = append(decided, u)
		}
	}
	if len(decided) == 0 {
		return crudOwnerDecision{}, true
	}
	if borrowed && placeholders > 0 {
		// Another entity still on the placeholder may have kept it on
		// purpose (its column means the user); beside one that resolved
		// it, the column's meaning across tables is not settled.
		return crudOwnerDecision{Undetermined: "other entities' " + column +
			" wrappers disagree on its owner: some keep forge's placeholder " +
			crudAuthSeam + "(ctx).UserID, some resolve it"}, true
	}
	pool := decided
	var sameClass []CRUDOwnerUse
	for _, u := range decided {
		if u.Read == target.read {
			sameClass = append(sameClass, u)
		}
	}
	// Class affinity is a reading of ONE entity's policy (its writes may
	// need a stricter principal than its reads). Borrowed from other
	// entities, every class must agree, or the read-only subset could
	// hide a table whose wrappers use another principal.
	if len(sameClass) > 0 && !borrowed {
		pool = sameClass
	}
	// Sources are told apart by identity, not spelling: another file's
	// `tenancy.TenantID(ctx)` may bind `tenancy` to another package.
	distinct := map[string]CRUDOwnerUse{}
	for _, u := range pool {
		distinct[u.Source.identity(u.Dir)] = u
	}
	if len(distinct) == 1 {
		for _, u := range distinct {
			s := u.Source
			if u.Dir != "" && s.Import == "" {
				// Bound in another file's package (a local value, a dot
				// import): the target file cannot name the same function.
				return crudOwnerDecision{Undetermined: "other entities' " + column +
					" wrappers read their owner from " + s.String() + " in " + u.Dir +
					", which is not an imported package's function, so this file cannot call it"}, true
			}
			return crudOwnerDecision{Source: &s}, true
		}
	}
	// Name each source by its spelling, adding its package where two
	// files spell different functions alike.
	spellings := map[string]int{}
	for _, u := range distinct {
		spellings[u.Source.String()]++
	}
	names := make([]string, 0, len(distinct))
	for _, u := range distinct {
		n := u.Source.String()
		if spellings[n] > 1 {
			pkg := "local to " + u.Dir
			if f := strings.Fields(u.Source.Import); len(f) > 0 {
				pkg = f[len(f)-1]
			}
			n += " (" + pkg + ")"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return crudOwnerDecision{Undetermined: "the " + column + " wrappers disagree on its owner: " +
		strings.Join(names, ", ")}, true
}
