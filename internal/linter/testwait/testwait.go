// Package testwait flags Go tests that wait on the wall clock where they
// should wait on an event: the largest single source of flaky tests on a
// loaded, shared machine.
//
// # WHAT IT FLAGS
//
// Three rules, each a shape whose failure mode is "the machine was busy":
//
//   - RuleFixedSleep: a time.Sleep (or a bare <-time.After) in a test body,
//     followed by more of the test. It is a guess at how long something
//     takes. Under load the guess is short, the next line reads state that
//     is not there yet, and the test fails; when it is long, every run pays
//     for it.
//   - RuleTightDeadline: a wait for an event whose expiry FAILS the test and
//     whose constant deadline is under [LoadTolerantDeadline] — a select
//     case on time.After / a timer / a context.WithTimeout ctx.Done() that
//     calls t.Fatal, a testify Eventually, a poll loop bounded by
//     time.Now().Add(d), time.Since(start) < d or a counted sleep. On an
//     idle laptop a second is forever; on a machine running ten gates a
//     goroutine can wait that long for a CPU.
//   - RuleWallClockBound: an assertion that real elapsed time stayed under
//     a constant below LoadTolerantDeadline (`if time.Since(start) > 100ms
//     { t.Fatal }`, assert.Less(t, elapsed, …), WithinDuration against
//     time.Now()). It measures the machine, not the code.
//
// # WHAT IT DELIBERATELY DOES NOT FLAG
//
// Precision is the point: a noisy rule is suppressed wholesale and then
// catches nothing. Every ambiguity resolves to silence.
//
//   - A sleep inside a loop. A loop with a sleep in it is a poll — it
//     re-checks a condition — and polling is the remedy, not the defect. A
//     poll is judged only on its deadline (RuleTightDeadline).
//   - A sleep in any function that is not a test body: a fake's method, an
//     http handler simulating latency, a `go func() { time.Sleep(d);
//     close(ch) }()` trigger. A test body is a function taking *testing.T
//     or testing.TB (a Test func, a t.Run literal, a helper) or a Test*
//     method (a testify suite).
//   - A sleep that is the last thing its test does: nothing reads the state
//     it waited for. Nor one right before a context cancel func is called
//     (`time.Sleep(50ms); cancel(); <-done`): it aims the cancel at the
//     middle of the run, and a correct shutdown passes wherever it lands.
//   - A sleep of a constant up to 2ms: a clock tick so two timestamps
//     differ, not a wait for work to finish.
//   - A helper-process Test func (one that calls os.Exit): its sleeps
//     simulate the child's work. Code inside a synctest bubble, whose clock
//     is fake.
//   - A select case on a timer that fails the test only under a condition
//     it checks: `for { select { case <-done: return; case
//     <-time.After(1ms): if sample() != want { t.Fatal() } } }` is a
//     sampling tick, not a deadline.
//   - A deadline whose expiry does not fail the test — the negative
//     assertion `select { case <-ch: t.Fatal("unexpected"); case
//     <-time.After(50ms): }` is how "nothing happens" is tested, and load
//     only makes it pass more easily. Likewise a lower bound on elapsed time
//     (`elapsed < backoff` → fail): load cannot make it fail.
//   - A duration that is not a constant the file spells out. A bound
//     computed from configuration may be generous; the rule cannot tell.
//   - Anything outside a _test.go file, and generated files.
//
// # THE FIX IT POINTS TO
//
// forge/pkg/testkit.Eventually polls a condition and testkit.Receive waits
// on a channel, both with a load-tolerant default deadline
// (testkit.DefaultWaitTimeout) that a passing test never pays: they return
// the moment the event happens. A sleep that only advances the clock
// wants an injected clock instead.
//
// A deliberate wait — a negative assertion written as a sleep, a test OF
// timing — is suppressed like any forge rule, with the reason:
//
//	// forge:lint-disable-next-line forge-test-fixed-sleep: asserts nothing is sent within 100ms
//	time.Sleep(100 * time.Millisecond)
//
// The analysis is syntactic (go/ast, no type checking), so it costs a
// parse per test file and runs in every `forge lint`. Imports are resolved
// by their local names, so an aliased "time" import is still read.
//
//forge:exclude-contract: a pure source analyzer; its surface is CheckFile and the rule IDs
package testwait

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs, as `forge lint` reports them.
const (
	// RuleFixedSleep: a fixed sleep a test waits on before going on.
	RuleFixedSleep = "forge-test-fixed-sleep"
	// RuleTightDeadline: an event wait that fails the test after a
	// constant deadline too short for a loaded machine.
	RuleTightDeadline = "forge-test-tight-deadline"
	// RuleWallClockBound: an upper bound asserted on real elapsed time.
	RuleWallClockBound = "forge-test-wall-clock-bound"
)

// RuleIDs lists every rule the analyzer emits.
func RuleIDs() []string { return []string{RuleFixedSleep, RuleTightDeadline, RuleWallClockBound} }

// LoadTolerantDeadline is the shortest constant deadline the analyzer
// accepts on a wait that fails the test, and the largest elapsed-time upper
// bound it flags. Waits of 5s and over are the most common deadlines in the
// suites this was measured on and rarely flake; 1-3s ones flake under a
// loaded gate.
const LoadTolerantDeadline = 5 * time.Second

// Fix hints, one per rule.
const (
	fixSleep = "wait for the event itself: a channel or sync.WaitGroup, or " +
		"testkit.Eventually(t, \"<what>\", func() bool { … }) / testkit.Receive(t, \"<what>\", ch) " +
		"(forge/pkg/testkit), which return the moment it happens and fail only after a load-tolerant " +
		"deadline. A sleep that advances the clock wants an injected clock (a `now func() time.Time` field). " +
		"A deliberate wait: `// forge:lint-disable-next-line " + RuleFixedSleep + ": <why>` above it"
	fixDeadline = "give the wait a load-tolerant deadline: testkit.Eventually / testkit.Receive " +
		"(forge/pkg/testkit) wait up to testkit.DefaultWaitTimeout and return as soon as the event happens, " +
		"so a generous deadline costs a passing test nothing; or raise the constant to at least 5s"
	fixBound = "assert what the code did, not how fast this machine ran it: count the calls, check the " +
		"retries a fake clock advanced through, or bound it at a timeout the code must beat (≥ 5s). " +
		"A test OF timing: `// forge:lint-disable-next-line " + RuleWallClockBound + ": <why>` above it"
)

// CheckFile analyzes one Go source file. A file that is not a _test.go file,
// is generated, or does not parse yields nothing (a broken file is the
// compiler's to report). Findings carry file as their File, severity error.
func CheckFile(file string, src []byte) []finding.Finding {
	if !strings.HasSuffix(file, "_test.go") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil || ast.IsGenerated(f) {
		return nil
	}
	c := &checker{fset: fset, file: file, imp: resolveImports(f), consts: map[string]ast.Expr{}, seen: map[string]bool{}, bubbles: map[*ast.FuncLit]bool{}, litRole: map[*ast.FuncLit]litRole{}}
	if c.imp.time == "" && c.imp.testify == "" {
		return nil // nothing it reads can appear
	}
	c.collectConsts(f)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		suiteTest := fd.Recv != nil && strings.HasPrefix(fd.Name.Name, "Test")
		var recv string
		if suiteTest && len(fd.Recv.List) == 1 && len(fd.Recv.List[0].Names) == 1 {
			recv = fd.Recv.List[0].Names[0].Name
		}
		c.function(fd.Type, fd.Body, nil, suiteTest, recv, 0)
	}
	return c.out
}

// imports holds the local names the file gives the packages the analyzer
// reads; "" when the file does not import one.
type imports struct {
	time, testing, context string
	os, synctest           string
	assert, require        string
	// testify is any github.com/stretchr/testify import's path, set when
	// one is imported: a method named Eventually is then testify's.
	testify string
}

func resolveImports(f *ast.File) imports {
	var imp imports
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				continue
			}
			name = spec.Name.Name
		}
		switch p {
		case "time":
			imp.time = name
		case "testing":
			imp.testing = name
		case "context":
			imp.context = name
		case "os":
			imp.os = name
		case "testing/synctest":
			imp.synctest = name
		case "github.com/stretchr/testify/assert":
			imp.assert = name
		case "github.com/stretchr/testify/require":
			imp.require = name
		}
		if strings.HasPrefix(p, "github.com/stretchr/testify/") {
			imp.testify = p
		}
	}
	return imp
}

type checker struct {
	fset *token.FileSet
	file string
	imp  imports
	// consts maps a constant's name to its value expression: the file's
	// package-level and function-local constants, by name (a constant
	// declared twice under one name is read as the last one; the analyzer
	// only uses it to size a duration).
	consts map[string]ast.Expr
	seen   map[string]bool
	// bubbles are the function literals run by synctest.Test / Run: time
	// inside them is fake, so nothing in them waits on the wall clock.
	bubbles map[*ast.FuncLit]bool
	// litRole is how each function literal runs, read from its call site
	// before the literal is analyzed: litInline (called where it is
	// written, so it runs inside the enclosing function) or litAsync
	// (started with go, so it runs beside the test). A literal assigned or
	// passed on has no entry: what calls it is unknown.
	litRole map[*ast.FuncLit]litRole
	out     []finding.Finding
}

// litRole is how a function literal runs; see checker.litRole.
type litRole int

const (
	litInline litRole = iota + 1
	litAsync
)

// varKind is what a variable holds, when it was assigned from a timing
// source the analyzer reads.
type varKind int

const (
	varTimer      varKind = iota + 1 // time.After(d), or a time.NewTimer(d) read as .C
	varCtxTimeout                    // the ctx of context.WithTimeout(_, d)
	varDeadline                      // time.Now().Add(d)
	varElapsed                       // time.Since(x) or time.Now().Sub(x)
	varCancel                        // the cancel func of a context.With* call
)

type varInfo struct {
	kind varKind
	d    time.Duration
	ok   bool // d is known
	pos  token.Pos
}

// scope is one function's view: the testing handles it can fail through
// (its own parameters and those of the functions it closes over) and the
// timing variables it can see.
type scope struct {
	parent *scope
	// tNames are *testing.T / B / F / testing.TB identifiers.
	tNames map[string]bool
	// suite is a testify suite receiver's name: s.Fail / s.FailNow /
	// s.T().Fatal fail the test.
	suite string
	// testBody: the function is a test body (it takes *testing.T or
	// testing.TB, or is a suite's Test method). Only a test body's own
	// polls are judged: a poll in an http handler or a fake is the
	// behaviour under test, not a wait.
	testBody bool
	vars     map[string]varInfo
}

func (s *scope) isT(name string) bool {
	for ; s != nil; s = s.parent {
		if s.tNames[name] {
			return true
		}
	}
	return false
}

func (s *scope) isSuite(name string) bool {
	for ; s != nil; s = s.parent {
		if s.suite != "" && s.suite == name {
			return true
		}
	}
	return false
}

func (s *scope) lookup(name string) (varInfo, bool) {
	for ; s != nil; s = s.parent {
		if v, ok := s.vars[name]; ok {
			return v, true
		}
	}
	return varInfo{}, false
}

// function analyzes one function body, then every function literal in it
// (each in its own scope, closing over this one).
//
// A literal's test-body status comes from how it runs as well as from its
// parameters: one called inline (`func() { … }()`, a deferred call) runs
// inside the enclosing function and is a test body exactly when that is;
// one started with go runs beside the test and never is, whatever it
// takes — its sleeps are triggers and its loops are simulated work, the
// shapes the analyzer exempts. Select deadlines that fail the test are
// still read in either: a failing deadline flakes wherever it runs.
func (c *checker) function(ft *ast.FuncType, body *ast.BlockStmt, parent *scope, suiteTest bool, recv string, role litRole) {
	sc := &scope{parent: parent, tNames: map[string]bool{}, vars: map[string]varInfo{}}
	if suiteTest {
		sc.suite = recv
	}
	testBody := suiteTest
	if ft.Params != nil {
		for _, field := range ft.Params.List {
			kind := c.testingType(field.Type)
			if kind == "" {
				continue
			}
			for _, n := range field.Names {
				sc.tNames[n.Name] = true
			}
			if kind == "T" || kind == "TB" {
				testBody = true
			}
		}
	}
	switch role {
	case litInline:
		testBody = testBody || (parent != nil && parent.testBody)
	case litAsync:
		testBody = false
	}
	sc.testBody = testBody
	c.collectVars(body, sc)
	if testBody && !c.exitsProcess(body) {
		c.sleeps(body.List, false, sc)
	}
	c.waits(body, sc)
}

// exitsProcess reports whether body calls os.Exit: a Test func that does is
// the entry point of a helper subprocess (the TestHelperProcess pattern),
// whose sleeps simulate the work a real child does — no test waits on
// them in-process.
func (c *checker) exitsProcess(body *ast.BlockStmt) bool {
	found := false
	inspectOwn(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && c.isCall(call, c.imp.os, "Exit") {
			found = true
		}
		return !found
	})
	return found
}

// testingType names the testing type expr spells ("T", "B", "F", "TB"),
// or "".
func (c *checker) testingType(expr ast.Expr) string {
	if c.imp.testing == "" {
		return ""
	}
	if star, ok := expr.(*ast.StarExpr); ok {
		if sel, ok := star.X.(*ast.SelectorExpr); ok && c.isPkg(sel.X, c.imp.testing) {
			switch sel.Sel.Name {
			case "T", "B", "F":
				return sel.Sel.Name
			}
		}
		return ""
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok && c.isPkg(sel.X, c.imp.testing) && sel.Sel.Name == "TB" {
		return "TB"
	}
	return ""
}

// inspectOwn walks root without entering function literals (each is its
// own function, analyzed with its own scope).
func inspectOwn(root ast.Node, fn func(ast.Node) bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		if _, lit := n.(*ast.FuncLit); lit && n != root {
			return false
		}
		return fn(n)
	})
}

// collectConsts records the file's constants (package-level and local).
func (c *checker) collectConsts(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					c.consts[name.Name] = vs.Values[i]
				}
			}
		}
		return true
	})
}

// collectVars records the function's variables assigned from a timing
// source.
func (c *checker) collectVars(body *ast.BlockStmt, sc *scope) {
	record := func(lhs []ast.Expr, rhs []ast.Expr) {
		if len(rhs) == 1 && len(lhs) == 2 {
			// The ctx and cancel results of a context.WithTimeout call.
			call, ok := rhs[0].(*ast.CallExpr)
			if !ok {
				return
			}
			if c.isCall(call, c.imp.context, "WithTimeout") && len(call.Args) == 2 {
				if id, ok := lhs[0].(*ast.Ident); ok && id.Name != "_" {
					d, known := c.dur(call.Args[1])
					sc.vars[id.Name] = varInfo{kind: varCtxTimeout, d: d, ok: known, pos: call.Pos()}
				}
			}
			for _, fn := range []string{"WithCancel", "WithCancelCause", "WithTimeout", "WithTimeoutCause", "WithDeadline", "WithDeadlineCause"} {
				if c.isCall(call, c.imp.context, fn) {
					if id, ok := lhs[1].(*ast.Ident); ok && id.Name != "_" {
						sc.vars[id.Name] = varInfo{kind: varCancel, pos: call.Pos()}
					}
				}
			}
			return
		}
		if len(lhs) != len(rhs) {
			return
		}
		for i, l := range lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			if v, ok := c.timingSource(rhs[i]); ok {
				sc.vars[id.Name] = v
			}
		}
	}
	inspectOwn(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			record(x.Lhs, x.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(x.Names))
			for i, id := range x.Names {
				lhs[i] = id
			}
			record(lhs, x.Values)
		}
		return true
	})
}

// timingSource classifies an expression a variable is assigned from.
func (c *checker) timingSource(e ast.Expr) (varInfo, bool) {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok {
		return varInfo{}, false
	}
	switch {
	case c.isCall(call, c.imp.time, "After"), c.isCall(call, c.imp.time, "NewTimer"):
		if len(call.Args) == 1 {
			d, known := c.dur(call.Args[0])
			return varInfo{kind: varTimer, d: d, ok: known, pos: call.Pos()}, true
		}
	case c.isCall(call, c.imp.time, "Since"):
		return varInfo{kind: varElapsed, pos: call.Pos()}, true
	}
	// time.Now().Add(d) / time.Now().Sub(x)
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && c.isNow(sel.X) && len(call.Args) == 1 {
		switch sel.Sel.Name {
		case "Add":
			d, known := c.dur(call.Args[0])
			return varInfo{kind: varDeadline, d: d, ok: known, pos: call.Pos()}, true
		case "Sub":
			return varInfo{kind: varElapsed, pos: call.Pos()}, true
		}
	}
	return varInfo{}, false
}

// ── RuleFixedSleep ────────────────────────────────────────────────────────

// sleeps reports the fixed sleeps in a test body's statements. A loop is
// not entered (a sleep in a loop is a poll), nor a function literal, go or
// defer statement. followed reports whether a statement runs after the
// list (in an enclosing block).
func (c *checker) sleeps(stmts []ast.Stmt, followed bool, sc *scope) {
	for i, st := range stmts {
		next := followed || i < len(stmts)-1
		if i+1 < len(stmts) && c.cancels(stmts[i+1], sc) {
			continue // a sleep before cancel(): see cancels
		}
		c.sleepStmt(st, next, sc)
	}
}

// cancels reports a call of a context cancel func. A test that sleeps and
// then cancels (`go w.Start(ctx); time.Sleep(50ms); cancel(); <-done`)
// is aiming the cancel at the middle of the run; whether it lands before or
// after the work starts, a correct Start returns, so load changes which
// branch the test exercises, not whether it passes. Not a wait.
func (c *checker) cancels(st ast.Stmt, sc *scope) bool {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := ast.Unparen(es.X).(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := sc.lookup(id.Name)
	return ok && v.kind == varCancel
}

func (c *checker) sleepStmt(st ast.Stmt, followed bool, sc *scope) {
	switch s := st.(type) {
	case *ast.ExprStmt:
		if !followed {
			return
		}
		if what, ok := c.isSleep(s.X); ok && !c.clockTick(s.X) {
			c.report(RuleFixedSleep, s.Pos(), fmt.Sprintf(
				"%s waits a fixed time and the test goes on as if what it waited for had happened: "+
					"on a loaded machine it can take longer, and the test flakes", what), fixSleep)
		}
	case *ast.BlockStmt:
		c.sleeps(s.List, followed, sc)
	case *ast.LabeledStmt:
		c.sleepStmt(s.Stmt, followed, sc)
	case *ast.IfStmt:
		c.sleeps(s.Body.List, followed, sc)
		if s.Else != nil {
			c.sleepStmt(s.Else, followed, sc)
		}
	case *ast.SwitchStmt:
		c.clauses(s.Body, followed, sc)
	case *ast.TypeSwitchStmt:
		c.clauses(s.Body, followed, sc)
	case *ast.SelectStmt:
		c.clauses(s.Body, followed, sc)
	}
}

func (c *checker) clauses(body *ast.BlockStmt, followed bool, sc *scope) {
	for _, cl := range body.List {
		switch cc := cl.(type) {
		case *ast.CaseClause:
			c.sleeps(cc.Body, followed, sc)
		case *ast.CommClause:
			c.sleeps(cc.Body, followed, sc)
		}
	}
}

// maxClockTick is the longest constant sleep read as a clock tick rather
// than a wait: a test sleeping a millisecond or two so two timestamps
// differ. Nothing concurrent finishes in that time reliably enough for a
// test to have relied on it.
const maxClockTick = 2 * time.Millisecond

// clockTick reports a sleep of a constant at most maxClockTick.
func (c *checker) clockTick(e ast.Expr) bool {
	var arg ast.Expr
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		arg = x.Args[0]
	case *ast.UnaryExpr:
		arg = ast.Unparen(x.X).(*ast.CallExpr).Args[0]
	}
	d, ok := c.dur(arg)
	return ok && d <= maxClockTick
}

// isSleep reports a time.Sleep(d) call or a bare <-time.After(d) receive,
// rendered for the message.
func (c *checker) isSleep(e ast.Expr) (string, bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		if c.isCall(x, c.imp.time, "Sleep") && len(x.Args) == 1 {
			return "time.Sleep(" + c.durText(x.Args[0]) + ")", true
		}
	case *ast.UnaryExpr:
		if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok && x.Op == token.ARROW &&
			c.isCall(call, c.imp.time, "After") && len(call.Args) == 1 {
			return "<-time.After(" + c.durText(call.Args[0]) + ")", true
		}
	}
	return "", false
}

// ── RuleTightDeadline and RuleWallClockBound ─────────────────────────────

// waits reads every deadline and elapsed-time check in the function's own
// code, then recurses into its function literals.
func (c *checker) waits(body *ast.BlockStmt, sc *scope) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			if !c.bubbles[x] {
				c.function(x.Type, x.Body, sc, false, "", c.litRole[x])
			}
			return false
		case *ast.GoStmt:
			// Visited before its call, so the call below sees it.
			if lit, ok := ast.Unparen(x.Call.Fun).(*ast.FuncLit); ok {
				c.litRole[lit] = litAsync
			}
		case *ast.SelectStmt:
			c.selectDeadlines(x, sc)
		case *ast.ForStmt:
			if sc.testBody {
				c.forDeadline(x, sc)
			}
		case *ast.RangeStmt:
			if sc.testBody {
				c.rangePoll(x)
			}
		case *ast.IfStmt:
			if c.fails(x.Body, sc) {
				if cmp, d, ok := c.elapsedAbove(x.Cond, sc); ok {
					c.reportBound(cmp.Pos(), d)
				}
			}
		case *ast.CallExpr:
			if lit, ok := ast.Unparen(x.Fun).(*ast.FuncLit); ok && c.litRole[lit] == 0 {
				c.litRole[lit] = litInline // func() { … }(), deferred or not
			}
			if c.isCall(x, c.imp.synctest, "Test") || c.isCall(x, c.imp.synctest, "Run") {
				for _, a := range x.Args {
					if lit, ok := a.(*ast.FuncLit); ok {
						c.bubbles[lit] = true
					}
				}
			}
			c.testifyCall(x, sc)
		}
		return true
	})
}

// selectDeadlines reports a select case on a short timer whose body fails
// the test unconditionally: the timer firing IS the failure. A case that
// fails only under a condition it checks — `case <-time.After(1ms): if
// got := sample(); got != want { t.Fatal() }`, a sampling tick in a loop —
// is a periodic check, not a deadline: load makes it sample less often, and
// never makes it fail.
func (c *checker) selectDeadlines(sel *ast.SelectStmt, sc *scope) {
	for _, cl := range sel.Body.List {
		cc, ok := cl.(*ast.CommClause)
		if !ok || cc.Comm == nil {
			continue
		}
		var recv ast.Expr
		switch comm := cc.Comm.(type) {
		case *ast.ExprStmt:
			recv = comm.X
		case *ast.AssignStmt:
			if len(comm.Rhs) == 1 {
				recv = comm.Rhs[0]
			}
		}
		u, ok := ast.Unparen(recv).(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			continue
		}
		d, what, ok := c.timerOf(u.X, sc)
		if !ok || d >= LoadTolerantDeadline || !c.failsUnconditionally(cc.Body, sc) {
			continue
		}
		c.report(RuleTightDeadline, cc.Pos(), fmt.Sprintf(
			"the test fails when %s fires, %s after the wait began: on a loaded machine the event it "+
				"waits for can take longer, and the test flakes", what, d), fixDeadline)
	}
}

// timerOf reads the channel a select case receives from as a timer:
// time.After(d), time.NewTimer(d).C, a variable holding either, or the
// Done() of a context.WithTimeout ctx.
func (c *checker) timerOf(ch ast.Expr, sc *scope) (time.Duration, string, bool) {
	switch x := ast.Unparen(ch).(type) {
	case *ast.CallExpr:
		if c.isCall(x, c.imp.time, "After") && len(x.Args) == 1 {
			d, ok := c.dur(x.Args[0])
			return d, "time.After(" + c.durText(x.Args[0]) + ")", ok
		}
		// ctx.Done()
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" && len(x.Args) == 0 {
			if id, ok := sel.X.(*ast.Ident); ok {
				if v, ok := sc.lookup(id.Name); ok && v.kind == varCtxTimeout {
					return v.d, "the context.WithTimeout deadline", v.ok
				}
			}
		}
	case *ast.Ident:
		if v, ok := sc.lookup(x.Name); ok && v.kind == varTimer {
			return v.d, x.Name + " (a timer)", v.ok
		}
	case *ast.SelectorExpr:
		if x.Sel.Name != "C" {
			break
		}
		if id, ok := x.X.(*ast.Ident); ok {
			if v, ok := sc.lookup(id.Name); ok && v.kind == varTimer {
				return v.d, id.Name + ".C (a timer)", v.ok
			}
		}
		if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok && c.isCall(call, c.imp.time, "NewTimer") && len(call.Args) == 1 {
			d, ok := c.dur(call.Args[0])
			return d, "time.NewTimer(" + c.durText(call.Args[0]) + ")", ok
		}
	}
	return 0, "", false
}

// forDeadline reports a poll loop bounded by a short deadline: a
// time.Now().Add(d) deadline compared against the clock anywhere in the
// loop, a `time.Since(start) < d` condition, or a counted loop of sleeps
// that can exit early.
func (c *checker) forDeadline(loop *ast.ForStmt, sc *scope) {
	reported := false
	inspectOwn(loop, func(n ast.Node) bool {
		if reported {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if v, ok := c.deadlineCheck(call, sc); ok && v.ok && v.d < LoadTolerantDeadline {
			reported = true
			c.report(RuleTightDeadline, v.pos, fmt.Sprintf(
				"this poll gives up %s after it starts: on a loaded machine the condition it waits for "+
					"can take longer, and the test flakes", v.d), fixDeadline)
		}
		return true
	})
	if reported {
		return
	}
	if loop.Cond != nil {
		if _, d, ok := c.elapsedBelow(loop.Cond, sc); ok {
			c.report(RuleTightDeadline, loop.Pos(), fmt.Sprintf(
				"this poll gives up %s after it starts: on a loaded machine the condition it waits for "+
					"can take longer, and the test flakes", d), fixDeadline)
			return
		}
	}
	if n, ok := c.countedLoop(loop); ok {
		c.countedPoll(loop.Pos(), n, loop.Body)
	}
}

// rangePoll is countedPoll for `for range n` / `for i := range n`.
func (c *checker) rangePoll(loop *ast.RangeStmt) {
	n, ok := c.intConst(loop.X)
	if !ok {
		return
	}
	c.countedPoll(loop.Pos(), n, loop.Body)
}

// countedPoll reports a loop of n iterations that sleeps a constant d at
// the top level of its body and can leave early (a break or return): a poll
// whose whole budget is n*d.
func (c *checker) countedPoll(pos token.Pos, n int64, body *ast.BlockStmt) {
	var d time.Duration
	found := false
	for _, st := range body.List {
		es, ok := st.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := ast.Unparen(es.X).(*ast.CallExpr)
		if !ok || !c.isCall(call, c.imp.time, "Sleep") || len(call.Args) != 1 {
			continue
		}
		if v, ok := c.dur(call.Args[0]); ok {
			d, found = v, true
		}
	}
	if !found || d <= 0 || !exits(body) {
		return
	}
	budget := time.Duration(n) * d
	if budget >= LoadTolerantDeadline {
		return
	}
	c.report(RuleTightDeadline, pos, fmt.Sprintf(
		"this poll gives up after %d tries %s apart (%s in all): on a loaded machine the condition it "+
			"waits for can take longer, and the test flakes", n, d, budget), fixDeadline)
}

// countedLoop reads `for i := 0; i < n; i++` with a constant n.
func (c *checker) countedLoop(loop *ast.ForStmt) (int64, bool) {
	init, ok := loop.Init.(*ast.AssignStmt)
	if !ok || len(init.Lhs) != 1 || len(init.Rhs) != 1 {
		return 0, false
	}
	id, ok := init.Lhs[0].(*ast.Ident)
	if !ok {
		return 0, false
	}
	start, ok := c.intConst(init.Rhs[0])
	if !ok {
		return 0, false
	}
	cond, ok := loop.Cond.(*ast.BinaryExpr)
	if !ok {
		return 0, false
	}
	if x, ok := cond.X.(*ast.Ident); !ok || x.Name != id.Name {
		return 0, false
	}
	end, ok := c.intConst(cond.Y)
	if !ok {
		return 0, false
	}
	post, ok := loop.Post.(*ast.IncDecStmt)
	if !ok || post.Tok != token.INC {
		return 0, false
	}
	if x, ok := post.X.(*ast.Ident); !ok || x.Name != id.Name {
		return 0, false
	}
	switch cond.Op {
	case token.LSS:
		return end - start, end > start
	case token.LEQ:
		return end - start + 1, end >= start
	}
	return 0, false
}

// exits reports whether body can leave its loop early: a break that
// belongs to the loop, or a return.
func exits(body *ast.BlockStmt) bool {
	found := false
	var walk func(n ast.Node, nested bool) bool
	walk = func(n ast.Node, nested bool) bool {
		ast.Inspect(n, func(x ast.Node) bool {
			if found {
				return false
			}
			switch s := x.(type) {
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				found = true
			case *ast.BranchStmt:
				// An unlabeled break inside a nested loop, switch or select
				// leaves that, not this loop; a labeled one may leave this.
				if s.Tok == token.BREAK && (!nested || s.Label != nil) {
					found = true
				}
			case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				if x != n {
					walk(x, true)
					return false
				}
			}
			return true
		})
		return found
	}
	return walk(body, false)
}

// deadlineCheck reads a comparison of the clock with a time.Now().Add(d)
// deadline: time.Now().Before(D) / .After(D), D.After(time.Now()) /
// .Before(time.Now()), time.Until(D).
func (c *checker) deadlineCheck(call *ast.CallExpr, sc *scope) (varInfo, bool) {
	deadline := func(e ast.Expr) (varInfo, bool) {
		e = ast.Unparen(e)
		if id, ok := e.(*ast.Ident); ok {
			if v, ok := sc.lookup(id.Name); ok && v.kind == varDeadline {
				return v, true
			}
			return varInfo{}, false
		}
		if v, ok := c.timingSource(e); ok && v.kind == varDeadline {
			return v, true
		}
		return varInfo{}, false
	}
	if c.isCall(call, c.imp.time, "Until") && len(call.Args) == 1 {
		return deadline(call.Args[0])
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) != 1 || (sel.Sel.Name != "Before" && sel.Sel.Name != "After") {
		return varInfo{}, false
	}
	if c.isNow(sel.X) {
		return deadline(call.Args[0])
	}
	if c.isNow(call.Args[0]) {
		return deadline(sel.X)
	}
	return varInfo{}, false
}

// elapsedBelow reads a condition holding `elapsed < d` / `d > elapsed`
// with a constant d under LoadTolerantDeadline: as a loop condition, a poll
// that runs until d has passed; asserted true, an upper bound on elapsed
// time.
func (c *checker) elapsedBelow(cond ast.Expr, sc *scope) (*ast.BinaryExpr, time.Duration, bool) {
	var hit *ast.BinaryExpr
	var hitD time.Duration
	c.comparisons(cond, func(b *ast.BinaryExpr) bool {
		var el, bound ast.Expr
		switch b.Op {
		case token.LSS, token.LEQ:
			el, bound = b.X, b.Y
		case token.GTR, token.GEQ:
			el, bound = b.Y, b.X
		default:
			return false
		}
		if !c.isElapsed(el, sc) {
			return false
		}
		d, ok := c.dur(bound)
		if !ok || d >= LoadTolerantDeadline {
			return false
		}
		hit, hitD = b, d
		return true
	})
	return hit, hitD, hit != nil
}

// elapsedAbove reads a condition holding `elapsed > d` / `d < elapsed`
// (or >=, <=) with a constant d under LoadTolerantDeadline: in an if whose
// body fails the test, a bound on how fast the machine ran.
func (c *checker) elapsedAbove(cond ast.Expr, sc *scope) (*ast.BinaryExpr, time.Duration, bool) {
	var hit *ast.BinaryExpr
	var hitD time.Duration
	c.comparisons(cond, func(b *ast.BinaryExpr) bool {
		var el, bound ast.Expr
		switch b.Op {
		case token.GTR, token.GEQ:
			el, bound = b.X, b.Y
		case token.LSS, token.LEQ:
			el, bound = b.Y, b.X
		default:
			return false
		}
		if !c.isElapsed(el, sc) {
			return false
		}
		d, ok := c.dur(bound)
		if !ok || d >= LoadTolerantDeadline {
			return false
		}
		hit, hitD = b, d
		return true
	})
	return hit, hitD, hit != nil
}

// comparisons calls fn on each comparison the condition's && / || tree
// holds, until fn returns true. A negated sub-expression is not entered:
// its direction is inverted.
func (c *checker) comparisons(e ast.Expr, fn func(*ast.BinaryExpr) bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.BinaryExpr:
		if x.Op == token.LAND || x.Op == token.LOR {
			return c.comparisons(x.X, fn) || c.comparisons(x.Y, fn)
		}
		return fn(x)
	}
	return false
}

// isElapsed reports time.Since(x), time.Now().Sub(x), or a variable
// assigned from one.
func (c *checker) isElapsed(e ast.Expr, sc *scope) bool {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok {
		v, ok := sc.lookup(id.Name)
		return ok && v.kind == varElapsed
	}
	v, ok := c.timingSource(e)
	return ok && v.kind == varElapsed
}

func (c *checker) reportBound(pos token.Pos, d time.Duration) {
	c.report(RuleWallClockBound, pos, fmt.Sprintf(
		"the test fails when real elapsed time passes %s: that measures how busy the machine was, "+
			"and on a loaded one the test flakes", d), fixBound)
}

// testifyCall reads testify's Eventually (RuleTightDeadline) and its
// elapsed-time assertions (RuleWallClockBound).
func (c *checker) testifyCall(call *ast.CallExpr, sc *scope) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || c.imp.testify == "" {
		return
	}
	// Package form assert.X(t, …) / require.X(t, …): the arguments are
	// shifted by the leading t. Method form (a suite, or require.New(t)):
	// they are not.
	shift := -1
	switch {
	case c.isPkg(sel.X, c.imp.assert), c.isPkg(sel.X, c.imp.require):
		shift = 1
	case !c.isAnyPkg(sel.X):
		shift = 0
	}
	if shift < 0 {
		return
	}
	arg := func(i int) ast.Expr {
		if i+shift < len(call.Args) {
			return call.Args[i+shift]
		}
		return nil
	}
	switch sel.Sel.Name {
	case "Eventually", "Eventuallyf", "EventuallyWithT", "EventuallyWithTf":
		wait := arg(1)
		if wait == nil || arg(2) == nil {
			return
		}
		if d, ok := c.dur(wait); ok && d < LoadTolerantDeadline {
			c.report(RuleTightDeadline, call.Pos(), fmt.Sprintf(
				"%s gives up after %s: on a loaded machine the condition it waits for can take longer, "+
					"and the test flakes", sel.Sel.Name, d), fixDeadline)
		}
	case "Less", "Lessf", "LessOrEqual", "LessOrEqualf":
		if el, bound := arg(0), arg(1); el != nil && bound != nil && c.isElapsed(el, sc) {
			if d, ok := c.dur(bound); ok && d < LoadTolerantDeadline {
				c.reportBound(call.Pos(), d)
			}
		}
	case "Greater", "Greaterf", "GreaterOrEqual", "GreaterOrEqualf":
		if bound, el := arg(0), arg(1); el != nil && bound != nil && c.isElapsed(el, sc) {
			if d, ok := c.dur(bound); ok && d < LoadTolerantDeadline {
				c.reportBound(call.Pos(), d)
			}
		}
	case "True", "Truef":
		if cond := arg(0); cond != nil {
			if _, d, ok := c.elapsedBelow(cond, sc); ok {
				c.reportBound(call.Pos(), d)
			}
		}
	case "WithinDuration", "WithinDurationf":
		a, b, delta := arg(0), arg(1), arg(2)
		if delta == nil || (!c.isNow(a) && !c.isNow(b)) {
			return
		}
		if d, ok := c.dur(delta); ok && d < LoadTolerantDeadline {
			c.reportBound(call.Pos(), d)
		}
	}
}

// fails reports whether body fails the test: t.Fatal / Fatalf / Error /
// Errorf / Fail / FailNow on a testing handle in scope, a suite's
// s.Fail / s.FailNow / s.T().X, testify's assert.Fail / require.Fail /
// FailNow, or a panic.
func (c *checker) fails(body *ast.BlockStmt, sc *scope) bool {
	found := false
	inspectOwn(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && c.failCall(call, sc) {
			found = true
		}
		return !found
	})
	return found
}

// failsUnconditionally reports whether stmts fail the test whenever they
// run: a fail call (see fails) among the list's own statements, or in a
// plain block nested in it — not one behind an if, a loop, a switch or a
// function literal.
func (c *checker) failsUnconditionally(stmts []ast.Stmt, sc *scope) bool {
	for _, st := range stmts {
		switch x := st.(type) {
		case *ast.ExprStmt:
			if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok && c.failCall(call, sc) {
				return true
			}
		case *ast.BlockStmt:
			if c.failsUnconditionally(x.List, sc) {
				return true
			}
		case *ast.LabeledStmt:
			if c.failsUnconditionally([]ast.Stmt{x.Stmt}, sc) {
				return true
			}
		}
	}
	return false
}

// failCall reports whether call fails the test: t.Fatal / Fatalf / Error /
// Errorf / Fail / FailNow on a testing handle in scope, a suite's
// s.Fail / s.FailNow / s.T().X, testify's assert.Fail / require.Fail /
// FailNow, or a panic.
func (c *checker) failCall(call *ast.CallExpr, sc *scope) bool {
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	name := sel.Sel.Name
	switch recv := ast.Unparen(sel.X).(type) {
	case *ast.Ident:
		switch {
		case sc.isT(recv.Name):
			switch name {
			case "Fatal", "Fatalf", "FailNow", "Fail", "Error", "Errorf":
				return true
			}
		case sc.isSuite(recv.Name), c.isPkg(recv, c.imp.assert), c.isPkg(recv, c.imp.require):
			return name == "Fail" || name == "FailNow" || name == "Failf" || name == "FailNowf"
		}
	case *ast.CallExpr:
		// s.T().Fatal(…)
		if inner, ok := recv.Fun.(*ast.SelectorExpr); ok && inner.Sel.Name == "T" && len(recv.Args) == 0 {
			if id, ok := inner.X.(*ast.Ident); ok && sc.isSuite(id.Name) {
				switch name {
				case "Fatal", "Fatalf", "FailNow", "Fail", "Error", "Errorf":
					return true
				}
			}
		}
	}
	return false
}

// ── helpers ──────────────────────────────────────────────────────────────

func (c *checker) report(rule string, pos token.Pos, msg, fix string) {
	key := fmt.Sprintf("%s|%d", rule, pos)
	if c.seen[key] {
		return // a deadline read by two nested loops
	}
	c.seen[key] = true
	c.out = append(c.out, finding.Finding{
		Rule:        rule,
		Severity:    finding.SeverityError,
		File:        c.file,
		Line:        c.fset.Position(pos).Line,
		Message:     msg,
		Remediation: fix,
	})
}

// isPkg reports whether e is the identifier a package is imported as.
func (c *checker) isPkg(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && name != "" && id.Name == name
}

// isAnyPkg reports whether e names one of the packages the analyzer
// resolves (a method call on it is a package function, not a method).
func (c *checker) isAnyPkg(e ast.Expr) bool {
	for _, n := range []string{c.imp.time, c.imp.testing, c.imp.context, c.imp.assert, c.imp.require} {
		if c.isPkg(e, n) {
			return true
		}
	}
	return false
}

// isCall reports a call of pkg.name.
func (c *checker) isCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name && c.isPkg(sel.X, pkg)
}

// isNow reports a time.Now() call.
func (c *checker) isNow(e ast.Expr) bool {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	return ok && c.isCall(call, c.imp.time, "Now") && len(call.Args) == 0
}

// durText renders a duration argument for a message: its value when
// constant, its source otherwise.
func (c *checker) durText(e ast.Expr) string {
	if d, ok := c.dur(e); ok {
		return d.String()
	}
	return types.ExprString(e)
}

// dur evaluates a constant duration expression: integer literals, the time
// unit constants, constants the file declares, time.Duration(x), and
// + - * / over them.
func (c *checker) dur(e ast.Expr) (time.Duration, bool) {
	v, ok := c.eval(e, 0)
	return time.Duration(v), ok
}

func (c *checker) intConst(e ast.Expr) (int64, bool) { return c.eval(e, 0) }

var timeUnits = map[string]time.Duration{
	"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
	"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour,
}

func (c *checker) eval(e ast.Expr, depth int) (int64, bool) {
	if depth > 8 {
		return 0, false
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if x.Kind != token.INT {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
		return n, err == nil
	case *ast.SelectorExpr:
		if c.isPkg(x.X, c.imp.time) {
			if u, ok := timeUnits[x.Sel.Name]; ok {
				return int64(u), true
			}
		}
	case *ast.Ident:
		if v, ok := c.consts[x.Name]; ok {
			return c.eval(v, depth+1)
		}
	case *ast.CallExpr:
		// A time.Duration conversion of a duration expression.
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Duration" && c.isPkg(sel.X, c.imp.time) && len(x.Args) == 1 {
			return c.eval(x.Args[0], depth+1)
		}
	case *ast.BinaryExpr:
		a, ok := c.eval(x.X, depth+1)
		if !ok {
			return 0, false
		}
		b, ok := c.eval(x.Y, depth+1)
		if !ok {
			return 0, false
		}
		switch x.Op {
		case token.ADD:
			return a + b, true
		case token.SUB:
			return a - b, true
		case token.MUL:
			return a * b, true
		case token.QUO:
			if b == 0 {
				return 0, false
			}
			return a / b, true
		}
	}
	return 0, false
}
