package cigate

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Reader scan (ci.gate.affected_readers: true). The package graph places Go
// source, embedded files and a package's own testdata/; a test that reads a
// repo file from disk at run time (`os.ReadFile(filepath.Join(root,
// "deploy", "kcl", "main.k"))`) is invisible to it, so any such file sends the
// unit lane to the full suite. The reader scan reads each test package's Go
// sources — test and non-test files — for string literals that name a tracked
// repo path. The scan adds those packages when an owned file (including Go
// source) or a non-Go file changes, even when a rule also selects tests.
// An explicit ignore still skips implicit ownership and reader selection.
//
// An unplaced, unclaimed non-Go file no package names still runs the full
// suite; an unplaced Go file does not use reader matches to narrow it. The
// scan over-approximates on purpose — a test run for no reason costs seconds,
// a test wrongly skipped costs a red default branch.
//
// What counts as naming a path (ported from reliant's
// tools/test-affected/graph.go scanSources and rootfacts.go):
//
//   - A package whose sources never locate the repo root (no repoRoot(,
//     runtime.Caller(, "..", git rev-parse, "go.mod", os.Getwd( …) reads only
//     its own directory, and is not scanned.
//   - A filepath.Join / path.Join is judged by its anchor, the first argument:
//     joined from the first argument on, its literals (and package string
//     constants) are relative to the package directory, where go test runs;
//     joined onto the repo root — a value the package derives from
//     os.Getwd, runtime.Caller, filepath.Abs("<relative>"), a "..", `git
//     rev-parse --show-toplevel`, or a function, variable or field holding
//     one — they name that path under the root; joined onto anything else
//     (t.TempDir(), a parameter) they name nothing in the repo.
//   - Any other literal is judged alone: a multi-segment one naming a tracked
//     file or directory is a read wherever it appears; a bare word only when
//     it is a file at the repo root, or the lone literal argument of a path
//     call (readTree(root, "deploy")).
//   - Consecutive literal arguments of any call are joined: readRepoFile(t,
//     "deploy", "kcl") names deploy/kcl. A glob or format verb cuts the
//     literal at the last directory before it.
//
// Limits, as in reliant (dev-docs/testing-affected.md): the scan is
// syntactic and per package. A path assembled at run time (from a loop
// variable, a flag, a config file, another package's constant) is not seen,
// and neither is a read done by a child process from a path the test never
// spells. Names are not scoped, so a field called root holds the root if any
// assignment to any such field does. It is Go-only: frontend tests that read
// repo files are not scanned (route those with ci.gate.affected_rules).

// readerIndex maps each test package to the tracked repo paths its sources
// name (files or directories, project-relative, slash-separated).
type readerIndex map[string][]string

// scanReaders builds the reader index for g's test packages against the
// tracked files.
func scanReaders(g *packageGraph, tracked []string) readerIndex {
	tr := newTrackedIndex(tracked)
	idx := readerIndex{}
	for dir, pkg := range g.dirPkg {
		if _, ok := g.testDeps[pkg]; !ok {
			continue
		}
		rel, err := filepath.Rel(g.root, dir)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		src := readPackageSources(dir, rel)
		if !src.locatesRoot {
			continue
		}
		if reads := tr.reads(src.candidates); len(reads) > 0 {
			idx[pkg] = reads
		}
	}
	return idx
}

// readersOf returns the test packages that name f or a directory holding
// it, each with the path it names.
func (idx readerIndex) readersOf(f string) map[string]string {
	f = filepath.ToSlash(f)
	out := map[string]string{}
	for pkg, reads := range idx {
		for _, r := range reads {
			if f == r || strings.HasPrefix(f, r+"/") {
				out[pkg] = r
				break
			}
		}
	}
	return out
}

// readerLine says what one changed file selected through the reader scan.
func readerLine(f string, pkgs map[string]string) string {
	names := make([]string, 0, len(pkgs))
	for pkg, r := range pkgs {
		if r == f {
			names = append(names, pkg)
		} else {
			names = append(names, pkg+" (names "+r+")")
		}
	}
	sort.Strings(names)
	return fmt.Sprintf("ci.gate.affected_readers: %s changed → %s (its sources name it)", f, strings.Join(names, ", "))
}

// trackedFiles lists the repo's tracked files, project-relative.
func trackedFiles(ctx context.Context, git func(ctx context.Context, dir string, args ...string) (string, error), root string) ([]string, error) {
	out, err := git(ctx, root, "ls-files", "--cached")
	if err != nil {
		return nil, err
	}
	return uniqueLines(out), nil
}

type trackedIndex struct {
	files map[string]bool
	dirs  map[string]bool
}

func newTrackedIndex(tracked []string) *trackedIndex {
	t := &trackedIndex{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, f := range tracked {
		f = filepath.ToSlash(f)
		t.files[f] = true
		for d := path.Dir(f); d != "." && d != "/"; d = path.Dir(d) {
			t.dirs[d] = true
		}
	}
	return t
}

// resolve maps a literal to the tracked file or directory it names. An
// unanchored literal may be relative to anywhere, so its leading ./ and ../
// are dropped; an anchored one is already repo-relative, and a path that
// climbs out of the repo names nothing in it.
func (t *trackedIndex) resolve(c string, anchored bool) (string, bool) {
	if strings.ContainsAny(c, " \t\n") {
		return "", false // a path literal has no whitespace; prose is not a read
	}
	for !anchored && (strings.HasPrefix(c, "../") || strings.HasPrefix(c, "./")) {
		c = strings.TrimPrefix(strings.TrimPrefix(c, "../"), "./")
	}
	c = path.Clean(c)
	if c == "." || c == "" || strings.HasPrefix(c, "/") || strings.HasPrefix(c, "..") {
		return "", false
	}
	if i := strings.IndexAny(c, "*?[{%$"); i >= 0 {
		c = c[:i]
		j := strings.LastIndex(c, "/")
		if j < 0 {
			return "", false
		}
		c = c[:j]
	}
	if t.files[c] || t.dirs[c] {
		return c, true
	}
	return "", false
}

func (t *trackedIndex) reads(cands []readCandidate) []string {
	seen := map[string]bool{}
	var reads []string
	for _, c := range cands {
		r, ok := t.resolve(c.path, c.anchored)
		if !ok {
			continue
		}
		// A bare word ("deploy", "api", "db") is also a label or a table: it
		// names a directory only as an argument of a path call or when its
		// anchor is known; alone it counts only as a real root-level file.
		if !strings.Contains(r, "/") && !c.anchored && !c.dirArg && !t.files[r] {
			continue
		}
		if !seen[r] {
			seen[r] = true
			reads = append(reads, r)
		}
	}
	sort.Strings(reads)
	return reads
}

type pkgSources struct {
	candidates  []readCandidate
	locatesRoot bool
}

// readCandidate is a string of a package's sources that may name a repo path.
type readCandidate struct {
	path string
	// anchored: path is repo-relative because its anchor is known (joined
	// onto the repo root or the package directory).
	anchored bool
	// dirArg: a lone literal argument of a path call whose anchor is unknown.
	dirArg bool
}

var rootMarkers = []string{
	"repoRoot(", "RepoRoot(", "repoRootFromTest(", "runtime.Caller(", `".."`, `"../`, "git rev-parse", "findRoot", `"go.mod"`, "os.Getwd(",
}

// readPackageSources scans the Go files of the package in dir, whose
// project-relative directory is rel.
func readPackageSources(dir, rel string) pkgSources {
	var out pkgSources
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		text := string(src)
		for _, m := range rootMarkers {
			if strings.Contains(text, m) {
				out.locatesRoot = true
				break
			}
		}
		f, err := parser.ParseFile(fset, e.Name(), src, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		files = append(files, f)
	}
	rf := findRootFacts(files)
	for _, f := range files {
		out.candidates = append(out.candidates, literalCandidates(f, rel, rf)...)
	}
	return out
}

// literalCandidates lists the strings of one file that may name a repo path
// (see the file comment for how a Join is judged by its anchor).
func literalCandidates(f *ast.File, rel string, rf *rootFacts) []readCandidate {
	var cands []readCandidate
	for _, decl := range f.Decls {
		local := map[string]bool{}
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			local = rf.localRoots(fd.Body)
		}
		judged := map[*ast.BasicLit]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if isJoinCall(x) {
					cands = append(cands, joinCandidates(x, rel, rf, local)...)
					for _, a := range x.Args {
						if b, ok := a.(*ast.BasicLit); ok {
							judged[b] = true
						}
					}
				} else {
					cands = append(cands, callCandidates(x)...)
				}
			case *ast.BasicLit:
				if s, ok := strLit(x); ok && !judged[x] {
					cands = append(cands, readCandidate{path: s})
				}
			}
			return true
		})
	}
	return cands
}

// joinCandidates is what one Join names, by its anchor. Only the literals
// right after the anchor count: a run after a second non-literal argument is
// under a directory nothing here knows.
func joinCandidates(x *ast.CallExpr, rel string, rf *rootFacts, local map[string]bool) []readCandidate {
	if len(x.Args) == 0 {
		return nil
	}
	lits := func(from int) []string {
		var run []string
		for _, a := range x.Args[from:] {
			s, ok := rf.literal(a)
			if !ok {
				break
			}
			run = append(run, s)
		}
		return run
	}
	if run := lits(0); len(run) > 0 {
		joined := path.Join(run...)
		if path.IsAbs(joined) {
			return nil // under /, not in this repo
		}
		// go test starts in the package directory, but a test may change
		// directory or pass this path to a child running at the repo root.
		// Keep both interpretations, even for a lone directory name: Join
		// is a path call, not a bare label in prose.
		return []readCandidate{
			{path: path.Join(rel, joined), anchored: true},
			{path: joined, dirArg: true},
		}
	}
	if run := lits(1); len(run) > 0 && rf.locates(x.Args[0], local) {
		return []readCandidate{{path: path.Join(run...), anchored: true}}
	}
	return nil
}

// isJoinCall is filepath.Join or path.Join.
func isJoinCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && (id.Name == "filepath" || id.Name == "path")
}

// callCandidates joins the consecutive literal arguments of one call into the
// path they spell: readRepoFile(t, "deploy", "kcl") names deploy/kcl.
func callCandidates(x *ast.CallExpr) []readCandidate {
	var cands []readCandidate
	var run []string
	pathy := isPathCall(x)
	flush := func() {
		switch {
		case len(run) > 1:
			cands = append(cands, readCandidate{path: strings.Join(run, "/")})
		case len(run) == 1 && pathy && len(x.Args) > 1:
			cands = append(cands, readCandidate{path: run[0], dirArg: true})
		}
		run = nil
	}
	for _, a := range x.Args {
		if s, ok := strLit(a); ok {
			run = append(run, s)
		} else {
			flush()
		}
	}
	flush()
	return cands
}

func isPathCall(c *ast.CallExpr) bool {
	name := calleeName(c)
	switch name {
	case "Join", "ReadFile", "ReadDir", "Glob", "Stat", "Open", "WalkDir", "Walk", "Abs", "Rel", "Dir", "Clean":
		return true
	}
	lower := strings.ToLower(name)
	return strings.Contains(lower, "path") || strings.Contains(lower, "read")
}

func strLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

// rootFacts is where one package's sources say the repo root is: which
// functions return it (or a path under it), and which package variables and
// struct fields hold one. It is syntactic and per package, so it
// over-approximates where names meet; what it never does is treat a value it
// cannot trace to the root, such as a parameter or t.TempDir(), as the root.
type rootFacts struct {
	funcs  map[string]bool
	vars   map[string]bool
	fields map[string]bool
	// consts are the package's string constants, which a Join spells like
	// literals: filepath.Join(root, protoDir, "package.json").
	consts map[string]string
}

// literal is the string a Join argument spells: a string literal, or a
// package string constant.
func (rf *rootFacts) literal(e ast.Expr) (string, bool) {
	if s, ok := strLit(e); ok {
		return s, true
	}
	if id, ok := e.(*ast.Ident); ok {
		s, ok := rf.consts[id.Name]
		return s, ok
	}
	return "", false
}

// findRootFacts settles the facts of a package's files: a function returning
// a root-located value makes its callers' values root-located, and so on,
// until nothing changes.
func findRootFacts(files []*ast.File) *rootFacts {
	rf := &rootFacts{funcs: map[string]bool{}, vars: map[string]bool{}, fields: map[string]bool{}, consts: map[string]string{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if s, ok := strLit(vs.Values[i]); ok {
							rf.consts[name.Name] = s
						}
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		mark := func(set map[string]bool, name string) {
			if name != "" && name != "_" && !set[name] {
				set[name] = true
				changed = true
			}
		}
		for _, f := range files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					rf.assignments(d, map[string]bool{}, func(name string) { mark(rf.vars, name) }, func(field string) { mark(rf.fields, field) })
				case *ast.FuncDecl:
					if d.Body == nil {
						continue
					}
					local := rf.localRoots(d.Body)
					rf.assignments(d.Body, local, func(string) {}, func(field string) { mark(rf.fields, field) })
					ast.Inspect(d.Body, func(n ast.Node) bool {
						if ret, ok := n.(*ast.ReturnStmt); ok {
							for _, r := range ret.Results {
								if d.Recv == nil && rf.locates(r, local) {
									mark(rf.funcs, d.Name.Name)
								}
							}
						}
						return true
					})
				}
			}
		}
	}
	return rf
}

// localRoots is the set of local variables in body that hold a root-located
// value.
func (rf *rootFacts) localRoots(body ast.Node) map[string]bool {
	local := map[string]bool{}
	for changed := true; changed; {
		changed = false
		rf.assignments(body, local, func(name string) {
			if name != "_" && !local[name] {
				local[name] = true
				changed = true
			}
		}, func(string) {})
	}
	return local
}

// assignments calls onVar for every variable and onField for every struct
// field under n that is assigned a root-located value.
func (rf *rootFacts) assignments(n ast.Node, local map[string]bool, onVar, onField func(string)) {
	lhs := func(l ast.Expr) {
		switch x := l.(type) {
		case *ast.Ident:
			onVar(x.Name)
		case *ast.SelectorExpr:
			onField(x.Sel.Name)
		}
	}
	// pairs is the value of each left-hand side: its own, or the one call
	// all of them come from (`_, file, _, ok := runtime.Caller(0)`).
	pairs := func(n int, values []ast.Expr, set func(int)) {
		for i := 0; i < n; i++ {
			var v ast.Expr
			switch {
			case len(values) == n:
				v = values[i]
			case len(values) == 1:
				v = values[0]
			}
			if v != nil && rf.locates(v, local) {
				set(i)
			}
		}
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			pairs(len(x.Lhs), x.Rhs, func(i int) { lhs(x.Lhs[i]) })
		case *ast.ValueSpec:
			pairs(len(x.Names), x.Values, func(i int) { onVar(x.Names[i].Name) })
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.Ident); ok && rf.locates(x.Value, local) {
				onField(k.Name)
			}
		}
		return true
	})
}

// locates reports whether e's value is the repo root or a path derived from
// it: os.Getwd(), runtime.Caller(...), filepath.Abs("<relative>"), `git
// rev-parse --show-toplevel` output, a ".." literal, a call to a function
// that returns one, a variable or field assigned one, and a path built from
// one (Join/Dir/Abs/Clean/... of it, a string conversion or trim, `it + "/x"`).
func (rf *rootFacts) locates(e ast.Expr, local map[string]bool) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return rf.locates(x.X, local)
	case *ast.StarExpr:
		return rf.locates(x.X, local)
	case *ast.UnaryExpr:
		return rf.locates(x.X, local)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && rf.locates(x.X, local)
	case *ast.BasicLit:
		s, ok := strLit(x)
		return ok && (s == ".." || strings.HasPrefix(s, "../"))
	case *ast.Ident:
		return local[x.Name] || rf.vars[x.Name]
	case *ast.SelectorExpr:
		return rf.fields[x.Sel.Name]
	case *ast.CallExpr:
		name := calleeName(x)
		// Only a local free function whose body establishes a root fact is
		// known here. A same-named method or imported helper is not that
		// function, and a familiar name alone proves nothing.
		if id, ok := x.Fun.(*ast.Ident); ok && rf.funcs[id.Name] {
			return true
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && ((pkg.Name == "os" && name == "Getwd") || (pkg.Name == "runtime" && name == "Caller")) {
				return true
			}
			if name == "Output" || name == "CombinedOutput" {
				return rf.locates(sel.X, local) // exec.Command(...).Output()
			}
		}
		for _, a := range x.Args {
			if s, ok := strLit(a); ok && s == "--show-toplevel" {
				return true
			}
		}
		if len(x.Args) == 0 || !pathTransforms[name] {
			return false
		}
		if name == "Abs" {
			if _, ok := strLit(x.Args[0]); ok {
				return true
			}
		}
		return rf.locates(x.Args[0], local)
	}
	return false
}

// pathTransforms build or clean a path from their first argument.
var pathTransforms = map[string]bool{
	"Join": true, "Dir": true, "Abs": true, "Clean": true, "EvalSymlinks": true,
	"ToSlash": true, "FromSlash": true, "string": true,
	"TrimSpace": true, "TrimSuffix": true, "TrimRight": true, "Trim": true,
}
