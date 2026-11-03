// Package fmtguard keeps forge's own tree gofmt-clean.
//
// WHY A TEST AND NOT JUST THE LINTER. `forge lint` AUTO-FIXES formatting by
// default before it gates. That is the right default for a user, and it is
// exactly why an unformatted file on main is expensive here: every agent and
// every contributor who runs `forge lint` gets the same unrelated files
// rewritten in their working tree, and the churn rides into whatever PR they
// open next. Main carried five such files for long enough that it happened on
// every branch. The linter could not catch it, because the linter is the
// thing that silently repaired it.
//
// This guard fails instead of fixing, runs in `go test -short` in about a
// second, and needs no golangci-lint binary.
package fmtguard

import (
	"bytes"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrackedGoFilesAreGofmtClean asserts every git-tracked .go file outside
// testdata is already in gofmt's canonical form.
//
// testdata is skipped because fixtures there are inputs to formatters and
// linters under test; some are deliberately unformatted.
func TestTrackedGoFilesAreGofmtClean(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every file in the forge repository; runs in task test")
	}
	t.Parallel()
	root := repoRoot(t)

	cmd := exec.Command("git", "ls-files", "-z", "--", "*.go")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("not a git checkout (git ls-files: %v) — the guard needs the tracked set", err)
	}

	checked := 0
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" || strings.Contains(filepath.ToSlash(rel), "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			// Tracked but deleted in this working tree: not ours to judge.
			continue
		}
		formatted, err := format.Source(src)
		if err != nil {
			t.Errorf("%s does not parse: %v", rel, err)
			continue
		}
		checked++
		if !bytes.Equal(src, formatted) {
			t.Errorf("%s is not gofmt-clean. Run `gofmt -w %s`. An unformatted file on main is "+
				"rewritten by every `forge lint` run (it auto-fixes by default), so the diff lands in "+
				"unrelated PRs.", rel, rel)
		}
	}
	if checked == 0 {
		t.Fatal("checked no files — the guard is broken, and a guard that inspects nothing certifies everything")
	}
}

// repoRoot is two levels above this package's source directory, which is the
// working directory `go test` runs it in whatever directory it was invoked
// from.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return root
}
