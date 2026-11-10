// Copyright (c) 2025 Reliant Labs

package authz

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// absentFallbackAllowed names the branches, keyed "<file>:<func>:<condition>",
// where a decision that asks this package may still answer true because
// something is absent (a nil or "" org, project or gate), with the reason it
// is not an access decision.
var absentFallbackAllowed = map[string]string{
	"internal/handlers/streaming/user_update_orgs.go:allows:g == nil":                                                                     "a nil gate is a filter built without one (a test of the project filter); the stream always installs one",
	"internal/handlers/mcp/mcp_config.go:IsEnabled:c.Enabled == nil || *c.Enabled":                                                        "an MCP server's enabled flag defaults on; no caller or resource",
	"internal/handlers/project/rpc_project_daemons.go:projectDirIsHealable:p.RemoteURL == nil || strings.TrimSpace(*p.RemoteURL) == \"\"": "whether a directory may be made, of a project already authorized",
	"internal/handlers/terminal/terminal_ws.go:workingDirMayAppear:c.checkouts == nil || worktreeID == \"\"":                              "whether a working directory may still appear on disk; no access decision",
	"internal/handlers/terminal/terminal_ws.go:workingDirMayAppear:err != nil || wt == nil":                                               "whether a working directory may still appear on disk; no access decision",
	"internal/handlers/terminal/terminal_ws.go:isMainCheckout:worktreeID == \"\"":                                                         "the main checkout has no worktree id; no access decision",
}

// TestNoAbsentFallbackBypassesAuthz fails on a branch that answers true
// because a value is absent, in any file that decides through this package
// (a helper a Visible predicate calls is in scope too): `if org == nil { return true }`, `case got == nil: return
// true`, or `return id == "" || decide(...)`. A row of no org is not
// everyone's: a credential bound to an org reaches it only through its
// owner's default org (credentialReaches), so ask CanReadOwn(ctx, userID,
// nil) or the resource's own Can* instead. A fallback that is not an access
// decision goes in absentFallbackAllowed with the reason.
//
// It reads every file itself (os.ReadFile), so Go's test cache sees each one.
func TestNoAbsentFallbackBypassesAuthz(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	used := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/authz/") || !bytes.Contains(src, []byte("authz.")) {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			flag := func(n ast.Node, cond ast.Expr) {
				key := rel + ":" + fn.Name.Name + ":" + string(src[fset.Position(cond.Pos()).Offset:fset.Position(cond.End()).Offset])
				if _, ok := absentFallbackAllowed[key]; ok {
					used[key] = true
					return
				}
				found = append(found, fset.Position(n.Pos()).String()+" in "+key)
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.IfStmt:
					if testsAbsence(x.Cond) && returnsTrue(x.Body.List) {
						flag(x, x.Cond)
					}
				case *ast.CaseClause:
					for _, e := range x.List {
						if testsAbsence(e) && returnsTrue(x.Body) {
							flag(x, e)
						}
					}
				case *ast.ReturnStmt:
					if len(x.Results) == 1 && orsAbsence(x.Results[0]) {
						flag(x, x.Results[0])
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("a branch at %s answers true because a value is absent: decide it through internal/authz (CanReadOwn(ctx, userID, nil) for a row of no org)", f)
	}
	for key := range absentFallbackAllowed {
		if !used[key] {
			t.Errorf("absentFallbackAllowed[%q] matches nothing any more: remove it", key)
		}
	}
}

// testsAbsence reports whether e compares something with nil or "" for
// equality, alone or in a chain of || and &&.
func testsAbsence(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.EQL && (isZeroLiteral(be.X) || isZeroLiteral(be.Y)) {
			found = true
		}
		return !found
	})
	return found
}

// orsAbsence reports whether e is `<absence> || ...`: true whenever the value
// is absent, whatever the rest decides.
func orsAbsence(e ast.Expr) bool {
	be, ok := ast.Unparen(e).(*ast.BinaryExpr)
	if !ok || be.Op != token.LOR {
		return false
	}
	for _, side := range []ast.Expr{be.X, be.Y} {
		side = ast.Unparen(side)
		if s, ok := side.(*ast.BinaryExpr); ok && s.Op == token.EQL && (isZeroLiteral(s.X) || isZeroLiteral(s.Y)) {
			return true
		}
		if orsAbsence(side) {
			return true
		}
	}
	return false
}

func returnsTrue(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		if r, ok := s.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			if id, ok := r.Results[0].(*ast.Ident); ok && id.Name == "true" {
				return true
			}
		}
	}
	return false
}
