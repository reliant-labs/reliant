// File: internal/cli/audit/audit_unscoped_auth_owned_op.go
//
// The table-level owner declaration (`COMMENT ON TABLE t IS
// 'forge:owner=<spec>'`, schemadef.TableMarkerOwner) as `unscoped_auth`
// reads it.
//
// It does two things here, and both follow from it being ENFORCED where the
// column form is only declared:
//
//   - It arms the gate exactly as a `forge:owner` column does: an
//     authenticated rpc over the table whose handler resolves no caller is an
//     error, and hand-written SQL over it is classified like SQL over a
//     column-owned table.
//   - It is an answer to the gate. The generated op of an owned table scopes
//     every read and write through `s.deps.Owners` (forge/pkg/crud's Owned*
//     helpers), and resolving the owners IS resolving the caller. So a
//     handler that reaches its own `s.crud<Method>Op()` — directly or through
//     package helpers, the same fixed point the seam check uses — counts as
//     reading the caller, provided that op really goes through
//     `s.deps.Owners`. That proviso is what keeps the answer falsifiable: a
//     declaration added after the last `forge generate` leaves an op that
//     scopes nothing, and the rpc still gates until generate rewrites it.

package audit

import (
	"fmt"
	"go/ast"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// commentOnTableRE matches `COMMENT ON TABLE <t> IS '...'`, capturing the
// table and the comment body.
var commentOnTableRE = regexp.MustCompile(`(?is)\bcomment\s+on\s+table\s+(` + sqlident.Qualified + `)\s+is\s+'((?:[^']|'')*)'`)

// ownerTableSpecs maps each table whose comment declares `forge:owner=` to
// the declaration, read from the project's migrations in order.
func ownerTableSpecs(migrations []string) map[string]string {
	out := map[string]string{}
	for _, sql := range migrations {
		ownerTableSpecsIn(sql, out)
	}
	return out
}

// ownerTableSpecsIn applies one migration's COMMENT ON TABLE statements to
// specs. COMMENT ON replaces the whole comment, so a later comment that does
// not carry the marker withdraws it. A declaration that does not parse still
// arms the gate (generate refuses it with the precise error); its value is
// then the raw comment.
func ownerTableSpecsIn(sql string, specs map[string]string) {
	for _, m := range commentOnTableRE.FindAllStringSubmatch(sql, -1) {
		table := sqlident.Last(m[1])
		t := schemadef.Table{Name: table, Comment: strings.ReplaceAll(m[2], "''", "'")}
		terms, declared, err := t.OwnerSpec()
		switch {
		case !declared:
			delete(specs, table)
		case err != nil:
			specs[table] = t.Comment
		default:
			specs[table] = renderOwnerSpec(terms)
		}
	}
}

// renderOwnerSpec spells parsed terms the way the marker takes them.
func renderOwnerSpec(terms []schemadef.OwnerTerm) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		parts = append(parts, t.Kind+":"+strings.Join(t.Path, ">"))
	}
	return schemadef.TableMarkerOwner + "=" + strings.Join(parts, ",")
}

// sqlOwnerColumns is the table → owner column map the raw-SQL check reads:
// every column-form declaration, plus each table-form one by the first
// column of its first owner (the column a scoped query must name — the
// owner itself, or the foreign key that leads to it).
func sqlOwnerColumns(columns, tables map[string]string) map[string]string {
	out := make(map[string]string, len(columns)+len(tables))
	for t, c := range columns {
		out[t] = c
	}
	for t, spec := range tables {
		if _, ok := out[t]; ok {
			continue
		}
		terms, err := schemadef.ParseOwnerSpec(strings.TrimPrefix(spec, schemadef.TableMarkerOwner+"="))
		if err != nil || len(terms) == 0 || len(terms[0].Path) == 0 {
			continue
		}
		out[t] = terms[0].Path[0]
	}
	return out
}

// crudOpName is the generated op method a CRUD handler named method
// delegates to (handlers_crud_ops_gen.go.tmpl: crud{{.MethodName}}Op).
func crudOpName(method string) string { return "crud" + method + "Op" }

// crudOpCallRE recognises a call to a generated op method.
var crudOpCallRE = regexp.MustCompile(`^crud[A-Z]\w*Op$`)

// ownedOpDelegations reports, per declaration name in one handler package,
// whether the handler reaches its OWN generated op and that op resolves the
// caller through `s.deps.Owners` — both at any depth inside the package.
func ownedOpDelegations(decls []*ast.FuncDecl) func(handler string) bool {
	direct := make([][]string, len(decls))
	for i, fn := range decls {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch f := call.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			if crudOpCallRE.MatchString(name) {
				direct[i] = append(direct[i], name)
			}
			return true
		})
	}
	// reachableSQL is a name-keyed closure over the package's call graph;
	// here it carries op names instead of SQL text.
	reached := reachableSQL(decls, direct)
	resolving := seamReachingNames(decls, func(fn *ast.FuncDecl) bool { return bodyReadsOwnerResolver(fn.Body) })
	return func(handler string) bool {
		op := crudOpName(handler)
		return resolving[op] && containsString(reached[handler], op)
	}
}

// bodyReadsOwnerResolver reports whether a body names `<recv>.deps.Owners`,
// the crud.OwnerResolver dep the generated ops of an owned table resolve the
// caller through.
func bodyReadsOwnerResolver(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Owners" {
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "deps" {
				found = true
			}
		}
		return !found
	})
	return found
}

// ownedOpRemediation is the remediation for an rpc over a table that declares
// `forge:owner=`: the generated op already scopes by it, so the fix is to
// delegate to it (the scaffolded shim's own body). Empty for an rpc with no
// generated op (not CRUD).
func ownedOpRemediation(method codegen.Method, table, spec string) string {
	op, _ := codegen.CRUDShape(method)
	if op == "" {
		return ""
	}
	name := method.GoName()
	return fmt.Sprintf("// %s declares %s: its generated op scopes every read and write to the caller\n"+
		"// through s.deps.Owners, so delegate to it (run `forge generate` first if the op predates the declaration).\n"+
		"func (s *Service) %s(ctx context.Context, req *connect.Request[pb.%s]) (*connect.Response[pb.%s], error) {\n"+
		"\treturn %s.Handle%s(s.%s())(ctx, req)\n}\n",
		table, spec, name, method.InputType, method.OutputType,
		codegen.CRUDDelegatePkgName(), strings.ToUpper(op[:1])+op[1:], crudOpName(name))
}

// sortedStrings returns v sorted, never nil, so the emitted JSON is stable
// and says [] rather than null.
func sortedStrings(v []string) []string {
	out := append([]string{}, v...)
	sort.Strings(out)
	return out
}
