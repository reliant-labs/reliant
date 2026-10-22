// Copyright (c) 2025 Reliant Labs
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// notTheDatabase names the persistence-shaped slots compose.go may fill with
// something other than g.Database(), each with why. A slot is "<type>.<field>"
// for a struct field, "<func>#<n>" for a call's nth argument. An entry no slot
// matches any more fails the test: the list never outlives its reason.
var notTheDatabase = map[string]string{}

// Every slot compose.go fills whose type *db.Repo satisfies — a consumer's
// persistence role, a provider's DB field, a constructor's repository
// parameter — is filled by the Database node, g.Database().
//
// The guard is for what forge's resolution cannot see. A role interface is
// structural, so a narrow one (a single GetChat, say) may be satisfied by
// some other node too; forge then fills a NEW key with whichever node is the
// one assignable producer, and a key written by hand is never re-resolved.
// Either way the field compiles while reading something that is not the
// database — or opening a transaction on a second producer with another retry
// policy (research/forge-migration/10-repo-split-plan.md §2.3, §3). The
// compiler cannot tell; this test can.
func TestPersistenceRolesAreTheDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks internal/app against go list -export data (builds every dependency); skipped under -short")
	}
	pkg, files, info, fset := typeCheckApp(t)

	repo := lookupRepoType(t, pkg)
	database := lookupGraphMethod(t, pkg, "Database")

	var (
		checked int
		seen    = map[string]bool{}
		bad     []string
	)
	check := func(slot string, slotType types.Type, value ast.Expr) {
		if slotType == nil || !types.AssignableTo(repo, slotType) {
			return
		}
		if iface, ok := slotType.Underlying().(*types.Interface); ok && iface.NumMethods() == 0 {
			return // any: every value is one, and none of them is a role
		}
		checked++
		if isCallOf(info, value, database) {
			return
		}
		if _, ok := notTheDatabase[slot]; ok {
			seen[slot] = true
			return
		}
		bad = append(bad, fmt.Sprintf("%s: %s (%s) is filled with %s, not g.Database()",
			fset.Position(value.Pos()), slot, slotType, types.ExprString(value)))
	}

	for _, f := range files {
		if filepath.Base(fset.Position(f.Pos()).Filename) != "compose.go" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				st, ok := info.TypeOf(n).Underlying().(*types.Struct)
				if !ok {
					return true
				}
				name := types.TypeString(info.TypeOf(n), types.RelativeTo(pkg))
				for i, el := range n.Elts {
					field, value := structField(st, i, el)
					if field != nil {
						check(name+"."+field.Name(), field.Type(), value)
					}
				}
			case *ast.CallExpr:
				sig, ok := info.TypeOf(n.Fun).(*types.Signature)
				if !ok {
					return true // a conversion
				}
				name := types.ExprString(n.Fun)
				for i, arg := range n.Args {
					check(fmt.Sprintf("%s#%d", name, i), paramType(sig, i), arg)
				}
			}
			return true
		})
	}

	t.Logf("%d persistence slots in compose.go", checked)
	require.Empty(t, bad, "every persistence role compose.go fills is the Database node")
	// A guard that matched nothing guards nothing: compose.go hands the
	// database to dozens of handlers and providers.
	require.Greater(t, checked, 50, "found too few persistence slots in compose.go: is the *db.Repo lookup still right?")
	for slot, why := range notTheDatabase {
		require.True(t, seen[slot], "notTheDatabase[%q] (%s) matches no slot compose.go fills any more: delete it", slot, why)
	}
}

// structField is the field a struct literal's ith element sets, and the
// value it sets it to.
func structField(st *types.Struct, i int, el ast.Expr) (*types.Var, ast.Expr) {
	kv, ok := el.(*ast.KeyValueExpr)
	if !ok {
		if i < st.NumFields() {
			return st.Field(i), el
		}
		return nil, nil
	}
	key, ok := kv.Key.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	for j := range st.NumFields() {
		if st.Field(j).Name() == key.Name {
			return st.Field(j), kv.Value
		}
	}
	return nil, nil
}

// paramType is the type the ith argument of a call to sig is assigned to.
func paramType(sig *types.Signature, i int) types.Type {
	params := sig.Params()
	switch {
	case sig.Variadic() && i >= params.Len()-1:
		return params.At(params.Len() - 1).Type().(*types.Slice).Elem()
	case i < params.Len():
		return params.At(i).Type()
	}
	return nil
}

// isCallOf reports whether e is a call of method fn with no arguments.
func isCallOf(info *types.Info, e ast.Expr, fn *types.Func) bool {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && info.Uses[sel.Sel] == fn
}

func lookupRepoType(t *testing.T, pkg *types.Package) types.Type {
	t.Helper()
	const dbPath = "github.com/reliant-labs/reliant/internal/db"
	for _, imp := range pkg.Imports() {
		if imp.Path() != dbPath {
			continue
		}
		obj, ok := imp.Scope().Lookup("Repo").(*types.TypeName)
		require.True(t, ok, "%s declares no type Repo", dbPath)
		return types.NewPointer(obj.Type())
	}
	t.Fatalf("internal/app does not import %s", dbPath)
	return nil
}

func lookupGraphMethod(t *testing.T, pkg *types.Package, name string) *types.Func {
	t.Helper()
	graph, ok := pkg.Scope().Lookup("Graph").(*types.TypeName)
	require.True(t, ok, "internal/app declares no type Graph")
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(graph.Type()), true, pkg, name)
	fn, ok := obj.(*types.Func)
	require.True(t, ok, "*Graph has no method %s: is there still a Database node?", name)
	return fn
}

// typeCheckApp type-checks this package's non-test files from source, its
// imports from the export data `go list -export` builds (the standard
// library's importer, so the test needs no golang.org/x/tools).
func typeCheckApp(t *testing.T) (*types.Package, []*ast.File, *types.Info, *token.FileSet) {
	t.Helper()
	type listed struct {
		ImportPath string
		Dir        string
		Export     string
		GoFiles    []string
		DepOnly    bool
	}
	var stderr bytes.Buffer
	cmd := exec.Command("go", "list", "-export", "-deps", "-json=ImportPath,Dir,Export,GoFiles,DepOnly", ".")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go list -export: %s", stderr.String())

	exports := map[string]string{}
	var self *listed
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listed
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly {
			self = &p
		}
	}
	require.NotNil(t, self, "go list named no package for .")

	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range self.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(self.Dir, name), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		return fset.Position(files[i].Pos()).Filename < fset.Position(files[j].Pos()).Filename
	})

	lookup := func(path string) (io.ReadCloser, error) {
		export := exports[path]
		if export == "" {
			return nil, fmt.Errorf("go list reported no export data for %s", path)
		}
		return os.Open(export)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", lookup)}
	pkg, err := conf.Check(self.ImportPath, fset, files, info)
	require.NoError(t, err, "type-checking %s", self.ImportPath)
	return pkg, files, info, fset
}
