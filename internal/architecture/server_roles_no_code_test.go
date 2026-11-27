package architecture

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─── the server roles run no code ──────────────────────────────────────────
//
// Owner decision, 2026-11-27: anything that touches a user's code, or does
// per-user work on it, runs on the user's (untrusted) daemons, never on a
// server role. The merge queue built git checkouts inside the worker pod
// (internal/mergequeue's workdirs under /data/mergequeue), and the worker
// read pipeline.yaml through the GitHub contents API; both were removed, and
// this guard keeps them out. A bounded read the owner allowed (the coverage
// view's blob read) is on the reviewed list below.
//
// Every first-party package the server roles reach — the import closure of
// internal/app, whose roots are `reliant server api|worker|gateway` — must
// not:
//
//   - import the merge queue's git port (internal/mergequeue/gitport) or
//     go-git;
//   - exec git (exec.Command / exec.CommandContext naming "git");
//   - name a GitHub endpoint that returns repository contents: /contents,
//     /tarball, /zipball, /git/blobs, a PR's /files (each file's patch), a
//     .diff/.patch URL or media type, or a catalog action that calls one.
//
// The integration catalog's actions are declared in YAML the worker
// executes, so the YAML is held to the same endpoint rule.
//
// Reachable is by import, not by call: a package linked into the server for
// its types still carries its git code into the server binary, and a later
// edit can call it. The packages below are linked today for their wire types
// or schemas while their git code runs on the daemon; each is listed with
// exactly what it holds, so the list can only shrink. A new entry is the
// owner's decision, not a reviewer's.
//
// The walk parses import blocks itself (no `go list` child process), so the
// test reads every file it judges and its cached result is sound.

const reliantModule = "github.com/reliant-labs/reliant"

// serverRoots are the server roles' composition roots, and the server-side
// merge queue packages the worker is to be wired with (the scheduler, its
// gate dispatcher and its daemon job dispatcher), held to the rule before
// internal/app reaches them.
var serverRoots = []string{
	"internal/app",
	"internal/mergequeue",
	"internal/mergequeue/gaterun",
	"internal/mergequeue/jobrun",
}

// codeFinding kinds.
const (
	findGitPort  = "imports internal/mergequeue/gitport"
	findGoGit    = "imports go-git"
	findExecGit  = "execs git"
	findContents = "names a GitHub repository-contents endpoint"
)

// serverCodeAllowed is every reachable package that may still hold a
// finding, exactly which, and why. Keyed by package path relative to the
// module root.
var serverCodeAllowed = map[string]struct {
	findings []string
	reason   string
}{
	"internal/worktreereclaim": {
		[]string{findExecGit, findGoGit},
		"internal/handlers/worktree links it for the recovery-restore wire types it sends to the daemon; the git code (recovery_restore.go, gitrun.go, activity.go) runs only in the daemon's worktree reclaimer",
	},
	"internal/provetest": {
		[]string{findExecGit},
		"internal/llm/tools links it for the prove_test tool's definition; the git worktree it makes runs only on the daemon that executes the tool",
	},
	"internal/filetree": {
		[]string{findGoGit},
		"internal/mcp links it; it uses go-git's gitignore matcher only (no repository access), on the daemon's file tree",
	},
	"internal/integrations/ghblobs": {
		[]string{findContents},
		"GetCoverageSource reads one covered file's blob as the viewer, for the Forge Tests coverage view: a read, never a checkout or a build. The owner allowed it on the API server (2026-11-27 15:11Z), bounded in memory: MaxBytes per blob, MaxInFlight reads at once, the handler's capped cache",
	},
}

// catalogContentsAllowed are the integration catalog actions that call a
// repository-contents endpoint. They run in the worker on the calling user's
// own credential, at their workflow's request, and are a v1 public surface
// (workflow YAML names them). Pending the owner's decision.
var catalogContentsAllowed = map[string]string{
	"internal/integrations/catalog/github/actions/repo.get_content.yaml": "github/repo.get_content: a file's contents (user-invoked, pending the owner's decision)",
	"internal/integrations/catalog/github/actions/compare.yaml":          "github/compare: the comparison carries each file's patch (user-invoked, pending the owner's decision)",
	"internal/integrations/catalog/github/actions/pr.list_files.yaml":    "github/pr.list_files: each file's patch (user-invoked, pending the owner's decision)",
}

// contentsLiteral matches a string literal naming a contents endpoint.
var contentsLiteral = []*regexp.Regexp{
	// A path segment, not the bare permission name "contents".
	regexp.MustCompile(`^contents/|/contents(/|\?|$)`),
	regexp.MustCompile(`(^|/)(tarball|zipball)(/|$)`),
	regexp.MustCompile(`(^|/)git/blobs(/|$)`),
	regexp.MustCompile(`(^|/)pulls/[^ ]*/files(\?|$)`),
	regexp.MustCompile(`/(pull|pulls|commit|commits|compare)/[^ ]*\.(diff|patch)$`),
	regexp.MustCompile(`application/vnd\.github(\.v3)?\.(raw|diff|patch)`),
	regexp.MustCompile(`^github/(repo\.get_content|compare|pr\.list_files)@`),
}

// contentsPath matches a catalog action's request path.
var contentsPath = regexp.MustCompile(`^\s*path:\s*.*(/contents(/|\s|$)|/tarball|/zipball|/git/blobs|/compare/|/pulls/[^/]+/files)`)

func TestServerRolesRunNoCode(t *testing.T) {
	t.Parallel()
	repo := root(t)
	pkgs, err := serverClosure(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !pkgs["internal/app"] || len(pkgs) < 50 {
		t.Fatalf("the server closure is %d packages; the walk is broken", len(pkgs))
	}
	if pkgs["internal/mergequeue/gitport"] {
		t.Errorf("the server roles reach internal/mergequeue/gitport: git runs on a gate pool daemon, never on a server role")
	}
	names := make([]string, 0, len(pkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, pkg := range names {
		found, err := codeFindings(filepath.Join(repo, pkg))
		if err != nil {
			t.Fatal(err)
		}
		allowed := serverCodeAllowed[pkg]
		for _, f := range sortedKeys(found) {
			if !contains(allowed.findings, f) {
				t.Errorf("%s %s (%s), and the server roles reach it (%s). Code runs on the user's daemons, never on a server role "+
					"(owner, 2026-11-27): make it a daemon job, or move the code out of the package the server imports.",
					pkg, f, strings.Join(found[f], ", "), chain(repo, pkg))
			}
		}
		for _, f := range allowed.findings {
			if _, ok := found[f]; !ok {
				t.Errorf("%s no longer %s: remove that from serverCodeAllowed, so the list only shrinks", pkg, f)
			}
		}
	}
	for pkg := range serverCodeAllowed {
		if !pkgs[pkg] {
			t.Errorf("%s is no longer reachable from the server roles: remove it from serverCodeAllowed", pkg)
		}
	}
	checkCatalogContents(t, repo)
}

func checkCatalogContents(t *testing.T, repo string) {
	dir := filepath.Join(repo, "internal", "integrations", "catalog")
	seen := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			if contentsPath.MatchString(line) {
				rel, _ := filepath.Rel(repo, path)
				rel = filepath.ToSlash(rel)
				seen[rel] = true
				if _, ok := catalogContentsAllowed[rel]; !ok {
					t.Errorf("%s calls a GitHub repository-contents endpoint (%s), and the worker executes the catalog. "+
						"Repository contents are read on the user's daemons, never on a server role (owner, 2026-11-27).", rel, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range catalogContentsAllowed {
		if !seen[f] {
			t.Errorf("%s no longer calls a contents endpoint: remove it from catalogContentsAllowed", f)
		}
	}
}

// serverClosure is the first-party import closure of serverRoots, as
// module-relative package paths. Test files are not part of it.
func serverClosure(repo string) (map[string]bool, error) {
	seen := map[string]bool{}
	queue := append([]string(nil), serverRoots...)
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		imports, err := packageImports(filepath.Join(repo, pkg))
		if err != nil {
			return nil, err
		}
		for _, imp := range imports {
			if rel, ok := strings.CutPrefix(imp, reliantModule+"/"); ok && !seen[rel] {
				queue = append(queue, rel)
			}
		}
	}
	return seen, nil
}

// packageFiles are a package directory's non-test Go files, minus those
// excluded from every build (`//go:build ignore`).
func packageFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out, nil
}

func ignored(f *ast.File) bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if constraint.IsGoBuild(c.Text) {
				if expr, err := constraint.Parse(c.Text); err == nil && strings.Contains(expr.String(), "ignore") {
					return true
				}
			}
		}
	}
	return false
}

func packageImports(dir string) ([]string, error) {
	files, err := packageFiles(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var out []string
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return nil, err
		}
		if ignored(f) {
			continue
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			out = append(out, p)
		}
	}
	return out, nil
}

// codeFindings judges one package's non-test files: finding → where.
func codeFindings(dir string) (map[string][]string, error) {
	files, err := packageFiles(dir)
	if err != nil {
		return nil, err
	}
	found := map[string][]string{}
	add := func(kind string, fset *token.FileSet, pos token.Pos) {
		p := fset.Position(pos)
		found[kind] = append(found[kind], fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line))
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !mayFind(src) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		if ignored(f) {
			continue
		}
		execName := ""
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case p == reliantModule+"/internal/mergequeue/gitport":
				add(findGitPort, fset, imp.Pos())
			case strings.HasPrefix(p, "github.com/go-git/"):
				add(findGoGit, fset, imp.Pos())
			case p == "os/exec":
				execName = "exec"
				if imp.Name != nil {
					execName = imp.Name.Name
				}
			}
		}
		gitConsts := stringConsts(f, "git")
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil && matchesAny(contentsLiteral, s) {
						add(findContents, fset, n.Pos())
					}
				}
			case *ast.CallExpr:
				if execName == "" {
					return true
				}
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != execName {
					return true
				}
				arg := -1
				switch sel.Sel.Name {
				case "Command":
					arg = 0
				case "CommandContext":
					arg = 1
				}
				if arg >= 0 && len(n.Args) > arg && namesGit(n.Args[arg], gitConsts) {
					add(findExecGit, fset, n.Pos())
				}
			}
			return true
		})
	}
	return found, nil
}

// findHints are substrings every finding's source contains; a file with none
// is not parsed.
var findHints = [][]byte{
	[]byte(`"os/exec"`), []byte("go-git"), []byte("gitport"), []byte("contents"), []byte("tarball"),
	[]byte("zipball"), []byte("git/blobs"), []byte("/files"), []byte(".diff"), []byte(".patch"), []byte("vnd.github"), []byte("github/"),
}

func mayFind(src []byte) bool {
	for _, h := range findHints {
		if bytes.Contains(src, h) {
			return true
		}
	}
	return false
}

// stringConsts are the file's identifiers declared as the constant want.
func stringConsts(f *ast.File, want string) map[string]bool {
	out := map[string]bool{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok && i < len(vs.Names) {
					if s, err := strconv.Unquote(lit.Value); err == nil && isGit(s) {
						out[vs.Names[i].Name] = true
					}
				}
			}
		}
	}
	return out
}

func isGit(s string) bool { return s == "git" || strings.HasSuffix(s, "/git") }

func namesGit(e ast.Expr, consts map[string]bool) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(e.Value)
		return err == nil && isGit(s)
	case *ast.Ident:
		return consts[e.Name]
	}
	return false
}

func matchesAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// chain is one import path from a server root to pkg, for the message.
func chain(repo, pkg string) string {
	prev := map[string]string{}
	queue := append([]string(nil), serverRoots...)
	for _, r := range serverRoots {
		prev[r] = ""
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == pkg {
			var path []string
			for p := cur; p != ""; p = prev[p] {
				path = append([]string{p}, path...)
			}
			return strings.Join(path, " → ")
		}
		imports, _ := packageImports(filepath.Join(repo, cur))
		for _, imp := range imports {
			if rel, ok := strings.CutPrefix(imp, reliantModule+"/"); ok {
				if _, done := prev[rel]; !done {
					prev[rel] = cur
					queue = append(queue, rel)
				}
			}
		}
	}
	return pkg
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestServerRolesRunNoCodeCatchesEachKind proves the detector on a package
// written for it: a guard that finds nothing proves nothing.
func TestServerRolesRunNoCodeCatchesEachKind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `package bad

import (
	"context"
	run "os/exec"

	_ "github.com/go-git/go-git/v5"
	_ "github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

const gitBin = "git"

func a(ctx context.Context) {
	_ = run.CommandContext(ctx, gitBin, "fetch")
	_ = run.Command("/usr/bin/git", "status")
	_ = run.Command("bash", "-c", "true")
	_ = "/repos/o/r/contents/" + "x"
	_ = "git/blobs/abc"
	_ = "application/vnd.github.diff"
	_ = "github/repo.get_content@1"
	_ = map[string]string{"contents": "write"} // a permission name, not an endpoint
	_ = "repos/o/r/pulls/7/files"
}
`
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := codeFindings(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{findGitPort: 1, findGoGit: 1, findExecGit: 2, findContents: 5}
	for kind, n := range want {
		if len(found[kind]) != n {
			t.Errorf("%s: found %v, want %d", kind, found[kind], n)
		}
	}
	if !contentsPath.MatchString("  path: /repos/{{ params.owner }}/{{ params.repo }}/contents/{{ params.path }}") ||
		contentsPath.MatchString("  path: /repos/{{ params.owner }}/{{ params.repo }}/git/trees/{{ params.sha }}") {
		t.Error("the catalog path rule is wrong")
	}
}
