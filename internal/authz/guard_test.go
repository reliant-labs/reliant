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
	"regexp"
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

// shareGuard is what this head allows of sharing, pinned so a later change
// to it is deliberate (org-sharing S3 widens each list as it adds the rpcs).
var shareGuard = struct {
	// visibilityWriters may write a visibility column (SQL or ORM field).
	visibilityWriters map[string]string
	// applyCallers may call ApplyShareChange.
	applyCallers []string
	// viewerReadSites may serve viewers (WithViewerReads).
	viewerReadSites map[string]string
	// shareRPCs are the share rpcs that exist; none yet.
	shareRPCs []string
}{
	visibilityWriters: map[string]string{
		"internal/store/share_event_repo_ext.go": "ApplyShareChange, the one writer, and the shared lists' read predicates",
		"internal/viewertest/viewertest.go":      "the viewer-matrix fixture seeds a shared row; imported by _test.go files only",
	},
	applyCallers:    []string{"internal/db/", "internal/store/"},
	viewerReadSites: map[string]string{},
	shareRPCs:       nil,
}

// visibilityWrite matches a write of a visibility column: SQL that assigns
// it, or an ORM field constant naming it (generated constants live in
// *_gen.go, which is not scanned).
var visibilityWrite = regexp.MustCompile(`(?i)\bvisibility\s*=\s*('|\?|NULL|\$)|Set\(\s*"visibility|FieldVisibility\b`)

// goSources walks the non-test, non-generated Go files of internal/ and cmd/,
// reading each itself so Go's test cache sees it.
func goSources(t *testing.T, fn func(rel string, src []byte)) {
	t.Helper()
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			fn(filepath.ToSlash(rel), src)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestSharingIsNotReachableYet pins that at this head nothing can share a row
// or serve one to a viewer: the one writer of a visibility column is
// store.ApplyShareChange, only the store and the repository call it, no proto
// declares a share rpc, and no read site serves viewers. So a viewer can read
// a shared row only once a later change deliberately adds the rpc and the
// viewer-serving sites (with their redaction), and updates shareGuard.
func TestSharingIsNotReachableYet(t *testing.T) {
	goSources(t, func(rel string, src []byte) {
		if bytes.Contains(bytes.ToLower(src), []byte("visibility")) && visibilityWrite.Match(src) {
			if _, ok := shareGuard.visibilityWriters[rel]; !ok {
				t.Errorf("%s writes a visibility column: only store.ApplyShareChange may (shareGuard.visibilityWriters)", rel)
			}
		}
		if bytes.Contains(src, []byte("ApplyShareChange(")) {
			allowed := false
			for _, prefix := range shareGuard.applyCallers {
				allowed = allowed || strings.HasPrefix(rel, prefix)
			}
			if !allowed {
				t.Errorf("%s calls ApplyShareChange: no rpc may share yet (shareGuard.applyCallers)", rel)
			}
		}
		if !strings.HasPrefix(rel, "internal/authz/") && bytes.Contains(src, []byte("WithViewerReads(")) {
			if _, ok := shareGuard.viewerReadSites[rel]; !ok {
				t.Errorf("%s serves viewers: no read site may yet (shareGuard.viewerReadSites)", rel)
			}
		}
		if bytes.Contains(src, []byte(`"github.com/reliant-labs/reliant/internal/viewertest"`)) {
			t.Errorf("%s imports internal/viewertest outside a test", rel)
		}
	})

	root := filepath.Join("..", "..")
	protos, err := filepath.Glob(filepath.Join(root, "proto", "reliant", "v1", "*.proto"))
	if err != nil {
		t.Fatal(err)
	}
	rpc := regexp.MustCompile(`(?m)^\s*rpc\s+(Set\w*Visibility|ListShared\w*|StreamOrgUpdates)\s*\(`)
	var found []string
	for _, p := range protos {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range rpc.FindAllSubmatch(src, -1) {
			// SetWorkflowVisibility is the saved-workflow hidden flag, not sharing.
			if name := string(m[1]); name != "SetWorkflowVisibility" {
				found = append(found, filepath.Base(p)+":"+name)
			}
		}
	}
	sort.Strings(found)
	if strings.Join(found, ",") != strings.Join(shareGuard.shareRPCs, ",") {
		t.Errorf("share rpcs declared: %v, want %v (shareGuard.shareRPCs)", found, shareGuard.shareRPCs)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "gen", "forge_service_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(shareGuard.shareRPCs) == 0 && bytes.Contains(catalog, []byte("SharingService")) {
		t.Error("gen/forge_service_catalog.json names SharingService")
	}
}
