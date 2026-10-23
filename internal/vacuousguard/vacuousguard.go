package vacuousguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Rule names. They double as the stable first half of a ledger key.
const (
	RuleVacuousLoop = "vacuous-loop"
	RuleDeadSkip    = "dead-skip"
)

// RuleSilentCapability lives in capability.go with the rule it names.

// Finding is one violation, keyed by "<repo-relative file>::<TestFunc>" so a
// ledger entry survives reformatting and line moves.
type Finding struct {
	Rule   string
	Key    string
	Line   int
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("[%s] %s (line %d): %s", f.Rule, f.Key, f.Line, f.Detail)
}

// skipDirs are never scanned. testdata is excluded because it holds fixtures —
// including this package's own planted defects, which must not fail the guard
// that plants them.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"bin": true, ".next": true, ".turbo": true, "coverage": true,
	"tmp": true, ".forge": true, "testdata": true,
	// .kilo holds nested git worktrees, excluded via .git/info/exclude. They
	// are whole second copies of the repo, so scanning one makes every finding
	// appear twice and — because a worktree can sit on any commit — reports
	// defects that are not in this tree at all.
	".kilo": true,
}

// walkGo returns every .go file under root, repo-relative and sorted, that
// keep(rel) accepts. Scan and LookPathProbeFuncs share it so a future skipDirs
// edit cannot narrow one surface without narrowing the other.
func walkGo(root string, keep func(rel string) bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			// internal/templates holds the scaffolds forge WRITES into
			// user projects. They are inputs to codegen, not this
			// repository's tests.
			if rel == "internal/templates" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".go") && keep(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// goFiles returns every .go file under root, test and non-test alike.
func goFiles(root string) ([]string, error) {
	return walkGo(root, func(string) bool { return true })
}

// Scan walks root and returns every finding, sorted by rule then key.
func Scan(root string) ([]Finding, error) {
	files, err := walkGo(root, func(rel string) bool { return strings.HasSuffix(rel, "_test.go") })
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("found no _test.go files under %s — a scan that sees nothing passes everything", root)
	}

	var out []Finding
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		found, err := ScanSource(rel, src)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ScanSource applies both rules to a single test file's source. Scan is this
// over the repository; the guard's own tests are this over a file of planted
// defects, so the rules under test are the rules that ship.
func ScanSource(rel string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	if ast.IsGenerated(f) {
		return nil, nil
	}
	return checkFile(fset, f, rel), nil
}

func checkFile(fset *token.FileSet, f *ast.File, rel string) []Finding {
	var out []Finding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Recv != nil {
			continue
		}
		key := rel + "::" + fd.Name.Name
		if strings.HasPrefix(fd.Name.Name, "Test") {
			if fnd, ok := checkVacuousLoop(fset, fd, key); ok {
				out = append(out, fnd)
			}
		}
		out = append(out, checkSkips(fset, fd, key)...)
		out = append(out, checkCapabilityGuards(fset, fd, key)...)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// vacuous-loop
// ─────────────────────────────────────────────────────────────────────────────

// discoveryFns are the calls whose result is a set the FILESYSTEM decides the
// size of. That is the whole point: the test author did not choose how many
// elements there are, so the test must say what it expects of that number.
var discoveryFns = map[string]bool{
	"ReadDir": true, "Glob": true, "WalkDir": true, "Walk": true,
}

var discoveryPkgs = map[string]bool{
	"os": true, "filepath": true, "fs": true, "ioutil": true,
}

func checkVacuousLoop(fset *token.FileSet, fd *ast.FuncDecl, key string) (Finding, bool) {
	disc := discoveredNames(fd)
	if len(disc) == 0 {
		return Finding{}, false
	}
	if !rangesOver(fd, disc) {
		return Finding{}, false
	}
	if provesNonEmpty(fd, disc) {
		return Finding{}, false
	}
	names := make([]string, 0, len(disc))
	for n := range disc {
		names = append(names, n)
	}
	sort.Strings(names)
	return Finding{
		Rule: RuleVacuousLoop,
		Key:  key,
		Line: fset.Position(fd.Pos()).Line,
		Detail: fmt.Sprintf("ranges over %s, discovered from the filesystem, without ever proving the set "+
			"was non-empty — an empty discovery makes this test pass while asserting nothing. "+
			"Add `if len(%s) == 0 { t.Fatal(...) }`, or a witness the loop sets and the test checks after it.",
			strings.Join(names, "/"), names[0]),
	}, true
}

// discoveredNames returns the variables bound to a filesystem discovery result.
func discoveredNames(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		isDiscovery := false
		for _, r := range as.Rhs {
			ast.Inspect(r, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok &&
						discoveryPkgs[pkg.Name] && discoveryFns[sel.Sel.Name] {
						isDiscovery = true
					}
				}
				return true
			})
		}
		if !isDiscovery {
			return true
		}
		for _, lhs := range as.Lhs {
			// An error variable is the discovery's failure channel, not
			// its result. Tests spell it err, rerr, serr, readErr…
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" && !isErrName(id.Name) {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// isErrName reports whether a variable name is conventionally an error.
func isErrName(n string) bool {
	l := strings.ToLower(n)
	return l == "err" || strings.HasSuffix(l, "err") || strings.HasSuffix(l, "error")
}

func rangesOver(fd *ast.FuncDecl, names map[string]bool) bool {
	found := false
	ast.Inspect(fd, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if id, ok := rs.X.(*ast.Ident); ok && names[id.Name] {
			found = true
		}
		return true
	})
	return found
}

// provesNonEmpty reports whether fd establishes that the discovered set was not
// empty. Four shapes count, and every one of them is a pattern this repository
// really uses — a rule that accepted only the first would flag good tests, and
// a flagged good test is how a guard gets deleted.
//
//	len(entries) == 0 → t.Fatal        an explicit size guard
//	found := false; …; if !found       a witness the loop sets and the test
//	                                   reads AFTER it, transitively through
//	                                   nested loops
//	goFiles[0]                          an index at a literal position: out of
//	                                   range panics, and a panic is a failure
//	for _, e := range entries {        a loop whose body is NOTHING BUT
//	    t.Fatalf(…)                    failures: an empty set is the PASS
//	}                                  condition, so vacuity is the point
//
// The third and fourth shapes are the rule's honest limits. In particular, a
// loop whose body is only per-element failure checks is INDISTINGUISHABLE from
// one asserting the set is empty — `for _, e := range entries { if bad(e) {
// t.Error() } }` is written by authors who mean both things — so that shape is
// let through and the blind spot is accepted rather than guessed at.
func provesNonEmpty(fd *ast.FuncDecl, disc map[string]bool) bool {
	if everyLoopAssertsEmpty(fd, disc) {
		return true
	}
	witnesses := witnessClosure(fd, disc)
	if indexesWitness(fd, witnesses) {
		return true
	}
	inLoop := conditionsInsideLoops(fd, witnesses)
	proof := false
	ast.Inspect(fd, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// A check INSIDE the loop is not a proof: when the set is empty
		// the loop body never runs, so the check never happens. That is
		// the entire failure mode, and accepting an in-loop check would
		// excuse it.
		if inLoop[ifs] {
			return true
		}
		ast.Inspect(ifs.Cond, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && witnesses[id.Name] {
				proof = true
			}
			return true
		})
		return true
	})
	return proof
}

// indexesWitness reports whether the test indexes a witness at a literal
// position — goFiles[0]. An out-of-range index panics, and a panic is a test
// FAILURE, so the empty case is already loud. This is the fourth proof shape,
// and forge's plan_orm_typecheck test is the reason it exists.
func indexesWitness(fd *ast.FuncDecl, witnesses map[string]bool) bool {
	found := false
	ast.Inspect(fd, func(n ast.Node) bool {
		ix, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		base, ok := ix.X.(*ast.Ident)
		if !ok || !witnesses[base.Name] {
			return true
		}
		if lit, ok := ix.Index.(*ast.BasicLit); ok && lit.Kind == token.INT {
			found = true
		}
		return true
	})
	return found
}

// conditionsInsideLoops returns every if-statement lexically inside a range
// loop over one of the named sets.
func conditionsInsideLoops(fd *ast.FuncDecl, names map[string]bool) map[*ast.IfStmt]bool {
	out := map[*ast.IfStmt]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		id, ok := rs.X.(*ast.Ident)
		if !ok || !names[id.Name] {
			return true
		}
		ast.Inspect(rs.Body, func(m ast.Node) bool {
			if ifs, ok := m.(*ast.IfStmt); ok {
				out[ifs] = true
			}
			return true
		})
		return true
	})
	return out
}

func everyLoopAssertsEmpty(fd *ast.FuncDecl, disc map[string]bool) bool {
	sawLoop, all := false, true
	ast.Inspect(fd, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		id, ok := rs.X.(*ast.Ident)
		if !ok || !disc[id.Name] {
			return true
		}
		sawLoop = true
		if !bodyOnlyFails(rs.Body) {
			all = false
		}
		return true
	})
	return sawLoop && all
}

// bodyOnlyFails reports whether every statement in b is a t.Error/t.Fatal call,
// possibly guarded by ifs. Nested calls in the ARGUMENTS of such a call do not
// count against it — `t.Fatalf("saw %s", e.Name())` is still just a failure.
func bodyOnlyFails(b *ast.BlockStmt) bool {
	fails := 0
	var ok func(stmts []ast.Stmt) bool
	ok = func(stmts []ast.Stmt) bool {
		for _, st := range stmts {
			switch s := st.(type) {
			case *ast.IfStmt:
				if s.Init != nil || !ok(s.Body.List) {
					return false
				}
				switch e := s.Else.(type) {
				case nil:
				case *ast.BlockStmt:
					if !ok(e.List) {
						return false
					}
				case *ast.IfStmt:
					if !ok([]ast.Stmt{e}) {
						return false
					}
				default:
					return false
				}
			case *ast.ExprStmt:
				call, isCall := s.X.(*ast.CallExpr)
				if !isCall {
					return false
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel {
					return false
				}
				if !strings.HasPrefix(sel.Sel.Name, "Fatal") && !strings.HasPrefix(sel.Sel.Name, "Error") {
					return false
				}
				fails++
			case *ast.BranchStmt:
			default:
				return false
			}
		}
		return true
	}
	return ok(b.List) && fails > 0
}

// witnessClosure grows the discovered names by everything the loop body WRITES,
// to a fixed point. That transitivity is what lets the rule accept
// `for _, e := range entries { migs[e.Name()] = true }` followed by
// `for name := range migs { found = true }` and `if !found { t.Error(…) }`.
//
// "Writes" is assignment OR increment. A test that filters the discovered set
// counts what survived — `checked++` in the body, `if checked != 3` after it —
// and that counter is a strictly STRONGER proof than a bool flag: `found`
// says at least one, `checked != 3` says exactly the three that must exist.
// Reading only AssignStmt made the rule blind to the better shape and flag the
// test that used it, which is how a guard earns a reputation for crying wolf.
func witnessClosure(fd *ast.FuncDecl, disc map[string]bool) map[string]bool {
	seed := map[string]bool{}
	for k := range disc {
		seed[k] = true
	}
	for round := 0; round < 8; round++ {
		grew := false
		ast.Inspect(fd, func(n ast.Node) bool {
			rs, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			id, ok := rs.X.(*ast.Ident)
			if !ok || !seed[id.Name] {
				return true
			}
			ast.Inspect(rs.Body, func(m ast.Node) bool {
				var targets []ast.Expr
				switch w := m.(type) {
				case *ast.AssignStmt:
					targets = w.Lhs
				case *ast.IncDecStmt:
					targets = []ast.Expr{w.X}
				default:
					return true
				}
				for _, lhs := range targets {
					var name string
					switch t := lhs.(type) {
					case *ast.Ident:
						name = t.Name
					case *ast.IndexExpr:
						if base, ok := t.X.(*ast.Ident); ok {
							name = base.Name
						}
					}
					if name != "" && name != "_" && !seed[name] {
						seed[name] = true
						grew = true
					}
				}
				return true
			})
			return true
		})
		if !grew {
			break
		}
	}
	return seed
}

// ─────────────────────────────────────────────────────────────────────────────
// dead-skip
// ─────────────────────────────────────────────────────────────────────────────

// presenceFns answer "is this path there?". A skip downstream of one of them
// is a skip on a FIXTURE, which is the shape that rots.
var presenceFns = map[string]bool{
	"Stat": true, "Lstat": true, "ReadFile": true, "ReadDir": true,
	"Open": true, "OpenFile": true, "Glob": true,
}

// envFns answer "what is this machine configured with?". A path derived from
// one of them is genuinely outside the repository and may legitimately be
// absent, so a skip on it is not a dead skip.
var envFns = map[string]bool{
	"Getenv": true, "LookupEnv": true, "Environ": true,
	"UserHomeDir": true, "UserConfigDir": true, "UserCacheDir": true,
}

func checkSkips(fset *token.FileSet, fd *ast.FuncDecl, key string) []Finding {
	assigns := collectAssigns(fd)
	var out []Finding
	var stack []ast.Node

	ast.Inspect(fd, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Skip", "Skipf", "SkipNow":
		default:
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || (recv.Name != "t" && recv.Name != "b" && recv.Name != "tb") {
			return true
		}

		line := fset.Position(call.Pos()).Line
		guard := enclosingGuard(stack)
		if guard == nil {
			out = append(out, Finding{
				Rule: RuleDeadSkip, Key: key, Line: line,
				Detail: "this skip is unconditional: the test can never run. A permanently disabled " +
					"test is a deleted test that still reports green — delete it, or fix it.",
			})
			return true
		}
		ifs, isIf := guard.(*ast.IfStmt)
		if !isIf {
			return true // switch/select: not judged
		}
		if why, bad := fixturePresence(ifs, assigns); bad {
			out = append(out, Finding{
				Rule: RuleDeadSkip, Key: key, Line: line,
				Detail: "this skip fires when a REPO-RELATIVE path is missing (" + why + "). " +
					"That path is on every checkout, so the skip is either unfalsifiable or it " +
					"silently deletes the test the day the layout moves. Assert the fixture with " +
					"t.Fatal instead — a missing fixture is a broken test, not an inapplicable one.",
			})
		}
		return true
	})
	return out
}

// enclosingGuard returns the innermost conditional construct around the node on
// top of stack, stopping at the function boundary.
func enclosingGuard(stack []ast.Node) ast.Node {
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.IfStmt, *ast.CaseClause, *ast.CommClause:
			return stack[i]
		case *ast.FuncDecl, *ast.FuncLit:
			// The search stops at the function boundary: a t.Skip in a
			// t.Run closure is guarded by what is inside that closure,
			// not by an `if` wrapping the closure's construction.
			return nil
		}
	}
	return nil
}

// fixturePresence reports whether ifs asks "does this repo-relative path
// exist?". All three conditions must hold, and each one removes a class of
// legitimate skip:
//
//   - a filesystem-presence call must be in the expansion (else it is a
//     toolchain / platform / short-mode skip);
//   - a NON-ABSOLUTE string literal must be in the expansion (else the path
//     came from somewhere this repository does not control);
//   - no environment lookup may be in the expansion (a $HOME- or
//     $FORGE_*-derived path is genuinely machine-dependent — that is how
//     cpforge_dev_render_test's skip on a sibling checkout stays legal).
func fixturePresence(ifs *ast.IfStmt, assigns map[string][]assignment) (string, bool) {
	exp := expand(ifs, assigns)
	hasPresence, hasEnv, relLit := false, false, ""
	for _, e := range exp {
		ast.Inspect(e, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				pkg, ok := v.X.(*ast.Ident)
				if !ok {
					return true
				}
				if (pkg.Name == "os" || pkg.Name == "fs" || pkg.Name == "filepath" || pkg.Name == "ioutil") &&
					presenceFns[v.Sel.Name] {
					hasPresence = true
				}
				if envFns[v.Sel.Name] {
					hasEnv = true
				}
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return true
				}
				s := strings.Trim(v.Value, "`\"")
				if s == "" || strings.HasPrefix(s, "/") {
					return true
				}
				if relLit == "" {
					relLit = s
				}
			}
			return true
		})
	}
	if hasPresence && !hasEnv && relLit != "" {
		return fmt.Sprintf("%q", relLit), true
	}
	return "", false
}

type assignment struct {
	pos token.Pos
	rhs ast.Expr
}

// collectAssigns records every assignment in fd with its position, so an
// identifier in a condition can be resolved to its NEAREST PRECEDING
// definition. Resolving to all definitions instead would let an unrelated
// earlier os.ReadFile misclassify a later toolchain skip.
func collectAssigns(fd *ast.FuncDecl) map[string][]assignment {
	out := map[string][]assignment{}
	ast.Inspect(fd, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			// x, y := f() binds both names to the same call.
			rhs := as.Rhs[0]
			if len(as.Rhs) == len(as.Lhs) {
				rhs = as.Rhs[i]
			}
			out[id.Name] = append(out[id.Name], assignment{pos: as.Pos(), rhs: rhs})
		}
		return true
	})
	for _, v := range out {
		sort.Slice(v, func(i, j int) bool { return v[i].pos < v[j].pos })
	}
	return out
}

// expand returns the condition of ifs plus, transitively, the right-hand sides
// its identifiers were most recently assigned from.
func expand(ifs *ast.IfStmt, assigns map[string][]assignment) []ast.Expr {
	out := []ast.Expr{ifs.Cond}
	if ifs.Init != nil {
		if as, ok := ifs.Init.(*ast.AssignStmt); ok {
			out = append(out, as.Rhs...)
		}
	}
	seen := map[string]bool{}
	for depth := 0; depth < 4; depth++ {
		var next []ast.Expr
		for _, e := range out {
			ast.Inspect(e, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || seen[id.Name] {
					return true
				}
				defs := assigns[id.Name]
				if len(defs) == 0 {
					return true
				}
				seen[id.Name] = true
				// nearest definition at or before the condition
				pick := defs[0]
				for _, d := range defs {
					if d.pos <= ifs.Pos() {
						pick = d
					}
				}
				next = append(next, pick.rhs)
				return true
			})
		}
		if len(next) == 0 {
			break
		}
		out = append(out, next...)
	}
	return out
}
