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
// and runs in `go test -short` in well under a second.
package trimpathguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/repowalk"
)

// callerFileBound matches a runtime.Caller call whose FILE result is kept:
// `_, file, _, ok := runtime.Caller(0)`, `if _, self, _, ok = runtime.Caller(1)`.
var callerFileBound = regexp.MustCompile(`(?m)^\s*(?:if\s+)?\w+\s*,\s*([A-Za-z_]\w*)\s*,\s*\w+\s*,\s*\w+\s*:?=\s*runtime\.Caller\(`)

// allowed are files whose use of the file result already copes with a
// trimmed path, keyed by slash path from the repository root.
var allowed = map[string]string{
	// Only an ABSOLUTE path is used (it walks to a checkout to bridge a dev
	// forge); a trimmed, module-relative one returns "" — a release build.
	"internal/buildinfo/buildinfo.go": "checks filepath.IsAbs before using the path",
}

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
		if !strings.Contains(string(src), "runtime.Caller(") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := allowed[rel]; ok {
			return nil
		}
		for _, m := range callerFileBound.FindAllStringSubmatchIndex(string(src), -1) {
			if string(src[m[2]:m[3]]) == "_" {
				continue
			}
			line := 1 + strings.Count(string(src[:m[0]]), "\n")
			t.Errorf("%s:%d keeps runtime.Caller's file path. Under -trimpath (the Reliant workspace image's "+
				"GOFLAGS default) that is a module-relative path, not a file on disk. Resolve paths against the "+
				"package directory the test runs in instead (filepath.Abs, or walk up to the go.mod).", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
