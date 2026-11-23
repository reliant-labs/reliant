// File: internal/generator/contract/observed_ctor.go
//
// NewInstrumented — the package's own constructor, already decorated.
//
// The decorator exists only on the value `NewServiceWithForgeMiddleware(New(…))`
// returns, so every construction site had to remember to wrap. compose.go is
// kept wrapped by `forge generate`, but a real project constructs most of its
// components elsewhere: providers.go, *_wire.go files, a second binary's main,
// a package composing its own sub-services. In the control plane 23 packages
// gained a decorator and not one construction used it — the wrapping is a
// two-call nesting, and with a fallible New it does not even compose inline:
//
//	x, err := pkg.New(deps)            // where would the wrapper go?
//
// NewInstrumented has New's exact signature — same parameters, same results —
// and returns the decorated value. Instrumenting a construction is a rename of
// one identifier, valid in every position a call can appear (an assignment, a
// return, a struct field, an argument), with no change to any type. That is
// what `forge lint --tracing --fix` rewrites, and what makes the rule enforceable:
// a missed site is a one-token fix, not a refactor.

package contract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// InstrumentedCtorName is the generated decorated twin of a package's New.
const InstrumentedCtorName = "NewInstrumented"

// instrumentedCtor is NewInstrumented's rendering input.
type instrumentedCtor struct {
	Params   string // declaration list, e.g. "deps Deps, opts ...Option"
	Args     string // forwarding list, e.g. "deps, opts..."
	Fallible bool   // New returns (Iface, error)
	Imports  []observedImport
}

// findInstrumentableCtor inspects dir's `func New` and reports whether a
// NewInstrumented can be generated for it: New must be a top-level function
// returning exactly iface or (iface, error), and the package must not already
// declare NewInstrumented in a file of its own.
func findInstrumentableCtor(dir, iface string) (instrumentedCtor, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return instrumentedCtor{}, false
	}
	var (
		found   *ast.FuncDecl
		srcFile *ast.File
		fset    = token.NewFileSet()
	)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == observedFileName() {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			switch fd.Name.Name {
			case InstrumentedCtorName:
				return instrumentedCtor{}, false // the user owns one; never shadow it
			case "New":
				found, srcFile = fd, f
			}
		}
	}
	if found == nil || found.Type.TypeParams != nil {
		return instrumentedCtor{}, false
	}
	res := found.Type.Results
	if res == nil || res.NumFields() == 0 || res.NumFields() > 2 {
		return instrumentedCtor{}, false
	}
	if id, ok := res.List[0].Type.(*ast.Ident); !ok || id.Name != iface || len(res.List[0].Names) > 1 {
		return instrumentedCtor{}, false
	}
	fallible := res.NumFields() == 2
	if fallible {
		last := res.List[len(res.List)-1].Type
		if id, ok := last.(*ast.Ident); !ok || id.Name != "error" {
			return instrumentedCtor{}, false
		}
	}

	var params, args []string
	n := 0
	for _, field := range found.Type.Params.List {
		var tb bytes.Buffer
		if err := printer.Fprint(&tb, fset, field.Type); err != nil {
			return instrumentedCtor{}, false
		}
		typ := tb.String()
		_, variadic := field.Type.(*ast.Ellipsis)
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{{Name: "p" + strconv.Itoa(n)}}
		}
		for _, nm := range names {
			name := nm.Name
			if name == "_" {
				name = "p" + strconv.Itoa(n)
			}
			n++
			params = append(params, name+" "+typ)
			if variadic {
				args = append(args, name+"...")
			} else {
				args = append(args, name)
			}
		}
	}
	return instrumentedCtor{
		Params:   strings.Join(params, ", "),
		Args:     strings.Join(args, ", "),
		Fallible: fallible,
		Imports:  paramImports(found.Type.Params, srcFile),
	}, true
}

// paramImports returns the imports of src that New's parameter types name.
func paramImports(params *ast.FieldList, src *ast.File) []observedImport {
	names := map[string]bool{}
	ast.Inspect(params, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				names[id.Name] = true
			}
		}
		return true
	})
	var out []observedImport
	for _, spec := range src.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		leaf := path[strings.LastIndex(path, "/")+1:]
		local, alias := leaf, ""
		if spec.Name != nil {
			local, alias = spec.Name.Name, spec.Name.Name
		}
		if names[local] {
			out = append(out, observedImport{Alias: alias, Path: path})
		}
	}
	return out
}

// renderInstrumentedCtor renders NewInstrumented for a decorator constructed
// by ctorName.
func renderInstrumentedCtor(c instrumentedCtor, iface, ctorName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n// %s is New with its result wrapped in %s: the same\n", InstrumentedCtorName, ctorName)
	b.WriteString("// parameters and results, so a construction is instrumented by renaming\n")
	fmt.Fprintf(&b, "// New to %s wherever it is called (providers.go, a *_wire.go\n", InstrumentedCtorName)
	b.WriteString("// file, another binary). `forge lint --tracing --fix` rewrites any it finds.\n")
	if c.Fallible {
		fmt.Fprintf(&b, "func %s(%s) (%s, error) {\n", InstrumentedCtorName, c.Params, iface)
		fmt.Fprintf(&b, "\tinstrumentedInner, instrumentedErr := New(%s)\n", c.Args)
		b.WriteString("\tif instrumentedErr != nil {\n\t\treturn nil, instrumentedErr\n\t}\n")
		fmt.Fprintf(&b, "\treturn %s(instrumentedInner), nil\n}\n", ctorName)
	} else {
		fmt.Fprintf(&b, "func %s(%s) %s {\n", InstrumentedCtorName, c.Params, iface)
		fmt.Fprintf(&b, "\treturn %s(New(%s))\n}\n", ctorName, c.Args)
	}
	return b.String()
}

// mergeImports adds extra to base, deduplicated by path, sorted by path.
func mergeImports(base, extra []observedImport) []observedImport {
	seen := map[string]bool{}
	var out []observedImport
	for _, imp := range append(append([]observedImport(nil), base...), extra...) {
		if seen[imp.Path] {
			continue
		}
		seen[imp.Path] = true
		out = append(out, imp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
