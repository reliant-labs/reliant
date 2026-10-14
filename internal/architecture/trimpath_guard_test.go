package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── no source paths from runtime.Caller ───────────────────────────────────
//
// The Reliant workspace image makes -trimpath the Go toolchain's default
// ($GOROOT/go.env), so worktrees of one repo at one commit share compiled
// packages instead of each compiling its own copy. Under -trimpath,
// runtime.Caller reports "github.com/reliant-labs/reliant/internal/x/x_test.go"
// — a module-relative path that is not a file on disk — so a test that finds
// testdata or the repo root from its own source path passes on a laptop and in
// CI, and fails (or, for a directory walk, silently scans nothing) in every
// workspace.
//
// A test runs in its package's source directory: resolve paths against it
// (filepath.Abs("testdata/x"), or root(t) above).

// callerFileBound matches a runtime.Caller call whose FILE result is kept:
// `_, file, _, ok := runtime.Caller(0)`, `if _, self, _, ok := runtime.Caller(1)`.
var callerFileBound = regexp.MustCompile(`(?m)^\s*(?:if\s+)?\w+\s*,\s*([A-Za-z_]\w*)\s*,\s*\w+\s*,\s*\w+\s*:?=\s*runtime\.Caller\(`)

func TestNoSourcePathsFromRuntimeCaller(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
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
		for _, m := range callerFileBound.FindAllStringSubmatchIndex(string(src), -1) {
			if string(src[m[2]:m[3]]) == "_" {
				continue
			}
			rel, _ := filepath.Rel(repo, path)
			line := 1 + strings.Count(string(src[:m[0]]), "\n")
			t.Errorf("%s:%d keeps runtime.Caller's file path. Under -trimpath (the Reliant workspace image's "+
				"GOFLAGS default) that is a module-relative path, not a file on disk. Resolve paths against the "+
				"package directory the test runs in instead (filepath.Abs, or walk up to the go.mod).", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}
}
