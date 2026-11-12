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

// rawComparisonAllowed names the comparisons of a `UserID` field that are not
// a caller-may-act-on-resource decision, keyed "<file>:<func>". Anything else
// that compares a UserID field must ask this package, so the rule (and, from
// phase 4, the org) lives in one place.
var rawComparisonAllowed = map[string]string{
	"internal/triggers/origin.go:fromWrite":                                           "matches two rows' owners (an integration write and its trigger), no caller",
	"internal/triggers/workflowevent/dispatch.go:Dispatch":                            "matches a run event to the triggers of the same user, no caller",
	"internal/db/repository_impl.go:UpsertDaemon":                                     "re-registration consistency: the same user re-registering the same daemon",
	"internal/tokenauthority/localstore.go:MintForUser":                               "self-hosted orgs: the only org a user has is their own id",
	"internal/tokenauthority/memory.go:MintForUser":                                   "self-hosted orgs: the only org a user has is their own id",
	"internal/workflow/workflowlisttest/workflowlisttest.go:GetProjectWithUserCheck":  "test fake of the store's owner-filtered read",
	"internal/workflow/workflowlisttest/workflowlisttest.go:ListWorkflowDraftsByUser": "test fake of the store's owner-filtered list",
	"internal/workflow/workflowlisttest/workflowlisttest.go:draftBySlug":              "test fake of the store's owner-filtered read",
}

// rawComparisonAllowedDirs are whole layers below the decision: the store's
// owner-filtered queries are visibility predicates (`user_id = ?` in SQL, or
// its Go equivalent on a row it just read), which internal/db confirms
// through this package.
var rawComparisonAllowedDirs = map[string]string{
	"internal/store/":          "owner-filtered queries (visibility predicates), confirmed by internal/db through authz",
	"internal/connectorgrant/": "the grant store's owner-filtered read",
	"internal/authz/":          "the rule itself",
}

// TestNoRawOwnershipComparisons fails on a new `x.UserID != y` (or ==)
// outside this package. Ask CanReadChat / CanWriteProject / ... instead, or,
// for a comparison that is not an access decision, add it to
// rawComparisonAllowed with the reason.
//
// It reads every file itself (os.ReadFile), so Go's test cache sees each one.
func TestNoRawOwnershipComparisons(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	used := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
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
			if !bytes.Contains(src, []byte("UserID")) {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for prefix := range rawComparisonAllowedDirs {
				if strings.HasPrefix(rel, prefix) {
					return nil
				}
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
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					be, ok := n.(*ast.BinaryExpr)
					if !ok || (be.Op != token.EQL && be.Op != token.NEQ) {
						return true
					}
					if !isUserIDField(be.X) && !isUserIDField(be.Y) {
						return true
					}
					if isZeroLiteral(be.X) || isZeroLiteral(be.Y) {
						return true
					}
					key := rel + ":" + fn.Name.Name
					if _, ok := rawComparisonAllowed[key]; ok {
						used[key] = true
						return true
					}
					found = append(found, fset.Position(be.Pos()).String()+" in "+fn.Name.Name)
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("raw ownership comparison at %s: ask internal/authz instead", f)
	}
	for key := range rawComparisonAllowed {
		if !used[key] {
			t.Errorf("rawComparisonAllowed[%q] matches nothing any more: remove it", key)
		}
	}
}

func isUserIDField(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "UserID" || sel.Sel.Name == "OwnerUserID")
}

// isZeroLiteral is `""` or nil: a presence test, not a comparison of owners.
func isZeroLiteral(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value == `""`
	case *ast.Ident:
		return v.Name == "nil"
	}
	return false
}

// visibilityReadAllowedDirs may read a row's Visibility: the store that maps
// the column, and the rule itself.
var visibilityReadAllowedDirs = []string{"internal/store/", "internal/authz/"}

// rowPackages are the imports a file needs to hold a row type that carries
// Visibility (core.Chat, core.Project, ...), directly or through internal/db's
// aliases.
var rowPackages = []string{
	`"github.com/reliant-labs/reliant/internal/db/core"`,
	`"github.com/reliant-labs/reliant/internal/db"`,
}

// TestNoVisibilityReadsOutsideAuthz keeps the sharing rule in one place
// (control-plane docs/design/org-sharing.md §3.2): no code outside
// internal/authz and internal/store reads a row's `.Visibility`. Decide with
// CanReadChat / IsViewer; show it with ChatVisibility / ProjectVisibility.
//
// A file is inspected when it imports a package that holds the row types, so
// an unrelated Visibility field (an LLM model's) in a file that never imports
// them is not flagged. It reads every file itself (os.ReadFile), so Go's test
// cache sees each one.
func TestNoVisibilityReadsOutsideAuthz(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, prefix := range visibilityReadAllowedDirs {
				if strings.HasPrefix(rel, prefix) {
					return nil
				}
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Contains(src, []byte("Visibility")) {
				return nil
			}
			holdsRows := false
			for _, imp := range rowPackages {
				holdsRows = holdsRows || bytes.Contains(src, []byte(imp))
			}
			if !holdsRows {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Visibility" {
					found = append(found, fset.Position(sel.Pos()).String())
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("a row's Visibility read at %s: ask internal/authz (CanReadChat, IsViewer, ChatVisibility) instead", f)
	}
}
