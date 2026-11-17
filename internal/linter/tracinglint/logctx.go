// File: logctx.go — forge-tracing-log-without-ctx.
//
// # The defect
//
// observe's slog handler stamps trace_id and span_id on a record from the
// context the record was logged with. `slog.Info(msg)` passes
// context.Background(), so a line written in the middle of a request is an
// orphan: nothing joins it to the trace, and "show me the logs for this
// request" returns everything except the line that explains the failure. In
// the reliant audit, under a fifth of request-time log lines carried a
// trace_id.
//
// # What is flagged — only where the fix is mechanical
//
// A call to a context-free level method — Debug, Info, Warn, Error — on
//
//   - the slog package itself (`slog.Info`),
//   - a *slog.Logger (a parameter, a local or package var, or a struct field
//     every declaration of which in the package is a *slog.Logger; plus
//     slog.Default() / slog.New / .With chains), or
//   - an app helper declared in forge.yaml `lint.tracing.log_wrappers`,
//
// made where a context IS in scope: a named context.Context parameter of the
// enclosing function (or an enclosing closure's), or a *http.Request one
// (`r.Context()`). A call with no context in scope is not flagged — there is
// no trace to join, and forcing `context.Background()` into it would be
// noise. So every finding has the same fix, and `forge lint --tracing --fix`
// applies it:
//
//	slog.Info("msg", k, v)       → slog.InfoContext(ctx, "msg", k, v)
//	s.log.Warn("msg")            → s.log.WarnContext(ctx, "msg")
//	logging.Error("msg", "e", e) → logging.ErrorContext(ctx, "msg", "e", e)
//
// The *slog.Logger detection is syntactic (no type checker), so it is
// deliberately narrow: a field name declared with two different types in one
// package is not treated as a logger, and neither is anything it cannot see
// declared. A zap or logrus logger is never rewritten.

package tracinglint

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

const slogPath = "log/slog"

// logLevels are the context-free level methods, on the package and on
// *slog.Logger alike; each has a <Level>Context twin.
var logLevels = map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true}

// loggerNames is what a package declares as a *slog.Logger.
type loggerNames struct {
	fields map[string]bool // struct field names, true only when every declaration is *slog.Logger
	vars   map[string]bool // package-level vars
}

// logSite is one call to rewrite.
type logSite struct {
	call      *ast.CallExpr
	ctxExpr   string // the context argument to insert
	oldFun    string // the call's function as written, for the message
	newFun    string // its replacement
	addImport string // an import path the replacement needs, or ""
	addName   string // the local name to import it as
	// dropImport is the wrapper's import path when the replacement lives in
	// another package: removed once the rewrite leaves it unused.
	dropImport string
}

func (s logSite) finding(f *goFile) finding.Finding {
	newCall := s.newFun + "(" + s.ctxExpr + ", …)"
	return finding.Finding{
		Rule:     RuleLogWithoutCtx,
		Severity: finding.SeverityError,
		File:     f.rel,
		Line:     f.line(s.call.Pos()),
		Message: fmt.Sprintf("%s logs without the context in scope, so the line carries no trace_id and cannot be joined to its request; use %s",
			s.oldFun, newCall),
		Remediation: "`forge lint --tracing --fix` rewrites every such call to its …Context form",
	}
}

// collectLoggerFields scans one package's files for *slog.Logger struct
// fields and package-level vars.
func collectLoggerFields(files []*goFile) loggerNames {
	names := loggerNames{fields: map[string]bool{}, vars: map[string]bool{}}
	for _, f := range files {
		slogName := f.importName(slogPath)
		ast.Inspect(f.file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				isLogger := isSlogLoggerType(field.Type, slogName)
				fieldNames := field.Names
				if len(fieldNames) == 0 { // embedded: the field is named for its type
					if isLogger {
						fieldNames = []*ast.Ident{{Name: "Logger"}}
					}
				}
				for _, id := range fieldNames {
					prev, seen := names.fields[id.Name]
					names.fields[id.Name] = isLogger && (!seen || prev)
				}
			}
			return true
		})
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					switch {
					case vs.Type != nil:
						names.vars[id.Name] = isSlogLoggerType(vs.Type, slogName)
					case i < len(vs.Values):
						names.vars[id.Name] = isSlogConstructor(f, vs.Values[i])
					}
				}
			}
		}
	}
	return names
}

// isSlogLoggerType reports `*slog.Logger`.
func isSlogLoggerType(expr ast.Expr, slogName string) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok || slogName == "" {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == slogName && sel.Sel.Name == "Logger"
}

// isSlogConstructor reports slog.Default() / slog.New(…) / slog.With(…).
func isSlogConstructor(f *goFile, expr ast.Expr) bool {
	fn, _, ok := pkgCall(f, expr, slogPath)
	return ok && (fn == "Default" || fn == "New" || fn == "With")
}

// funcScope is what one enclosing function contributes.
type funcScope struct {
	ctx     string          // context expression from its params, "" if none
	params  map[string]bool // every parameter name (for shadowing)
	loggers map[string]bool // parameter/local names that are *slog.Logger; false = ambiguous
}

// logWithoutCtxSites finds every flagged call in f.
func logWithoutCtxSites(f *goFile, pkg loggerNames, wrappers []LogWrapper) []logSite {
	var sites []logSite
	var stack []*funcScope
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body == nil {
				return false
			}
			stack = append(stack, newFuncScope(f, fn.Recv, fn.Type, fn.Body, pkg))
			ast.Inspect(fn.Body, walk)
			stack = stack[:len(stack)-1]
			return false
		case *ast.FuncLit:
			stack = append(stack, newFuncScope(f, nil, fn.Type, fn.Body, pkg))
			ast.Inspect(fn.Body, walk)
			stack = stack[:len(stack)-1]
			return false
		case *ast.CallExpr:
			if s, ok := classifyLogCall(f, fn, stack, pkg, wrappers); ok {
				sites = append(sites, s)
			}
		}
		return true
	}
	ast.Inspect(f.file, walk)
	return sites
}

func newFuncScope(f *goFile, recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt, pkg loggerNames) *funcScope {
	sc := &funcScope{params: map[string]bool{}, loggers: map[string]bool{}}
	slogName := f.importName(slogPath)
	ctxName, httpName := f.importName("context"), f.importName("net/http")
	var reqCtx string
	for _, list := range []*ast.FieldList{recv, typ.Params} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, id := range field.Names {
				sc.params[id.Name] = true
				if id.Name == "_" {
					continue
				}
				switch {
				case isPkgType(field.Type, ctxName, "Context", false):
					if sc.ctx == "" {
						sc.ctx = id.Name
					}
				case isPkgType(field.Type, httpName, "Request", true):
					if reqCtx == "" {
						reqCtx = id.Name + ".Context()"
					}
				case isSlogLoggerType(field.Type, slogName):
					sc.loggers[id.Name] = true
				}
			}
		}
	}
	if sc.ctx == "" {
		sc.ctx = reqCtx
	}
	// Locals: a name is a logger only if every definition of it in this
	// function is one.
	ast.Inspect(body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if st.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range st.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				isLogger := len(st.Rhs) == len(st.Lhs) && isLoggerValue(f, st.Rhs[i], pkg, []*funcScope{sc})
				prev, seen := sc.loggers[id.Name]
				sc.loggers[id.Name] = isLogger && (!seen || prev)
			}
		case *ast.ValueSpec:
			for i, id := range st.Names {
				isLogger := false
				switch {
				case st.Type != nil:
					isLogger = isSlogLoggerType(st.Type, slogName)
				case i < len(st.Values):
					isLogger = isLoggerValue(f, st.Values[i], pkg, []*funcScope{sc})
				}
				prev, seen := sc.loggers[id.Name]
				sc.loggers[id.Name] = isLogger && (!seen || prev)
			}
		}
		return true
	})
	return sc
}

// isPkgType reports `<pkgName>.<typeName>`, or its pointer when ptr.
func isPkgType(expr ast.Expr, pkgName, typeName string, ptr bool) bool {
	if pkgName == "" {
		return false
	}
	if ptr {
		star, ok := expr.(*ast.StarExpr)
		if !ok {
			return false
		}
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkgName && sel.Sel.Name == typeName
}

// isLoggerValue reports whether expr is known to be a *slog.Logger.
func isLoggerValue(f *goFile, expr ast.Expr, pkg loggerNames, stack []*funcScope) bool {
	switch x := expr.(type) {
	case *ast.ParenExpr:
		return isLoggerValue(f, x.X, pkg, stack)
	case *ast.Ident:
		for i := len(stack) - 1; i >= 0; i-- {
			if v, ok := stack[i].loggers[x.Name]; ok {
				return v
			}
			if stack[i].params[x.Name] {
				return false
			}
		}
		return pkg.vars[x.Name]
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && f.imports[id.Name] != "" {
			return false // a package-qualified identifier, not a field
		}
		return pkg.fields[x.Sel.Name]
	case *ast.CallExpr:
		if isSlogConstructor(f, x) {
			return true
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "With" || sel.Sel.Name == "WithGroup") {
			return isLoggerValue(f, sel.X, pkg, stack)
		}
	}
	return false
}

// ctxInScope returns the nearest context expression the call can use, or "".
// An outer function's context is unusable when an inner function rebinds its
// name as a parameter.
func ctxInScope(stack []*funcScope) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if c := stack[i].ctx; c != "" {
			root := strings.TrimSuffix(c, ".Context()")
			for j := i + 1; j < len(stack); j++ {
				if stack[j].params[root] {
					return ""
				}
			}
			return c
		}
	}
	return ""
}

func classifyLogCall(f *goFile, call *ast.CallExpr, stack []*funcScope, pkg loggerNames, wrappers []LogWrapper) (logSite, bool) {
	if len(call.Args) == 0 {
		return logSite{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return logSite{}, false
	}
	site := logSite{call: call, oldFun: f.text(sel)}
	switch {
	case isPkgSel(f, sel, slogPath) && logLevels[sel.Sel.Name]:
		site.newFun = f.text(sel) + "Context"
	case !isAnyPkgSel(f, sel) && logLevels[sel.Sel.Name] && isLoggerValue(f, sel.X, pkg, stack):
		site.newFun = f.text(sel) + "Context"
	default:
		w, ok := matchWrapper(f, sel, wrappers)
		if !ok {
			return logSite{}, false
		}
		if !resolveWrapperTarget(f, w, &site) {
			return logSite{}, false
		}
	}
	site.ctxExpr = ctxInScope(stack)
	if site.ctxExpr == "" {
		return logSite{}, false
	}
	return site, true
}

func (f *goFile) text(n ast.Node) string {
	return string(f.src[f.fset.Position(n.Pos()).Offset:f.fset.Position(n.End()).Offset])
}

func isPkgSel(f *goFile, sel *ast.SelectorExpr, path string) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && f.imports[id.Name] == path
}

func isAnyPkgSel(f *goFile, sel *ast.SelectorExpr) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && f.imports[id.Name] != ""
}

// matchWrapper reports whether sel is a call to a configured wrapper.
func matchWrapper(f *goFile, sel *ast.SelectorExpr, wrappers []LogWrapper) (LogWrapper, bool) {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return LogWrapper{}, false
	}
	path := f.imports[id.Name]
	if path == "" {
		return LogWrapper{}, false
	}
	for _, w := range wrappers {
		wp, wf := splitQualified(w.Call)
		if wp == path && wf == sel.Sel.Name {
			return w, true
		}
	}
	return LogWrapper{}, false
}

// resolveWrapperTarget fills site.newFun (and an import, when the target
// lives in another package). It declines when the target package's default
// name is already bound to a different import in this file.
func resolveWrapperTarget(f *goFile, w LogWrapper, site *logSite) bool {
	wp, wf := splitQualified(w.Call)
	tp, tf := wp, wf+"Context"
	if w.ContextCall != "" {
		tp, tf = splitQualified(w.ContextCall)
	}
	if tp == "" || tf == "" {
		return false
	}
	if tp != wp {
		site.dropImport = wp
	}
	if name := f.importName(tp); name != "" {
		site.newFun = name + "." + tf
		return true
	}
	name := defaultImportName(tp)
	if _, taken := f.imports[name]; taken {
		return false
	}
	site.newFun, site.addImport, site.addName = name+"."+tf, tp, name
	return true
}

// splitQualified splits "<import path>.<Func>" at the last dot after the last
// slash.
func splitQualified(q string) (string, string) {
	slash := strings.LastIndex(q, "/")
	dot := strings.LastIndex(q, ".")
	if dot <= slash || dot == len(q)-1 {
		return "", ""
	}
	return q[:dot], q[dot+1:]
}

// applyLogFixes rewrites f's sites in place and returns how many it rewrote.
// The result must parse and gofmt, or the file is left untouched and the
// sites are reported instead.
func applyLogFixes(f *goFile, sites []logSite, write func(abs string, data []byte, mode os.FileMode) error) (int, error) {
	if write == nil {
		write = atomicfile.WriteFile
	}
	type edit struct {
		at, end int
		text    string
	}
	var edits []edit
	imports := map[string]string{}
	drops := map[string]bool{}
	for _, s := range sites {
		if s.dropImport != "" {
			drops[s.dropImport] = true
		}
		funStart := f.fset.Position(s.call.Fun.Pos()).Offset
		funEnd := f.fset.Position(s.call.Fun.End()).Offset
		lparen := f.fset.Position(s.call.Lparen).Offset
		edits = append(edits,
			edit{at: funStart, end: funEnd, text: s.newFun},
			edit{at: lparen + 1, end: lparen + 1, text: s.ctxExpr + ", "},
		)
		if s.addImport != "" {
			imports[s.addImport] = s.addName
		}
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	out := append([]byte(nil), f.src...)
	for _, e := range edits {
		out = append(out[:e.at], append([]byte(e.text), out[e.end:]...)...)
	}
	if len(imports)+len(drops) > 0 {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.abs, out, parser.ParseComments)
		if err != nil {
			return 0, nil
		}
		for path, name := range imports {
			if defaultImportName(path) == name {
				astutil.AddImport(fset, file, path)
			} else {
				astutil.AddNamedImport(fset, file, name, path)
			}
		}
		for path := range drops {
			if !astutil.UsesImport(file, path) {
				astutil.DeleteNamedImport(fset, file, explicitImportName(file, path), path)
			}
		}
		var buf bytes.Buffer
		if err := format.Node(&buf, fset, file); err != nil {
			return 0, nil
		}
		out = buf.Bytes()
	}
	formatted, err := format.Source(out)
	if err != nil {
		return 0, nil
	}
	info, err := os.Stat(f.abs)
	if err != nil {
		return 0, err
	}
	if err := write(f.abs, formatted, info.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("tracing lint: rewrite %s: %w", f.rel, err)
	}
	return len(sites), nil
}

// explicitImportName is the alias file gives path's import, or "" when it is
// imported under its own name.
func explicitImportName(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil && p == path && spec.Name != nil {
			return spec.Name.Name
		}
	}
	return ""
}
