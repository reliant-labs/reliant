package slowtest

// git against a repository the test created in a temp dir is not slow work.
//
// `git init` / `git commit` in t.TempDir() is how a test builds a fixture
// repo. It reads nothing the test cache cannot see — the repo is the test's
// own, under a directory it created — and it is usually milliseconds. What is
// slow, or cache-unsound, is git over the CHECKOUT: `git ls-files` from the
// package directory reads the module's files behind the cache's back. So an
// exec of git counts as slow work only when the analyzer cannot show that it
// runs against a temp dir. A temp-dir git that is slow anyway is the timing
// guard's (`forge ci test-budget`) to report.
//
// "Against a temp dir" is read from source, within reach of the test:
//
//   - the command's Dir is temp-derived (`cmd.Dir = dir`), or, with no Dir
//     set, one of its arguments is (`git init <dir>`, `git -C <dir> ...`);
//   - temp-derived means t.TempDir() (on a T, B, F or TB), os.MkdirTemp,
//     filepath.Join / path.Join or `+` whose first operand is temp-derived,
//     or a local variable every assignment of which is;
//   - a struct field set in the same function from a temp-derived value
//     (`f := &fixture{repo: filepath.Join(base, "repo")}` then `f.repo`);
//   - the result of a function of the package every return of which is
//     temp-derived (`dir := newProject(t)`);
//   - through a helper, by parameter: `gitIn(dir string, args ...string)`
//     whose git runs in `dir` is slow at a call site only when the argument
//     passed for `dir` is not temp-derived there. That holds across packages
//     (the slow fact carries the parameter) and through any number of helper
//     hops that pass the directory down.
//
// A directory held in a struct that came from elsewhere, returned by another
// package's helper, or computed any other way is not followed: the exec stays slow work, and the test needs the marker or a
// suppression with a reason.

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// CondParam is slow work a function does unless the argument at Index is a
// temp dir. Exported (with its fields) so analysis facts can encode it.
type CondParam struct {
	Index int
	Why   string
}

// dirClass is what the analyzer knows about a directory expression.
type dirClass struct {
	temp  bool
	param int // index of the enclosing function's parameter it is, or -1
	// cycle: the value refers back to a location still being classified
	// (`dir = filepath.Join(dir, "sub")`). It adds nothing of its own, so a
	// fold ignores it; a location that is ONLY cycles is unknown.
	cycle bool
}

var unknownDir = dirClass{param: -1}

// dirFlow answers dirClass questions inside one function body.
type dirFlow struct {
	pass   *analysis.Pass
	params map[types.Object]int
	// assigns: every expression assigned to a local variable. A nil entry is
	// an assignment the analyzer cannot read (a non-first result of a call).
	assigns map[types.Object][]ast.Expr
	// cmdVar: the *exec.Cmd variable an exec.Command call is assigned to.
	cmdVar map[*ast.CallExpr]types.Object
	// cmdDir: every expression assigned to that variable's Dir field.
	cmdDir map[types.Object][]ast.Expr
	// fields: every expression assigned to a local struct variable's field,
	// by composite literal or by assignment. noFields marks a variable also
	// assigned something else (a call, another variable), whose fields are
	// then unknown.
	fields   map[types.Object]map[string][]ast.Expr
	noFields map[types.Object]bool
	// retTemp: the package's functions that return a temp dir (shared).
	retTemp map[types.Object]bool
}

func newDirFlow(pass *analysis.Pass, fnObj types.Object, root ast.Node, retTemp map[types.Object]bool) *dirFlow {
	f := &dirFlow{
		pass:     pass,
		params:   map[types.Object]int{},
		assigns:  map[types.Object][]ast.Expr{},
		cmdVar:   map[*ast.CallExpr]types.Object{},
		cmdDir:   map[types.Object][]ast.Expr{},
		fields:   map[types.Object]map[string][]ast.Expr{},
		noFields: map[types.Object]bool{},
		retTemp:  retTemp,
	}
	addField := func(v types.Object, name string, e ast.Expr) {
		if f.fields[v] == nil {
			f.fields[v] = map[string][]ast.Expr{}
		}
		f.fields[v][name] = append(f.fields[v][name], e)
	}
	if sig, ok := fnObj.Type().(*types.Signature); ok {
		for i := range sig.Params().Len() {
			f.params[sig.Params().At(i)] = i
		}
	}
	record := func(lhs []ast.Expr, rhs []ast.Expr) {
		for i, l := range lhs {
			var r ast.Expr
			switch {
			case len(rhs) == len(lhs):
				r = rhs[i]
			case len(rhs) == 1 && i == 0:
				r = rhs[0] // the first result of a multi-value call
			}
			if sel, ok := l.(*ast.SelectorExpr); ok {
				v := f.objOf(sel.X)
				switch {
				case v == nil:
				case isExecCmd(v.Type()) && sel.Sel.Name == "Dir":
					f.cmdDir[v] = append(f.cmdDir[v], r)
				default:
					addField(v, sel.Sel.Name, r)
				}
				continue
			}
			v := f.objOf(l)
			if v == nil {
				continue
			}
			f.assigns[v] = append(f.assigns[v], r)
			if lit := structLiteral(r); lit != nil {
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok {
							addField(v, key.Name, kv.Value)
						}
					}
				}
			} else {
				f.noFields[v] = true
			}
			if call, ok := ast.Unparen(r).(*ast.CallExpr); ok && r != nil {
				f.cmdVar[call] = v
			}
		}
	}
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.ASSIGN || n.Tok == token.DEFINE {
				record(n.Lhs, n.Rhs)
			}
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(n.Names))
			for i, name := range n.Names {
				lhs[i] = name
			}
			if len(n.Values) > 0 {
				record(lhs, n.Values)
			}
		}
		return true
	})
	return f
}

func (f *dirFlow) objOf(e ast.Expr) types.Object {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return nil
	}
	if obj := f.pass.TypesInfo.Defs[id]; obj != nil {
		return obj
	}
	return f.pass.TypesInfo.Uses[id]
}

// classify reports whether e is a temp dir, a parameter, or neither.
func (f *dirFlow) classify(e ast.Expr) dirClass {
	c := f.classifySeen(e, map[any]bool{})
	if c.cycle {
		return unknownDir
	}
	return c
}

// fieldKey is a struct field of a local variable, as a location.
type fieldKey struct {
	obj  types.Object
	name string
}

// classifySeen classifies e. seen holds the locations on the current path,
// so a location reached again through itself is a cycle, not a recursion.
func (f *dirFlow) classifySeen(e ast.Expr, seen map[any]bool) dirClass {
	if e == nil {
		return unknownDir
	}
	switch e := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		fn, _ := typeutil.Callee(f.pass.TypesInfo, e).(*types.Func)
		switch {
		case fn == nil || fn.Pkg() == nil:
		case f.retTemp[fn.Origin()]:
			return dirClass{temp: true, param: -1}
		case fn.Pkg().Path() == "testing" && fn.Name() == "TempDir",
			fn.Pkg().Path() == "os" && fn.Name() == "MkdirTemp":
			return dirClass{temp: true, param: -1}
		case (fn.Pkg().Path() == "path/filepath" || fn.Pkg().Path() == "path") && fn.Name() == "Join" && len(e.Args) > 0:
			return f.classifySeen(e.Args[0], seen)
		}
	case *ast.SelectorExpr:
		v := f.objOf(e.X)
		values, ok := f.fields[v][e.Sel.Name]
		if v == nil || !ok || f.noFields[v] {
			return unknownDir
		}
		key := fieldKey{v, e.Sel.Name}
		if seen[key] {
			return dirClass{param: -1, cycle: true}
		}
		seen[key] = true
		defer delete(seen, key)
		classes := make([]dirClass, len(values))
		for i, x := range values {
			classes[i] = f.classifySeen(x, seen)
		}
		return foldAll(classes)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return f.classifySeen(e.X, seen)
		}
	case *ast.Ident:
		obj := f.pass.TypesInfo.Uses[e]
		if i, ok := f.params[obj]; ok {
			return dirClass{param: i}
		}
		exprs, ok := f.assigns[obj]
		if !ok {
			return unknownDir
		}
		if seen[obj] {
			return dirClass{param: -1, cycle: true}
		}
		seen[obj] = true
		defer delete(seen, obj)
		classes := make([]dirClass, len(exprs))
		for i, x := range exprs {
			classes[i] = f.classifySeen(x, seen)
		}
		return foldAll(classes)
	}
	return unknownDir
}

// foldAll folds the classes of every value one location can hold: temp when
// all are temp, parameter p when each is temp or p, otherwise unknown.
// Cycles are ignored; a location holding nothing else is itself a cycle.
func foldAll(classes []dirClass) dirClass {
	param := -1
	nonCycle := 0
	for _, c := range classes {
		if c.cycle {
			continue
		}
		nonCycle++
		switch {
		case c.temp:
		case c.param < 0, param >= 0 && param != c.param:
			return unknownDir
		default:
			param = c.param
		}
	}
	switch {
	case nonCycle == 0:
		return dirClass{param: -1, cycle: true}
	case param >= 0:
		return dirClass{param: param}
	}
	return dirClass{temp: true, param: -1}
}

// classifyAny folds several expressions any one of which may name the
// directory (exec arguments): temp if one is temp, else the first parameter.
func (f *dirFlow) classifyAny(exprs []ast.Expr) dirClass {
	out := unknownDir
	for _, e := range exprs {
		c := f.classify(e)
		if c.temp {
			return c
		}
		if c.param >= 0 && out.param < 0 {
			out = c
		}
	}
	return out
}

// flatten expands a spread argument into the expressions it is built from:
// `append([]string{"-C", dir}, args...)` is "-C", dir and args.
func (f *dirFlow) flatten(e ast.Expr) []ast.Expr {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return e.Elts
	case *ast.CallExpr:
		if id, ok := ast.Unparen(e.Fun).(*ast.Ident); ok && id.Name == "append" {
			if _, builtin := f.pass.TypesInfo.Uses[id].(*types.Builtin); builtin {
				var out []ast.Expr
				for _, a := range e.Args {
					out = append(out, f.flatten(a)...)
				}
				return out
			}
		}
	}
	return []ast.Expr{e}
}

// gitScope classifies where an exec of git (call, binary at argument binIdx)
// runs: in its Dir when one is assigned, otherwise wherever its arguments
// point it.
func (f *dirFlow) gitScope(call *ast.CallExpr, binIdx int) dirClass {
	if v, ok := f.cmdVar[call]; ok {
		if dirs := f.cmdDir[v]; len(dirs) > 0 {
			classes := make([]dirClass, len(dirs))
			for i, d := range dirs {
				classes[i] = f.classify(d)
			}
			return foldAll(classes)
		}
	}
	var args []ast.Expr
	for i, a := range call.Args[min(binIdx+1, len(call.Args)):] {
		if call.Ellipsis.IsValid() && binIdx+1+i == len(call.Args)-1 {
			args = append(args, f.flatten(a)...)
			continue
		}
		args = append(args, a)
	}
	return f.classifyAny(args)
}

// argClasses classifies, for each parameter of the callee sig, the argument
// call passes for it. A variadic parameter folds every argument it receives.
func (f *dirFlow) argClasses(sig *types.Signature, call *ast.CallExpr) []dirClass {
	n := sig.Params().Len()
	out := make([]dirClass, n)
	for i := range n {
		out[i] = unknownDir
		if i >= len(call.Args) {
			continue
		}
		if sig.Variadic() && i == n-1 {
			rest := call.Args[i:]
			if call.Ellipsis.IsValid() {
				rest = f.flatten(call.Args[i])
			}
			out[i] = f.classifyAny(rest)
			continue
		}
		out[i] = f.classify(call.Args[i])
	}
	return out
}

// applyCond carries a callee's parameter-conditional slowness to the caller
// n at one call site. It reports whether n changed.
func applyCond(n *node, callee string, cond []CondParam, classes []dirClass) bool {
	changed := false
	for _, c := range cond {
		cls := unknownDir
		if c.Index < len(classes) {
			cls = classes[c.Index]
		}
		why := "calls " + callee + ", which " + c.Why
		switch {
		case cls.temp:
		case cls.param >= 0:
			if n.cond == nil {
				n.cond = map[int]string{}
			}
			if n.cond[cls.param] == "" {
				n.cond[cls.param] = why
				changed = true
			}
		case n.slow == "":
			n.slow = why
			changed = true
		}
	}
	return changed
}

// condList is n's conditional slowness in parameter order.
func condList(cond map[int]string) []CondParam {
	out := make([]CondParam, 0, len(cond))
	for i, why := range cond {
		out = append(out, CondParam{Index: i, Why: why})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Index < out[b].Index })
	return out
}

// structLiteral is the composite literal e is, or takes the address of.
func structLiteral(e ast.Expr) *ast.CompositeLit {
	e = ast.Unparen(e)
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = ast.Unparen(u.X)
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

// returnsTemp reports whether every return statement of body (its own, not
// a nested function literal's) yields a temp dir as its first result.
func (f *dirFlow) returnsTemp(body ast.Node) bool {
	sawReturn := false
	all := true
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			sawReturn = true
			if len(n.Results) == 0 || !f.classify(n.Results[0]).temp {
				all = false
			}
		}
		return all
	})
	return sawReturn && all
}

// tempReturners computes which functions of the package return a temp dir,
// to a fixed point (one may return another's result).
func tempReturners(pass *analysis.Pass, nodes map[types.Object]*node, bodies map[types.Object]ast.Node) map[types.Object]bool {
	ret := map[types.Object]bool{}
	for changed := true; changed; {
		changed = false
		for obj, body := range bodies {
			if ret[obj] || !nodes[obj].followed {
				continue
			}
			if _, isFunc := obj.(*types.Func); !isFunc {
				continue
			}
			if newDirFlow(pass, obj, body, ret).returnsTemp(body) {
				ret[obj] = true
				changed = true
			}
		}
	}
	return ret
}

func isExecCmd(t types.Type) bool {
	named, ok := types.Unalias(derefType(t)).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "os/exec" && named.Obj().Name() == "Cmd"
}
