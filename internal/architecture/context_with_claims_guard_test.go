package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── ContextWithClaims stays in tests ──────────────────────────────────────
//
// pkg/middleware.ContextWithClaims is the seam forge's generated handler test
// harness authenticates through. It installs any principal it is handed as a
// person signed in to the app (auth.CredentialSession), because that is what
// the harness's default principal stands for. On a serving path it would
// upgrade whatever principal reached it — a machine token's included — to a
// credential that may decide approvals (auth.RequirePerson). A serving path
// classifies its caller in the validator that verified the bearer instead.

func TestContextWithClaimsIsTestOnly(t *testing.T) {
	t.Parallel()

	repo := root(t)
	definition := filepath.Join(repo, "pkg", "middleware", "claims.go")
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true, ".forge": true,
	}
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == definition {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "ContextWithClaims") {
			return nil
		}
		// Parse, so a comment naming the seam is not a use of it.
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != "ContextWithClaims" {
				return true
			}
			rel, _ := filepath.Rel(repo, path)
			t.Errorf("%s:%d references middleware.ContextWithClaims outside a test. It installs every principal "+
				"as an app session (auth.CredentialSession), which may decide approvals; a serving path must "+
				"classify its caller in the validator that verified the bearer.", rel, fset.Position(id.Pos()).Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}
}
