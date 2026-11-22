package codegen

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ResolvesCallerDirective declares that calling a function resolves the
// caller: it reads the authenticated principal the auth interceptor put on
// the request context. It goes in the function's doc comment:
//
//	// GetUserIDFromContext returns the signed-in user's id.
//	//
//	//forge:resolves-caller
//	func GetUserIDFromContext(ctx context.Context) (string, bool)
//
// Forge's own seam (CRUDAuthSeam, middleware.GetUser in the scaffolded
// pkg/middleware) needs no marker. A project with its own auth package — an
// adopted one, whose interceptor predates forge — marks its accessors
// instead, and everything that asks "does this code resolve the caller?"
// (the unscoped_auth audit first) accepts them. The declaration sits beside
// the code it describes, like //forge:no-orm, rather than in forge.yaml,
// which never mentions the package.
//
// Package-level functions only. A method cannot be matched at a call site
// without type-checking (the call names no receiver type), and matching it
// by name alone would clear handlers on coincidence.
const ResolvesCallerDirective = "forge:resolves-caller"

// CallerResolver is one function a project declared with
// ResolvesCallerDirective.
type CallerResolver struct {
	// ImportPath is the declaring package's import path.
	ImportPath string `json:"import_path"`
	// Package is the declaring package's name, which is the identifier an
	// unaliased import binds.
	Package string `json:"package"`
	// Func is the function's name.
	Func string `json:"func"`
	// File and Line locate the declaration (File project-relative).
	File string `json:"file"`
	Line int    `json:"line"`
}

// String renders the resolver as code calls it: `auth.GetUserIDFromContext`.
func (r CallerResolver) String() string { return r.Package + "." + r.Func }

// IgnoredResolverMarker is a ResolvesCallerDirective forge found and could
// not honour, with the reason — reported rather than dropped, so a marker
// that does nothing is visible.
type IgnoredResolverMarker struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// DeclaredCallerResolvers finds every function in projectDir's main module
// declared with ResolvesCallerDirective. It reads non-test .go files only,
// and skips hidden directories, node_modules, vendor, testdata and nested
// modules (a directory with its own go.mod is another module's code). A file
// is parsed only when its bytes mention the directive, so the walk costs
// little more than reading the tree.
//
// Results are sorted by import path, then name. A project without a go.mod
// declares nothing.
func DeclaredCallerResolvers(projectDir string) ([]CallerResolver, []IgnoredResolverMarker) {
	module, err := GetModulePath(projectDir)
	if err != nil {
		return nil, nil
	}
	var (
		found   []CallerResolver
		ignored []IgnoredResolverMarker
		needle  = []byte(ResolvesCallerDirective)
		fset    = token.NewFileSet()
	)
	_ = filepath.WalkDir(projectDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // an unreadable subtree declares nothing
		}
		if d.IsDir() {
			if p == projectDir {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "node_modules" || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			if _, statErr := os.Stat(filepath.Join(p, "go.mod")); statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil || !bytes.Contains(data, needle) {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, p, data, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			return nil //nolint:nilerr // a file that does not parse declares nothing
		}
		rel, _ := filepath.Rel(projectDir, p)
		rel = filepath.ToSlash(rel)
		importPath := path.Join(module, filepath.ToSlash(filepath.Dir(rel)))
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !hasLineDirective(fn.Doc, ResolvesCallerDirective) {
				continue
			}
			line := fset.Position(fn.Pos()).Line
			if fn.Recv != nil {
				ignored = append(ignored, IgnoredResolverMarker{
					File: rel, Line: line,
					Reason: "on a method: a call site does not name its receiver's type, so only package-level functions can be matched",
				})
				continue
			}
			found = append(found, CallerResolver{
				ImportPath: importPath, Package: f.Name.Name, Func: fn.Name.Name,
				File: rel, Line: line,
			})
		}
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].ImportPath != found[j].ImportPath {
			return found[i].ImportPath < found[j].ImportPath
		}
		return found[i].Func < found[j].Func
	})
	return found, ignored
}

// hasLineDirective reports whether a doc comment carries directive as a
// whole line, spaced (`// forge:x`) or not (`//forge:x`), optionally
// followed by `:` and prose. Prose that merely mentions the directive
// mid-sentence does not count.
func hasLineDirective(doc *ast.CommentGroup, directive string) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if text == directive || strings.HasPrefix(text, directive+":") || strings.HasPrefix(text, directive+" ") {
			return true
		}
	}
	return false
}
