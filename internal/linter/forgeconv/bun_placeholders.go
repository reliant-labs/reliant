// File: internal/linter/forgeconv/bun_placeholders.go
//
// Two placeholder dialects share one data-access API, and nothing at the
// call site says which one a string is in:
//
//   - The raw passthroughs — orm.Context's Exec / Query / QueryRow — hand
//     the SQL to the driver unchanged. They take postgres's native `$1`,
//     `$2`, bound by the server.
//   - Everything bun formats — db.Bun().NewRaw, the query builders'
//     Where / Set / ColumnExpr / …, bun.SafeQuery, and db.Bun()'s own
//     ExecContext / QueryContext / QueryRowContext — takes bun's `?` (in
//     order) or `?0`, `?1` (0-based, reusable), and interpolates the
//     arguments into the text client-side. EVERY unescaped `?` in that text
//     is a placeholder: one inside a SQL comment or string literal, and the
//     jsonb operators `?`, `?|` and `?&`.
//
// Porting hand SQL from the first dialect into the second is mechanical and
// easy to get half right, and both halves fail badly:
//
//   - forgeconv-bun-dollar-placeholder: a `$1` left in bun-formatted SQL is
//     sent to postgres literally while bun drops the arguments, so the
//     statement fails at run time ("there is no parameter $1").
//   - forgeconv-bun-literal-question-mark: a `?` bun was not meant to see
//     consumes the next argument and shifts every later one onto the wrong
//     placeholder. When the counts happen to line up the statement runs —
//     against the wrong values.
//
// Both are errors. The fixes are spelled out in the remediation: `?` / `?N`
// for bun, `\?` to escape a literal question mark (`\?|`, `\?&` for the
// jsonb operators, or jsonb_exists / jsonb_exists_any / jsonb_exists_all),
// or the raw passthrough when the statement should keep `$N`.
//
// The analysis is syntactic. A call is bun-formatted when it is NewRaw (any
// receiver), bun.SafeQuery, ExecContext / QueryContext / QueryRowContext on
// a receiver chain that goes through .Bun() or .BunDB(), or a query-builder
// method whose receiver chain starts at a bun New* constructor or at a
// parameter typed *bun.<X>Query (an orm.QueryOption's `q`). Only string
// literals and concatenations of them are read; a query assembled at run
// time is not judged.

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	bunDollarPlaceholderRule = "forgeconv-bun-dollar-placeholder"
	bunLiteralQuestionRule   = "forgeconv-bun-literal-question-mark"
)

// bunBuilderQueryArg maps a bun query-builder method to the index of its
// query-string argument.
var bunBuilderQueryArg = map[string]int{
	"Where": 0, "WhereOr": 0, "Set": 0, "ColumnExpr": 0, "OrderExpr": 0,
	"TableExpr": 0, "ModelTableExpr": 0, "GroupExpr": 0, "Having": 0,
	"Returning": 0, "On": 0, "For": 0, "DistinctOn": 0, "Join": 0,
	"JoinOn": 0, "JoinOnOr": 0, "SetColumn": 1, "Value": 1,
}

// bunConstructors start a bun query-builder chain.
var bunConstructors = map[string]bool{
	"NewSelect": true, "NewInsert": true, "NewUpdate": true, "NewDelete": true,
	"NewMerge": true, "NewValues": true, "NewRaw": true,
}

// bunDirectExec are bun.IDB's own formatting executors, reached as
// db.Bun().ExecContext(ctx, query, args...): query is argument 1.
var bunDirectExec = map[string]bool{"ExecContext": true, "QueryContext": true, "QueryRowContext": true}

// LintBunPlaceholders walks rootDir for Go files and reports SQL in
// bun-formatted calls that is written for the other placeholder dialect.
// Generated files (gen/, *_gen.go), vendor/, node_modules/ and testdata/
// are skipped. Findings are in deterministic order.
func LintBunPlaceholders(rootDir string) (Result, error) {
	var files []string
	err := filepath.WalkDir(rootDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "gen", "testdata", ".forge":
				if p != rootDir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_gen.go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", rootDir, err)
	}
	sort.Strings(files)

	// Parse per directory first: a query is usually a named constant, and
	// the constant may live in another file of the package.
	type parsed struct {
		path string
		file *ast.File
	}
	fset := token.NewFileSet()
	byDir := map[string][]parsed{}
	var dirs []string
	for _, fp := range files {
		file, perr := parser.ParseFile(fset, fp, nil, parser.SkipObjectResolution)
		if perr != nil {
			continue // the Go toolchain reports parse errors
		}
		dir := filepath.Dir(fp)
		if _, seen := byDir[dir]; !seen {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], parsed{fp, file})
	}

	relOf := func(path string) string {
		if rel, err := filepath.Rel(rootDir, path); err == nil {
			return filepath.ToSlash(rel)
		}
		return filepath.ToSlash(path)
	}
	var result Result
	for _, dir := range dirs {
		// Package-level string constants and vars, keyed by package name so
		// an external _test package does not resolve the package's names.
		consts := map[string]map[string]ast.Expr{}
		for _, pf := range byDir[dir] {
			pkg := pf.file.Name.Name
			if consts[pkg] == nil {
				consts[pkg] = map[string]ast.Expr{}
			}
			collectStringDecls(pf.file.Decls, consts[pkg])
		}
		for _, pf := range byDir[dir] {
			rel, relErr := filepath.Rel(rootDir, pf.path)
			if relErr != nil {
				rel = pf.path
			}
			result.Findings = append(result.Findings,
				lintBunPlaceholdersFile(fset, pf.file, filepath.ToSlash(rel), consts[pf.file.Name.Name], relOf)...)
		}
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return result, nil
}

// collectStringDecls records every `const` / `var` name in decls bound to
// a single expression, so a query held in a named constant can be read.
func collectStringDecls(decls []ast.Decl, into map[string]ast.Expr) {
	for _, decl := range decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, n := range vs.Names {
				into[n.Name] = vs.Values[i]
			}
		}
	}
}

func lintBunPlaceholdersFile(fset *token.FileSet, file *ast.File, rel string, pkgConsts map[string]ast.Expr, relOf func(string) string) []Finding {
	bunName := importName(file, "github.com/uptrace/bun")
	var findings []Finding
	// Functions enclosing the current node, innermost last, so a builder
	// receiver that is a parameter or a local can be resolved.
	var funcs []funcScope
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				funcs = append(funcs, funcScope{typ: n.Type, body: n.Body})
				ast.Inspect(n.Body, visit)
				funcs = funcs[:len(funcs)-1]
			}
			return false
		case *ast.FuncLit:
			funcs = append(funcs, funcScope{typ: n.Type, body: n.Body})
			ast.Inspect(n.Body, visit)
			funcs = funcs[:len(funcs)-1]
			return false
		case *ast.CallExpr:
			if idx, ok := bunQueryArgIndex(n, bunName, funcs); ok && idx < len(n.Args) {
				// A stray `?` only misbinds when there is an argument for
				// it to consume; with none, bun leaves it in the text.
				hasArgs := len(n.Args) > idx+1 || n.Ellipsis.IsValid()
				resolve := func(name string) ast.Expr {
					for i := len(funcs) - 1; i >= 0; i-- {
						local := map[string]ast.Expr{}
						for _, st := range funcs[i].body.List {
							if ds, ok := st.(*ast.DeclStmt); ok {
								collectStringDecls([]ast.Decl{ds.Decl}, local)
							}
						}
						if e, ok := local[name]; ok {
							return e
						}
					}
					return pkgConsts[name]
				}
				site := fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line)
				findings = append(findings, checkBunSQL(fset, n.Args[idx], site, hasArgs, resolve, relOf)...)
			}
		}
		return true
	}
	ast.Inspect(file, visit)
	return findings
}

// funcScope is one enclosing function: its signature and body.
type funcScope struct {
	typ  *ast.FuncType
	body *ast.BlockStmt
}

// bunQueryArgIndex reports whether call is bun-formatted and, if so, which
// argument holds its query string.
func bunQueryArgIndex(call *ast.CallExpr, bunName string, funcs []funcScope) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	name := sel.Sel.Name
	if pkg, ok := sel.X.(*ast.Ident); ok && bunName != "" && pkg.Name == bunName {
		return 0, name == "SafeQuery"
	}
	if name == "NewRaw" {
		return 0, true
	}
	if bunDirectExec[name] {
		return 1, chainHasCall(sel.X, "Bun", "BunDB")
	}
	if idx, ok := bunBuilderQueryArg[name]; ok && bunBuilderReceiver(sel.X, bunName, funcs) {
		return idx, true
	}
	return 0, false
}

// chainHasCall reports whether the receiver chain x contains a call to one
// of the named methods.
func chainHasCall(x ast.Expr, names ...string) bool {
	for {
		switch e := x.(type) {
		case *ast.CallExpr:
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				for _, n := range names {
					if sel.Sel.Name == n {
						return true
					}
				}
				x = sel.X
				continue
			}
			return false
		case *ast.SelectorExpr:
			x = e.X
		case *ast.ParenExpr:
			x = e.X
		default:
			return false
		}
	}
}

// bunBuilderReceiver reports whether x — the receiver of a builder method —
// is a bun query: a chain rooted at a bun New* constructor, a parameter of
// an enclosing function declared as *bun.<X>Query, or a local assigned from
// a constructor chain (`q := db.Bun().NewSelect()…; q.Where(…)`).
func bunBuilderReceiver(x ast.Expr, bunName string, funcs []funcScope) bool {
	if chainHasCall(x, constructorNames()...) {
		return true
	}
	root := x
	for {
		switch e := root.(type) {
		case *ast.CallExpr:
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				root = sel.X
				continue
			}
			return false
		case *ast.ParenExpr:
			root = e.X
			continue
		}
		break
	}
	id, ok := root.(*ast.Ident)
	if !ok {
		return false
	}
	for i := len(funcs) - 1; i >= 0; i-- {
		if localFromConstructor(funcs[i].body, id.Name) {
			return true
		}
		if funcs[i].typ.Params == nil || bunName == "" {
			continue
		}
		for _, field := range funcs[i].typ.Params.List {
			for _, n := range field.Names {
				if n.Name == id.Name {
					return isBunQueryType(field.Type, bunName)
				}
			}
		}
	}
	return false
}

// localFromConstructor reports whether body assigns name from a bun
// constructor chain anywhere (`name := db.Bun().NewSelect()…`).
func localFromConstructor(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name && chainHasCall(as.Rhs[i], constructorNames()...) {
				found = true
			}
		}
		return true
	})
	return found
}

func constructorNames() []string {
	names := make([]string, 0, len(bunConstructors))
	for n := range bunConstructors {
		names = append(names, n)
	}
	return names
}

// isBunQueryType matches *bun.<X>Query.
func isBunQueryType(t ast.Expr, bunName string) bool {
	star, ok := t.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == bunName && strings.HasSuffix(sel.Sel.Name, "Query")
}

// importName returns the name path is imported under in file, or "".
func importName(file *ast.File, path string) string {
	for _, imp := range file.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}

// sqlSegment is one string literal of a (possibly concatenated) query.
type sqlSegment struct {
	start int // offset of the literal's value in the joined query
	lit   *ast.BasicLit
	value string
}

// literalSQL flattens a string literal, a named string constant (resolved
// through resolve), or a + concatenation of them, into its segments. ok is
// false for anything assembled at run time.
func literalSQL(e ast.Expr, resolve func(string) ast.Expr) (segs []sqlSegment, ok bool) {
	var walk func(e ast.Expr) bool
	offset := 0
	depth := 0
	walk = func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			target := resolve(e.Name)
			if target == nil || depth > 8 {
				return false
			}
			depth++
			defer func() { depth-- }()
			return walk(target)
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return false
			}
			v, err := strconv.Unquote(e.Value)
			if err != nil {
				return false
			}
			segs = append(segs, sqlSegment{start: offset, lit: e, value: v})
			offset += len(v)
			return true
		case *ast.BinaryExpr:
			return e.Op == token.ADD && walk(e.X) && walk(e.Y)
		case *ast.ParenExpr:
			return walk(e.X)
		}
		return false
	}
	ok = walk(e)
	return segs, ok
}

// positionAt returns the source file and line of offset in the joined
// query — the literal that holds it, which for a named constant is the
// constant's declaration. The line is exact inside a raw string literal and
// the literal's own line inside an interpreted one (whose escapes do not map
// onto source lines).
func positionAt(fset *token.FileSet, segs []sqlSegment, offset int) (string, int) {
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if offset < s.start {
			continue
		}
		pos := fset.Position(s.lit.Pos())
		line := pos.Line
		if strings.HasPrefix(s.lit.Value, "`") {
			line += strings.Count(s.value[:offset-s.start], "\n")
		}
		return pos.Filename, line
	}
	return "", 0
}

// sqlContext classifies where a byte of SQL text sits.
type sqlContext int

const (
	sqlCode sqlContext = iota
	sqlString
	sqlQuotedIdent
	sqlLineComment
	sqlBlockComment
	sqlDollarQuoted
)

func (c sqlContext) String() string {
	switch c {
	case sqlString, sqlDollarQuoted:
		return "a SQL string literal"
	case sqlLineComment, sqlBlockComment:
		return "a SQL comment"
	case sqlQuotedIdent:
		return "a quoted identifier"
	}
	return "SQL"
}

// checkBunSQL reads the bun-formatted query in arg, passed by the call at
// site. hasArgs reports whether the call passes arguments for the query's
// placeholders. Findings are reported where the SQL text is written — a
// named constant's declaration, when that is where it lives.
func checkBunSQL(fset *token.FileSet, arg ast.Expr, site string, hasArgs bool, resolve func(string) ast.Expr, relOf func(string) string) []Finding {
	segs, ok := literalSQL(arg, resolve)
	if !ok {
		return nil
	}
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString(s.value)
	}
	q := sb.String()

	var findings []Finding
	add := func(rule string, offset int, msg, fix string) {
		file, line := positionAt(fset, segs, offset)
		file = relOf(file)
		if inArg := segs[0].lit.Pos() >= arg.Pos() && segs[0].lit.End() <= arg.End(); !inArg {
			msg += " (query passed to bun at " + site + ")"
		}
		findings = append(findings, Finding{
			Rule: rule, Severity: SeverityError, File: file,
			Line: line, Message: msg, Remediation: fix,
		})
	}

	ctx := sqlCode
	dollarTag := ""
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch ctx {
		case sqlString:
			if c == '\'' {
				if i+1 < len(q) && q[i+1] == '\'' {
					i++
					continue
				}
				ctx = sqlCode
			}
		case sqlQuotedIdent:
			if c == '"' {
				ctx = sqlCode
			}
		case sqlLineComment:
			if c == '\n' {
				ctx = sqlCode
			}
		case sqlBlockComment:
			if c == '*' && i+1 < len(q) && q[i+1] == '/' {
				ctx = sqlCode
				i++
				continue
			}
		case sqlDollarQuoted:
			if c == '$' && strings.HasPrefix(q[i:], dollarTag) {
				ctx = sqlCode
				i += len(dollarTag) - 1
				continue
			}
		case sqlCode:
			switch {
			case c == '\'':
				ctx = sqlString
			case c == '"':
				ctx = sqlQuotedIdent
			case c == '-' && i+1 < len(q) && q[i+1] == '-':
				ctx = sqlLineComment
				i++
				continue
			case c == '/' && i+1 < len(q) && q[i+1] == '*':
				ctx = sqlBlockComment
				i++
				continue
			case c == '$':
				if n := digitsAt(q, i+1); n > 0 {
					add(bunDollarPlaceholderRule, i,
						fmt.Sprintf("%#q in a bun-formatted query: bun binds only its own `?` placeholders, interpolating the arguments client-side, so %#q reaches postgres literally with no argument bound (\"there is no parameter %s\")", q[i:i+1+n], q[i:i+1+n], q[i:i+1+n]),
						"write `?` (in argument order) or `?0`, `?1`, … (0-based, reusable) — or run the statement through the raw passthrough db.Exec / db.Query / db.QueryRow, which takes native `$N`")
					i += n
					continue
				}
				if tag, ok := dollarQuoteTag(q[i:]); ok {
					dollarTag = tag
					ctx = sqlDollarQuoted
					i += len(tag) - 1
					continue
				}
			}
		}

		if c != '?' || (i > 0 && q[i-1] == '\\') || !hasArgs {
			continue
		}
		if ctx != sqlCode {
			add(bunLiteralQuestionRule, i,
				fmt.Sprintf("`?` inside %s is still a bun placeholder: bun reads EVERY unescaped `?` in the text, so this one consumes an argument and shifts every later argument onto the wrong placeholder", ctx),
				"escape it as `\\?` (Go: \"\\\\?\", or `\\?` in a raw string) or remove it")
			continue
		}
		if op, ok := jsonbQuestionOperator(q, i); ok {
			add(bunLiteralQuestionRule, i,
				fmt.Sprintf("the jsonb %#q operator is read by bun as a placeholder: it consumes an argument, and every later argument lands on the wrong placeholder", op),
				fmt.Sprintf("escape the operator as `\\%s` (Go: \"\\\\%s\"), or use jsonb_exists / jsonb_exists_any / jsonb_exists_all", op, op))
		}
	}
	return findings
}

func digitsAt(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] >= '0' && s[i+n] <= '9' {
		n++
	}
	return n
}

// dollarQuoteTag returns the opening tag ($$ or $name$) at the start of s.
func dollarQuoteTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '$':
			return s[:j+1], true
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (j > 1 && c >= '0' && c <= '9'):
		default:
			return "", false
		}
	}
	return "", false
}

// jsonbQuestionOperator reports whether the unescaped `?` at i (outside any
// string or comment) is the jsonb key-exists operator rather than a
// placeholder: `?|` and `?&`, or a `?` whose operand is a string literal
// (`payload ? 'key'`). A placeholder is not followed by either. `payload ?
// ?` (the key as an argument) is indistinguishable from two placeholders —
// `? ?` with an identifier and a keyword is common — so it is not judged.
func jsonbQuestionOperator(q string, i int) (string, bool) {
	if i+1 < len(q) && (q[i+1] == '|' || q[i+1] == '&') {
		return q[i : i+2], true
	}
	j := i + 1
	for j < len(q) && (q[j] == ' ' || q[j] == '\t' || q[j] == '\n' || q[j] == '\r') {
		j++
	}
	if j == i+1 || j >= len(q) {
		return "", false
	}
	if q[j] == '\'' {
		return "?", true
	}
	return "", false
}
