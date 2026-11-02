// File: internal/codegen/composition_graph_process_reads.go
//
// What the PROCESS POLICY reads of each config (docs/design/composition-graph.md
// §9.4). A root's config reach is what its closure reads through compose.go
// AND what runRoot — the owned process policy in cmd/<bin>/cmd/serve.go —
// reads for every root before it builds one: the listen and probe address,
// the drain budget, logging, telemetry, the database settings. serve.go is
// yours, so which fields those are is whatever it reads, not a list forge
// keeps: a project that moves telemetry into an `observability` block of a
// subtree config, or adds a PROMETHEUS_ENABLED of its own, reaches exactly
// those fields in every root, in ROOTS, Roots() and LoadReach alike.
//
// The reads are taken off the source the same way compose.go's are, by
// syntax, and from the same selector rules (cfgSelectorPath): runRoot is
// walked from its body, through every package-level function of the command
// tree it calls, following each config value it hands one:
//
//   - `cfg, err := config.LoadReach(...)` (or config.Load) is the root
//     config, g.Cfg's value;
//   - runRoot's rootSpec parameter's Configs (`root.Configs`) is every
//     config its command loaded beside it, `root.Configs.<Msg>` one of them;
//   - `x := <config value>` and an argument bound to a parameter of a
//     function in the package carry the value on, selector path and all
//     (`observabilitySettings(process.GetObservability(), cfg)` reads, in
//     the callee, the `observability` block's leaves);
//   - every other use of a config value is a read of what it names:
//     `cfg.GetLog().GetFormat()` is log.format, a block handed to a function
//     forge does not read is the whole block, a config handed on whole is
//     every field of it. Comparing one with nil reads nothing, and handing
//     it to the graph (`app.Graph{Cfg: cfg}`, `g.Cfg = cfg`) reads nothing
//     either: what the graph reads is compose.go's to say.
//
// A database field (databaseConfigFields) runRoot reads is in the reach only
// of a root whose process opens the database (graphRootView.opensDB): the
// policy reads the URL and the pool tuning for whichever root migrates or
// connects, and a worker that never opens the database must start without
// its credential.
//
// A command tree with no runRoot (a serve.go from before the per-root
// commands, or none yet) falls back to the fields the scaffolded serve.go
// reads, processConfigFields and databaseConfigFields;
// TestProcessReads_TheScaffoldedServeReadsTheServeFields pins the two to
// the template.

package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bindKind is what a name in runRoot's call tree holds.
type bindKind int

const (
	// bindConfig: a config message, or a part of one (input, path).
	bindConfig bindKind = iota
	// bindBundle: rootConfigs, every config the command loaded beside the
	// root config.
	bindBundle
	// bindSpec: the rootSpec runRoot is handed, whose Configs is the bundle.
	bindSpec
)

// cfgBinding is one name's config value.
type cfgBinding struct {
	kind bindKind
	// input is the graph input the value is read through: rootConfigInput
	// for the root config, a config input's message name for the others.
	input string
	// path is the Go selector path from the config message.
	path []string
}

func (c cfgBinding) with(sel string) cfgBinding {
	return cfgBinding{kind: c.kind, input: c.input, path: append(append([]string(nil), c.path...), sel)}
}

func (c cfgBinding) String() string {
	return string(rune('0'+c.kind)) + ":" + c.input + ":" + strings.Join(c.path, ".")
}

// processReadAnalyzer walks runRoot's call tree.
type processReadAnalyzer struct {
	funcs  map[string]*ast.FuncDecl
	inputs map[string]bool
	reads  nodeConfigReads
	seen   map[string]bool
}

// runRootConfigReads is what the primary command tree's runRoot reads of
// each config, keyed like nodeConfigReads; nil when the tree declares no
// runRoot (or the project has no command tree).
func runRootConfigReads(projectDir string, inputs map[string]bool) nodeConfigReads {
	treeDir := primaryCmdTreeDir(projectDir)
	if treeDir == "" {
		return nil
	}
	treeAbs := filepath.Join(projectDir, treeDir)
	entries, err := os.ReadDir(treeAbs)
	if err != nil {
		return nil
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == rootsGenFile {
			continue
		}
		file, perr := parser.ParseFile(token.NewFileSet(), filepath.Join(treeAbs, n), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		files = append(files, file)
	}
	return runRootConfigReadsOf(files, inputs)
}

// runRootConfigReadsOf is runRootConfigReads over parsed files of one
// package.
func runRootConfigReadsOf(files []*ast.File, inputs map[string]bool) nodeConfigReads {
	a := &processReadAnalyzer{funcs: map[string]*ast.FuncDecl{}, inputs: inputs, reads: nodeConfigReads{}, seen: map[string]bool{}}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				a.funcs[fd.Name.Name] = fd
			}
		}
	}
	fd := a.funcs["runRoot"]
	if fd == nil {
		return nil
	}
	b := map[string]cfgBinding{}
	for _, p := range fd.Type.Params.List {
		if id, ok := p.Type.(*ast.Ident); ok && id.Name == "rootSpec" {
			for _, n := range p.Names {
				b[n.Name] = cfgBinding{kind: bindSpec}
			}
		}
	}
	a.walkFunc(fd, b)
	return a.reads
}

// walkFunc walks fd's body under the bindings its parameters were handed,
// once per distinct set of them.
func (a *processReadAnalyzer) walkFunc(fd *ast.FuncDecl, b map[string]cfgBinding) {
	keys := make([]string, 0, len(b))
	for k, v := range b {
		keys = append(keys, k+"="+v.String())
	}
	sort.Strings(keys)
	key := fd.Name.Name + "(" + strings.Join(keys, ",") + ")"
	if a.seen[key] {
		return
	}
	a.seen[key] = true
	a.walk(fd.Body, maps.Clone(b))
}

// chain resolves e when it is a config value: a bound name, and the
// selectors and Get<X>() calls applied to it.
func (a *processReadAnalyzer) chain(e ast.Expr, b map[string]cfgBinding) (cfgBinding, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		c, ok := b[x.Name]
		return c, ok
	case *ast.ParenExpr:
		return a.chain(x.X, b)
	case *ast.SelectorExpr:
		base, ok := a.chain(x.X, b)
		if !ok {
			return cfgBinding{}, false
		}
		switch base.kind {
		case bindSpec:
			if x.Sel.Name == "Configs" {
				return cfgBinding{kind: bindBundle}, true
			}
			return cfgBinding{}, false
		case bindBundle:
			if a.inputs[x.Sel.Name] {
				return cfgBinding{kind: bindConfig, input: x.Sel.Name}, true
			}
			return cfgBinding{}, false
		}
		return base.with(x.Sel.Name), true
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok || len(x.Args) != 0 || len(sel.Sel.Name) <= 3 || !strings.HasPrefix(sel.Sel.Name, "Get") {
			return cfgBinding{}, false
		}
		base, ok := a.chain(sel.X, b)
		if !ok || base.kind != bindConfig {
			return cfgBinding{}, false
		}
		return base.with(strings.TrimPrefix(sel.Sel.Name, "Get")), true
	}
	return cfgBinding{}, false
}

// record is a read of what c names.
func (a *processReadAnalyzer) record(c cfgBinding) {
	read := func(input string, path []string) {
		r := a.reads[input]
		if r == nil {
			r = &configRead{fields: map[string]bool{}, leaves: map[[2]string]bool{}}
			a.reads[input] = r
		}
		r.read(path)
	}
	if c.kind == bindConfig {
		read(c.input, c.path)
		return
	}
	// The bundle (or the rootSpec holding it) handed on whole: every
	// config in it, whole.
	for in := range a.inputs {
		read(in, nil)
	}
}

// isGraphLiteral reports whether t is app.Graph: a config value handed to
// the graph is the graph's to read.
func isGraphLiteral(t ast.Expr) bool {
	sel, ok := t.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Graph" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "app"
}

// isGraphInputField reports whether e is `<x>.<input>`, a graph input
// field: `g.Cfg = cfg` hands the value to the graph.
func (a *processReadAnalyzer) isGraphInputField(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == rootConfigInput || a.inputs[sel.Sel.Name])
}

// isConfigLoad reports whether e is config.LoadReach(...) or
// config.Load(...): the root config's value.
func isConfigLoad(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "LoadReach" && sel.Sel.Name != "Load") {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "config"
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// walk records every config read under n.
func (a *processReadAnalyzer) walk(n ast.Node, b map[string]cfgBinding) {
	if n == nil {
		return
	}
	ast.Inspect(n, func(x ast.Node) bool {
		if e, ok := x.(ast.Expr); ok {
			if c, ok := a.chain(e, b); ok {
				a.record(c)
				return false
			}
		}
		switch x := x.(type) {
		case *ast.AssignStmt:
			a.assign(x, b)
			return false
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				for _, pair := range [][2]ast.Expr{{x.X, x.Y}, {x.Y, x.X}} {
					if _, ok := a.chain(pair[0], b); ok && isNilIdent(pair[1]) {
						return false // a nil guard reads nothing
					}
				}
			}
		case *ast.CompositeLit:
			graph := isGraphLiteral(x.Type)
			for _, elt := range x.Elts {
				v := elt
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					v = kv.Value
				}
				if _, ok := a.chain(v, b); ok && graph {
					continue
				}
				a.walk(v, b)
			}
			return false
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				if fd := a.funcs[id.Name]; fd != nil {
					a.call(fd, x.Args, b)
					return false
				}
			}
		case *ast.SelectorExpr:
			// x.Sel names a field or method, never one of the bindings. A
			// member of the rootSpec other than Configs (root.Name,
			// root.Config, root.Run) is no config.
			if _, ok := a.chain(x.X, b); !ok {
				a.walk(x.X, b)
			}
			return false
		case *ast.FuncType, *ast.StructType, *ast.InterfaceType:
			// Parameter and field names, never a use of a binding.
			return false
		case *ast.ValueSpec:
			for _, n := range x.Names {
				delete(b, n.Name)
			}
			for _, v := range x.Values {
				a.walk(v, b)
			}
			return false
		case *ast.RangeStmt:
			a.walk(x.X, b)
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					delete(b, id.Name)
				}
			}
			a.walk(x.Body, b)
			return false
		case *ast.LabeledStmt:
			a.walk(x.Stmt, b)
			return false
		case *ast.BranchStmt:
			return false
		}
		return true
	})
}

// assign carries config values into the names they are assigned to, and
// walks everything else.
func (a *processReadAnalyzer) assign(x *ast.AssignStmt, b map[string]cfgBinding) {
	unbind := func(e ast.Expr) {
		if id, ok := e.(*ast.Ident); ok {
			delete(b, id.Name)
			return
		}
		a.walk(e, b)
	}
	if len(x.Lhs) != len(x.Rhs) {
		for _, r := range x.Rhs {
			a.walk(r, b)
		}
		for _, l := range x.Lhs {
			unbind(l)
		}
		if len(x.Rhs) == 1 && isConfigLoad(x.Rhs[0]) {
			if id, ok := x.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
				b[id.Name] = cfgBinding{kind: bindConfig, input: rootConfigInput}
			}
		}
		return
	}
	for i, r := range x.Rhs {
		l := x.Lhs[i]
		c, isCfg := a.chain(r, b)
		if !isCfg {
			a.walk(r, b)
			unbind(l)
			if isConfigLoad(r) {
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
					b[id.Name] = cfgBinding{kind: bindConfig, input: rootConfigInput}
				}
			}
			continue
		}
		switch {
		case a.isGraphInputField(l):
			// Handed to the graph.
		case isIdentNamed(l, "_"):
		default:
			if id, ok := l.(*ast.Ident); ok {
				b[id.Name] = c
				continue
			}
			a.record(c)
			a.walk(l, b)
		}
	}
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// call walks fd with each config value among args bound to its parameter.
func (a *processReadAnalyzer) call(fd *ast.FuncDecl, args []ast.Expr, b map[string]cfgBinding) {
	var params []string
	variadic := false
	for _, p := range fd.Type.Params.List {
		if _, ok := p.Type.(*ast.Ellipsis); ok {
			variadic = true
		}
		if len(p.Names) == 0 {
			params = append(params, "_")
			continue
		}
		for _, n := range p.Names {
			params = append(params, n.Name)
		}
	}
	callee := map[string]cfgBinding{}
	for i, arg := range args {
		c, ok := a.chain(arg, b)
		if !ok {
			a.walk(arg, b)
			continue
		}
		if i >= len(params) || (variadic && i >= len(params)-1) {
			a.record(c) // into a variadic slice: whole
			continue
		}
		if params[i] != "_" {
			callee[params[i]] = c
		}
	}
	a.walkFunc(fd, callee)
}
