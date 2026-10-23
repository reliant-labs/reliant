// File: internal/linter/forgeconv/handler_tests_use_tdd.go
//
// The forgeconv-handler-tests-use-tdd analyzer warns when a handler test
// file under handlers/<svc>/ hand-rolls the `tests := []struct{name,
// call}` shape instead of delegating to `forge/pkg/tdd.RunRPCCases`.
//
// The hand-rolled shape (one big TestHandlers/TestIntegration func, a
// per-RPC slice of {name, call} rows, a single `for _, tt := range tests`
// loop) was the dominant pattern across the 25-pass control-plane-next
// dogfood — 7 of 9 handler test files (~2,160 LOC) carried it, only 2
// landed on `tdd.RunRPCCases`. The library is documented in pkg/tdd/doc.go,
// and the canonical row shape is documented under
// `forge skill load testing/patterns`.
//
// The lint is advisory (warning, not error) — projects that pre-date the
// scaffold default may legitimately ship the hand-rolled shape until the
// project owner decides to migrate. The codemod
// `forge project migrate tdd` converts most files in one shot.
//
// Detection criteria, evaluated per test file under handlers/<svc>/:
//
//   - file is a *_test.go with package suffix _test
//   - file body declares `tests := []struct {` with at least one of
//     the tell-tale field names (`name string`, `call func`, etc.)
//   - file imports do NOT include `github.com/reliant-labs/forge/pkg/tdd`
//
// The third gate is the disambiguator: a file that already imports
// pkg/tdd is migrated (or partially migrated), and we don't want to
// re-warn on those.

package forgeconv

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

// LintHandlerTests walks rootDir/handlers/ for *_test.go files and warns
// when each looks like the hand-rolled `tests := []struct{name, call}`
// shape rather than `tdd.RunRPCCases`. Returns findings in deterministic
// order (file, then line). A missing internal/handlers/ directory is not an error
// — projects without an internal/handlers/ tree (CLI, library kinds) get an empty
// result.
func LintHandlerTests(rootDir string) (Result, error) {
	handlersDir := filepath.Join(rootDir, "internal", "handlers")
	if _, err := os.Stat(handlersDir); os.IsNotExist(err) {
		return Result{}, nil
	}

	var files []string
	err := filepath.WalkDir(handlersDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip vendored / generated / cache dirs that occasionally land
			// under handlers/.
			if shouldSkipHandlerSubdir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", handlersDir, err)
	}
	sort.Strings(files)

	var result Result
	for _, f := range files {
		findings, ferr := lintHandlerTestFile(f, rootDir)
		if ferr != nil {
			return Result{}, ferr
		}
		result.Findings = append(result.Findings, findings...)
	}

	// Stable: file, then line, then rule.
	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

func shouldSkipHandlerSubdir(name string) bool {
	switch name {
	case "testdata", "node_modules", "gen", "vendor":
		return true
	}
	return false
}

// lintHandlerTestFile parses a single Go test file and reports a finding
// when the recognised hand-rolled shape is present and pkg/tdd is not
// imported. relRoot is used to produce stable, project-relative paths in
// the finding output.
func lintHandlerTestFile(path, relRoot string) ([]Finding, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		// A test file that doesn't parse is the user's problem to fix; the
		// regular Go toolchain will report it. Don't double-report here.
		return nil, nil
	}

	// Gate: import already includes pkg/tdd → file is migrated (or in
	// the middle of being migrated). Skip silently.
	if importsForgeTDD(file) {
		return nil, nil
	}

	// Find the first `tests := []struct { ... }` declaration whose struct
	// has the canonical hand-rolled fields. Functions can have more than
	// one such slice, but a single match is enough to fire the rule.
	finding, ok := findHandRolledTable(fset, file, path, relRoot)
	if !ok {
		return nil, nil
	}
	return []Finding{finding}, nil
}

// importsForgeTDD returns true iff the file imports
// "github.com/reliant-labs/forge/pkg/tdd" under any alias.
func importsForgeTDD(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if path == "github.com/reliant-labs/forge/pkg/tdd" {
			return true
		}
	}
	return false
}

// findHandRolledTable walks the file's top-level functions for the
// canonical `tests := []struct{...}` shape. Returns (finding, true) on
// the first match.
func findHandRolledTable(fset *token.FileSet, file *ast.File, path, relRoot string) (Finding, bool) {
	rel := relPath(path, relRoot)
	var (
		match    Finding
		matchSet bool
	)
	ast.Inspect(file, func(n ast.Node) bool {
		if matchSet {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if !isHandRolledTestsAssign(assign) {
			return true
		}
		pos := fset.Position(assign.Pos())
		match = Finding{
			Rule:     "forgeconv-handler-tests-use-tdd",
			Severity: SeverityWarning,
			File:     rel,
			Line:     pos.Line,
			Message: "use `tdd.RunRPCCases` for per-RPC table tests — see the testing/patterns skill " +
				"or run `forge project migrate tdd` to convert this file automatically.",
			Remediation: "import \"github.com/reliant-labs/forge/pkg/tdd\" and replace the `tests := []struct{name, call}` " +
				"slice with one `func TestXxx_Generated(t *testing.T)` per RPC delegating to `tdd.RunRPCCases`.",
		}
		matchSet = true
		return false
	})
	return match, matchSet
}

// isHandRolledTestsAssign returns true when the assignment matches the
// canonical hand-rolled shape:
//
//	tests := []struct {
//	    name string
//	    call func() error          // or func(client X) error
//	    ...
//	}{...}
//
// We require the LHS identifier to be `tests`, the RHS to be a slice of
// struct, and the struct to declare BOTH a `name` field of type string
// AND a `call` field of func type. Either the field-name or the
// field-type alone over-fires (a struct with a `call` int field would
// false-trigger; a struct with `name` only might be unrelated).
func isHandRolledTestsAssign(s *ast.AssignStmt) bool {
	if s.Tok != token.DEFINE {
		return false
	}
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false
	}
	id, ok := s.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "tests" {
		return false
	}
	cl, ok := s.Rhs[0].(*ast.CompositeLit)
	if !ok {
		return false
	}
	at, ok := cl.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	st, ok := at.Elt.(*ast.StructType)
	if !ok || st.Fields == nil {
		return false
	}

	var (
		hasName bool
		hasCall bool
	)
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			switch name.Name {
			case "name":
				if isStringType(field.Type) {
					hasName = true
				}
			case "call":
				if _, ok := field.Type.(*ast.FuncType); ok {
					hasCall = true
				}
			}
		}
	}
	return hasName && hasCall
}

func isStringType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

// relPath returns path relative to relRoot, falling back to path on Rel
// failure. Forge-conv findings use rel paths so CI logs stay stable.
func relPath(path, relRoot string) string {
	rel, err := filepath.Rel(relRoot, path)
	if err != nil {
		return path
	}
	return rel
}
