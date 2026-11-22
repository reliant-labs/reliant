package opindex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// Declaration kinds a goDecl carries.
const (
	declFunc   = "func"
	declMethod = "method"
	declType   = "type"
	declVar    = "var"
	declConst  = "const"
)

// goDecl is one top-level declaration of a Go file with the line span the
// change detector intersects diff hunks with.
//
// Key is the declaration's node in the package call graph. Funcs, types,
// vars and consts share Go's package scope, so their bare name is unique;
// a method is "<Recv>.<Name>", which no package-scope identifier can spell.
type goDecl struct {
	Key     string
	Display string // what a Reason shows: "(*service).Send", "helper", "type service"
	Kind    string
	Recv    string // receiver type name for a method, without "*"
	Name    string
	Pointer bool // the method has a pointer receiver
	Start   int  // first line (the `func` keyword or the spec), 1-based
	End     int  // last line, inclusive
}

// parseGoDecls parses src (the file at path) and lists its top-level
// declarations. Imports are not declarations here: an import alone changes
// no behaviour, and the code that uses it is what the change detector sees.
func parseGoDecls(path string, src []byte) (*ast.File, *token.FileSet, []goDecl, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, nil, err
	}
	return f, fset, declsOf(f, func(p token.Pos) int { return fset.Position(p).Line }), nil
}

// declsOf lists f's declarations, numbering lines with line.
func declsOf(f *ast.File, line func(token.Pos) int) []goDecl {
	var out []goDecl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			out = append(out, funcDecl(d, line(d.Pos()), line(d.End())))
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, spec := range d.Specs {
				start, end := line(spec.Pos()), line(spec.End())
				// An ungrouped `var x = 1` starts at its keyword.
				if !d.Lparen.IsValid() {
					start = line(d.Pos())
				}
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out = append(out, goDecl{Key: s.Name.Name, Display: "type " + s.Name.Name, Kind: declType, Name: s.Name.Name, Start: start, End: end})
				case *ast.ValueSpec:
					kind := declVar
					if d.Tok == token.CONST {
						kind = declConst
					}
					for _, n := range s.Names {
						if n.Name == "_" {
							continue
						}
						out = append(out, goDecl{Key: n.Name, Display: kind + " " + n.Name, Kind: kind, Name: n.Name, Start: start, End: end})
					}
				}
			}
		}
	}
	return out
}

func funcDecl(fn *ast.FuncDecl, start, end int) goDecl {
	name := fn.Name.Name
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return goDecl{Key: name, Display: name, Kind: declFunc, Name: name, Start: start, End: end}
	}
	recv, ptr := recvType(fn.Recv.List[0].Type)
	display := "(" + recv + ")." + name
	if ptr {
		display = "(*" + recv + ")." + name
	}
	return goDecl{Key: recv + "." + name, Display: display, Kind: declMethod, Recv: recv, Name: name, Pointer: ptr, Start: start, End: end}
}

// recvType names a receiver's base type: `*T`, `T`, `T[K]` and `*T[K, V]`
// are all T.
func recvType(e ast.Expr) (string, bool) {
	ptr := false
	if s, ok := e.(*ast.StarExpr); ok {
		ptr, e = true, s.X
	}
	switch t := e.(type) {
	case *ast.IndexExpr:
		e = t.X
	case *ast.IndexListExpr:
		e = t.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name, ptr
	}
	return "", ptr
}

// callGraph is one package's static reference graph: an edge from a
// declaration to every same-package declaration its source names. It is
// deliberately untyped (no go/types, no imports resolved), so it is cheap and
// works on a tree that does not build:
//
//   - an identifier naming a package-level func, type, var or const is an
//     edge to it, whether it is called, passed as a callback or used as a
//     type (a method's receiver type is an edge, so a change to a struct
//     reaches every method on it);
//   - a selector `x.M` is an edge to EVERY method named M declared in the
//     package, whatever x is. Without types the receiver is unknown, so this
//     over-approximates; it never misses a same-package method call.
//
// Local variables that shadow a package-level name also produce an edge.
// Both errors make the graph larger, never smaller: a mapping built on it
// may name an op the change cannot reach, but does not miss one that it can
// through a same-package call.
type callGraph struct {
	edges map[string]map[string]bool
}

// buildGraph builds the graph over files (the package's hand-written,
// non-test files).
func buildGraph(files []*ast.File) *callGraph {
	g := &callGraph{edges: map[string]map[string]bool{}}
	scope := map[string]bool{}       // package-scope names
	methods := map[string][]string{} // method name -> its node keys
	for _, f := range files {
		for _, d := range declsOf(f, func(token.Pos) int { return 0 }) {
			if d.Kind == declMethod {
				methods[d.Name] = append(methods[d.Name], d.Key)
			} else {
				scope[d.Key] = true
			}
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				key := funcDecl(d, 0, 0).Key
				g.collect(key, d, d.Name, scope, methods)
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						g.collect(s.Name.Name, s, s.Name, scope, methods)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							g.collect(n.Name, s, n, scope, methods)
						}
					}
				}
			}
		}
	}
	return g
}

// collect records every reference node makes, skipping the declaring
// identifier self.
func (g *callGraph) collect(from string, node ast.Node, self *ast.Ident, scope map[string]bool, methods map[string][]string) {
	add := func(to string) {
		if to == from {
			return
		}
		if g.edges[from] == nil {
			g.edges[from] = map[string]bool{}
		}
		g.edges[from][to] = true
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			// Sel is a field or method name, never a package-scope
			// identifier: handle it here so the generic ident case below
			// does not read `x.Send` as the package func Send.
			for _, k := range methods[n.Sel.Name] {
				add(k)
			}
			ast.Inspect(n.X, visit)
			return false
		case *ast.Ident:
			if n != self && scope[n.Name] {
				add(n.Name)
			}
		}
		return true
	}
	ast.Inspect(node, visit)
}

// reach returns every node reachable from start, start included.
func (g *callGraph) reach(start string) []string {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for to := range g.edges[n] {
			if !seen[to] {
				seen[to] = true
				queue = append(queue, to)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// isGeneratedPath reports whether a project-relative slash path is forge or
// protoc output: a *_gen.go file, a *.pb.go file, or anything under gen/.
func isGeneratedPath(p string) bool {
	return strings.HasPrefix(p, "gen/") || strings.HasSuffix(p, "_gen.go") || strings.HasSuffix(p, ".pb.go")
}
