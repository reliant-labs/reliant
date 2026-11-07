package servicecatalog

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// Locate turns every committed Source in c into a located one: it resolves
// each Symbol to its line in the source being built, which is what a bundle
// carries. readFile reads a module-relative path (Decode has already refused
// absolute and escaping ones). Each file is parsed once, syntax only.
//
// A source whose file cannot be read or parsed, or no longer declares its
// symbol, becomes the zero Source: the location is unknown, never guessed.
// Sources that are already located or unknown are left alone.
func Locate(c *Catalog, readFile func(path string) ([]byte, error)) {
	files := map[string]map[string]int{}
	c.eachSource(func(_ string, s *Source) {
		if s.Symbol == "" {
			return
		}
		decls, ok := files[s.Path]
		if !ok {
			decls = declLines(s.Path, readFile)
			files[s.Path] = decls
		}
		if line := decls[s.Symbol]; line > 0 {
			*s = Source{Path: s.Path, Line: line}
		} else {
			*s = Source{}
		}
	})
}

// declLines maps every top-level symbol of the Go file at path to the line
// of its name: funcs, types, methods ("Type.Method", generic receivers
// included) and the methods an interface type declares. A file that does not
// read or parse maps nothing.
func declLines(path string, readFile func(string) ([]byte, error)) map[string]int {
	out := map[string]int{}
	src, err := readFile(path)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return out
	}
	add := func(sym string, id *ast.Ident) {
		if _, dup := out[sym]; !dup {
			out[sym] = fset.Position(id.Pos()).Line
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil || len(d.Recv.List) == 0 {
				add(d.Name.Name, d.Name)
			} else if recv := receiverName(d.Recv.List[0].Type); recv != "" {
				add(recv+"."+d.Name.Name, d.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				add(ts.Name.Name, ts.Name)
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok || iface.Methods == nil {
					continue
				}
				for _, m := range iface.Methods.List {
					for _, name := range m.Names {
						add(ts.Name.Name+"."+name.Name, name)
					}
				}
			}
		}
	}
	return out
}

// receiverName is the base type name of a method receiver: T for T, *T,
// T[K] and *T[K, V].
func receiverName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
