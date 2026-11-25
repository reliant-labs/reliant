// Package callerpath flags Go code that builds a filesystem path from the
// source file runtime.Caller reports, which -trimpath turns into a path that
// is not on disk.
//
// # WHY
//
// -trimpath is forge's default build mode: scaffolded Taskfiles add it to
// GOFLAGS, forge's own go subprocesses merge it in, and the Reliant
// workspace image sets it in $GOROOT/go.env, so worktrees of one repository
// share compiled packages instead of each building its own copy. Under it,
// runtime.Caller reports "github.com/acme/app/internal/x/x_test.go" — the
// module path joined to the file's directory in the module — instead of
// "/home/me/app/internal/x/x_test.go". A test that finds its testdata or the
// repository root from that path (filepath.Dir(file), then Join) passes in
// an untrimmed `go test` and fails in a trimmed one, which is to say it
// passes on one machine and fails on the next.
//
// # WHAT IT FLAGS
//
// RuleID: a runtime.Caller call whose second result, the file, is kept in a
// named variable (`_, file, _, ok := runtime.Caller(0)`, `var _, f, _, _ =
// runtime.Caller(1)`, an `=` assignment) and then used as anything but
// text to display. Any .go file, not only _test.go ones: a dev tool that
// locates its templates the same way breaks the same way.
//
// # WHAT IT DELIBERATELY DOES NOT FLAG
//
//   - The file result discarded (`pc, _, line, ok := runtime.Caller(1)`).
//   - A file result whose every use is display: the argument of
//     filepath.Base / path.Base / Ext, or of a call into fmt, log, log/slog
//     or errors. A caller location in a log line or an error message is
//     still correct under -trimpath; only a path opened from it is not.
//   - Generated files, and files that do not parse (the compiler's to
//     report). The lane skips testdata/, vendor/, node_modules/, gen/ and
//     hidden directories.
//
// An IsAbs check alone does not prove subsequent uses safe. This syntactic
// analyzer conservatively flags guarded path uses too; code that uses the
// path only when absolute can suppress the rule with that explanation.
//
// A use the analyzer cannot classify — passed to a helper, returned,
// stored in a field — is flagged: a function that hands its caller's file
// to someone else is almost always the "find my directory" helper. A
// deliberate one is suppressed like any forge rule, with the reason.
//
// The analysis is syntactic (go/ast, no type checking): a parse per file
// that mentions runtime.Caller. Imports are resolved by their local names,
// so an aliased "runtime" or "path/filepath" import is still read.
//
//forge:exclude-contract: a pure source analyzer; its surface is CheckFile and the rule ID
package callerpath

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// RuleID is the rule `forge lint` reports.
const RuleID = "forge-runtime-caller-path"

// RuleIDs lists every rule the analyzer emits.
func RuleIDs() []string { return []string{RuleID} }

// Fix is the remediation every finding carries.
const Fix = "resolve paths against the package directory the test runs in instead — " +
	"filepath.Abs(\"testdata/x\"), or walk up from os.Getwd() to the go.mod — or, where the " +
	"source path is optional, use it only when filepath.IsAbs reports it absolute and suppress " +
	"with that explanation (this analyzer does not prove control flow). A deliberate " +
	"use: `// forge:lint-disable-next-line " + RuleID + ": <why>` above it"

// CheckFile analyzes one Go source file. A file that does not mention
// runtime.Caller, is generated, or does not parse yields nothing. Findings
// carry file as their File, severity error.
func CheckFile(file string, src []byte) []finding.Finding {
	if !bytes.Contains(src, []byte("Caller(")) {
		return nil
	}
	fset := token.NewFileSet()
	// Keep parser object resolution: identity, not spelling, distinguishes
	// this binding from nested locals, parameters and closure captures.
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil || ast.IsGenerated(f) {
		return nil
	}
	imp := resolveImports(f)
	if imp["runtime"] == "" {
		return nil
	}
	c := &checker{fset: fset, file: file, imp: imp}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == 4 && len(n.Rhs) == 1 && c.isCaller(n.Rhs[0]) {
				c.binding(n.Lhs[1], scopeOf(stack, f))
			}
		case *ast.ValueSpec:
			if len(n.Names) == 4 && len(n.Values) == 1 && c.isCaller(n.Values[0]) {
				c.binding(n.Names[1], scopeOf(stack, f))
			}
		}
		return true
	})
	return c.out
}

// resolveImports maps each import path the analyzer reads to its local
// name in the file; a path the file does not import (or imports as _ or .)
// is absent.
func resolveImports(f *ast.File) map[string]string {
	out := map[string]string{}
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
		out[p] = name
	}
	return out
}

// scopeOf is the node whose uses of a binding are read: the innermost
// enclosing function's body, or the whole file for a package-level var.
func scopeOf(stack []ast.Node, f *ast.File) ast.Node {
	for i := len(stack) - 1; i >= 0; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncDecl:
			return fn.Body
		case *ast.FuncLit:
			return fn.Body
		}
	}
	return f
}

type checker struct {
	fset *token.FileSet
	file string
	imp  map[string]string
	out  []finding.Finding
}

// pkgCall reports the import path and function name of a call to a
// package-level function of an imported package (`filepath.Dir(x)`).
func (c *checker) pkgCall(e ast.Expr) (pkgPath, fn string, ok bool) {
	call, isCall := e.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	x, isIdent := sel.X.(*ast.Ident)
	if !isIdent || x.Obj != nil {
		return "", "", false
	}
	for p, name := range c.imp {
		if name == x.Name {
			return p, sel.Sel.Name, true
		}
	}
	return "", "", false
}

func (c *checker) isCaller(e ast.Expr) bool {
	p, fn, ok := c.pkgCall(e)
	return ok && p == "runtime" && fn == "Caller"
}

// binding judges one kept file result: target is the expression it is
// assigned to, scope where its uses are read.
func (c *checker) binding(target ast.Expr, scope ast.Node) {
	id, isIdent := target.(*ast.Ident)
	if isIdent && id.Name == "_" {
		return
	}
	if !isIdent {
		// Stored in a field or an element: its uses cannot be followed.
		c.report(target.Pos(), "runtime.Caller's file result")
		return
	}
	display, other := 0, 0
	var stack []ast.Node
	ast.Inspect(scope, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		use, ok := n.(*ast.Ident)
		if !ok || use.Name != id.Name || use.Obj != id.Obj || use == id || len(stack) < 2 {
			return true
		}
		switch c.classify(use, stack[len(stack)-2]) {
		case useDisplay:
			display++
		case useOther:
			other++
		}
		return true
	})
	if display > 0 && other == 0 {
		return
	}
	c.report(id.Pos(), "`"+id.Name+"`")
}

type use int

const (
	useNone    use = iota // not a read: the name written, a field selector, a declaration
	useDisplay            // text for a human: Base, fmt, log, errors
	useOther              // anything else: a path built, a helper handed it, returned
)

// displayPkgs are the packages whose functions only format their
// arguments as text.
var displayPkgs = map[string]bool{"fmt": true, "log": true, "log/slog": true, "errors": true}

// classify judges one occurrence of the binding's name by its parent node.
func (c *checker) classify(id *ast.Ident, parent ast.Node) use {
	switch p := parent.(type) {
	case *ast.SelectorExpr:
		if p.Sel == id {
			return useNone // x.file: a field, not the variable
		}
	case *ast.KeyValueExpr:
		if p.Key == id {
			return useNone // T{file: …}: a field name
		}
	case *ast.AssignStmt:
		for _, l := range p.Lhs {
			if l == id {
				return useNone // written, not read
			}
		}
	case *ast.ValueSpec:
		for _, n := range p.Names {
			if n == id {
				return useNone // a declaration of the name
			}
		}
	case *ast.Field:
		return useNone // a parameter or field of that name
	case *ast.CallExpr:
		pkg, fn, ok := c.pkgCall(p)
		if !ok || p.Fun == id {
			break
		}
		switch {
		case (pkg == "path/filepath" || pkg == "path") && (fn == "Base" || fn == "Ext"):
			return useDisplay
		case displayPkgs[pkg]:
			return useDisplay
		}
	}
	return useOther
}

func (c *checker) report(pos token.Pos, what string) {
	c.out = append(c.out, finding.Finding{
		Rule:     RuleID,
		Severity: finding.SeverityError,
		File:     c.file,
		Line:     c.fset.Position(pos).Line,
		Message: fmt.Sprintf("%s holds the source path runtime.Caller reports, used for more than display. "+
			"Under -trimpath, forge's default build mode, that path is module-relative (such as "+
			"example.com/app/internal/x/x_test.go), not a file on disk, so a file found from it is found "+
			"by an untrimmed build and missing in a trimmed one", what),
		Remediation: Fix,
	})
}
