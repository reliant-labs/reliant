// File: internal/linter/forgeconv/no_handler_error_mapping.go
//
// The forgeconv-no-handler-error-mapping analyzer warns when a handler
// package re-rolls the canonical service-error → connect.Error switch
// statement that ships in forge/pkg/svcerr.
//
// It is one of the two rules LintHandlerErrorMapping runs over the
// handler tree — the two directions the same convention can be broken
// in. This one catches TOO MUCH mapping (a re-rolled per-service
// switch); forgeconv-unwrapped-domain-error, in
// unwrapped_domain_error.go, catches NONE AT ALL (`return nil, err`
// straight out of a collaborator call). The second is the one that
// silently ships, so it is the one at error severity.
//
// Why this exists
//
// The 2026-05-06 dogfood pass of control-plane-next surfaced 4
// byte-identical copies of `toConnectError` in
// handlers/{billing,daemon,llm_gateway,org}/handlers.go. Each copy
// switched on the same internal/svcerr.Err* sentinels and mapped to the
// same connect.Code values. The api/handlers skill at the time
// prescribed the per-service helper, so the duplication was earned —
// not a sloppiness signal but a convention signal. Pairing the new
// pkg/svcerr ship with this lint nudge prevents the next project from
// repeating the pattern.
//
// Heuristic
//
// We flag any function/method declared in a Go file under handlers/
// (or under any directory whose package name suggests handler code)
// that looks like a hand-rolled error-mapping helper. Three signals:
//
//   1. Function name matches a known mapping-helper name (mapServiceError,
//      toConnectError, errToConnect, mapErr, etc.).
//   2. Two or more errors.Is / errors.As sentinel tests select a switch arm
//      (or an if body) that produces a CONNECT ERROR: it returns a
//      connect.NewError as its error, or returns or assigns a connect code.
//      Arms that pick something else — a response status, a retry — are
//      not mapping, however many sentinels they test.
//
// Either signal alone is suspicious; combined they are the duplication
// pattern this rule targets. Severity is warning (not error): the
// false-positive risk is non-trivial — some projects legitimately need
// custom mapping for project-specific sentinels — and the cost of a
// stray warning is low.
//
// The remediation message points at `svcerr.Wrap(err)` so the LLM has
// the canonical replacement on screen at the moment of friction.

package forgeconv

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

// LintHandlerErrorMapping walks rootDir for Go files under handlers/
// directories and runs the handler error rules over each:
//
//   - forgeconv-no-handler-error-mapping — a hand-rolled service-error →
//     connect.Error helper (this file). Warning.
//   - forgeconv-unwrapped-domain-error — a domain error returned across
//     the RPC boundary with nothing applied to it
//     (unwrapped_domain_error.go). Error.
//   - forgeconv-handler-connect-error — a hand-built connect.NewError,
//     which publishes its error's text unredacted
//     (handler_connect_error.go). Warning; a project flips it to error
//     once migrated.
//
// Returns findings in deterministic order.
//
// The walk recognises any directory whose path component is exactly
// "handlers" as the canonical handler tree (matches the
// project-template `internal/handlers/<svc>/` shape). Test files
// (_test.go) are skipped — fixtures and table
// tests sometimes legitimately construct connect errors for assertions.
func LintHandlerErrorMapping(rootDir string) (Result, error) {
	var goFiles []string
	walkErr := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip generated / vendored / tooling subtrees outright so the
			// analyzer stays cheap on real projects.
			base := d.Name()
			if base == "gen" || base == "node_modules" || base == ".git" ||
				base == "testdata" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Path-based filter: only files under a `handlers/` directory.
		// Use slash-form for cross-platform comparison.
		slashPath := filepath.ToSlash(path)
		if !strings.Contains(slashPath, "/handlers/") && !strings.HasPrefix(slashPath, "handlers/") {
			return nil
		}
		goFiles = append(goFiles, path)
		return nil
	})
	if walkErr != nil {
		return Result{}, fmt.Errorf("walk %s: %w", rootDir, walkErr)
	}

	sort.Strings(goFiles)

	var result Result
	for _, p := range goFiles {
		rel, relErr := filepath.Rel(rootDir, p)
		if relErr != nil {
			rel = p
		}
		fset := token.NewFileSet()
		// ParseComments: forgeconv-handler-connect-error skips generated
		// files, which only their header comment identifies.
		parsed, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			// A parse failure shouldn't blank the report; emit a warning so
			// the user knows the file was skipped.
			result.Findings = append(result.Findings, Finding{
				Rule:     "forgeconv-no-handler-error-mapping",
				Severity: SeverityWarning,
				File:     rel,
				Message:  fmt.Sprintf("could not parse handler file: %v", err),
			})
			continue
		}
		result.Findings = append(result.Findings, lintHandlerErrorMappingFile(rel, fset, parsed)...)
		result.Findings = append(result.Findings, lintUnwrappedDomainErrorFile(rel, fset, parsed)...)
		result.Findings = append(result.Findings, lintHandlerConnectErrorFile(rel, fset, parsed)...)
	}

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

// suspectMapperNames lists the function names the dogfood pass found
// (and a few likely siblings). The check is an OR with the body-shape
// heuristic; either signal triggers, both signals together strengthen
// the message.
var suspectMapperNames = map[string]bool{
	"mapServiceError": true,
	"toConnectError":  true,
	"errToConnect":    true,
	"mapErr":          true,
	"mapError":        true,
	"asConnectError":  true,
	"toConnectErr":    true,
	"connectErrorOf":  true,
}

// lintHandlerErrorMappingFile inspects one parsed handler-tree Go file
// and flags candidate mapping helpers. Returns at most one finding per
// candidate function — no per-line spam.
//
// A file that imports svcerr is linted like any other. It used to be
// skipped, on the theory that a file on the canonical path keeps a leftover
// helper on purpose. What that did in practice was let any import of the
// package — one svcerr.Wrap anywhere in the file, or a codemod adding the
// import — silence every mapper beside it without fixing one: a switch
// that maps sentinels to connect codes next to svcerr.Wrap is still the
// switch svcerr replaces.
func lintHandlerErrorMappingFile(relPath string, fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		nameSuspect := suspectMapperNames[fn.Name.Name]
		bodySuspect, sentinelCount := bodyLooksLikeMapper(fn)

		if !nameSuspect && !bodySuspect {
			continue
		}

		pos := fset.Position(fn.Pos())
		message := mappingFindingMessage(fn.Name.Name, nameSuspect, bodySuspect, sentinelCount)

		findings = append(findings, Finding{
			Rule:     "forgeconv-no-handler-error-mapping",
			Severity: SeverityWarning,
			File:     relPath,
			Line:     pos.Line,
			Message:  message,
			Remediation: "delete this helper and use `svcerr.Wrap(err)` from `github.com/reliant-labs/forge/pkg/svcerr`. " +
				"Define your domain sentinels as `svcerr.ErrNotFound` etc. (or wrap them via `svcerr.NotFound(\"user\")`) " +
				"in `internal/<pkg>/contract.go`; the handler then becomes one line: `return nil, svcerr.Wrap(err)`. " +
				"See skill: api/handlers.",
		})
	}

	return findings
}

// bodyLooksLikeMapper inspects the function body for the canonical
// "switch on errors.Is(err, sentinel) → connect.NewError(connect.CodeX, err)"
// shape. Returns (true, sentinelCount) when at least two sentinel tests
// (errors.Is / errors.As) select an arm that MAPS to a connect error. The
// threshold is intentionally modest — a one-arm switch could legitimately
// be a domain-specific translation, but two or more sentinels mapped to
// connect errors is the duplication pattern.
//
// An arm is a `case` clause of a switch or the body of an `if`, and it is
// counted only when what it produces is a connect error (see armMapsToConnect).
// Seeing errors.Is and connect.NewError anywhere in one function is not
// enough: a handler routinely does both for unrelated reasons. reliant's
// PollCopilotDeviceAuth switches on four device-flow sentinels to pick a
// RESPONSE status (PENDING / EXPIRED / DENIED) and returns connect.NewError
// only for a missing argument and a failed save — that is not a mapper, and
// svcerr.Wrap could not replace it.
func bodyLooksLikeMapper(fn *ast.FuncDecl) (bool, int) {
	if fn.Body == nil {
		return false, 0
	}
	buildsError := containsNode(fn.Body, isConnectNewError)

	sentinels := 0
	arm := func(cond ast.Expr, body []ast.Stmt) {
		n := countSentinelTests(cond)
		if n > 0 && armMapsToConnect(body, buildsError) {
			sentinels += n
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CaseClause:
			for _, e := range x.List {
				arm(e, x.Body)
			}
		case *ast.IfStmt:
			arm(x.Cond, x.Body.List)
		}
		return true
	})
	return sentinels >= 2, sentinels
}

// countSentinelTests counts the errors.Is / errors.As calls in a condition.
func countSentinelTests(cond ast.Expr) int {
	if cond == nil {
		return 0
	}
	n := 0
	ast.Inspect(cond, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok &&
			(isQualifiedCall(call.Fun, "errors", "Is") || isQualifiedCall(call.Fun, "errors", "As")) {
			n++
		}
		return true
	})
	return n
}

// armMapsToConnect reports whether an arm's statements turn the sentinel
// into a connect error, in one of the three spellings a mapper takes:
//
//   - it RETURNS a connect.NewError(...) as its error (the last result);
//   - it RETURNS a connect code (`return connect.CodeNotFound`, a code
//     mapper whose caller builds the error);
//   - it ASSIGNS a connect code (`code = connect.CodeNotFound`) in a
//     function that builds a connect.NewError — the switch picks the code,
//     one NewError after it uses it.
//
// A nested function literal is its own scope, not the arm's result, and is
// not looked into.
func armMapsToConnect(body []ast.Stmt, fnBuildsError bool) bool {
	found := false
	for _, stmt := range body {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if found {
				return false
			}
			switch x := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				if len(x.Results) == 0 {
					return true
				}
				if containsNode(x.Results[len(x.Results)-1], isConnectNewError) {
					found = true
				}
				for _, r := range x.Results {
					if isConnectCode(r) {
						found = true
					}
				}
			case *ast.AssignStmt:
				if !fnBuildsError {
					return true
				}
				for _, r := range x.Rhs {
					if isConnectCode(r) {
						found = true
					}
				}
			case *ast.ValueSpec:
				if !fnBuildsError {
					return true
				}
				for _, v := range x.Values {
					if isConnectCode(v) {
						found = true
					}
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// isConnectNewError matches a `connect.NewError(...)` call.
func isConnectNewError(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	return ok && isQualifiedCall(call.Fun, "connect", "NewError")
}

// isConnectCode matches a `connect.Code<Name>` constant.
func isConnectCode(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || !strings.HasPrefix(sel.Sel.Name, "Code") || sel.Sel.Name == "Code" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "connect"
}

// containsNode reports whether any node under root satisfies match.
func containsNode(root ast.Node, match func(ast.Node) bool) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		if match(n) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isQualifiedCall reports whether expr is a selector of the form
// `pkg.fn`. Used to spot connect.NewError / errors.Is without dragging
// in a full type-checker.
func isQualifiedCall(expr ast.Expr, pkg, fn string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel == nil || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == pkg
}

// mappingFindingMessage composes a context-aware diagnostic — the
// LLM-readable hint scales with how confident the rule is.
func mappingFindingMessage(name string, nameSuspect, bodySuspect bool, arms int) string {
	switch {
	case nameSuspect && bodySuspect:
		return fmt.Sprintf(
			"function %q maps %d sentinel(s) to connect errors — "+
				"this is the per-service mapping helper that pkg/svcerr replaces. "+
				"Use `svcerr.Wrap(err)` instead.",
			name, arms)
	case nameSuspect:
		return fmt.Sprintf(
			"function %q has the canonical service-error mapper name — "+
				"projects on forge >=1.7 use `svcerr.Wrap(err)` from forge/pkg/svcerr instead.",
			name)
	default:
		return fmt.Sprintf(
			"function %q maps %d sentinel(s) to connect errors — "+
				"this looks like a hand-rolled service-error mapper. "+
				"Use `svcerr.Wrap(err)` from forge/pkg/svcerr.",
			name, arms)
	}
}
