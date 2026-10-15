// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type listPkg struct {
	ImportPath      string
	Dir             string
	ForTest         string
	GoFiles         []string
	TestGoFiles     []string
	XTestGoFiles    []string
	IgnoredGoFiles  []string
	EmbedFiles      []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
	EmbedPatterns   []string
	Deps            []string
}

const listFields = "ImportPath,Dir,ForTest,GoFiles,TestGoFiles,XTestGoFiles,IgnoredGoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles,EmbedPatterns,Deps"

func goList(ctx context.Context, root string) ([]listPkg, error) {
	args := []string{"list", "-e", "-test", "-json=" + listFields, "./..."}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, tail(stderr.String(), 20))
	}
	return decodeList(out)
}

func decodeList(out []byte) ([]listPkg, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []listPkg
	for {
		var p listPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var variantSuffix = regexp.MustCompile(` \[.*\]$`)

// base strips go list's test-variant decoration: "p [p.test]" and "p_test
// [p.test]" are both parts of p's test binary.
func base(importPath string) string {
	s := variantSuffix.ReplaceAllString(importPath, "")
	return strings.TrimSuffix(s, "_test")
}

// FileKind says what a Go file is to the package that owns it.
type FileKind int

const (
	// KindCode is non-test Go source built by default.
	KindCode FileKind = iota
	// KindTest is a _test.go file built by default.
	KindTest
	// KindIgnored is Go source the default test lane does not build: a build
	// tag (e2e, live, replayfixtures...) or another OS.
	KindIgnored
)

// FileInfo is a Go or embedded file's owner.
type FileInfo struct {
	Pkg      string
	Kind     FileKind
	Embedded bool
}

// Graph is everything Classify needs to know about the module. LoadGraph builds
// it from `go list`; the table tests build it by hand.
type Graph struct {
	Module string
	// DirPkg maps a repo-relative directory (".") to its package.
	DirPkg map[string]string
	// Files maps a repo-relative path to its owner: Go files and embedded files.
	Files map[string]FileInfo
	// HasEmbed marks packages with any //go:embed directive.
	HasEmbed map[string]bool
	// TestDeps[P] is the set of module packages P's test binary contains (P
	// included). A package with no tests has no entry.
	TestDeps map[string]map[string]bool
	// BuildDeps[P] is the same for P itself, for every package (tests or not):
	// the packages `go build` of P compiles.
	BuildDeps map[string]map[string]bool
	// Readers[P] is the repo paths P's Go sources name: what its tests may read.
	Readers map[string][]string
	// Walkers read every Go file of the module (a tree-wide guard test).
	Walkers map[string]bool
	// DBTests are the packages whose test files use the DB test helpers.
	DBTests map[string]bool
	// Web is the import graph of web/ (nil when web/ is absent).
	Web *WebGraph
	// ElectronText holds the source of electron tests that name files outside
	// electron/src (repo-relative path -> text).
	ElectronText map[string]string
}

// LoadGraph reads the module's package graph with `go list` (default tags: what
// `make test-short` builds) and scans the sources for what their tests read.
func LoadGraph(ctx context.Context, root, module string, tracked []string) (*Graph, error) {
	list, err := goList(ctx, root)
	if err != nil {
		return nil, err
	}
	return buildGraph(root, module, tracked, list), nil
}

func buildGraph(root, module string, tracked []string, list []listPkg) *Graph {
	g := &Graph{
		Module:    module,
		DirPkg:    map[string]string{},
		Files:     map[string]FileInfo{},
		HasEmbed:  map[string]bool{},
		TestDeps:  map[string]map[string]bool{},
		BuildDeps: map[string]map[string]bool{},
		Readers:   map[string][]string{},
		Walkers:   map[string]bool{},
		DBTests:   map[string]bool{},
	}
	ix := &indexer{g: g, root: root, module: module, pkgDirs: map[string]string{}}
	ix.index(list)
	for _, p := range list {
		if ix.ownPackage(p) {
			g.BuildDeps[p.ImportPath] = depSet(p, module)
			continue
		}
		if p.ForTest == "" && strings.HasSuffix(p.ImportPath, ".test") {
			pkg := strings.TrimSuffix(p.ImportPath, ".test")
			if strings.HasPrefix(pkg, module) {
				g.TestDeps[pkg] = depSet(p, module)
				g.TestDeps[pkg][pkg] = true
			}
		}
	}
	scanSources(g, ix.pkgDirs, tracked)
	return g
}

func depSet(p listPkg, module string) map[string]bool {
	set := map[string]bool{strings.TrimSuffix(p.ImportPath, ".test"): true}
	for _, d := range p.Deps {
		if b := base(d); strings.HasPrefix(b, module) {
			set[b] = true
		}
	}
	return set
}

type indexer struct {
	g       *Graph
	root    string
	module  string
	pkgDirs map[string]string
}

func (ix *indexer) relDir(abs string) string {
	r, err := filepath.Rel(ix.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

// ownPackage is a real package of this module (not a test variant, test main or
// external dependency).
func (ix *indexer) ownPackage(p listPkg) bool {
	return p.ForTest == "" && !strings.HasSuffix(p.ImportPath, ".test") && p.Dir != "" &&
		strings.HasPrefix(p.ImportPath, ix.module) && !strings.Contains(p.ImportPath, " ")
}

func (ix *indexer) index(list []listPkg) {
	for _, p := range list {
		if !ix.ownPackage(p) {
			continue
		}
		dir := ix.relDir(p.Dir)
		ix.g.DirPkg[dir] = p.ImportPath
		ix.pkgDirs[p.ImportPath] = p.Dir
		add := func(names []string, kind FileKind) {
			for _, n := range names {
				ix.g.Files[path.Join(dir, n)] = FileInfo{Pkg: p.ImportPath, Kind: kind}
			}
		}
		add(p.GoFiles, KindCode)
		add(p.TestGoFiles, KindTest)
		add(p.XTestGoFiles, KindTest)
		for _, names := range [][]string{p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
			for _, n := range names {
				f := path.Join(dir, n)
				if _, isGo := ix.g.Files[f]; !isGo {
					ix.g.Files[f] = FileInfo{Pkg: p.ImportPath, Kind: KindCode, Embedded: true}
				}
			}
		}
		if len(p.EmbedPatterns) > 0 || len(p.EmbedFiles) > 0 || len(p.TestEmbedFiles) > 0 || len(p.XTestEmbedFiles) > 0 {
			ix.g.HasEmbed[p.ImportPath] = true
		}
	}
	for _, p := range list {
		if !ix.ownPackage(p) {
			continue
		}
		dir := ix.relDir(p.Dir)
		for _, n := range p.IgnoredGoFiles {
			if f := path.Join(dir, n); ix.g.Files[f] == (FileInfo{}) {
				ix.g.Files[f] = FileInfo{Pkg: p.ImportPath, Kind: KindIgnored}
			}
		}
	}
}

// ── what a package's tests read ─────────────────────────────────────────────

// dbHelpers are the identifiers that make a test DB-backed: the helpers in
// internal/db/testutil.go and the environment variable they read.
var dbHelpers = []string{"SetupTestDB", "NewTestRepo", "resolveTestDSN", "DATABASE_URL", "SetupTestDBWithRawDB"}

// scanSources finds, per package: the tracked repo paths its Go sources name as
// string literals (alone, or joined as consecutive arguments of one call, the
// way filepath.Join(root, "deploy", "kcl") spells a path); whether it walks
// the whole tree; and whether its tests use the DB helpers. It over-approximates
// on purpose: a package selected for no reason costs seconds; one wrongly
// skipped costs a broken main.
func scanSources(g *Graph, pkgDirs map[string]string, tracked []string) {
	tr := newTrackedIndex(tracked)
	for pkg, dir := range pkgDirs {
		src := readPackageSources(dir)
		if src.walker {
			g.Walkers[pkg] = true
		}
		if src.db {
			g.DBTests[pkg] = true
		}
		if !src.locatesRoot {
			// A package that never locates the repo root reads only its own directory.
			continue
		}
		if reads := tr.reads(src.candidates); len(reads) > 0 {
			g.Readers[pkg] = reads
		}
	}
}

type trackedIndex struct {
	files map[string]bool
	dirs  map[string]bool
}

func newTrackedIndex(tracked []string) *trackedIndex {
	t := &trackedIndex{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, f := range tracked {
		t.files[f] = true
		for d := path.Dir(f); d != "." && d != "/"; d = path.Dir(d) {
			t.dirs[d] = true
		}
	}
	return t
}

// resolve maps a literal to the tracked file or directory it names.
func (t *trackedIndex) resolve(c string) (string, bool) {
	// A path literal has no whitespace; prose is not a read.
	if strings.ContainsAny(c, " \t\n") {
		return "", false
	}
	for strings.HasPrefix(c, "../") || strings.HasPrefix(c, "./") {
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

func (t *trackedIndex) reads(cands []string) []string {
	seen := map[string]bool{}
	var reads []string
	for _, c := range cands {
		dirArg := strings.HasPrefix(c, "dir:")
		r, ok := t.resolve(strings.TrimPrefix(c, "dir:"))
		if !ok {
			continue
		}
		// A bare word ("deploy", "api", "docs", "db") is also a label or a
		// table: it names a directory only as an argument of a path call;
		// alone it counts only when it is a real file at the repo root.
		if !strings.Contains(r, "/") && !dirArg && !t.files[r] {
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
	candidates  []string
	locatesRoot bool
	walker      bool
	db          bool
}

var rootMarkers = []string{
	"repoRoot(", "RepoRoot(", "repoRootFromTest(", "runtime.Caller(", `".."`, `"../`, "git rev-parse", "findRoot", `"go.mod"`, "os.Getwd(",
}

func readPackageSources(dir string) pkgSources {
	var out pkgSources
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		text := string(src)
		isTest := strings.HasSuffix(e.Name(), "_test.go")
		if isTest && (strings.Contains(text, "WalkDir(") || strings.Contains(text, "filepath.Walk(")) && strings.Contains(text, `".go"`) {
			out.walker = true
		}
		if isTest {
			for _, h := range dbHelpers {
				if strings.Contains(text, h) {
					out.db = true
					break
				}
			}
		}
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
		out.candidates = append(out.candidates, literalCandidates(f)...)
	}
	return out
}

// literalCandidates lists the strings of one file that may name a repo path.
func literalCandidates(f *ast.File) []string {
	var cands []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			cands = append(cands, callCandidates(x)...)
		case *ast.BasicLit:
			if s, ok := strLit(x); ok {
				cands = append(cands, s)
			}
		}
		return true
	})
	return cands
}

// callCandidates joins the consecutive literal arguments of one call into the
// path they spell: filepath.Join(root, "deploy", "kcl") names deploy/kcl.
func callCandidates(x *ast.CallExpr) []string {
	var cands, run []string
	pathy := isPathCall(x)
	flush := func() {
		switch {
		case len(run) > 1:
			cands = append(cands, strings.Join(run, "/"))
		case len(run) == 1 && pathy && len(x.Args) > 1:
			cands = append(cands, "dir:"+run[0])
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
	var name string
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		name = f.Sel.Name
	case *ast.Ident:
		name = f.Name
	}
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
