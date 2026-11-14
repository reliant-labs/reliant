// Package tracinglint implements `forge lint --tracing`: the rules that keep a
// request's work inside ONE trace.
//
// The tracing audit (control-plane and reliant, prod) found traces that were
// mostly a single span. Not because instrumentation was missing from forge —
// otelconnect, dbtrace, observe.NewHTTPClient and the generated component
// decorators all exist — but because the app's own code stepped around them:
// a hand-built *http.Client, a sql.Open, a contract constructed without its
// decorator, and above all a log line written without the request's context,
// so it carried no trace_id and could not be joined to anything. Each is
// invisible at review time and each silently cuts the trace.
//
// The rules, all at error by default (a project adopting them mid-flight dials
// one to warn with forge.yaml `lint.rules`):
//
//	forge-tracing-log-without-ctx         slog.Info(…) / logger.Info(…) / a
//	                                      configured wrapper, called where a
//	                                      context.Context (or *http.Request) is
//	                                      in scope. Autofixed to the …Context
//	                                      form (logctx.go).
//	forge-tracing-raw-http-client         an *http.Client with no transport, or
//	                                      http.DefaultClient / http.Get
//	                                      (rawclient.go).
//	forge-tracing-raw-sql-open            sql.Open, sqlx.Connect, pgxpool.New …
//	                                      instead of dbtrace (rawclient.go).
//	forge-tracing-contract-uninstrumented an instrumented contract package's
//	                                      New(…) used without its generated
//	                                      …WithForgeMiddleware decorator
//	                                      (uninstrumented.go).
//
// Every rule is a pure AST pass over the project's OWNED Go source: tests,
// generated files and vendored trees are out of scope. Each errs toward
// silence where the code may already be right; a false negative costs one
// span, a false positive costs trust in the gate.
package tracinglint

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs. Addressable from forge.yaml `lint.rules` and from
// `// forge:lint-disable-next-line <rule>: <reason>` directives.
const (
	RuleLogWithoutCtx           = "forge-tracing-log-without-ctx"
	RuleRawHTTPClient           = "forge-tracing-raw-http-client"
	RuleRawSQLOpen              = "forge-tracing-raw-sql-open"
	RuleContractUninstrumented  = "forge-tracing-contract-uninstrumented"
	middlewareConstructorSuffix = "WithForgeMiddleware"
)

// LogWrapper names an app-owned logging helper that writes through slog
// without a context — reliant's `logging.Info(msg, args...)` — and the
// context-taking function a call to it is rewritten to.
type LogWrapper struct {
	// Call is the wrapper, as "<import path>.<Func>".
	Call string
	// ContextCall is the replacement, as "<import path>.<Func>", taking the
	// context as its first argument and then the wrapper's arguments
	// unchanged. Empty means "<same package>.<Func>Context".
	ContextCall string
}

// CheckLogWrapper reports a wrapper whose Call or ContextCall is not
// "<import path>.<Func>".
func CheckLogWrapper(w LogWrapper) error {
	if p, f := splitQualified(w.Call); p == "" || f == "" {
		return fmt.Errorf("call %q is not \"<import path>.<Func>\" (e.g. github.com/acme/app/internal/logging.Info)", w.Call)
	}
	if w.ContextCall != "" {
		if p, f := splitQualified(w.ContextCall); p == "" || f == "" {
			return fmt.Errorf("context_call %q is not \"<import path>.<Func>\" (e.g. log/slog.InfoContext)", w.ContextCall)
		}
	}
	return nil
}

// Options configures a run.
type Options struct {
	// LogWrappers are the app's own context-free logging helpers.
	LogWrappers []LogWrapper
	// Fix rewrites every autofixable finding in place (only
	// forge-tracing-log-without-ctx has a fix) and reports what it rewrote
	// instead of the finding.
	Fix bool
	// FixFilter, when non-nil, narrows Fix to the findings it accepts: the
	// lane declines a finding a suppression directive covers, or whose rule
	// forge.yaml grades below the level it fixes at. A declined finding is
	// reported instead.
	FixFilter func(f finding.Finding) bool
	// Write, when non-nil, is how a fix lands on disk — the caller's hook
	// for verifying the rewrite (forge lint type-checks every package a fix
	// touched and takes back a fix that broke one). nil writes in place.
	Write func(abs string, data []byte, mode os.FileMode) error
	// Instrumented reports whether the component package in dir (absolute)
	// gets the generated decorator. nil disables
	// forge-tracing-contract-uninstrumented.
	Instrumented func(dir string) bool
	// LibraryOnly narrows every rule to the code another module can import:
	// everything outside the top-level internal/ and cmd/ trees that is not
	// package main. It is for a project that runs no traced process of its
	// own (forge.yaml observability off, which is the derived default for a
	// project with no server): its internal packages and its mains run only
	// in its own binaries, which record no trace, so there is no trace there
	// for a log line or a client to cut. Its library packages still run
	// inside the traced processes that import them.
	LibraryOnly bool
}

// Fixed is one file the run rewrote.
type Fixed struct {
	File  string // project-relative, slash-separated
	Rule  string
	Count int
}

// Result is a run's outcome.
type Result struct {
	Findings []finding.Finding
	Fixed    []Fixed
}

// goFile is one parsed owned Go file.
type goFile struct {
	rel     string // project-relative, slash-separated
	abs     string
	src     []byte
	fset    *token.FileSet
	file    *ast.File
	imports map[string]string // local name → import path
}

// importName returns the local name the file uses for path, or "".
func (f *goFile) importName(path string) string {
	for name, p := range f.imports {
		if p == path {
			return name
		}
	}
	return ""
}

func (f *goFile) line(pos token.Pos) int { return f.fset.Position(pos).Line }

// Lint runs every rule over the owned Go source under root.
func Lint(root string, opts Options) (Result, error) {
	files, err := loadOwnedGo(root)
	if err != nil {
		return Result{}, err
	}
	if opts.LibraryOnly {
		files = libraryFiles(files)
	}
	var res Result
	pkgs := map[string][]*goFile{}
	for _, f := range files {
		dir := filepath.Dir(f.abs)
		pkgs[dir] = append(pkgs[dir], f)
	}
	module := modulePath(root)
	var instrumented func(string) bool
	if opts.Instrumented != nil {
		instrumented = memoize(opts.Instrumented)
	}
	for _, f := range files {
		// Constructions first: the fix rewrites and reloads f, so every rule
		// after it reads the file as it now is.
		if instrumented != nil && module != "" {
			var fix, report []ctorSite
			for _, s := range uninstrumentedSites(f, root, module, instrumented) {
				if s.rename != "" && opts.Fix && (opts.FixFilter == nil || opts.FixFilter(s.finding(f))) {
					fix = append(fix, s)
				} else {
					report = append(report, s)
				}
			}
			if len(fix) > 0 {
				findings := make([]finding.Finding, 0, len(fix))
				for _, s := range fix {
					findings = append(findings, s.finding(f))
				}
				n, err := applyCtorFixes(f, fix)
				switch {
				case err != nil:
					return Result{}, err
				case n > 0:
					res.Fixed = append(res.Fixed, Fixed{File: f.rel, Rule: RuleContractUninstrumented, Count: n})
				default:
					res.Findings = append(res.Findings, findings...)
				}
			}
			for _, s := range report {
				res.Findings = append(res.Findings, s.finding(f))
			}
		}
		res.Findings = append(res.Findings, rawHTTPClientFindings(f)...)
		res.Findings = append(res.Findings, rawSQLOpenFindings(f)...)
	}
	for _, dir := range sortedKeys(pkgs) {
		loggers := collectLoggerFields(pkgs[dir])
		for _, f := range pkgs[dir] {
			var fix, report []logSite
			for _, s := range logWithoutCtxSites(f, loggers, opts.LogWrappers) {
				if opts.Fix && (opts.FixFilter == nil || opts.FixFilter(s.finding(f))) {
					fix = append(fix, s)
				} else {
					report = append(report, s)
				}
			}
			if len(fix) > 0 {
				n, err := applyLogFixes(f, fix, opts.Write)
				if err != nil {
					return Result{}, err
				}
				if n > 0 {
					res.Fixed = append(res.Fixed, Fixed{File: f.rel, Rule: RuleLogWithoutCtx, Count: n})
				} else {
					report = append(report, fix...)
				}
			}
			for _, s := range report {
				res.Findings = append(res.Findings, s.finding(f))
			}
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return res, nil
}

// skippedDirs are never project-owned Go source.
var skippedDirs = map[string]bool{
	"node_modules": true, "vendor": true, "testdata": true, "gen": true, "dist": true,
}

// loadOwnedGo parses every owned, non-test, non-generated Go file under root.
// A file that does not parse is skipped: the compiler reports it better.
func loadOwnedGo(root string) ([]*goFile, error) {
	var out []*goFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skippedDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			// A nested module is another project.
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, "_gen.go") || strings.HasSuffix(name, ".pb.go") ||
			strings.HasSuffix(name, ".connect.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isGenerated(src) {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, &goFile{
			rel: filepath.ToSlash(rel), abs: path, src: src, fset: fset, file: file,
			imports: fileImports(file),
		})
		return nil
	})
	return out, err
}

// libraryFiles keeps the files another module can import (Options.LibraryOnly):
// not under the top-level internal/ or cmd/, and not package main. A nested
// internal/ (pkg/x/internal) stays: pkg/x runs it inside its importers.
func libraryFiles(files []*goFile) []*goFile {
	out := files[:0:0]
	for _, f := range files {
		top, _, _ := strings.Cut(f.rel, "/")
		if top == "internal" || top == "cmd" || f.file.Name.Name == "main" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isGenerated reports the standard "Code generated … DO NOT EDIT." header.
func isGenerated(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if strings.HasPrefix(line, "// Code generated ") && strings.HasSuffix(line, "DO NOT EDIT.") {
			return true
		}
	}
	return false
}

// fileImports maps each import's local name to its path. Blank and dot
// imports are omitted: neither can qualify a call.
func fileImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := defaultImportName(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		out[name] = path
	}
	return out
}

// defaultImportName is the package name an unaliased import binds: the last
// path element, skipping a major-version suffix (pgx/v5 → pgx).
func defaultImportName(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && isDigits(last[1:]) {
		last = parts[len(parts)-2]
	}
	return strings.TrimPrefix(last, "go-")
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// pkgCall reports whether expr is a call `<name>.<fn>(…)` where name is the
// file's import of path, returning fn.
func pkgCall(f *goFile, expr ast.Expr, path string) (string, *ast.CallExpr, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", nil, false
	}
	fn, ok := pkgSelector(f, call.Fun, path)
	return fn, call, ok
}

// pkgSelector reports whether expr is `<name>.<Sel>` with name the file's
// import of path, returning Sel.
func pkgSelector(f *goFile, expr ast.Expr, path string) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || f.imports[id.Name] != path {
		return "", false
	}
	return sel.Sel.Name, true
}

// modulePath reads the module path from root/go.mod, or "".
func modulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeAndReload gofmts out, writes it over f's file, and re-parses f from it.
// A result that does not gofmt is not written (and is not an error): the
// caller's sites are then reported instead of fixed — see the n==0 returns.
func writeAndReload(f *goFile, out []byte) error {
	formatted, err := format.Source(out)
	if err != nil {
		return errNotFixed
	}
	info, err := os.Stat(f.abs)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(f.abs, formatted, info.Mode().Perm()); err != nil {
		return fmt.Errorf("tracing lint: rewrite %s: %w", f.rel, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.abs, formatted, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("tracing lint: re-parse %s: %w", f.rel, err)
	}
	f.src, f.fset, f.file, f.imports = formatted, fset, file, fileImports(file)
	return nil
}

// errNotFixed marks a rewrite that was abandoned because its result did not
// gofmt; the sites are reported instead.
var errNotFixed = errors.New("tracing lint: rewrite abandoned")
