// File: internal/linter/forgeconv/tx_lock_under_serializable.go
//
// forgeconv-tx-lock-under-serializable: a SERIALIZABLE (or REPEATABLE READ)
// transaction whose body takes a lock — SELECT … FOR UPDATE / FOR NO KEY
// UPDATE, bun's .For("UPDATE"), pg_advisory_xact_lock / pg_advisory_lock,
// or LOCK TABLE.
//
// The two correctness arguments a transaction can make are exclusive:
//
//   - SERIALIZABLE: the database proves the transactions equivalent to a
//     serial order and aborts (40001) the one it cannot place. Correct with
//     no locks, provided the aborted one is retried.
//   - READ COMMITTED + a lock: the lock makes writers take turns, and each
//     statement after the wait sees the row the previous holder committed.
//
// Combining them is the trap. Under SERIALIZABLE (and REPEATABLE READ) the
// snapshot is taken at the transaction's first statement — before the lock
// wait — so the writer that waited wakes into a stale snapshot, and writing
// the row its predecessor changed fails with 40001. It survives only by being
// retried: every contended wait costs a full rollback and re-run, and with a
// retry budget of one (BeginDetached, MaxAttempts 1) it simply fails. That is
// how a port that moved a FOR UPDATE helper from READ COMMITTED onto the
// SERIALIZABLE default dropped updates under contention.
//
// The analysis is syntactic: the level is read from the options argument as
// written (orm.Serializable / orm.RepeatableRead and their With… variants,
// or an orm.TxOptions literal whose Isolation is one of them), and the
// lock from the SQL string literals (and
// the string constants they name) in the transaction's body — the closure
// passed to RunTx / RunTransaction, the package function named there, or,
// for BeginDetached, the rest of the function that opened it. A lock taken
// inside a helper the body calls is not seen. A read-only transaction
// (WithReadOnly) is not judged: it cannot write the row it locked.
//
// The openers judged are pkg/orm's (RunTx, RunTransaction, BeginDetached)
// and every one the project declares. A project's own opener — a wrapper
// that takes an orm.TxPolicy and runs the transaction itself, typically
// resolving it with orm.ResolveTxPolicy — says so on its declaration:
//
//	//forge:tx-opener level-arg=1
//	func (r *Repo) RunTx(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error
//
// level-arg is the 0-based index of the call argument that names the policy
// (ctx is argument 0; a method's receiver is not counted). body-arg, when
// given, is the index of the func argument that is the transaction's body;
// it defaults to the last func-typed parameter, and an opener with none is
// detached — like BeginDetached, its transaction runs for the rest of the
// function that opened it. Calls are matched by name and arity, project
// wide, as pkg/orm's are. A marker that does not parse, or sits anywhere but
// on a func declaration, is an error: an ignored opener would leave its
// calls unjudged without a word.
//
// Warning by default; an error under `forge lint --strict`.

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

const txLockUnderSerializableRule = "forgeconv-tx-lock-under-serializable"

const ormImportPath = "github.com/reliant-labs/forge/pkg/orm"

// sqlLockPatterns name the locks in SQL text, each with how the finding
// spells it.
var sqlLockPatterns = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`(?i)\bFOR\s+(NO\s+KEY\s+)?UPDATE\b`), "SELECT … FOR UPDATE"},
	{regexp.MustCompile(`(?i)\bpg_(try_)?advisory_(xact_)?lock\s*\(`), "pg_advisory_xact_lock"},
	{regexp.MustCompile(`(?i)\bLOCK\s+TABLE\b`), "LOCK TABLE"},
}

// bunForLock is the argument of a bun builder's .For("…") that locks rows
// for writing.
var bunForLock = regexp.MustCompile(`(?i)^\s*(NO\s+KEY\s+)?UPDATE\b`)

// txOpener is one transaction opener the lint judges, by the shape of its
// calls: how many arguments they pass, which names the policy and which is
// the body (-1: detached — the transaction runs for the rest of the function
// that opened it). A variadic opener's calls pass at least params-1
// arguments.
type txOpener struct {
	params   int
	variadic bool
	levelArg int
	bodyArg  int
}

func (o txOpener) matches(args int) bool {
	if o.variadic {
		return args >= o.params-1
	}
	return args == o.params
}

// ormTxOpeners are pkg/orm's openers, judged in every project.
var ormTxOpeners = map[string][]txOpener{
	"RunTx":          {{params: 3, levelArg: 1, bodyArg: 2}},
	"RunTransaction": {{params: 3, levelArg: 1, bodyArg: 2}},
	"BeginDetached":  {{params: 2, levelArg: 1, bodyArg: -1}},
}

// txOpenerForm is the documented spelling of the opener marker.
const txOpenerForm = "//forge:tx-opener level-arg=<n> [body-arg=<n>]"

// LintTxLockUnderSerializable walks rootDir for Go files and reports
// SERIALIZABLE transactions that take a lock — through pkg/orm's openers and
// every one declared with //forge:tx-opener. Generated files (gen/,
// *_gen.go), vendor/, node_modules/, testdata/ and pkg/orm itself are
// skipped. Findings are in deterministic order.
func LintTxLockUnderSerializable(rootDir string, opts LintOptions) (Result, error) {
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

	type parsed struct {
		path string
		file *ast.File
	}
	fset := token.NewFileSet()
	byDir := map[string][]parsed{}
	var dirs []string
	var declaring []parsed
	for _, fp := range files {
		src, rerr := os.ReadFile(fp)
		if rerr != nil {
			continue
		}
		namesPolicy := strings.Contains(string(src), ormImportPath)
		declares := strings.Contains(string(src), codegen.TxOpenerMarker)
		if !namesPolicy && !declares {
			continue // names no orm policy and declares no opener
		}
		file, perr := parser.ParseFile(fset, fp, src, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue // the Go toolchain reports parse errors
		}
		if declares {
			declaring = append(declaring, parsed{fp, file})
		}
		if !namesPolicy {
			continue
		}
		dir := filepath.Dir(fp)
		if _, seen := byDir[dir]; !seen {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], parsed{fp, file})
	}

	var result Result
	// The openers the project declares, beside pkg/orm's.
	openers := map[string][]txOpener{}
	for _, pf := range declaring {
		rel, relErr := filepath.Rel(rootDir, pf.path)
		if relErr != nil {
			rel = pf.path
		}
		declared, bad := declaredTxOpeners(fset, pf.file, filepath.ToSlash(rel))
		for name, o := range declared {
			openers[name] = append(openers[name], o...)
		}
		result.Findings = append(result.Findings, bad...)
	}

	severity := SeverityWarning
	if opts.Strict {
		severity = SeverityError
	}
	for _, dir := range dirs {
		consts := map[string]map[string]ast.Expr{}
		funcs := map[string]map[string]*ast.FuncDecl{}
		for _, pf := range byDir[dir] {
			pkg := pf.file.Name.Name
			if consts[pkg] == nil {
				consts[pkg] = map[string]ast.Expr{}
				funcs[pkg] = map[string]*ast.FuncDecl{}
			}
			collectStringDecls(pf.file.Decls, consts[pkg])
			for _, d := range pf.file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
					funcs[pkg][fd.Name.Name] = fd
				}
			}
		}
		for _, pf := range byDir[dir] {
			ormName := importName(pf.file, ormImportPath)
			if ormName == "" {
				continue // pkg/orm itself, or a file that names no policy
			}
			rel, relErr := filepath.Rel(rootDir, pf.path)
			if relErr != nil {
				rel = pf.path
			}
			l := txLockLint{
				fset: fset, rel: filepath.ToSlash(rel), orm: ormName, severity: severity,
				consts: consts[pf.file.Name.Name], funcs: funcs[pf.file.Name.Name], openers: openers,
			}
			result.Findings = append(result.Findings, l.file(pf.file)...)
		}
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return result, nil
}

type txLockLint struct {
	fset     *token.FileSet
	rel      string
	orm      string
	severity Severity
	consts   map[string]ast.Expr
	funcs    map[string]*ast.FuncDecl
	// openers are the project's declared openers, by name.
	openers map[string][]txOpener
}

// declaredTxOpeners reads the //forge:tx-opener markers in file: the openers
// its func declarations declare, by name, and a finding for every marker it
// cannot read — one that does not parse, or one on anything but a func
// declaration.
func declaredTxOpeners(fset *token.FileSet, file *ast.File, rel string) (map[string][]txOpener, []Finding) {
	out := map[string][]txOpener{}
	var bad []Finding
	report := func(c *ast.Comment, problem string) {
		bad = append(bad, Finding{
			Rule:        txLockUnderSerializableRule,
			Severity:    SeverityError,
			File:        rel,
			Line:        fset.Position(c.Pos()).Line,
			Message:     fmt.Sprintf("//%s %s, so forge ignores it and the calls it declares go unjudged", codegen.TxOpenerMarker, problem),
			Remediation: fmt.Sprintf("write %#q on the opener's func declaration: level-arg is the 0-based index of the call argument naming the orm.TxPolicy (ctx is 0, a receiver is not counted); body-arg, the func argument that is the transaction's body, defaults to the last func-typed parameter", txOpenerForm),
		})
	}
	onDecl := map[*ast.Comment]bool{}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Doc == nil {
			continue
		}
		for _, c := range fd.Doc.List {
			rest, ok := txOpenerMarkerRest(c.Text)
			if !ok {
				continue
			}
			onDecl[c] = true
			o, problem := parseTxOpener(rest, fd.Type)
			if problem != "" {
				report(c, fmt.Sprintf("on %s %s", fd.Name.Name, problem))
				continue
			}
			out[fd.Name.Name] = append(out[fd.Name.Name], o)
		}
	}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if _, ok := txOpenerMarkerRest(c.Text); ok && !onDecl[c] {
				report(c, "is not on a func declaration")
			}
		}
	}
	return out, bad
}

// txOpenerMarkerRest returns what follows the marker in a `//forge:tx-opener`
// or `// forge:tx-opener` comment.
func txOpenerMarkerRest(text string) (string, bool) {
	t, ok := strings.CutPrefix(text, "//")
	if !ok {
		return "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimLeft(t, " \t"), codegen.TxOpenerMarker)
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != ':') {
		return "", false // forge:tx-openerX is another word
	}
	return rest, true
}

// parseTxOpener reads a marker's arguments against the declaration's
// signature. It returns the opener, or what is wrong with the marker.
func parseTxOpener(rest string, sig *ast.FuncType) (txOpener, string) {
	var params []ast.Expr // one per parameter, its type
	if sig.Params != nil {
		for _, f := range sig.Params.List {
			for range max(1, len(f.Names)) {
				params = append(params, f.Type)
			}
		}
	}
	o := txOpener{params: len(params), levelArg: -1, bodyArg: -1}
	if n := len(params); n > 0 {
		_, o.variadic = params[n-1].(*ast.Ellipsis)
	}
	isFunc := func(i int) bool { _, ok := params[i].(*ast.FuncType); return ok }
	bodySet := false
	for _, field := range strings.Fields(rest) {
		key, val, ok := strings.Cut(field, "=")
		n, err := strconv.Atoi(val)
		if !ok || err != nil {
			return o, fmt.Sprintf("has %q, not key=<index>", field)
		}
		if n < 0 || n >= len(params) {
			return o, fmt.Sprintf("names argument %d, but it takes %d", n, len(params))
		}
		switch key {
		case "level-arg":
			o.levelArg = n
		case "body-arg":
			if !isFunc(n) {
				return o, fmt.Sprintf("names argument %d as the body, but it is not a func", n)
			}
			o.bodyArg, bodySet = n, true
		default:
			return o, fmt.Sprintf("has an unknown key %q (level-arg, body-arg)", key)
		}
	}
	if o.levelArg < 0 {
		return o, "names no level-arg"
	}
	if !bodySet {
		for i := len(params) - 1; i >= 0; i-- {
			if isFunc(i) {
				o.bodyArg = i
				break
			}
		}
	}
	if o.levelArg == o.bodyArg {
		return o, fmt.Sprintf("names argument %d as both the level and the body", o.levelArg)
	}
	return o, ""
}

func (l txLockLint) file(file *ast.File) []Finding {
	var findings []Finding
	// The bodies enclosing the current node, innermost last: BeginDetached
	// holds its transaction for the rest of the function that opened it.
	var bodies []*ast.BlockStmt
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				bodies = append(bodies, n.Body)
				ast.Inspect(n.Body, visit)
				bodies = bodies[:len(bodies)-1]
			}
			return false
		case *ast.FuncLit:
			bodies = append(bodies, n.Body)
			ast.Inspect(n.Body, visit)
			bodies = bodies[:len(bodies)-1]
			return false
		case *ast.CallExpr:
			if f, ok := l.call(n, bodies); ok {
				findings = append(findings, f)
			}
		}
		return true
	}
	ast.Inspect(file, visit)
	return findings
}

// opener returns the opener a call is, matched by its name and arity: a
// method call (x.RunTx(…)) for any opener, a bare call (runTx(…)) for one
// the project declares.
func (l txLockLint) opener(call *ast.CallExpr) (txOpener, bool) {
	var candidates []txOpener
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		candidates = slices.Concat(ormTxOpeners[fn.Sel.Name], l.openers[fn.Sel.Name])
	case *ast.Ident:
		candidates = l.openers[fn.Name] // pkg/orm's openers are methods
	}
	for _, o := range candidates {
		if o.matches(len(call.Args)) {
			return o, true
		}
	}
	return txOpener{}, false
}

// call judges one call: a SERIALIZABLE opener whose body takes a lock.
func (l txLockLint) call(call *ast.CallExpr, bodies []*ast.BlockStmt) (Finding, bool) {
	o, ok := l.opener(call)
	if !ok {
		return Finding{}, false
	}
	policy := call.Args[o.levelArg]
	var body []ast.Node
	if o.bodyArg >= 0 {
		if o.bodyArg >= len(call.Args) {
			return Finding{}, false
		}
		switch fn := call.Args[o.bodyArg].(type) {
		case *ast.FuncLit:
			body = []ast.Node{fn.Body}
		case *ast.Ident:
			if fd := l.funcs[fn.Name]; fd != nil {
				body = []ast.Node{fd.Body}
			}
		}
	} else {
		if len(bodies) == 0 {
			return Finding{}, false
		}
		// The rest of the opening function, from the call on.
		for _, st := range bodies[len(bodies)-1].List {
			if st.End() > call.Pos() {
				body = append(body, st)
			}
		}
	}
	level, ok := l.lockingLevel(policy)
	if !ok || len(body) == 0 {
		return Finding{}, false
	}
	lock, at, found := l.findLock(body, call)
	if !found {
		return Finding{}, false
	}
	return Finding{
		Rule:     txLockUnderSerializableRule,
		Severity: l.severity,
		File:     l.rel,
		Line:     l.fset.Position(call.Pos()).Line,
		Message: fmt.Sprintf("%s transaction (%s) takes a lock — %s at %s:%d. Under %s the writer that waits on the lock wakes into a snapshot taken before the wait and fails with 40001: it survives only by retrying, and a contended wait costs a full rollback and re-run",
			strings.ToUpper(level), oneLineExpr(l.fset, policy), lock, l.rel, l.fset.Position(at).Line, strings.ToUpper(level)),
		Remediation: fmt.Sprintf("if the lock is what makes this correct, run it at %s.ReadCommitted — the lock makes the writers take turns and each statement after the wait sees the committed row; if %s is what makes it correct, drop the lock. Keeping both on purpose: `// forge:lint-disable-next-line %s: <why>`",
			l.orm, strings.ToUpper(level), txLockUnderSerializableRule),
	}, true
}

// lockingLevel reports the level a policy expression names when it is one a
// lock fights: SERIALIZABLE or REPEATABLE READ, read-write.
func (l txLockLint) lockingLevel(e ast.Expr) (string, bool) {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
			continue
		case *ast.CallExpr:
			// orm.Serializable.WithMaxAttempts(n) / .WithReadOnly()
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return "", false
			}
			switch sel.Sel.Name {
			case "WithReadOnly":
				return "", false
			case "WithMaxAttempts":
				e = sel.X
				continue
			}
			return "", false
		case *ast.SelectorExpr:
			if !l.isORM(x.X) {
				return "", false
			}
			switch x.Sel.Name {
			case "Serializable":
				return "serializable", true
			case "RepeatableRead":
				return "repeatable read", true
			}
			return "", false
		case *ast.CompositeLit:
			sel, ok := x.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "TxOptions" || !l.isORM(sel.X) {
				return "", false
			}
			level := ""
			for _, elt := range x.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "ReadOnly":
					if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "false" {
						return "", false
					}
				case "Isolation":
					v, ok := kv.Value.(*ast.SelectorExpr)
					if !ok || !l.isORM(v.X) {
						return "", false
					}
					switch v.Sel.Name {
					case "Serializable":
						level = "serializable"
					case "RepeatableRead":
						level = "repeatable read"
					}
				}
			}
			return level, level != ""
		}
		return "", false
	}
}

func (l txLockLint) isORM(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == l.orm
}

// findLock returns the first lock the nodes take: a locking string literal
// (or a string constant named there), or bun's .For("UPDATE"). Nested
// transaction openers are their own finding and are not looked into.
func (l txLockLint) findLock(nodes []ast.Node, opener *ast.CallExpr) (string, token.Pos, bool) {
	var name string
	var at token.Pos
	check := func(s string, pos token.Pos) bool {
		for _, p := range sqlLockPatterns {
			if p.re.MatchString(s) {
				name, at = p.name, pos
				return true
			}
		}
		return false
	}
	for _, root := range nodes {
		ast.Inspect(root, func(n ast.Node) bool {
			if at.IsValid() {
				return false
			}
			switch x := n.(type) {
			case *ast.CallExpr:
				if x == opener {
					return true
				}
				switch l.lockCallKind(x) {
				case "opener":
					return false
				case "for-update":
					name, at = `.For("UPDATE")`, x.Pos()
					return false
				}
			case *ast.BasicLit:
				if s, ok := stringLit(x); ok && check(s, x.Pos()) {
					return false
				}
			case *ast.Ident:
				if e, ok := l.consts[x.Name]; ok {
					if s, ok := stringLit(e); ok && check(s, x.Pos()) {
						return false
					}
				}
			}
			return true
		})
		if at.IsValid() {
			return name, at, true
		}
	}
	return "", token.NoPos, false
}

// lockCallKind classifies a call met in a transaction body: "opener" for a
// nested transaction opener (its own finding, not looked into),
// "for-update" for bun's .For("UPDATE"), "" otherwise.
func (l txLockLint) lockCallKind(call *ast.CallExpr) string {
	if _, ok := l.opener(call); ok {
		return "opener"
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch sel.Sel.Name {
	case "RunTx", "RunTransaction", "BeginDetached", "RunTxReadOnly":
		return "opener"
	case "For":
		if len(call.Args) != 1 {
			return ""
		}
		if s, ok := stringLit(call.Args[0]); ok && bunForLock.MatchString(s) {
			return "for-update"
		}
	}
	return ""
}

// stringLit is the value of a string literal, or of a concatenation of them.
func stringLit(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := stringLit(x.X)
		b, ok2 := stringLit(x.Y)
		return a + b, ok1 && ok2
	case *ast.ParenExpr:
		return stringLit(x.X)
	}
	return "", false
}

func oneLineExpr(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	start, end := fset.Position(e.Pos()), fset.Position(e.End())
	if start.Filename == "" {
		return "?"
	}
	src, err := os.ReadFile(start.Filename)
	if err != nil || end.Offset > len(src) {
		return "?"
	}
	b.WriteString(strings.Join(strings.Fields(string(src[start.Offset:end.Offset])), " "))
	return b.String()
}
