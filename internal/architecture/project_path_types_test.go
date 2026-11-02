package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// projectTypeIndex is what the projects.path guard knows, repo-wide, about
// which expressions hold a db.Project: every function, method and interface
// method whose first result is one (or a slice of them), and every struct
// field of that type. A read is then found by what the value IS — a parameter
// or variable declared with the type, the result of a call that returns one,
// an element ranged out of a slice of them, a field of that type — not by
// what the variable happens to be called. It is parsed, not type-checked,
// which keeps the guard fast and cache-sound.
type projectTypeIndex struct {
	funcs      map[string]bool // name -> first result is a project
	sliceFuncs map[string]bool // name -> first result is []project
	fields     map[string]bool // struct field name -> a project
}

func newProjectTypeIndex() *projectTypeIndex {
	return &projectTypeIndex{funcs: map[string]bool{}, sliceFuncs: map[string]bool{}, fields: map[string]bool{}}
}

// projectQualifiers are the names a file imports the db and db/core packages
// under, and whether the file is in one of them (where the type is plain
// `Project`).
type projectQualifiers struct {
	names map[string]bool
	local bool
}

func fileProjectQualifiers(file *ast.File, rel string) projectQualifiers {
	q := projectQualifiers{names: map[string]bool{}}
	dir := filepath.ToSlash(filepath.Dir(rel))
	q.local = dir == "internal/db" || dir == "internal/db/core"
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasSuffix(path, "/internal/db") && !strings.HasSuffix(path, "/internal/db/core") {
			continue
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		q.names[name] = true
	}
	return q
}

// projectType reports whether a type expression is a db.Project (by value or
// pointer), or a slice of them.
func (q projectQualifiers) projectType(e ast.Expr) (single, slice bool) {
	if arr, ok := e.(*ast.ArrayType); ok && arr.Len == nil {
		elem, _ := q.projectType(arr.Elt)
		return false, elem
	}
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	switch t := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok && q.names[id.Name] && t.Sel.Name == "Project" {
			return true, false
		}
	case *ast.Ident:
		return q.local && t.Name == "Project", false
	}
	return false, false
}

// indexProjectTypes adds file's declarations to the index.
func (ix *projectTypeIndex) indexProjectTypes(file *ast.File, rel string) {
	q := fileProjectQualifiers(file, rel)
	addFunc := func(name string, ft *ast.FuncType) {
		if ft.Results == nil || len(ft.Results.List) == 0 {
			return
		}
		single, slice := q.projectType(ft.Results.List[0].Type)
		if single {
			ix.funcs[name] = true
		}
		if slice {
			ix.sliceFuncs[name] = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			addFunc(n.Name.Name, n.Type)
		case *ast.InterfaceType:
			for _, m := range n.Methods.List {
				if ft, ok := m.Type.(*ast.FuncType); ok {
					for _, name := range m.Names {
						addFunc(name.Name, ft)
					}
				}
			}
		case *ast.StructType:
			for _, f := range n.Fields.List {
				if single, _ := q.projectType(f.Type); single {
					for _, name := range f.Names {
						ix.fields[name.Name] = true
					}
				}
			}
		}
		return true
	})
}

// calleeName is the function or method a call names.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// projectNamed reports whether a selector's receiver is NAMED like a project:
// `project`, `proj`, `prj`, or a field of that name (`tec.project`). Kept
// beside the type inference as a second net.
func projectNamed(x ast.Expr) bool {
	var name string
	switch x := x.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	}
	switch strings.ToLower(name) {
	case "project", "proj", "prj":
		return true
	}
	return false
}

// projectPathReads counts the reads of a project's .Path in each function of
// file, keyed "<rel>:<func>".
func (ix *projectTypeIndex) projectPathReads(file *ast.File, rel string, into map[string]int) {
	q := fileProjectQualifiers(file, rel)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		projects, slices := map[string]bool{}, map[string]bool{}
		mark := func(name string, single, slice bool) {
			projects[name] = projects[name] || single
			slices[name] = slices[name] || slice
		}
		// valueKind is what an expression yields: a project, a slice of
		// them, or neither.
		valueKind := func(e ast.Expr) (single, slice bool) {
			if u, ok := e.(*ast.UnaryExpr); ok {
				e = u.X
			}
			switch v := e.(type) {
			case *ast.CompositeLit:
				return q.projectType(v.Type)
			case *ast.CallExpr:
				name := calleeName(v)
				return ix.funcs[name], ix.sliceFuncs[name]
			case *ast.Ident:
				return projects[v.Name], slices[v.Name]
			case *ast.SelectorExpr:
				return ix.fields[v.Sel.Name], false
			}
			return false, false
		}
		for _, list := range []*ast.FieldList{fn.Type.Params, fn.Type.Results} {
			if list == nil {
				continue
			}
			for _, f := range list.List {
				single, slice := q.projectType(f.Type)
				for _, n := range f.Names {
					mark(n.Name, single, slice)
				}
			}
		}
		// Two passes, so a variable assigned from another found later is
		// still known: statement order does not change what a value is.
		for pass := 0; pass < 2; pass++ {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.ValueSpec:
					if n.Type != nil {
						single, slice := q.projectType(n.Type)
						for _, name := range n.Names {
							mark(name.Name, single, slice)
						}
					}
					for i, v := range n.Values {
						if i < len(n.Names) {
							single, slice := valueKind(v)
							mark(n.Names[i].Name, single, slice)
						}
					}
				case *ast.AssignStmt:
					for i, lhs := range n.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						switch {
						case len(n.Rhs) == len(n.Lhs):
							single, slice := valueKind(n.Rhs[i])
							mark(id.Name, single, slice)
						case len(n.Rhs) == 1 && i == 0: // x, err := call()
							single, slice := valueKind(n.Rhs[0])
							mark(id.Name, single, slice)
						}
					}
				case *ast.RangeStmt:
					if id, ok := n.Value.(*ast.Ident); ok {
						if _, slice := valueKind(n.X); slice {
							mark(id.Name, true, false)
						}
					}
				}
				return true
			})
		}
		isProject := func(x ast.Expr) bool {
			switch x := x.(type) {
			case *ast.Ident:
				return projects[x.Name]
			case *ast.SelectorExpr:
				return ix.fields[x.Sel.Name]
			case *ast.CallExpr:
				return ix.funcs[calleeName(x)]
			}
			return false
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Path" && (isProject(sel.X) || projectNamed(sel.X)) {
				into[rel+":"+fn.Name.Name]++
			}
			return true
		})
	}
}

func TestProjectPathReads(t *testing.T) {
	src := `package x
import (
	"github.com/acme/app/internal/db"
	dbcore "github.com/acme/app/internal/db/core"
)
type Store interface { GetProject(id string) (*db.Project, error); ListProjects() ([]*dbcore.Project, error) }
type Req struct { Owner *db.Project }
func a(project *P) { send(project.Path); _ = t.project.Path; _ = proj.Path }
func b(p *P, wt *W) { _ = p.Path; _ = wt.Path; _ = project.Name }
func c(s Store, r Req, existing *db.Project) {
	got, err := s.GetProject("x"); _ = err
	_ = got.Path
	_ = existing.Path
	_ = r.Owner.Path
	all, _ := s.ListProjects()
	for _, each := range all { _ = each.Path }
	lit := &db.Project{}
	_ = lit.Path
	var v dbcore.Project
	_ = v.Path
	_ = s.GetProject("y").Path
	alias := got
	_ = alias.Path
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "internal/x/x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	ix := newProjectTypeIndex()
	ix.indexProjectTypes(file, "internal/x/x.go")
	got := map[string]int{}
	ix.projectPathReads(file, "internal/x/x.go", got)
	want := map[string]int{"internal/x/x.go:a": 3, "internal/x/x.go:c": 8}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reads = %v, want %v: a by name, c by type, and nothing in b", got, want)
	}
}
