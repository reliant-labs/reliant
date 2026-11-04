// File: internal/codegen/composition_graph_compose.go
//
// internal/app/compose.go in the composition graph: OWNED and RECONCILED, one
// constructor method per node —
//
//	func (g *Graph) newAlpha() (*alpha.Service, error) {
//		return alpha.New(alpha.Deps{
//			Logger: g.Log,
//			Store:  g.Store(),
//		})
//	}
//
// Every edge of the graph is a value expression in one of these literals, and
// they are the author's. forge keeps the projection in sync and nothing else:
//
//   - a node with no new<X> method gets one, every key resolved;
//   - a Deps key the method's literal lacks is resolved and added;
//   - a key naming a field the Deps struct no longer has is dropped (it
//     cannot compile, so keeping it would only mean generate never heals a
//     refactored struct);
//   - a value beside a key that still exists is never read or rewritten, and
//     a method whose body has no Deps literal of the node's type is the
//     author's shape and is left alone entirely.
//
// The rewritten file must parse and gofmt, or generate fails without writing.

package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// graphComposeHeader opens a compose.go forge writes for the first time.
const graphComposeHeader = `// forge-scaffold (USER-OWNED, RECONCILED): forge writes this file once and then
// keeps only its projection in sync. It is the composition graph's wiring: one
// constructor method per node, holding the value expression for every field of
// that node's Deps. Those expressions are yours — change what fills a field
// here, and forge never touches it again.
//
// What forge reconciles on every ` + "`forge generate`" + `:
//   - a node with no new<X> method (a service, worker, package or provider you
//     just added) gets one, each Deps field resolved BY TYPE;
//   - a Deps field a node gained gets its key added; a key whose field the
//     Deps struct no longer has is dropped;
//   - a key two nodes could fill is a generate error until you write it here.
//
// The memoized accessors these feed (g.<X>()), the roots and their lifecycle
// are generated in graph_gen.go. ` + "`forge project disown internal/app/compose.go`" + `
// stops the reconciliation.
package app
`

// graphAssign is one `Field: Expr, // Comment` line.
type graphAssign struct {
	Field, Expr, Comment string
}

// renderGraphMethod renders a node's whole constructor method.
func renderGraphMethod(n *graphNode, assigns []graphAssign) string {
	var lit strings.Builder
	lit.WriteString(n.DepsType + "{\n")
	for _, a := range assigns {
		lit.WriteString("\t\t" + a.Field + ": " + a.Expr + ",")
		if a.Comment != "" {
			lit.WriteString(" // " + a.Comment)
		}
		lit.WriteString("\n")
	}
	lit.WriteString("\t}")
	call := n.Ctor + "(" + lit.String() + ")"

	var b strings.Builder
	fmt.Fprintf(&b, "\n// new%s constructs %s.\n", n.Name, n.Doc)
	fmt.Fprintf(&b, "func (g *Graph) new%s() (%s, error) {\n", n.Name, n.Type)
	switch {
	case n.Wrapped && n.Fallible:
		fmt.Fprintf(&b, "\tsvc, err := %s\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn %s(svc), nil\n",
			call, n.importAlias()+"."+n.MiddlewareCtor)
	case n.Wrapped:
		fmt.Fprintf(&b, "\treturn %s(%s), nil\n", n.importAlias()+"."+n.MiddlewareCtor, call)
	case n.Fallible:
		fmt.Fprintf(&b, "\treturn %s\n", call)
	default:
		fmt.Fprintf(&b, "\treturn %s, nil\n", call)
	}
	b.WriteString("}\n")
	return b.String()
}

// importAlias is the alias of a component node's package.
func (n *graphNode) importAlias() string {
	if n.Comp != nil {
		return n.Comp.Alias
	}
	return ""
}

type graphEdit struct {
	start, end int
	text       string
}

// graphLiteralEdits reconciles one existing new<X> method's Deps literal
// against its node: a key whose field the Deps struct no longer has is cut,
// and a field with no key gets one, resolved by type. Every existing value
// expression is the author's and is left alone.
func graphLiteralEdits(src string, off func(token.Pos) int, lit *ast.CompositeLit, n *graphNode, r *graphResolver, addImports func(map[string]string)) ([]graphEdit, []graphProblem) {
	var (
		edits    []graphEdit
		problems []graphProblem
	)
	have := map[string]bool{}
	dropped := map[ast.Expr]bool{}
	depsField := map[string]DepsField{}
	for _, df := range n.Deps {
		depsField[df.Name] = df
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		have[id.Name] = true
		if n.DepsFound && !n.DepsKeys[id.Name] {
			s, e := widenDepsElementCut(src, off(kv.Pos()), off(kv.End()))
			edits = append(edits, graphEdit{start: s, end: e})
			dropped[el] = true
			continue
		}
		if df, ok := depsField[id.Name]; ok {
			if e, ok := placeholderRefill(src, off, kv, n, df, r, addImports); ok {
				edits = append(edits, e)
			}
		}
	}
	var lines strings.Builder
	for _, df := range n.Deps {
		if have[df.Name] {
			continue
		}
		res, prob := r.resolve(n, df)
		if prob != nil {
			problems = append(problems, *prob)
			continue
		}
		addImports(res.Imports)
		lines.WriteString("\n" + df.Name + ": " + res.Expr + ",")
		if res.Comment != "" {
			lines.WriteString(" // " + res.Comment)
		}
	}
	if lines.Len() == 0 {
		return edits, problems
	}
	at := off(lit.Rbrace)
	// The last element that survives the stale-key drop needs a trailing
	// comma before anything is added after it.
	for i := len(lit.Elts) - 1; i >= 0; i-- {
		el := lit.Elts[i]
		if dropped[el] {
			continue
		}
		next := at
		if i+1 < len(lit.Elts) {
			next = off(lit.Elts[i+1].Pos())
		}
		if !strings.Contains(src[off(el.End()):next], ",") {
			edits = append(edits, graphEdit{start: off(el.End()), end: off(el.End()), text: ","})
		}
		break
	}
	// Insert on a line of its own: at the start of the closing brace's line
	// when nothing else is on it.
	text := lines.String() + "\n"
	if k := strings.LastIndexAny(src[:at], "\n"); k >= 0 && strings.TrimSpace(src[k+1:at]) == "" {
		at, text = k+1, strings.TrimPrefix(text, "\n")
	}
	edits = append(edits, graphEdit{start: at, end: at, text: text})
	return edits, problems
}

// placeholderRefill re-resolves a key that still holds forge's placeholder
// for an optional field with no producer (optionalPlaceholder): when a
// producer exists now, the value and its comment are replaced by the
// resolved expression. Anything else — a value the author wrote, or a
// placeholder that still has nothing to resolve to — is left alone.
func placeholderRefill(src string, off func(token.Pos) int, kv *ast.KeyValueExpr, n *graphNode, df DepsField, r *graphResolver, addImports func(map[string]string)) (graphEdit, bool) {
	start, end := off(kv.Value.Pos()), off(kv.Value.End())
	eol := strings.IndexByte(src[end:], '\n')
	if eol < 0 {
		eol = len(src) - end
	}
	rest := src[end : end+eol]
	comma, comment, ok := strings.Cut(rest, "//")
	if !ok || strings.TrimSpace(comma) != "," || !optionalPlaceholder(src[start:end], comment) {
		return graphEdit{}, false
	}
	res, prob := r.resolve(n, df)
	if prob != nil || optionalPlaceholder(res.Expr, res.Comment) {
		return graphEdit{}, false
	}
	addImports(res.Imports)
	text := res.Expr + ","
	if res.Comment != "" {
		text += " // " + res.Comment
	}
	return graphEdit{start: start, end: end + eol, text: text}, true
}

// graphWrapEdits brings an existing new<X> method in line with its node's
// instrument decision (ShouldInstrumentComponent), which the literal pass
// never looks at: a node that is now instrumented has its returned value
// wrapped in the generated decorator, and one that is no longer instrumented
// (it gained `// forge:no-observe`, so its middleware_gen.go is gone) has the
// wrapper call removed. Every edit is an insertion or a cut OUTSIDE the Deps
// literal, so it never overlaps graphLiteralEdits'.
//
// The shapes renderGraphMethod writes are recognised: `return call, nil`,
// `return call` (a fallible constructor) and `return alias.MW(svc), nil`. A
// method in any other shape is the author's and is left alone.
func graphWrapEdits(off func(token.Pos) int, fd *ast.FuncDecl, n *graphNode) []graphEdit {
	if n.Comp == nil || fd.Body == nil {
		return nil
	}
	alias := n.importAlias()
	var wrappers []*ast.CallExpr
	ast.Inspect(fd.Body, func(node ast.Node) bool {
		if _, isLit := node.(*ast.FuncLit); isLit {
			return false
		}
		if call, ok := node.(*ast.CallExpr); ok {
			if _, isWrapper := wrapperArg(call, alias); isWrapper {
				wrappers = append(wrappers, call)
			}
		}
		return true
	})
	switch {
	case !n.Wrapped && len(wrappers) > 0:
		var edits []graphEdit
		for _, call := range wrappers {
			arg := call.Args[0]
			edits = append(edits,
				graphEdit{start: off(call.Pos()), end: off(arg.Pos())},
				graphEdit{start: off(arg.End()), end: off(call.End())})
		}
		return edits
	case n.Wrapped && n.MiddlewareCtor != "" && len(wrappers) == 0:
		// Only a method whose body is the single return statement
		// renderGraphMethod writes for an unwrapped node.
		if len(fd.Body.List) != 1 {
			return nil
		}
		ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
		if !ok {
			return nil
		}
		mw := alias + "." + n.MiddlewareCtor
		switch len(ret.Results) {
		case 2: // return pkg.New(...), nil
			v := ret.Results[0]
			return []graphEdit{
				{start: off(v.Pos()), end: off(v.Pos()), text: mw + "("},
				{start: off(v.End()), end: off(v.End()), text: ")"},
			}
		case 1: // return pkg.New(...) — fallible
			v := ret.Results[0]
			if _, isCall := v.(*ast.CallExpr); !isCall {
				return nil
			}
			return []graphEdit{
				{start: off(ret.Pos()), end: off(v.Pos()), text: "svc, err := "},
				{start: off(v.End()), end: off(v.End()), text: "\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn " + mw + "(svc), nil"},
			}
		}
	}
	return nil
}

// reconcileGraphCompose returns compose.go reconciled against nodes, the
// keys it could not resolve, and an error when the file cannot be parsed or
// the result does not format.
//
// With problems, the returned source is a DRAFT — every key that did resolve,
// none that did not — which is never written: the caller reads the edges it
// draws to decide which nodes a root reaches (reconcileGraphComposeFile).
func reconcileGraphCompose(existing string, nodes []*graphNode, r *graphResolver) (string, []graphProblem, error) {
	src := existing
	if strings.TrimSpace(src) == "" {
		src = graphComposeHeader
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "compose.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return "", nil, fmt.Errorf("internal/app/compose.go does not parse, so forge cannot reconcile it (it is yours — fix it and re-run): %w", err)
	}
	imports := fileImportAliases(file)
	methods := graphComposeMethods(file)
	tf := fset.File(file.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }

	var (
		edits      []graphEdit
		added      []string
		problems   []graphProblem
		needImport = map[string]string{}
	)
	addImports := func(m map[string]string) {
		for a, p := range m {
			needImport[a] = p
		}
	}
	for _, n := range nodes {
		fd := methods["new"+n.Name]
		if fd == nil {
			var assigns []graphAssign
			for _, df := range n.Deps {
				res, prob := r.resolve(n, df)
				if prob != nil {
					problems = append(problems, *prob)
					continue
				}
				addImports(res.Imports)
				assigns = append(assigns, graphAssign{Field: df.Name, Expr: res.Expr, Comment: res.Comment})
			}
			addImports(n.Imports)
			added = append(added, renderGraphMethod(n, assigns))
			continue
		}
		lit := findDepsLiteral(fd, n, imports)
		if lit == nil {
			continue // the author's own shape: left alone
		}
		litEdits, litProblems := graphLiteralEdits(src, off, lit, n, r, addImports)
		edits = append(edits, litEdits...)
		problems = append(problems, litProblems...)
		edits = append(edits, graphWrapEdits(off, fd, n)...)
	}

	// Applied back to front. Two insertions at one offset (a literal's last
	// element ends at its closing brace: the trailing comma and the added
	// keys) land in the order they were appended: the later one is applied
	// first, so it ends up after the earlier one in the text.
	for i, j := 0, len(edits)-1; i < j; i, j = i+1, j-1 {
		edits[i], edits[j] = edits[j], edits[i]
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := src
	for _, e := range edits {
		out = out[:e.start] + e.text + out[e.end:]
	}
	if len(added) > 0 {
		out = strings.TrimRight(out, "\n") + "\n" + strings.Join(added, "")
	}
	formatted, err := fixGraphComposeImports(out, needImport, r)
	if err != nil {
		return "", nil, err
	}
	return formatted, problems, nil
}

// fixGraphComposeImports adds the imports the reconciled text names, drops a
// forge-known import it no longer uses (a dropped key's package), and
// formats the result.
func fixGraphComposeImports(out string, needImport map[string]string, r *graphResolver) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "compose.go", out, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return "", fmt.Errorf("reconciled internal/app/compose.go does not parse (a forge bug — please report it): %w\n%s", err, out)
	}
	have := fileImportAliases(file)
	aliases := make([]string, 0, len(needImport))
	for a := range needImport {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		p := needImport[a]
		if have[a] == p {
			continue
		}
		name := a
		if path.Base(p) == a {
			name = ""
		}
		astutil.AddNamedImport(fset, file, name, p)
	}
	known := map[string]bool{"time": true, "github.com/oklog/ulid/v2": true, r.module + "/" + r.store.dir: true}
	for _, n := range r.nodes {
		for _, p := range n.Imports {
			known[p] = true
		}
	}
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if !known[p] {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if !identUsedAsPackage(file, name) {
			astutil.DeleteNamedImport(fset, file, nameOrEmpty(imp), p)
		}
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return "", fmt.Errorf("format reconciled internal/app/compose.go: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("format reconciled internal/app/compose.go: %w", err)
	}
	return string(formatted), nil
}

func nameOrEmpty(imp *ast.ImportSpec) string {
	if imp.Name == nil {
		return ""
	}
	return imp.Name.Name
}

// identUsedAsPackage reports whether name is the X of any selector in file.
func identUsedAsPackage(file *ast.File, name string) bool {
	used := false
	ast.Inspect(file, func(n ast.Node) bool {
		if used {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				used = true
			}
		}
		return true
	})
	return used
}

// graphComposeMethods indexes compose.go's methods on *Graph by name.
func graphComposeMethods(file *ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || fd.Body == nil {
			continue
		}
		star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && id.Name == "Graph" {
			out[fd.Name.Name] = fd
		}
	}
	return out
}

// findDepsLiteral finds the composite literal of n's Deps type in fd.
func findDepsLiteral(fd *ast.FuncDecl, n *graphNode, imports map[string]string) *ast.CompositeLit {
	var found *ast.CompositeLit
	ast.Inspect(fd.Body, func(x ast.Node) bool {
		if found != nil {
			return false
		}
		lit, ok := x.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch t := lit.Type.(type) {
		case *ast.SelectorExpr:
			if id, ok := t.X.(*ast.Ident); ok && n.DepsImport != "" && imports[id.Name] == n.DepsImport && n.DepsType[strings.LastIndex(n.DepsType, ".")+1:] == t.Sel.Name {
				found = lit
			}
		case *ast.Ident:
			if n.DepsImport == "" && t.Name == n.DepsType {
				found = lit
			}
		}
		return true
	})
	return found
}

// graphComposeEdges reads each node's dependencies off compose.go: the nodes
// its new<X> method calls an accessor of. These — the author's expressions —
// are the edges the cycle check and the lifecycle plan use.
func graphComposeEdges(src string, nodes []*graphNode) map[string][]string {
	edges, _ := graphComposeEdgesLazy(src, nodes)
	return edges
}

// graphComposeEdgesLazy is graphComposeEdges plus the LAZY edges: an
// accessor named as a method value (g.X, not called), which constructs
// nothing when the method runs.
func graphComposeEdgesLazy(src string, nodes []*graphNode) (map[string][]string, map[string][]string) {
	out := map[string][]string{}
	lazy := map[string][]string{}
	file, err := parser.ParseFile(token.NewFileSet(), "compose.go", src, parser.SkipObjectResolution)
	if err != nil {
		return out, lazy
	}
	isNode := map[string]bool{}
	for _, n := range nodes {
		isNode[n.Name] = true
	}
	for name, fd := range graphComposeMethods(file) {
		x, ok := strings.CutPrefix(name, "new")
		if !ok || !isNode[x] {
			continue
		}
		recv := ""
		if len(fd.Recv.List[0].Names) == 1 {
			recv = fd.Recv.List[0].Names[0].Name
		}
		seen := map[string]bool{}
		called := map[*ast.SelectorExpr]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					called[sel] = true
				}
			}
			return true
		})
		lazySeen := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != recv || !isNode[sel.Sel.Name] {
				return true
			}
			name := sel.Sel.Name
			switch {
			case called[sel] && !seen[name]:
				seen[name] = true
				out[x] = append(out[x], name)
			case !called[sel] && !lazySeen[name]:
				lazySeen[name] = true
				lazy[x] = append(lazy[x], name)
			}
			return true
		})
	}
	return out, lazy
}

// graphCycle returns one dependency cycle among edges, as the node path that
// closes it (a → b → a), or nil.
func graphCycle(nodes []*graphNode, edges map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range edges[n] {
			switch color[d] {
			case grey:
				for i, s := range stack {
					if s == d {
						cycle = append(append([]string(nil), stack[i:]...), d)
						return true
					}
				}
			case white:
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range nodes {
		if color[n.Name] == white && visit(n.Name) {
			return cycle
		}
	}
	return nil
}

// composeMethodNames returns X for every new<X> method on *Graph in src.
func composeMethodNames(src string) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "compose.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for name := range graphComposeMethods(file) {
		if x, ok := strings.CutPrefix(name, "new"); ok && x != "" && ast.IsExported(x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
