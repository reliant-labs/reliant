// File: internal/cli/lint/lint_fix_guard.go
//
// An auto-fix never leaves the tree failing to build.
//
// `forge lint --fix` rewrites Go source: the format pre-pass and the tracing
// lint's log-without-ctx rewrite. Before this guard nothing checked the
// result. The incident that made it necessary came from golangci-lint --fix
// (SA9004 gave reliant's untyped const block a protoreflect.FullName type and
// internal/workflow/yaml stopped compiling); golangci-lint no longer runs with
// --fix at all, but the lesson is general — a rewrite that type-checks in the
// fixer's head is not a rewrite that type-checks in the package — so every Go
// fixer now writes through a goFixSet and the set is settled afterwards:
//
//  1. type-check every package a fix touched (tests included);
//  2. for each package that now fails, put its files back and type-check it
//     as it was;
//  3. a package that type-checked BEFORE the fixes and not after keeps its
//     original bytes, and the fix is reported as not applied, with the
//     compiler's error; a package that already failed before keeps the fix —
//     the breakage is the author's, not the fixer's, and withholding a
//     formatting fix would not repair it.
//
// So a fix is rolled back exactly when it is what broke the package. The
// type-check is go/packages over the touched packages only, through go list,
// whose environment (goexec.Env) never lets it edit go.mod.

package lint

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/goexec"
)

// goFixSet is one batch of Go rewrites, applied and then verified as a unit.
type goFixSet struct {
	root   string
	before map[string]goFixOriginal // abs path → bytes before the first rewrite
	after  map[string][]byte        // abs path → the fix's bytes
}

type goFixOriginal struct {
	data []byte
	mode fs.FileMode
}

// goTypecheck type-checks the packages in dirs (absolute) and returns the
// first error of each dir that fails. Dirs that type-check are absent.
type goTypecheck func(root string, dirs []string) map[string]string

// revertedGoFix is a fix the guard took back.
type revertedGoFix struct {
	File   string // project-relative, slash-separated
	Reason string // the type-check error the fix caused
}

func newGoFixSet(root string) *goFixSet {
	return &goFixSet{root: root, before: map[string]goFixOriginal{}, after: map[string][]byte{}}
}

// write applies one rewrite, capturing the file's bytes before its first.
func (s *goFixSet) write(abs string, data []byte, mode fs.FileMode) error {
	if _, seen := s.before[abs]; !seen {
		orig, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		s.before[abs] = goFixOriginal{data: orig, mode: mode}
	}
	if err := atomicfile.WriteFile(abs, data, mode); err != nil {
		return err
	}
	s.after[abs] = data
	return nil
}

// dirsOf groups the set's files by package directory.
func (s *goFixSet) dirsOf(files map[string][]byte) map[string][]string {
	out := map[string][]string{}
	for abs := range files {
		d := filepath.Dir(abs)
		out[d] = append(out[d], abs)
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// settle verifies the set (see the file comment) and returns the fixes it
// rolled back, sorted by file.
func (s *goFixSet) settle(check goTypecheck) []revertedGoFix {
	if len(s.after) == 0 {
		return nil
	}
	byDir := s.dirsOf(s.after)
	after := check(s.root, sortedKeys(byDir))
	if len(after) == 0 {
		return nil
	}
	failing := map[string][]string{}
	for dir := range after {
		failing[dir] = byDir[dir]
	}
	for _, files := range failing {
		for _, abs := range files {
			orig := s.before[abs]
			_ = atomicfile.WriteFile(abs, orig.data, orig.mode)
		}
	}
	before := check(s.root, sortedKeys(failing))
	var reverted []revertedGoFix
	for dir, files := range failing {
		if _, brokeAlready := before[dir]; brokeAlready {
			// Already failing without the fix: not the fixer's breakage.
			for _, abs := range files {
				_ = atomicfile.WriteFile(abs, s.after[abs], s.before[abs].mode)
			}
			continue
		}
		for _, abs := range files {
			rel, err := filepath.Rel(s.root, abs)
			if err != nil {
				rel = abs
			}
			reverted = append(reverted, revertedGoFix{File: filepath.ToSlash(rel), Reason: after[dir]})
			delete(s.after, abs)
		}
	}
	sort.Slice(reverted, func(i, j int) bool { return reverted[i].File < reverted[j].File })
	return reverted
}

// applied reports whether the set still holds abs's fix after settle.
func (s *goFixSet) applied(abs string) bool {
	_, ok := s.after[abs]
	return ok
}

// printRevertedGoFixes reports the fixes the guard took back.
func printRevertedGoFixes(w io.Writer, fixer string, reverted []revertedGoFix) {
	for _, r := range reverted {
		_, _ = fmt.Fprintf(w, "⚠️  %s: fix NOT applied to %s — it left the package failing to type-check (%s); the file is as you left it\n",
			fixer, r.File, r.Reason)
	}
}

// fixTypecheck is the type-check every Go fixer settles through. A variable
// only so this package's other tests — formatting fixtures in temp trees,
// hundreds of them — need not spawn `go list` each; the guard's own tests
// drive typecheckGoPackages directly.
var fixTypecheck goTypecheck = typecheckGoPackages

// typecheckGoPackages is the production goTypecheck: go/packages over the
// named package directories, test files included, deps from export data.
func typecheckGoPackages(root string, dirs []string) map[string]string {
	if len(dirs) == 0 {
		return nil
	}
	patterns := make([]string, 0, len(dirs))
	want := map[string]bool{}
	for _, d := range dirs {
		want[d] = true
		rel, err := filepath.Rel(root, d)
		if err != nil {
			rel = d
		}
		patterns = append(patterns, "./"+filepath.ToSlash(rel))
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:   root,
		Env:   goexec.Env(),
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	failed := map[string]string{}
	if err != nil {
		// The loader itself could not run: every dir is unverified, which
		// settle treats as failing both before and after — fixes kept, as
		// there is no build to compare.
		for d := range want {
			failed[d] = err.Error()
		}
		return failed
	}
	for _, p := range pkgs {
		if len(p.Errors) == 0 {
			continue
		}
		dir := packageDir(p)
		if !want[dir] {
			continue
		}
		if _, seen := failed[dir]; !seen {
			failed[dir] = strings.TrimSpace(p.Errors[0].Error())
		}
	}
	return failed
}

// packageDir is the directory a loaded package's own sources live in.
func packageDir(p *packages.Package) string {
	if p.Dir != "" {
		return filepath.Clean(p.Dir)
	}
	for _, f := range p.GoFiles {
		return filepath.Dir(f)
	}
	return ""
}
