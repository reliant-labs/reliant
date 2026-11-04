// Package slowtest is the static half of forge's fast/slow test tiers: an
// analyzer that flags a Go test doing slow work without being marked slow,
// and that reads the inputs a slow test declares, from source.
//
// # THE TIERS IT GUARDS
//
// A test is fast — it runs in the blocking `go test -short` tier — unless it
// is marked slow, with [testkit.Slow] or a plain `if testing.Short() {
// t.Skip(...) }`. Marking is enforced two ways, because neither catches
// everything:
//
//   - MEASURED: `forge ci test-budget` reads a -short `go test -json` run and
//     flags every test that took longer than the budget. It catches a slow
//     test whatever makes it slow, but only after it ran slowly.
//   - STATIC (this package): a test that does work KNOWN to be slow is
//     flagged before it ever runs. It is also the only guard for the second
//     reason the marker exists: a file read by a child process (`go build`
//     over the repo, `kcl` over deploy/, `git` over the checkout) is
//     invisible to Go's test cache, so an unmarked test that execs one can
//     replay a stale `ok (cached)` after the file it reads was broken. That
//     test can be FAST and still wrong in the cached tier.
//
// # WHAT IS SLOW
//
//   - exec of a toolchain or a container runtime by a constant name:
//     go, forge, kcl, node, npm, npx, pnpm, yarn, bun, git, docker, podman,
//     kubectl, k3d, kind, helm, buf, terraform (os/exec.Command and
//     CommandContext);
//   - a call into a container library: testcontainers-go, dockertest, the
//     docker / moby client;
//   - a dial to a real external host by a constant address: net.Dial,
//     net.DialTimeout, http.Get / Head / Post / PostForm (and the
//     *http.Client methods). Loopback and the reserved .test / .example /
//     .invalid / .localhost names are not external.
//
// forge/pkg/pgtest is deliberately NOT on the list. It leases a database on
// ONE shared server per test run (per-test databases are template clones),
// which is the harness forge scaffolds every CRUD test onto and means to run
// in the fast tier. A database test that is genuinely slow is the timing
// guard's to catch.
//
// # REACH: TEST CODE, NOT PRODUCTION CODE
//
// A test is judged on the TEST CODE it reaches: its own body and function
// literals, and every function it calls or references (a helper passed to
// t.Run counts) that is itself test code — declared in a _test.go file, in a
// file that imports "testing" (a helper file such as testkit's, or a
// project's rendertest), or taking a *testing.T / B / F or testing.TB. Test
// code in another package reaches it through analysis facts, so a shared
// helper package that execs forge taints the tests that call it.
//
// Production code is NOT read through. Measured on forge's own suite,
// following it reported 2,242 tests through call chains up to 23 deep —
// `git` behind a ledger lookup, `docker` behind a promote preflight — almost
// all of them on branches the test never takes. What production code does
// at runtime is what the timing guard measures; what a test's own code
// visibly does is what this one reads.
//
// "Marked" is anything that calls testing.Short: testkit.Slow does, and so
// does the plain `if testing.Short() { t.Skip(...) }` guard, so existing
// suites pass without a migration. A helper that marks the test (testkit.Slow
// inside it, or control-plane's testlane.Slow) protects every test that
// calls it. A testing.Short check in TestMain covers its package.
//
// Every ambiguity resolves to silence. A binary named by a variable, an
// address computed at runtime, an interface call: none is reported. A rule
// that invents findings gets switched off, and the timing guard backstops
// what this one cannot see.
//
// # INPUTS
//
// testkit.Slow(t, why, testkit.Inputs("deploy/**")) declares the files a slow
// test depends on. The analyzer returns them per test ([Result]) for a tool
// that selects tests by what a change touched, and reports an Inputs argument
// that is not a constant string: a glob only a test run could compute is one
// no selection tool can read.
//
//forge:exclude-contract: a go/analysis analyzer; its surface is the Analyzer value and the Result data it returns
package slowtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// Rule ids, as `forge lint` reports them.
const (
	// RuleUnmarked: a test reaches slow work and nothing on its path checks
	// testing.Short.
	RuleUnmarked = "forge-slow-test-unmarked"
	// RuleInputs: a testkit.Inputs argument the analyzer cannot read.
	RuleInputs = "forge-slow-test-inputs"
)

// testkitPath is the package whose Slow and Inputs the analyzer reads.
const testkitPath = "github.com/reliant-labs/forge/pkg/testkit"

// Analyzer flags unmarked slow tests and extracts slow-test declarations.
var Analyzer = &analysis.Analyzer{
	Name: "slowtest",
	Doc: "flags a test that execs a toolchain, starts a container or dials an external host without being marked slow " +
		"(testkit.Slow, or an if testing.Short() guard), and reads the inputs slow tests declare",
	Run:        run,
	FactTypes:  []analysis.Fact{new(slowFact), new(markFact)},
	ResultType: reflect.TypeOf((*Result)(nil)),
}

// Result is what the analyzer knows about one package's slow tests.
type Result struct {
	// Tests lists every marked test of the package, in source order.
	Tests []SlowTest
}

// SlowTest is one marked test and what it declares.
type SlowTest struct {
	Name     string
	Position token.Position
	// Declarations are the testkit.Slow calls the test reaches, its own or a
	// helper's. Empty for a test marked only by a raw testing.Short guard: it
	// declares no reason and no inputs, so a selection tool can only leave
	// it to the full tier.
	Declarations []Declaration
}

// Declaration is one testkit.Slow call.
type Declaration struct {
	// Why is the reason, when it is a constant string.
	Why string
	// Inputs are the testkit.Inputs globs, module-root-relative.
	Inputs []string
	// At is "file.go:line" of the call.
	At string
}

// slowFact is exported for a function that reaches slow work without
// checking testing.Short on the way.
// Cond is slow work it does unless the argument for a parameter is a temp
// dir (see tempdir.go).
type slowFact struct {
	Why  string
	Cond []CondParam
}

func (*slowFact) AFact() {}
func (f *slowFact) String() string {
	s := ""
	if f.Why != "" {
		s = "slow: " + f.Why
	}
	for _, c := range f.Cond {
		if s != "" {
			s += "; "
		}
		s += fmt.Sprintf("slow unless arg %d is a temp dir: %s", c.Index, c.Why)
	}
	return s
}

// markFact is exported for a function that checks testing.Short, directly or
// through a callee: calling it marks the caller. Decls carries the
// testkit.Slow declarations it makes, for the inventory.
type markFact struct{ Decls []Declaration }

func (*markFact) AFact()         {}
func (*markFact) String() string { return "marks slow" }

// node is one function of the package (or a package-level var whose
// initializer holds code, such as a sync.OnceValue closure).
type node struct {
	obj  types.Object
	name string
	// followed: the node is test code, so the analyzer reads through it
	// (see testCode). A production function is still a node — a test may
	// call it — but its reach is the timing guard's to measure.
	followed bool
	marked   bool
	// slow is why the node is slow ("execs `go` at x.go:12"), "" if it is not.
	slow string
	// cond is slow work the node does unless the argument for parameter i
	// is a temp dir (tempdir.go): git in a directory the caller chooses.
	cond    map[int]string
	decls   []Declaration
	callees []types.Object
	// calls are the node's calls to other functions of the package, with
	// what it passes for each parameter, for carrying cond up.
	calls []callSite
}

// callSite is one call to a function of the package.
type callSite struct {
	callee  types.Object
	classes []dirClass
}

func run(pass *analysis.Pass) (any, error) {
	nodes := map[types.Object]*node{}
	var order []types.Object
	type body struct {
		obj  types.Object
		root ast.Node
	}
	var bodies []body
	for _, file := range pass.Files {
		testCode := isTestFile(pass, file) || importsTesting(file)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				obj := pass.TypesInfo.Defs[d.Name]
				if obj == nil || d.Body == nil {
					continue
				}
				nodes[obj] = &node{obj: obj, name: d.Name.Name, followed: testCode || takesTesting(obj)}
				order = append(order, obj)
				bodies = append(bodies, body{obj, d.Body})
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						obj := pass.TypesInfo.Defs[name]
						if obj == nil {
							continue
						}
						nodes[obj] = &node{obj: obj, name: name.Name, followed: testCode}
						order = append(order, obj)
						bodies = append(bodies, body{obj, vs.Values[i]})
					}
				}
			}
		}
	}

	bodyOf := map[types.Object]ast.Node{}
	for _, b := range bodies {
		bodyOf[b.obj] = b.root
	}
	retTemp := tempReturners(pass, nodes, bodyOf)
	for _, b := range bodies {
		if nodes[b.obj].followed {
			scan(pass, nodes, nodes[b.obj], b.root, retTemp)
		}
	}
	propagate(nodes, order)

	// Facts are for what another package can call: exported functions and
	// vars of the package proper. A test file's helpers are visible to no
	// importer, and exporting facts for them would only cost encoding.
	for _, obj := range order {
		n := nodes[obj]
		if !obj.Exported() || isTestFile(pass, obj) {
			continue
		}
		switch {
		case n.marked:
			pass.ExportObjectFact(obj, &markFact{Decls: reachableDecls(nodes, obj)})
		case n.slow != "" || len(n.cond) > 0:
			pass.ExportObjectFact(obj, &slowFact{Why: n.slow, Cond: condList(n.cond)})
		}
	}

	return report(pass, nodes), nil
}

// scan reads one body: its own markers, slow operations, testkit.Slow
// declarations, and the functions it reaches.
func scan(pass *analysis.Pass, nodes map[types.Object]*node, n *node, root ast.Node, retTemp map[types.Object]bool) {
	seen := map[types.Object]bool{}
	flow := newDirFlow(pass, n.obj, root, retTemp)
	ast.Inspect(root, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.CallExpr:
			fn, _ := typeutil.Callee(pass.TypesInfo, x).(*types.Func)
			if fn == nil {
				return true
			}
			fn = fn.Origin()
			sig, _ := fn.Type().(*types.Signature)
			if _, local := nodes[fn]; local && sig != nil {
				n.calls = append(n.calls, callSite{callee: fn, classes: flow.argClasses(sig, x)})
			} else if fn.Pkg() != nil && fn.Pkg() != pass.Pkg && sig != nil {
				var slow slowFact
				if pass.ImportObjectFact(fn, &slow) && len(slow.Cond) > 0 {
					applyCond(n, qualifiedName(fn), slow.Cond, flow.argClasses(sig, x))
				}
			}
			if bin, idx, ok := execBinary(pass, fn, x); ok && bin == "git" {
				why := fmt.Sprintf("execs %#q at %s", bin, shortPos(pass, x.Pos()))
				switch c := flow.gitScope(x, idx); {
				case c.temp:
				case c.param >= 0:
					if n.cond == nil {
						n.cond = map[int]string{}
					}
					if n.cond[c.param] == "" {
						n.cond[c.param] = why
					}
				case n.slow == "":
					n.slow = why
				}
				return true
			}
			switch {
			case isFunc(fn, "testing", "Short"):
				n.marked = true
			case isFunc(fn, testkitPath, "Slow"):
				n.marked = true
				n.decls = append(n.decls, readSlowCall(pass, x))
			}
			if why := slowOperation(pass, fn, x); why != "" && n.slow == "" {
				n.slow = why + " at " + shortPos(pass, x.Pos())
			}
		case *ast.Ident:
			obj := pass.TypesInfo.Uses[x]
			if fn, ok := obj.(*types.Func); ok {
				obj = fn.Origin()
			}
			if obj == nil || obj == n.obj || seen[obj] {
				return true
			}
			seen[obj] = true
			if _, local := nodes[obj]; local {
				n.callees = append(n.callees, obj)
				return true
			}
			fn, ok := obj.(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg() == pass.Pkg {
				return true
			}
			var mark markFact
			if pass.ImportObjectFact(fn, &mark) {
				n.marked = true
				n.decls = append(n.decls, mark.Decls...)
				return true
			}
			var slow slowFact
			if pass.ImportObjectFact(fn, &slow) && slow.Why != "" && n.slow == "" {
				n.slow = "calls " + qualifiedName(fn) + ", which " + slow.Why
			}
		}
		return true
	})
}

// propagate closes marks and slowness over the package's call graph. A
// marked callee marks its caller (the skip runs inside it). A slow callee
// makes its caller slow unless the callee is marked, since then the slow work
// sits behind its own testing.Short check.
func propagate(nodes map[types.Object]*node, order []types.Object) {
	for changed := true; changed; {
		changed = false
		for _, obj := range order {
			n := nodes[obj]
			for _, c := range n.callees {
				callee := nodes[c]
				if callee.marked && !n.marked {
					n.marked = true
					changed = true
				}
				if !callee.marked && callee.slow != "" && n.slow == "" {
					n.slow = "calls " + callee.name + ", which " + callee.slow
					changed = true
				}
			}
			for _, cs := range n.calls {
				if callee := nodes[cs.callee]; !callee.marked && len(callee.cond) > 0 &&
					applyCond(n, callee.name, condList(callee.cond), cs.classes) {
					changed = true
				}
			}
		}
	}
}

// reachableDecls is every testkit.Slow declaration reachable from obj.
func reachableDecls(nodes map[types.Object]*node, obj types.Object) []Declaration {
	var out []Declaration
	seenDecl := map[string]bool{}
	visited := map[types.Object]bool{obj: true}
	queue := []types.Object{obj}
	for len(queue) > 0 {
		n := nodes[queue[0]]
		queue = queue[1:]
		for _, d := range n.decls {
			key := d.At + "|" + d.Why
			if !seenDecl[key] {
				seenDecl[key] = true
				out = append(out, d)
			}
		}
		for _, c := range n.callees {
			if !visited[c] {
				visited[c] = true
				queue = append(queue, c)
			}
		}
	}
	return out
}

// report emits a diagnostic for every unmarked slow test and returns the
// inventory of marked ones.
func report(pass *analysis.Pass, nodes map[types.Object]*node) *Result {
	result := &Result{}
	packageMarked := false
	var tests []*ast.FuncDecl
	for _, file := range pass.Files {
		filename := pass.Fset.File(file.Pos()).Name()
		if !strings.HasSuffix(filename, "_test.go") || ast.IsGenerated(file) {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			if fd.Name.Name == "TestMain" {
				if n := nodes[pass.TypesInfo.Defs[fd.Name]]; n != nil && n.marked {
					packageMarked = true
				}
				continue
			}
			if isTestFunc(pass, fd) {
				tests = append(tests, fd)
			}
		}
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].Pos() < tests[j].Pos() })
	for _, fd := range tests {
		obj := pass.TypesInfo.Defs[fd.Name]
		n := nodes[obj]
		if n == nil {
			continue
		}
		if n.marked {
			result.Tests = append(result.Tests, SlowTest{
				Name:         fd.Name.Name,
				Position:     pass.Fset.Position(fd.Name.Pos()),
				Declarations: reachableDecls(nodes, obj),
			})
			continue
		}
		if n.slow == "" || packageMarked {
			continue
		}
		pass.Report(analysis.Diagnostic{
			Pos:      fd.Name.Pos(),
			Category: RuleUnmarked,
			Message: fmt.Sprintf("%s is not marked slow but %s; mark it slow so it leaves the fast tier: "+
				"testkit.Slow(t, \"<what makes it slow>\") (forge/pkg/testkit), or `if testing.Short() { t.Skip(\"<why>\") }`",
				fd.Name.Name, n.slow),
		})
	}
	return result
}

// readSlowCall extracts one testkit.Slow call's declaration, reporting the
// option arguments the analyzer cannot read.
func readSlowCall(pass *analysis.Pass, call *ast.CallExpr) Declaration {
	decl := Declaration{At: shortPos(pass, call.Pos())}
	if len(call.Args) >= 2 {
		if why, ok := constString(pass, call.Args[1]); ok {
			decl.Why = why
		}
	}
	if call.Ellipsis.IsValid() {
		reportInputs(pass, call.Ellipsis, "testkit.Slow's options are spread from a slice; pass testkit.Inputs(...) inline")
		return decl
	}
	for _, arg := range call.Args[min(2, len(call.Args)):] {
		opt, ok := ast.Unparen(arg).(*ast.CallExpr)
		var fn *types.Func
		if ok {
			fn, _ = typeutil.Callee(pass.TypesInfo, opt).(*types.Func)
		}
		if fn == nil || !isFunc(fn, testkitPath, "Inputs") {
			reportInputs(pass, arg.Pos(), "pass testkit.Inputs(...) inline in the testkit.Slow call, not through a variable or helper")
			continue
		}
		if opt.Ellipsis.IsValid() {
			reportInputs(pass, opt.Ellipsis, "testkit.Inputs is spread from a slice; list the globs as string literals")
			continue
		}
		for _, g := range opt.Args {
			glob, ok := constString(pass, g)
			if !ok {
				reportInputs(pass, g.Pos(), "testkit.Inputs argument is not a constant string; list the globs as string literals")
				continue
			}
			decl.Inputs = append(decl.Inputs, glob)
		}
	}
	return decl
}

func reportInputs(pass *analysis.Pass, pos token.Pos, msg string) {
	pass.Report(analysis.Diagnostic{
		Pos:      pos,
		Category: RuleInputs,
		Message:  msg + ": tools read a slow test's inputs from source, without running it",
	})
}

// slowBinaries are the executables whose exec makes a test slow or reads repo
// files behind the test cache's back.
var slowBinaries = map[string]bool{
	"go": true, "forge": true, "kcl": true,
	"node": true, "npm": true, "npx": true, "pnpm": true, "yarn": true, "bun": true,
	"git": true, "docker": true, "podman": true,
	"kubectl": true, "k3d": true, "kind": true, "helm": true,
	"buf": true, "terraform": true,
}

// containerPackages are libraries every call into which drives containers.
var containerPackages = []string{
	"github.com/testcontainers/testcontainers-go",
	"github.com/ory/dockertest",
	"github.com/docker/docker/client",
	"github.com/moby/moby/client",
	"github.com/fsouza/go-dockerclient",
}

// slowOperation reports why calling fn (at call) is slow, or "".
func slowOperation(pass *analysis.Pass, fn *types.Func, call *ast.CallExpr) string {
	if fn.Pkg() == nil {
		return ""
	}
	pkg := fn.Pkg().Path()
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if named, ok := types.Unalias(derefType(sig.Recv().Type())).(*types.Named); ok {
			recv = named.Obj().Name()
		}
	}
	arg := func(i int) (string, bool) {
		if i < 0 || i >= len(call.Args) {
			return "", false
		}
		return constString(pass, call.Args[i])
	}

	if base, _, ok := execBinary(pass, fn, call); ok {
		if slowBinaries[base] {
			return fmt.Sprintf("execs %#q", base)
		}
		return ""
	}
	switch {
	case isContainerPackage(pkg):
		return "drives containers through " + qualifiedName(fn)
	case pkg == "net/http" && (recv == "" || recv == "Client"):
		switch fn.Name() {
		case "Get", "Head", "Post", "PostForm":
			if u, ok := arg(0); ok && externalURL(u) {
				return fmt.Sprintf("dials the external host in %q", u)
			}
		}
	case pkg == "net":
		idx := -1
		switch {
		case recv == "" && (fn.Name() == "Dial" || fn.Name() == "DialTimeout"):
			idx = 1
		case recv == "Dialer" && fn.Name() == "Dial":
			idx = 1
		case recv == "Dialer" && fn.Name() == "DialContext":
			idx = 2
		}
		if addr, ok := arg(idx); ok && externalAddress(addr) {
			return fmt.Sprintf("dials the external host %q", addr)
		}
	}
	return ""
}

// execBinary reports the base name of the binary an os/exec.Command or
// CommandContext call names by a constant, and the argument index naming it.
func execBinary(pass *analysis.Pass, fn *types.Func, call *ast.CallExpr) (base string, idx int, ok bool) {
	if fn.Pkg() == nil || fn.Pkg().Path() != "os/exec" || (fn.Name() != "Command" && fn.Name() != "CommandContext") {
		return "", 0, false
	}
	if sig, _ := fn.Type().(*types.Signature); sig == nil || sig.Recv() != nil {
		return "", 0, false
	}
	if fn.Name() == "CommandContext" {
		idx = 1
	}
	if idx >= len(call.Args) {
		return "", 0, false
	}
	bin, ok := constString(pass, call.Args[idx])
	if !ok {
		return "", 0, false
	}
	return strings.TrimSuffix(path.Base(filepath.ToSlash(bin)), ".exe"), idx, true
}

func isContainerPackage(pkg string) bool {
	for _, p := range containerPackages {
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

func externalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return externalHost(u.Hostname())
}

func externalAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return externalHost(host)
}

// externalHost reports whether host names a real machine off this one.
// Loopback, the unspecified address and the reserved names that never
// resolve (RFC 2606 / 6761) are not external.
func externalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified()
	}
	for _, tld := range []string{".localhost", ".test", ".example", ".invalid"} {
		if strings.HasSuffix(host, tld) {
			return false
		}
	}
	return true
}

func isTestFunc(pass *analysis.Pass, fd *ast.FuncDecl) bool {
	name := fd.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if rest := name[len("Test"):]; rest != "" && rest[0] >= 'a' && rest[0] <= 'z' {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	ptr, ok := pass.TypesInfo.TypeOf(params[0].Type).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "testing" && named.Obj().Name() == "T"
}

func isFunc(fn *types.Func, pkgPath, name string) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == pkgPath && fn.Name() == name
}

func constString(pass *analysis.Pass, expr ast.Expr) (string, bool) {
	tv, ok := pass.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

func qualifiedName(fn *types.Func) string {
	return path.Base(fn.Pkg().Path()) + "." + fn.Name()
}

func shortPos(pass *analysis.Pass, pos token.Pos) string {
	p := pass.Fset.Position(pos)
	return fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line)
}

// isTestFile reports whether node sits in a _test.go file.
func isTestFile(pass *analysis.Pass, node interface{ Pos() token.Pos }) bool {
	return strings.HasSuffix(pass.Fset.File(node.Pos()).Name(), "_test.go")
}

// importsTesting reports whether file imports "testing": a test helper file
// such as testkit's or a project's rendertest, compiled into the package
// proper so other packages' tests can call it.
func importsTesting(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path.Value == `"testing"` {
			return true
		}
	}
	return false
}

// takesTesting reports whether fn takes a *testing.T, *testing.B,
// *testing.F or testing.TB: a test helper by signature, wherever it lives.
func takesTesting(obj types.Object) bool {
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return false
	}
	for v := range sig.Params().Variables() {
		t := derefType(v.Type())
		if named, ok := types.Unalias(t).(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "testing" {
			switch named.Obj().Name() {
			case "T", "B", "F", "TB":
				return true
			}
		}
	}
	return false
}
