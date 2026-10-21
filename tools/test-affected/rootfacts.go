// Copyright (c) 2025 Reliant Labs
package main

import (
	"go/ast"
	"go/token"
	"strings"
)

// literal is the string a Join argument spells: a string literal, or a
// package string constant.
func (rf *rootFacts) literal(e ast.Expr) (string, bool) {
	if s, ok := strLit(e); ok {
		return s, true
	}
	if id, ok := e.(*ast.Ident); ok {
		s, ok := rf.consts[id.Name]
		return s, ok
	}
	return "", false
}

// rootFacts is where one package's sources say the repo root is: which
// functions return it (or a path under it), and which package variables and
// struct fields hold one. literalCandidates reads it to tell
// filepath.Join(repoRoot(t), "web") (a read of web/) from
// filepath.Join(t.TempDir(), "web") (a fixture's directory).
//
// It is syntactic and per package, so it over-approximates where names meet:
// they are not scoped (a field called root holds the root if any assignment to
// any field called root does). What it never does is treat a value it cannot
// trace to the root, such as a parameter or t.TempDir(), as the root.
type rootFacts struct {
	funcs  map[string]bool
	vars   map[string]bool
	fields map[string]bool
	// consts are the package's string constants, which a Join spells like
	// literals: filepath.Join(root, protocGenESDir, "package.json").
	consts map[string]string
}

// rootLocatorNames are helpers that locate the root by name, wherever they are
// declared.
var rootLocatorNames = map[string]bool{"repoRoot": true, "RepoRoot": true, "repoRootFromTest": true, "findRoot": true}

// findRootFacts settles the facts of a package's files: a function returning a
// root-located value makes its callers' values root-located, and so on, until
// nothing changes.
func findRootFacts(files []*ast.File) *rootFacts {
	rf := &rootFacts{funcs: map[string]bool{}, vars: map[string]bool{}, fields: map[string]bool{}, consts: map[string]string{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.CONST {
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if s, ok := strLit(vs.Values[i]); ok {
								rf.consts[name.Name] = s
							}
						}
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		mark := func(set map[string]bool, name string) {
			if name != "" && name != "_" && !set[name] {
				set[name] = true
				changed = true
			}
		}
		for _, f := range files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					rf.assignments(d, map[string]bool{}, func(name string) { mark(rf.vars, name) }, func(field string) { mark(rf.fields, field) })
				case *ast.FuncDecl:
					if d.Body == nil {
						continue
					}
					local := rf.localRoots(d.Body)
					rf.assignments(d.Body, local, func(string) {}, func(field string) { mark(rf.fields, field) })
					ast.Inspect(d.Body, func(n ast.Node) bool {
						if ret, ok := n.(*ast.ReturnStmt); ok {
							for _, r := range ret.Results {
								if rf.locates(r, local) {
									mark(rf.funcs, d.Name.Name)
								}
							}
						}
						return true
					})
				}
			}
		}
	}
	return rf
}

// localRoots is the set of local variables in body that hold a root-located
// value.
func (rf *rootFacts) localRoots(body ast.Node) map[string]bool {
	local := map[string]bool{}
	for changed := true; changed; {
		changed = false
		rf.assignments(body, local, func(name string) {
			if name != "_" && !local[name] {
				local[name] = true
				changed = true
			}
		}, func(string) {})
	}
	return local
}

// assignments calls onVar for every variable and onField for every struct
// field under n that is assigned a root-located value.
func (rf *rootFacts) assignments(n ast.Node, local map[string]bool, onVar, onField func(string)) {
	lhs := func(l ast.Expr) {
		switch x := l.(type) {
		case *ast.Ident:
			onVar(x.Name)
		case *ast.SelectorExpr:
			onField(x.Sel.Name)
		}
	}
	// pairs is the value of each left-hand side: its own, or the one call
	// all of them come from (`_, file, _, ok := runtime.Caller(0)`).
	pairs := func(n int, values []ast.Expr, set func(int)) {
		for i := 0; i < n; i++ {
			var v ast.Expr
			switch {
			case len(values) == n:
				v = values[i]
			case len(values) == 1:
				v = values[0]
			}
			if v != nil && rf.locates(v, local) {
				set(i)
			}
		}
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			pairs(len(x.Lhs), x.Rhs, func(i int) { lhs(x.Lhs[i]) })
		case *ast.ValueSpec:
			pairs(len(x.Names), x.Values, func(i int) { onVar(x.Names[i].Name) })
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.Ident); ok && rf.locates(x.Value, local) {
				onField(k.Name)
			}
		}
		return true
	})
}

// locates reports whether e's value is the repo root or a path derived from
// it. Derivation follows only what builds or cleans a path, so a value merely
// computed NEAR one (an index into a map a fixture filled, a struct holding a
// root among other things) is not one:
//
//   - os.Getwd(), runtime.Caller(...), filepath.Abs("<relative>") and
//     `git rev-parse --show-toplevel` output locate it (the package directory
//     is in the repo, and walking up from it finds the root);
//   - so does a ".." literal, a call to a function that returns one, and a
//     variable or field assigned one;
//   - and so does a path built from one: filepath.Join/Dir/Abs/Clean/... of
//     it, a string conversion or trim of it, or `it + "/sub"`.
func (rf *rootFacts) locates(e ast.Expr, local map[string]bool) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return rf.locates(x.X, local)
	case *ast.StarExpr:
		return rf.locates(x.X, local)
	case *ast.UnaryExpr:
		return rf.locates(x.X, local)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && rf.locates(x.X, local)
	case *ast.BasicLit:
		s, ok := strLit(x)
		return ok && (s == ".." || strings.HasPrefix(s, "../"))
	case *ast.Ident:
		return local[x.Name] || rf.vars[x.Name]
	case *ast.SelectorExpr:
		return rf.fields[x.Sel.Name]
	case *ast.CallExpr:
		name := calleeName(x)
		if rf.funcs[name] || rootLocatorNames[name] {
			return true
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && ((pkg.Name == "os" && name == "Getwd") || (pkg.Name == "runtime" && name == "Caller")) {
				return true
			}
			if name == "Output" || name == "CombinedOutput" {
				return rf.locates(sel.X, local) // exec.Command(...).Output()
			}
		}
		for _, a := range x.Args {
			if s, ok := strLit(a); ok && s == "--show-toplevel" {
				return true
			}
		}
		if len(x.Args) == 0 || !pathTransforms[name] {
			return false
		}
		if name == "Abs" {
			if _, ok := strLit(x.Args[0]); ok {
				return true
			}
		}
		return rf.locates(x.Args[0], local)
	}
	return false
}

// pathTransforms build or clean a path from their first argument.
var pathTransforms = map[string]bool{
	"Join": true, "Dir": true, "Abs": true, "Clean": true, "EvalSymlinks": true,
	"ToSlash": true, "FromSlash": true, "string": true,
	"TrimSpace": true, "TrimSuffix": true, "TrimRight": true, "Trim": true,
}

func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}
