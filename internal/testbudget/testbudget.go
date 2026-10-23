// Package testbudget is the measured half of forge's fast/slow test tiers: it
// reads a `go test -short -json` run and reports every test that took longer
// than the budget.
//
// Under -short a test marked slow (forge/pkg/testkit.Slow, or an
// `if testing.Short() { t.Skip(...) }` guard) skips, so every test that RAN
// in a -short run is, by construction, one that claims to be fast. One that
// took longer than the budget is a slow test nobody marked: it is making the
// blocking tier slow for everyone. The static half (internal/linter/slowtest)
// flags slow work it can recognize in source; this catches whatever makes a
// test slow, after it ran slowly once.
//
// Only top-level tests are judged. That is where the marker goes, and a
// parent's elapsed time includes its subtests', so a table whose cases are
// each quick but sum past the budget is still reported — it is still that
// long in the blocking tier. A t.Parallel test's paused time is excluded by
// the testing package itself.
//
//forge:exclude-contract: pure computation over a go test -json stream; a function and its data, no behaviour to substitute
package testbudget

import (
	"bufio"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
)

// DefaultBudget is the longest a test may take in the fast tier.
const DefaultBudget = 2 * time.Second

// ErrNoInput is returned when the stream held no `go test -json` events.
var ErrNoInput = errors.New("no go test -json events in the input")

// Overrun is one top-level test that took longer than the budget.
type Overrun struct {
	Package string
	Test    string
	Elapsed time.Duration
	// File and Line locate the test's declaration (File relative to the
	// module root), when Locate could resolve it.
	File string
	Line int
	// Marked reports that the declaration calls testing.Short or
	// testkit.Slow in its own body. A marked test that ran over budget is
	// either still slow under -short, or the run was not made with -short.
	Marked bool
}

// Report is a judged run.
type Report struct {
	Budget time.Duration
	// Ran counts the top-level tests that ran (passed or failed).
	Ran int
	// Overruns are the tests over budget, slowest first.
	Overruns []Overrun
	// FailedPackages names the packages that failed or did not build. A
	// budget check is no verdict on a red run: `go test -json | forge ...`
	// reports only forge's status unless the shell sets pipefail.
	FailedPackages []string
}

type event struct {
	Action     string  `json:"Action"`
	Package    string  `json:"Package"`
	ImportPath string  `json:"ImportPath"`
	Test       string  `json:"Test"`
	Elapsed    float64 `json:"Elapsed"`
}

// Check reads a `go test -json` stream and judges it against budget.
func Check(r io.Reader, budget time.Duration) (Report, error) {
	rep := Report{Budget: budget}
	failed := map[string]bool{}
	events := 0
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue // build output and other non-JSON noise
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil || ev.Action == "" {
			continue
		}
		events++
		switch {
		case ev.Action == "build-fail":
			failed[ev.ImportPath] = true
		case ev.Test == "" && ev.Action == "fail":
			failed[ev.Package] = true
		case ev.Test != "" && !strings.Contains(ev.Test, "/") && (ev.Action == "pass" || ev.Action == "fail"):
			rep.Ran++
			elapsed := time.Duration(ev.Elapsed * float64(time.Second))
			if elapsed > budget {
				rep.Overruns = append(rep.Overruns, Overrun{Package: ev.Package, Test: ev.Test, Elapsed: elapsed})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return rep, err
	}
	if events == 0 {
		return rep, ErrNoInput
	}
	for pkg := range failed {
		rep.FailedPackages = append(rep.FailedPackages, pkg)
	}
	sort.Strings(rep.FailedPackages)
	sort.SliceStable(rep.Overruns, func(i, j int) bool { return rep.Overruns[i].Elapsed > rep.Overruns[j].Elapsed })
	return rep, nil
}

// Locate fills each overrun's File, Line and Marked from the module rooted at
// moduleRoot. A package outside that module, or a test it cannot find, is
// left unlocated: the report still names the package and the test.
func Locate(rep *Report, moduleRoot string) {
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return
	}
	modPath := modfile.ModulePath(data)
	if modPath == "" {
		return
	}
	decls := map[string]map[string]located{} // package dir -> test -> declaration
	for i := range rep.Overruns {
		o := &rep.Overruns[i]
		rel, ok := strings.CutPrefix(o.Package, modPath)
		if !ok || (rel != "" && rel[0] != '/') {
			continue
		}
		dir := strings.TrimPrefix(rel, "/")
		if _, done := decls[dir]; !done {
			decls[dir] = testDecls(moduleRoot, dir)
		}
		if d, ok := decls[dir][o.Test]; ok {
			o.File, o.Line, o.Marked = d.file, d.line, d.marked
		}
	}
}

type located struct {
	file   string
	line   int
	marked bool
}

// testDecls parses the test files of one package directory.
func testDecls(moduleRoot, dir string) map[string]located {
	out := map[string]located{}
	files, _ := filepath.Glob(filepath.Join(moduleRoot, filepath.FromSlash(dir), "*_test.go"))
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(moduleRoot, path)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "Test") || fd.Body == nil {
				continue
			}
			out[fd.Name.Name] = located{
				file:   filepath.ToSlash(rel),
				line:   fset.Position(fd.Name.Pos()).Line,
				marked: callsMarker(fd.Body),
			}
		}
	}
	return out
}

// callsMarker reports a testing.Short() or testkit.Slow(...) call in body.
func callsMarker(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				found = (x.Name == "testing" && sel.Sel.Name == "Short") || (x.Name == "testkit" && sel.Sel.Name == "Slow")
			}
		}
		return !found
	})
	return found
}
