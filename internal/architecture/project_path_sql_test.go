package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// projectPathSQLAllowed is every string literal that names projects.path in
// SQL (`p.path`, `projects.path`), keyed "<file>:<func or decl name>". A query
// that selects the creation path hands a daemon another machine's directory
// as surely as a .Path read does; TestProjectPathSQLIsReviewed fails any
// other. It is empty: the sweep reads a worktree's owner's INSTALLED
// checkout row and nothing else (store.ownerCheckoutPath), and a row with no
// owner checkout gets no path at all, never a guess.
var projectPathSQLAllowed = map[string]struct {
	count  int
	reason projectPathReason
}{}

// projectPathSQL matches projects.path named in SQL text.
var projectPathSQL = regexp.MustCompile(`\b(p|projects)\.path\b`)

// projectPathSQLReads counts the string literals naming projects.path in
// each top-level declaration of file, keyed "<rel>:<name>".
func projectPathSQLReads(file *ast.File, rel string, into map[string]int) {
	for _, decl := range file.Decls {
		var names []string
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names = []string{d.Name.Name}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range vs.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
		if len(names) == 0 {
			continue
		}
		key := rel + ":" + strings.Join(names, ",")
		ast.Inspect(decl, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && projectPathSQL.MatchString(lit.Value) {
				into[key]++
			}
			return true
		})
	}
}

func TestProjectPathSQLReads(t *testing.T) {
	src := `package x
const q = "SELECT p.path FROM projects p"
func f() { _ = "COALESCE(pd.path, projects.path)"; _ = "pd.path"; _ = "the path" }
`
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	projectPathSQLReads(file, "x.go", got)
	if len(got) != 2 || got["x.go:q"] != 1 || got["x.go:f"] != 1 {
		t.Fatalf("reads = %v, want q and f once each", got)
	}
}

// TestProjectPathSQLIsReviewed is TestProjectPathNeverReachesADaemon for SQL
// text: the hand-written queries in internal/store included.
func TestProjectPathSQLIsReviewed(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
	}
	reads := map[string]int{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "pkg"} {
		dir := filepath.Join(repo, top)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == dir {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			projectPathSQLReads(file, filepath.ToSlash(rel), reads)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var problems []string
	for key, n := range reads {
		allowed, ok := projectPathSQLAllowed[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s names projects.path in SQL %d time(s). It is the CREATING machine's path: "+
				"join project_daemons on the machine that will use it (see ownerCheckoutPath in internal/store/worktree_repo_ext.go)", key, n))
		case n != allowed.count:
			problems = append(problems, fmt.Sprintf("%s names projects.path in SQL %d time(s), the list allows %d (%s)", key, n, allowed.count, allowed.reason))
		}
	}
	for key, allowed := range projectPathSQLAllowed {
		if _, ok := reads[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s is listed (%d, %s) but no longer names projects.path in SQL: remove the entry", key, allowed.count, allowed.reason))
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
