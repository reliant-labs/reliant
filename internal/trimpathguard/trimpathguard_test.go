// Package trimpathguard keeps forge's own tree working under -trimpath.
//
// WHY. The Reliant workspace image makes -trimpath the Go toolchain's default
// ($GOROOT/go.env), so worktrees of one repo at one commit share compiled
// packages instead of each compiling its own copy. Under -trimpath,
// runtime.Caller reports "github.com/reliant-labs/forge/internal/x/x_test.go"
// — a module-relative path that is not a file on disk — so a test that finds
// testdata or the repository root from its own source path passes on a laptop
// and in CI, and fails in every workspace. Every such helper in this tree used
// to do exactly that.
//
// A test runs in its package's source directory: resolve paths relative to
// it (filepath.Abs("testdata/x"), or walk up to the go.mod). This guard is a
// static scan, so it fails the same way whether or not the run is trimmed,
// and runs in `go test -short` in well under a second. It is the analyzer
// consumers get as `forge lint --caller-paths` (internal/linter/callerpath),
// run over forge's own tree: display-only uses pass, and optional discovery
// guarded by filepath.IsAbs (internal/buildinfo) needs a reasoned suppression.
package trimpathguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/callerpath"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/repowalk"
)

func TestNoSourcePathsFromRuntimeCaller(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected forge's go.mod at %s: %v", root, err)
	}
	skipDirs := map[string]bool{".git": true, "node_modules": true, "testdata": true, "vendor": true, "tmp": true, ".forge": true}

	err = repowalk.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		res := suppress.Apply(string(src), callerpath.CheckFile(filepath.ToSlash(rel), src))
		for _, f := range res.Kept {
			t.Errorf("%s:%d: %s. Fix: %s", f.File, f.Line, f.Message, f.Remediation)
		}
		for _, f := range res.Violations {
			t.Errorf("%s:%d: %s", f.File, f.Line, f.Message)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
