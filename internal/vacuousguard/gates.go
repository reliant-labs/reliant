package vacuousguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RuleUnwrittenGate names the third rule. See ScanGates.
const RuleUnwrittenGate = "unwritten-gate"

// gateRoots are the trees whose job is to JUDGE a generated project: the audit
// categories, the lint checks, the scaffold linters. A gate in one of these on
// a file forge does not write is a verdict nobody can ever fail.
//
// Deliberately not every tree. Codegen stats generated files constantly — to
// decide whether to write them, to reconcile them, to read back what it wrote —
// and none of that is a verdict. Widening this list would bury the rule in
// legitimate hits, which is how a rule gets switched off.
var gateRoots = []string{
	"internal/cli/audit",
	"internal/cli/lint",
	"internal/linter",
}

// ScanGates reports every existence-gate, in the judging trees, on a
// generated-project Go file.
//
// WHY THIS RULE EXISTS. `forge project audit` shipped a `wire_coverage`
// category that stat'd pkg/app/wire_gen.go and, finding nothing, returned
// StatusOK with the summary "n/a". Two `forge lint` flags scanned the same
// ghost. No code path had emitted pkg/app/wire_gen.go since the DI rewrite —
// forge's own generator test asserts the file is never written — so the
// category was permanently green, the flags permanently silent, and every
// dashboard read that as a clean bill of health. The checks had unit tests, and
// those tests passed: they fed the scanner synthetic strings. What no test
// asserted was that a real project could ever produce the input.
//
// The discriminator is not "does a test exist" but "does anything WRITE the
// file". So the rule reports the gates and the ledger in the test answers that
// question per path, one entry at a time, with the emitter named.
//
// Scoped to paths ending in `.go`: a gate on proto/, db/migrations/ or
// forge.yaml is reading the USER's tree, which is legitimately absent in
// projects that have none. A generated Go file is forge's own output, and forge
// either writes it or is lying about it.
func ScanGates(root string) ([]Finding, error) {
	var files []string
	for _, gr := range gateRoots {
		dir := filepath.Join(root, filepath.FromSlash(gr))
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", gr, err)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("found no production .go files under %v — a scan that sees nothing passes everything", gateRoots)
	}
	sort.Strings(files)

	var out []Finding
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		found, err := ScanGateSource(rel, src)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ScanGateSource applies the unwritten-gate rule to one file's source.
// ScanGates is this over the judging trees; the guard's own tests are this over
// planted defects, so the rule under test is the rule that ships.
func ScanGateSource(rel string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	if ast.IsGenerated(f) {
		return nil, nil
	}

	seen := map[string]bool{}
	var out []Finding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		// The path is usually bound to a local first
		// (`path := filepath.Join(projectDir, "pkg", "app", "x_gen.go")`)
		// and stat'd on the next line. Resolving one hop of assignment is
		// what makes the rule see the shape as written rather than only the
		// inlined form.
		assigns := collectAssigns(fd)
		ast.Inspect(fd, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isPresenceCall(call) || len(call.Args) == 0 {
				return true
			}
			p, ok := resolveProjectRelGoPath(call.Args[0], assigns, call.Pos())
			if !ok {
				return true
			}
			line := fset.Position(call.Pos()).Line
			key := rel + "::" + p
			if seen[key] {
				return true
			}
			seen[key] = true
			out = append(out, Finding{
				Rule:   RuleUnwrittenGate,
				Key:    key,
				Line:   line,
				Detail: p,
			})
			return true
		})
	}
	return out, nil
}

// resolveProjectRelGoPath is projectRelGoPath plus one hop through a local
// variable, resolved to its nearest PRECEDING assignment (the same rule
// dead-skip uses, for the same reason: an unrelated later rebinding must not
// answer for an earlier gate).
func resolveProjectRelGoPath(e ast.Expr, assigns map[string][]assignment, use token.Pos) (string, bool) {
	if p, ok := projectRelGoPath(e); ok {
		return p, true
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	var rhs ast.Expr
	for _, a := range assigns[id.Name] {
		if a.pos < use {
			rhs = a.rhs
		}
	}
	if rhs == nil {
		return "", false
	}
	return projectRelGoPath(rhs)
}

// isPresenceCall reports whether call asks "is this path there?" — the same
// vocabulary the dead-skip rule keys on, restricted to the os/filepath
// packages so a local method named Open cannot reach it.
func isPresenceCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "os" && pkg.Name != "filepath" {
		return false
	}
	return presenceFns[sel.Sel.Name]
}

// projectRelGoPath reconstructs the project-relative path an expression names,
// when the expression is a `filepath.Join(<dir>, "a", "b.go")` whose trailing
// arguments are all string literals, or a bare `"a/b.go"` literal. The leading
// directory argument is the project root and is dropped — it is a variable
// whose name (projectDir / root / abs) carries no information.
//
// Returns false for anything with a non-literal segment: a path assembled from
// a loop variable or a config field is a path the rule cannot name, and naming
// it wrong is worse than not naming it.
func projectRelGoPath(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(x.Value)
		if err != nil || !strings.HasSuffix(s, ".go") || !strings.Contains(s, "/") {
			return "", false
		}
		return path.Clean(filepath.ToSlash(s)), true
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "filepath" || sel.Sel.Name != "Join" || len(x.Args) < 2 {
			return "", false
		}
		var segs []string
		for _, a := range x.Args[1:] {
			lit, ok := a.(*ast.BasicLit)
			if !ok {
				return "", false
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return "", false
			}
			segs = append(segs, s)
		}
		joined := path.Join(segs...)
		if !strings.HasSuffix(joined, ".go") || !strings.Contains(joined, "/") {
			// A bare basename means the leading argument was some per-package
			// directory, not the project root — the reconstructed path would
			// name nothing. Naming it wrong is worse than not naming it.
			return "", false
		}
		return joined, true
	}
	return "", false
}
