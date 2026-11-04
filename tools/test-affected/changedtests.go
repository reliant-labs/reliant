// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The new-flake gate: which Go tests does this diff add or edit?
//
//	go run ./tools/test-affected changed-tests [-base <rev>]
//
// prints one line per package, `./<pkg>\t^(TestA|TestB)$`, naming every
// top-level Test function whose body the diff touches in a _test.go file that
// builds without extra tags. PR CI runs exactly those with `go test -count=3`:
// a test that passes once and fails on a repeat is a flake, and the cheapest
// moment to catch one is the PR that wrote it, before it reaches main and
// starts failing somebody else's run.
//
// It selects by FUNCTION, not by file, so touching one test in a 3000-line
// file repeats that test alone. A changed helper (a non-Test func) selects
// nothing: the gate is for tests the diff wrote, and the full lanes still run
// every test that calls the helper once.

// lineRange is an inclusive range of line numbers in the NEW version of a file.
type lineRange struct{ from, to int }

// changedPkg is one package's tests to repeat.
type changedPkg struct {
	Dir   string   // ./relative package directory
	Tests []string // sorted, unique
}

// runPattern is the -run regexp that selects exactly pkg's tests.
func (p changedPkg) runPattern() string {
	quoted := make([]string, len(p.Tests))
	for i, t := range p.Tests {
		quoted[i] = regexp.QuoteMeta(t)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// parseZeroContextDiff reads `git diff -U0` output and returns, per file, the
// line ranges of the new version the diff touched. A pure deletion (`+c,0`)
// touches the lines either side of where the text was, so a test that only
// LOST lines still counts as edited.
func parseZeroContextDiff(diffText string) map[string][]lineRange {
	out := map[string][]lineRange{}
	file := ""
	for _, line := range strings.Split(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = ""
			if p, ok := strings.CutPrefix(line, "+++ b/"); ok {
				file = p
			}
		case strings.HasPrefix(line, "@@ ") && file != "":
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				out[file] = append(out[file], lineRange{start, start + 1})
			} else {
				out[file] = append(out[file], lineRange{start, start + count - 1})
			}
		}
	}
	return out
}

var testFuncName = regexp.MustCompile(`^Test($|[^a-z])`)

// isTestFunc reports whether fd is a top-level `func TestXxx(t *testing.T)`.
func isTestFunc(fd *ast.FuncDecl) bool {
	if fd.Recv != nil || !testFuncName.MatchString(fd.Name.Name) {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

// testsTouched returns the Test functions in src whose lines intersect touched.
func testsTouched(filename string, src []byte, touched []lineRange) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestFunc(fd) {
			continue
		}
		from, to := fset.Position(fd.Pos()).Line, fset.Position(fd.End()).Line
		for _, r := range touched {
			if r.from <= to && r.to >= from {
				names = append(names, fd.Name.Name)
				break
			}
		}
	}
	return names, nil
}

// selectChangedTests maps touched _test.go files to the tests to repeat.
// read returns a file's current contents (false when it no longer exists);
// builds reports whether the file compiles in a plain `go test` (no -tags).
func selectChangedTests(touched map[string][]lineRange, read func(string) ([]byte, bool), builds func(string) bool) ([]changedPkg, error) {
	byDir := map[string]map[string]bool{}
	for file, ranges := range touched {
		if !strings.HasSuffix(file, "_test.go") || !builds(file) {
			continue
		}
		src, ok := read(file)
		if !ok {
			continue
		}
		names, err := testsTouched(file, src, ranges)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		dir := "./" + path.Dir(file)
		if path.Dir(file) == "." {
			dir = "."
		}
		for _, n := range names {
			if byDir[dir] == nil {
				byDir[dir] = map[string]bool{}
			}
			byDir[dir][n] = true
		}
	}
	out := make([]changedPkg, 0, len(byDir))
	for dir, set := range byDir {
		p := changedPkg{Dir: dir}
		for n := range set {
			p.Tests = append(p.Tests, n)
		}
		sort.Strings(p.Tests)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

func runChangedTests(args []string, stdout io.Writer) error {
	fs := newFlagSet("changed-tests")
	root := fs.String("root", ".", "repository root")
	baseFlag := fs.String("base", "", "base commit-ish (default: the merge-base with origin/main, or $TEST_BASE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *baseFlag == "" {
		*baseFlag = os.Getenv("TEST_BASE")
	}
	pkgs, err := changedTests(context.Background(), mustAbs(*root), *baseFlag)
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\n", p.Dir, p.runPattern()); err != nil {
			return err
		}
	}
	return nil
}

// changedTests is the Test functions the diff from baseFlag (the merge-base
// with origin/main when empty) adds or edits, per package.
func changedTests(ctx context.Context, rootDir, baseFlag string) ([]changedPkg, error) {
	changes, base, _, err := diff(ctx, rootDir, baseFlag)
	if err != nil {
		return nil, err
	}
	text, err := git(ctx, rootDir, "diff", "-U0", "--no-renames", "--no-color", base, "--", "*_test.go")
	if err != nil {
		return nil, err
	}
	touched := parseZeroContextDiff(text)
	// An untracked test file is new in its entirety.
	for _, c := range changes {
		if !c.Deleted && strings.HasSuffix(c.Path, "_test.go") {
			if _, seen := touched[c.Path]; !seen {
				touched[c.Path] = []lineRange{{1, 1 << 30}}
			}
		}
	}
	read := func(p string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(p)))
		return b, err == nil
	}
	ctxBuild := build.Default
	builds := func(p string) bool {
		ok, err := ctxBuild.MatchFile(filepath.Join(rootDir, filepath.FromSlash(path.Dir(p))), path.Base(p))
		return err == nil && ok
	}
	return selectChangedTests(touched, read, builds)
}
