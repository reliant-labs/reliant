package vacuousguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RuleSilentCapability names the third rule. See checkCapabilityGuards.
const RuleSilentCapability = "silent-capability"

// capabilityProbeNames are the helpers whose entire job is to answer "is this
// external tool on this machine?".
//
// A NAME is load-bearing here because ScanSource judges ONE FILE at a time, on
// purpose: that is what lets every rule be unit-tested against a single planted
// file instead of against a package graph. The cost is that a probe spelled a
// fourth way would be a MISSED finding — so TestCapabilityProbeSetIsComplete
// walks the repository for every function that is an exec.LookPath-to-bool and
// fails if one of them is not listed here. The map cannot silently rot.
// Every entry here was PUT here by TestCapabilityProbeSetIsComplete failing —
// isPluginAvailable and mkcertOnPath are production helpers nobody would have
// thought to guess, and a test that branched on either was invisible to this
// rule until they were listed. That is the completeness check paying for
// itself on its first run.
var capabilityProbeNames = map[string]bool{
	"toolAvailable":     true,
	"isPluginAvailable": true,
	"mkcertOnPath":      true,
}

// ─────────────────────────────────────────────────────────────────────────────
// silent-capability
// ─────────────────────────────────────────────────────────────────────────────
//
// WHY THIS RULE EXISTS. dead-skip catches a test that declines to run. It does
// not catch a test that RUNS and declines to check, which is the same green
// with less to look at:
//
//	if toolAvailable("golangci-lint") {
//	    runCmd(t, projectDir, "golangci-lint", "run", "./...")
//	} else {
//	    t.Log("golangci-lint not available, skipping lint check")
//	}
//
// On a machine without golangci-lint that is not a weaker test, it is NO test —
// and the function still reports PASS, so nothing anywhere says the lint gate
// did not happen. forge shipped five of these across its scaffold e2e suite,
// three of them the only lint gate their test had. The same shape wearing a
// different hat is the mid-test bail:
//
//	if !toolAvailable("node") || !toolAvailable("npm") {
//	    t.Log("node/npm not available — skipping npm build/test gate")
//	    return
//	}
//
// which drops npm install + next build + vitest + tsc and reports PASS.
//
// THE RULE. An `if` whose condition PROBES FOR AN EXTERNAL TOOL, where neither
// outcome skips and neither outcome fails, and where the two outcomes are not
// equivalent — one runs verification the other does not, or one abandons the
// rest of the test. Such a conditional turns a machine's provisioning into a
// silent change in what the test covers.
//
// WHAT IS DELIBERATELY NOT CAUGHT.
//
//	if !toolAvailable("kcl") { t.Skip(...) }   an honest skip. Package doc:
//	                                          a toolchain skip is legitimate,
//	                                          and a rule broad enough to catch
//	                                          it is a rule that gets switched
//	                                          off. Make it fail in CI with a
//	                                          requireTool-style helper if you
//	                                          want more; that is a policy
//	                                          choice, not a vacuous test.
//	if strings.Contains(x, "y") { t.Error() }  not a tool probe at all. The
//	                                          rule fires ONLY downstream of a
//	                                          capability probe, which is what
//	                                          keeps it from flagging the
//	                                          ordinary branching every test
//	                                          does.
//	if _, err := exec.LookPath(n); err != nil { missing = append(missing, n) }
//	                                          a probe, but both outcomes do
//	                                          the same amount of verifying
//	                                          (none): this is bookkeeping on
//	                                          the way to a decision, not the
//	                                          decision.

func checkCapabilityGuards(fset *token.FileSet, fd *ast.FuncDecl, key string) []Finding {
	assigns := collectAssigns(fd)
	var out []Finding
	ast.Inspect(fd, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		probe, isProbe := capabilityProbe(ifs, assigns)
		if !isProbe {
			return true
		}
		then := ast.Node(ifs.Body)
		els := ifs.Else // nil, *ast.BlockStmt, or *ast.IfStmt (else-if)

		// An outcome that SKIPS or FAILS has told someone. That is the
		// honest shape and it is explicitly out of scope.
		if reports(then) || reports(els) {
			return true
		}

		var detail string
		switch {
		case abandons(then):
			detail = fmt.Sprintf("the %s probe returns early from the test, so everything below this "+
				"point stops being checked — and the test still reports PASS", probe)
		case abandons(els):
			detail = fmt.Sprintf("the %s probe returns early from the test on its else branch, so "+
				"everything below this point stops being checked — and the test still reports PASS", probe)
		case work(then) > 0 && work(els) == 0:
			detail = fmt.Sprintf("the %s probe guards %d verification call(s) with nothing on the other "+
				"side, so a machine without the tool runs a test that checks that much less and still "+
				"reports PASS", probe, work(then))
		case work(els) > 0 && work(then) == 0:
			detail = fmt.Sprintf("the %s probe guards %d verification call(s) on its else branch with "+
				"nothing on the other side, so a machine with the tool runs a test that checks that "+
				"much less and still reports PASS", probe, work(els))
		default:
			return true
		}

		out = append(out, Finding{
			Rule: RuleSilentCapability,
			Key:  key,
			Line: fset.Position(ifs.Pos()).Line,
			Detail: detail + ". Make the tool a REQUIREMENT of the test — skip when a human is " +
				"running it, fail when CI is (internal/cli's requireTool is the worked example) — " +
				"so the check either happens or says out loud that it did not.",
		})
		return true
	})
	return out
}

// capabilityProbe reports whether ifs asks "is this external tool present?",
// naming the probe it found. The condition's EXPANSION is searched, not just
// the condition, so the two-line spelling
//
//	if _, err := exec.LookPath("npm"); err != nil {
//
// and the resolved-variable spelling are both seen. See expand.
func capabilityProbe(ifs *ast.IfStmt, assigns map[string][]assignment) (string, bool) {
	name := ""
	for _, e := range expand(ifs, assigns) {
		ast.Inspect(e, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "exec" && fn.Sel.Name == "LookPath" && name == "" {
					name = "exec.LookPath"
				}
			case *ast.Ident:
				if capabilityProbeNames[fn.Name] && name == "" {
					name = fn.Name + "()"
				}
			}
			return true
		})
	}
	return name, name != ""
}

// reports reports whether a branch tells someone what happened: t.Skip, t.Fatal,
// t.Error, t.FailNow. A branch that reports is not silent, and silence is the
// defect.
func reports(branch ast.Node) bool {
	if branch == nil {
		return false
	}
	found := false
	ast.Inspect(branch, func(n ast.Node) bool {
		sel, ok := testingSelector(n)
		if !ok {
			return true
		}
		switch {
		case strings.HasPrefix(sel, "Skip"),
			strings.HasPrefix(sel, "Fatal"),
			strings.HasPrefix(sel, "Error"),
			sel == "Fail", sel == "FailNow":
			found = true
		}
		return true
	})
	return found
}

// work counts the calls in a branch that the test DEPENDS ON — the ones whose
// absence makes it check less. Narration to the *testing.T (Log, Helper,
// Parallel, Cleanup, …) is not verification; it is the thing people write
// INSTEAD of verification, which is exactly why it must not count.
func work(branch ast.Node) int {
	if branch == nil {
		return 0
	}
	n := 0
	ast.Inspect(branch, func(node ast.Node) bool {
		es, ok := node.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, isTesting := testingSelector(call); isTesting {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" {
				return true
			}
		}
		n++
		return true
	})
	return n
}

// abandons reports whether a branch returns out of the test. Descent stops at a
// function literal: a `return` inside a closure returns from the closure, and
// counting it would flag every table-driven test that probes a tool.
func abandons(branch ast.Node) bool {
	if branch == nil {
		return false
	}
	found := false
	var walk = func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
		}
		return true
	}
	ast.Inspect(branch, walk)
	return found
}

// testingSelector returns the method name when n is a call on the test handle
// (t/b/tb), which is how this repository spells it everywhere.
func testingSelector(n ast.Node) (string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch recv.Name {
	case "t", "b", "tb":
		return sel.Sel.Name, true
	}
	return "", false
}

// ─────────────────────────────────────────────────────────────────────────────
// Keeping capabilityProbeNames honest
// ─────────────────────────────────────────────────────────────────────────────

// LookPathProbeFuncs returns the name of every function under root that is a
// capability probe by CONSTRUCTION: it takes a name, consults exec.LookPath,
// and answers a bool. Its caller (TestCapabilityProbeSetIsComplete) asserts
// that every one of them appears in capabilityProbeNames, so the rule cannot go
// blind because someone spelled the helper differently.
//
// Both test and non-test files are walked: a probe helper that migrates into
// production code and gets re-used from a test is the same blind spot.
func LookPathProbeFuncs(root string) ([]string, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			continue // not our job to police syntax
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Recv != nil {
				continue
			}
			if !returnsLoneBool(fd) || !mentionsLookPath(fd.Body) {
				continue
			}
			seen[fd.Name.Name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func returnsLoneBool(fd *ast.FuncDecl) bool {
	res := fd.Type.Results
	if res == nil || len(res.List) != 1 || len(res.List[0].Names) > 1 {
		return false
	}
	id, ok := res.List[0].Type.(*ast.Ident)
	return ok && id.Name == "bool"
}

func mentionsLookPath(b *ast.BlockStmt) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" && sel.Sel.Name == "LookPath" {
			found = true
		}
		return true
	})
	return found
}
