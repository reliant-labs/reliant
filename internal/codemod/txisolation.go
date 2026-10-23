package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// txisolation.go — `forge fix tx-isolation`, the v0.1.47 move to explicit
// transaction isolation. pkg/orm had two opposite defaults: RunTx,
// RunTxWithOptions and BeginDetached began SERIALIZABLE (RunTx retrying
// 40001/40P01 up to seven times) when the level was left unset, while
// Client.BeginTx and RunTransaction with nil options began at the database's
// default, READ COMMITTED, once. Porting code from one to the other changed
// its isolation without a diff line saying so. Every opener now takes a
// policy that must name its level and its attempt budget: orm.Serializable,
// orm.ReadCommitted, orm.RepeatableRead, or an orm.TxOptions refined from one.
//
// The rewrite is behaviour-preserving by construction: it spells out exactly
// what each call did before and NEVER chooses a different level. Whether a
// SERIALIZABLE site that takes a lock should be READ COMMITTED is a decision
// for a person; forgeconv-tx-lock-under-serializable names those sites. That
// is why `forge project upgrade` runs it on its own.
//
// It is type-aware: only calls whose receiver is a pkg/orm handle — *Client,
// *Tx, orm.Context, or a type that embeds one — are rewritten, so a
// project's own RunTx (reliant's Repo) or database/sql's BeginTx is left
// alone. It reads the types from a load of the project that tolerates errors,
// so it works both before the forge pin moves and after (when the old calls
// no longer compile).

const (
	ormPath = "github.com/reliant-labs/forge/pkg/orm"
	sqlPath = "database/sql"
)

// txTokens pre-filter the files worth type-checking.
var txTokens = [][]byte{
	[]byte("RunTx"), []byte("RunTransaction"), []byte("BeginDetached"),
	[]byte("BeginTx"), []byte("TxOptions"), []byte("IsolationDefault"),
}

func init() {
	register(Codemod{
		Name:    "tx-isolation",
		Version: "v0.1.47",
		Short:   "Spell out every pkg/orm transaction's isolation level and attempt budget (no default since v0.1.47)",
		Long: `Make every pkg/orm transaction name its isolation, exactly as it ran before.

forge v0.1.47 removed the default isolation level. RunTx, RunTransaction and
BeginDetached now take a policy that names the level and carries the attempt
budget (orm.Serializable, orm.ReadCommitted, orm.RepeatableRead, refined with
WithMaxAttempts / WithReadOnly into an orm.TxOptions); an unset level is
rejected with orm.ErrInvalidTxOptions. RunTxWithOptions,
RunTransactionWithOptions, Client.BeginTx and orm.IsolationDefault are gone,
and orm.IsolationSerializable / IsolationReadCommitted / IsolationRepeatableRead
are orm.Serializable / ReadCommitted / RepeatableRead.

This rewrites every call on a pkg/orm handle to the explicit form that keeps
what it did:

  RunTx(ctx, fn)                      → RunTx(ctx, orm.Serializable, fn)          serializable, 7 attempts
  tx.RunTx(ctx, fn) on an *orm.Tx     → tx.Join(ctx, fn)                          a join: its own level
  RunTxWithOptions(ctx, opts, fn)     → RunTx(ctx, <opts spelled out>, fn)
  BeginDetached(ctx, orm.TxOptions{}) → BeginDetached(ctx, orm.Serializable.WithMaxAttempts(1))
  RunTransaction(ctx, fn)             → RunTransaction(ctx, orm.ReadCommitted, fn) read committed, 1 attempt
  RunTransactionWithOptions(ctx, nil, fn), BeginTx(ctx, nil)
                                      → … orm.ReadCommitted (BeginTx → BeginDetached)

and every orm.TxOptions literal that left the level or the attempt budget to a
default. It never picks a different level: it prints each site, and choosing
READ COMMITTED for a SERIALIZABLE transaction that takes a lock is yours
(forgeconv-tx-lock-under-serializable lists them). A shape it cannot decide —
options computed at run time — is listed with the edit to make. Generated
files, vendor/ and testdata/ are left alone. Re-running it changes nothing.`,
		Upgrade: true,
		Scoped:  true,
		Run:     runTxIsolation,
	})
}

func runTxIsolation(dir string, opts Options) (Report, error) {
	candidates := map[string]bool{}
	mentionsORM := false
	err := ownedGoFiles(dir, func(rel string, src []byte) error {
		if bytes.Contains(src, []byte(ormPath)) {
			mentionsORM = true
		}
		for _, tok := range txTokens {
			// Out of scope a file is still type-checked, never rewritten.
			if opts.Scope.Contains(rel) && bytes.Contains(src, tok) {
				candidates[filepath.Join(dir, filepath.FromSlash(rel))] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	if goModulePath(dir) == "" || len(candidates) == 0 || !mentionsORM {
		return Report{}, nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Report{}, err
	}
	// A pass rewrites what the types it loaded can see. Rewriting
	// Client.BeginTx (gone, so its result has no type once the pin moves)
	// gives the *Tx it returned a type, which can expose calls on that *Tx
	// to the next pass — so a pass that rewrote one runs again, over the
	// rewritten sources (an overlay: nothing is written until the end).
	var rep Report
	overlay := map[string][]byte{}
	seenNext := map[string]bool{}
	for pass := 0; pass < 3; pass++ {
		passRep, retyped, err := txPass(absDir, candidates, overlay)
		if err != nil {
			return Report{}, err
		}
		rep.Rewrites = append(rep.Rewrites, passRep.Rewrites...)
		for _, n := range passRep.Next {
			if !seenNext[n] {
				seenNext[n] = true
				rep.Next = append(rep.Next, n)
			}
		}
		if !retyped {
			break
		}
	}
	if opts.Write {
		paths := make([]string, 0, len(overlay))
		for path := range overlay {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if err := writeFile(path, overlay[path]); err != nil {
				return Report{}, err
			}
		}
	}
	if rep.Changed() {
		rep.Next = append(rep.Next,
			"Nothing changed level: each rewritten RunTx is still SERIALIZABLE with 7 attempts, each RunTransaction / BeginTx still READ COMMITTED with one. "+
				"Where a SERIALIZABLE body takes a lock (SELECT … FOR UPDATE, pg_advisory_xact_lock, LOCK TABLE), `forge lint --conventions` names it "+
				"(forgeconv-tx-lock-under-serializable): the waiting writer wakes into a stale snapshot and survives only by retrying — decide whether it should be orm.ReadCommitted.")
		rep.Verify = []string{"go build ./...", "go vet ./...", "forge lint --conventions"}
	}
	return rep, nil
}

// txPass loads the project — the overlay standing in for the files already
// rewritten — and rewrites every candidate file once, recording the result
// in overlay. retyped reports a rewrite that can change what the next load
// type-checks (a BeginTx, whose result was untyped).
func txPass(absDir string, candidates map[string]bool, overlay map[string][]byte) (rep Report, retyped bool, err error) {
	cfg := &packages.Config{
		// Project packages are type-checked from source and keep their
		// type information through errors — after the pin moves the old
		// calls do not compile, and the receivers' types are what matter.
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:     absDir,
		Env:     goexec.Env(),
		Tests:   true,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return Report{}, false, fmt.Errorf("load the project's packages: %w", err)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })

	done := map[string]bool{}
	for _, p := range pkgs {
		// pkg/orm is the definition, not a consumer: its own literals are
		// what the policies are built from.
		if p.TypesInfo == nil || p.Types == nil || p.Types.Path() == ormPath {
			continue
		}
		for _, file := range p.Syntax {
			path := p.Fset.File(file.Pos()).Name()
			if !candidates[path] || done[path] {
				continue
			}
			done[path] = true
			rel, err := filepath.Rel(absDir, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			src, ok := overlay[path]
			if !ok {
				if src, err = os.ReadFile(path); err != nil {
					return Report{}, false, err
				}
			}
			out, fileRep, beganTx, err := rewriteTxFile(p, file, src, rel)
			if err != nil {
				return Report{}, false, err
			}
			rep.Rewrites = append(rep.Rewrites, fileRep.Rewrites...)
			rep.Next = append(rep.Next, fileRep.Next...)
			if out != nil {
				overlay[path] = out
				retyped = retyped || beganTx
			}
		}
	}
	return rep, retyped, nil
}

func goModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// txEdit replaces src[start:end] with text.
type txEdit struct {
	start, end int
	text       string
}

// txRewriter rewrites one file.
type txRewriter struct {
	fset   *token.FileSet
	info   *types.Info
	file   *ast.File
	src    []byte
	rel    string
	prefix string // how the file names pkg/orm: "orm."

	edits   []txEdit
	handled map[*ast.CompositeLit]bool
	rep     Report
	usedORM bool
	beganTx bool
	impls   map[string]bool
}

// rewriteTxFile rewrites one file's src. out is nil when nothing changed;
// beganTx reports a rewritten Client.BeginTx.
func rewriteTxFile(p *packages.Package, file *ast.File, src []byte, rel string) (out []byte, rep Report, beganTx bool, err error) {
	if tf := p.Fset.File(file.Pos()); tf == nil || tf.Size() != len(src) {
		return nil, Report{}, false, fmt.Errorf("%s changed while it was being read; re-run", rel)
	}
	r := &txRewriter{
		fset: p.Fset, info: p.TypesInfo, file: file, src: src, rel: rel,
		handled: map[*ast.CompositeLit]bool{}, impls: map[string]bool{},
	}
	ormName, addImport := importName(file, ormPath, "orm"), false
	if ormName == "" {
		addImport = true
		ormName = "orm"
		if nameTaken(file, ormName) {
			ormName = "forgeorm"
		}
	}
	r.prefix = ormName + "."
	r.walk()
	if len(r.edits) == 0 {
		return nil, r.rep, false, nil
	}

	sort.Slice(r.edits, func(i, j int) bool { return r.edits[i].start > r.edits[j].start })
	out = append([]byte(nil), src...)
	for _, e := range r.edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	out, err = fixTxImports(rel, out, addImport && r.usedORM, ormName)
	if err != nil {
		return nil, Report{}, false, err
	}
	return out, r.rep, r.beganTx, nil
}

// fixTxImports adds the orm import when the rewrite introduced one, and drops
// database/sql when the rewrite removed its last use.
func fixTxImports(rel string, src []byte, addORM bool, ormName string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("re-parse %s after the rewrite: %w", rel, err)
	}
	if addORM {
		name := ""
		if ormName != "orm" {
			name = ormName
		}
		astutil.AddNamedImport(fset, file, name, ormPath)
	}
	if sqlName := importName(file, sqlPath, "sql"); sqlName != "" && len(packageRefs(file, sqlName, nil)) == 0 {
		name := ""
		if sqlName != "sql" {
			name = sqlName
		}
		astutil.DeleteNamedImport(fset, file, name, sqlPath)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("format %s: %w", rel, err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", rel, err)
	}
	return formatted, nil
}

func (r *txRewriter) off(pos token.Pos) int { return r.fset.Position(pos).Offset }

func (r *txRewriter) text(n ast.Node) string {
	return string(r.src[r.off(n.Pos()):r.off(n.End())])
}

func (r *txRewriter) site(n ast.Node) string {
	return fmt.Sprintf("%s:%d", r.rel, r.fset.Position(n.Pos()).Line)
}

func (r *txRewriter) replace(n ast.Node, text string) {
	r.edits = append(r.edits, txEdit{start: r.off(n.Pos()), end: r.off(n.End()), text: text})
}

// edited reports whether n overlaps a replacement already made.
func (r *txRewriter) edited(n ast.Node) bool {
	start, end := r.off(n.Pos()), r.off(n.End())
	for _, e := range r.edits {
		if e.end > e.start && start < e.end && e.start < end {
			return true
		}
	}
	return false
}

// renamedIsolation maps the pre-v0.1.47 Isolation constants to their names
// now. IsolationDefault meant SERIALIZABLE.
var renamedIsolation = map[string]string{
	"IsolationDefault": "Serializable", "IsolationSerializable": "Serializable",
	"IsolationRepeatableRead": "RepeatableRead", "IsolationReadCommitted": "ReadCommitted",
}

func (r *txRewriter) insertAfter(n ast.Node, text string) {
	at := r.off(n.End())
	r.edits = append(r.edits, txEdit{start: at, end: at, text: text})
}

func (r *txRewriter) orm(name string) string {
	r.usedORM = true
	return r.prefix + name
}

func (r *txRewriter) walk() {
	calls := map[*ast.SelectorExpr]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				calls[sel] = true
				r.call(n, sel)
			}
		case *ast.FuncDecl:
			r.implementation(n)
		}
		return true
	})
	// Method values (db.RunTx passed as a func), the literals no call
	// consumed, and the old Isolation constant names.
	ast.Inspect(r.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if newName, old := renamedIsolation[n.Sel.Name]; old && r.isORMPackage(n.X) && !r.edited(n) {
				r.replace(n.Sel, newName)
				r.usedORM = true
				r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s → %s", r.site(n), r.text(n), r.text(n.X)+"."+newName))
				return false
			}
			if !calls[n] && r.isOldMethodValue(n) {
				r.rep.Next = append(r.rep.Next, fmt.Sprintf("%s: %s is used as a value; its signature changed (it takes an orm isolation policy now) — rewrite the use by hand", r.site(n), r.text(n)))
			}
		case *ast.CompositeLit:
			if r.handled[n] || !r.isTxOptionsLit(n) {
				return true
			}
			r.handled[n] = true
			text, desc, note, ok := r.mapTxOptionsLit(n, false, true)
			if note != "" {
				r.rep.Next = append(r.rep.Next, note)
			}
			if !ok {
				return true // its old constant names are still renamed
			}
			r.replace(n, text)
			r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s → %s  [%s]", r.site(n), oneLine(r.text(n)), text, desc))
			return false
		}
		return true
	})
}

var txMethodNames = map[string]bool{
	"RunTx": true, "RunTxWithOptions": true, "RunTransaction": true,
	"RunTransactionWithOptions": true, "BeginTx": true, "BeginDetached": true,
}

func (r *txRewriter) call(call *ast.CallExpr, sel *ast.SelectorExpr) {
	name := sel.Sel.Name
	if !txMethodNames[name] || !r.isORMCall(call, sel) {
		return
	}
	recv := oneLine(r.text(sel.X))
	switch name {
	case "RunTx":
		if r.isTxHandle(sel.X) {
			// A join: the transaction keeps the level it was begun at, so
			// spelling one here would claim an isolation it may not have.
			r.replace(sel.Sel, "Join")
			r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s.RunTx(ctx, fn) → %s.Join(ctx, fn)  [a join: the transaction's own level]", r.site(call), recv, recv))
			return
		}
		r.insertAfter(call.Args[0], ", "+r.orm("Serializable"))
		r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s.RunTx(ctx, fn) → %s.RunTx(ctx, %s, fn)  [serializable, 7 attempts]", r.site(call), recv, recv, r.orm("Serializable")))
	case "RunTransaction":
		r.insertAfter(call.Args[0], ", "+r.orm("ReadCommitted"))
		r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s.RunTransaction(ctx, fn) → %s.RunTransaction(ctx, %s, fn)  [read committed, 1 attempt]", r.site(call), recv, recv, r.orm("ReadCommitted")))
	case "RunTxWithOptions", "BeginDetached":
		detached := name == "BeginDetached"
		target := "RunTx"
		if detached {
			target = "BeginDetached"
		}
		arg := call.Args[1]
		newArg, desc := "", ""
		if lit := r.txOptionsLit(arg); lit != nil {
			// Decided here, with the opener's semantics, rather than by
			// the literal pass below.
			r.handled[lit] = true
			text, d, note, ok := r.mapTxOptionsLit(lit, detached, false)
			if note != "" {
				r.rep.Next = append(r.rep.Next, note)
			}
			if ok {
				r.replace(arg, text)
				newArg, desc = text, d
			}
		} else if !detached {
			// RunTxWithOptions exists only in old code, so this is
			// reported once. (A BeginDetached handed a variable is
			// validated at run time, which names the call site.)
			r.rep.Next = append(r.rep.Next, fmt.Sprintf("%s: RunTx's options come from %s — every TxOptions it can hold must name an Isolation and MaxAttempts ≥ 1; a zero value used to mean SERIALIZABLE with 7 attempts and now fails with orm.ErrInvalidTxOptions", r.site(call), oneLine(r.text(arg))))
		}
		if !detached {
			r.replace(sel.Sel, "RunTx")
		} else if newArg == "" {
			return // BeginDetached already explicit: nothing rewritten
		}
		if newArg == "" {
			newArg, desc = oneLine(r.text(arg)), "options as given"
		}
		r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s.%s(ctx, %s%s → %s.%s(ctx, %s%s  [%s]", r.site(call), recv, name, oneLine(r.text(arg)), argsTail(call), recv, target, newArg, argsTail(call), desc))
	case "RunTransactionWithOptions", "BeginTx":
		target := "RunTransaction"
		if name == "BeginTx" {
			target = "BeginDetached"
		}
		text, desc, note, ok := r.mapSQLOptions(call.Args[1])
		if !ok {
			r.rep.Next = append(r.rep.Next, fmt.Sprintf("%s: %s.%s(ctx, %s, …) — %s; rewrite it to %s.%s(ctx, %s…) with the level it holds (nil was the database default, READ COMMITTED, one attempt)",
				r.site(call), recv, name, oneLine(r.text(call.Args[1])), note, recv, target, r.prefix))
			return
		}
		if note != "" {
			r.rep.Next = append(r.rep.Next, fmt.Sprintf("%s: %s", r.site(call), note))
		}
		r.replace(sel.Sel, target)
		r.replace(call.Args[1], text)
		r.beganTx = r.beganTx || name == "BeginTx"
		r.rep.Rewrites = append(r.rep.Rewrites, fmt.Sprintf("%s: %s.%s(ctx, %s%s → %s.%s(ctx, %s%s  [%s]", r.site(call), recv, name, oneLine(r.text(call.Args[1])), argsTail(call), recv, target, text, argsTail(call), desc))
	}
}

// isTxHandle reports whether x is statically an *orm.Tx — a transaction
// someone else began, which RunTx on it only joins.
func (r *txRewriter) isTxHandle(x ast.Expr) bool {
	tv, ok := r.info.Types[x]
	return ok && tv.Type != nil && namedIn(tv.Type, ormPath, "Tx")
}

// isORMCall reports whether call is one of pkg/orm's transaction openers in
// its OLD shape — the shape this codemod rewrites.
func (r *txRewriter) isORMCall(call *ast.CallExpr, sel *ast.SelectorExpr) bool {
	if id, ok := sel.X.(*ast.Ident); ok {
		if _, isPkg := r.info.Uses[id].(*types.PkgName); isPkg {
			return false
		}
	}
	tv, ok := r.info.Types[sel.X]
	if !ok || tv.Type == nil || !tv.IsValue() {
		return false
	}
	recv := tv.Type
	name := sel.Sel.Name
	fn, found := lookupMethod(recv, name)
	switch name {
	case "RunTx", "RunTransaction":
		if len(call.Args) != 2 || !found {
			return false
		}
		// orm's own method, before or after the pin moved — or a
		// project interface already updated to orm's new signature.
		return inORM(fn) || takesTxOptions(fn)
	case "BeginDetached":
		return len(call.Args) == 2 && found && inORM(fn)
	case "RunTxWithOptions", "RunTransactionWithOptions":
		if len(call.Args) != 3 {
			return false
		}
	case "BeginTx":
		if len(call.Args) != 2 {
			return false
		}
	}
	if found {
		return inORM(fn)
	}
	// Removed from orm: the receiver must reach orm's method set.
	return isORMType(recv)
}

// isOldMethodValue reports a method value (db.RunTx, not called) of one of
// orm's old-shape openers.
func (r *txRewriter) isOldMethodValue(sel *ast.SelectorExpr) bool {
	tv, ok := r.info.Types[sel.X]
	if !ok || tv.Type == nil || !tv.IsValue() {
		return false
	}
	switch sel.Sel.Name {
	case "RunTxWithOptions", "RunTransactionWithOptions", "BeginTx":
		fn, found := lookupMethod(tv.Type, sel.Sel.Name)
		if found {
			return inORM(fn)
		}
		return isORMType(tv.Type)
	case "RunTx", "RunTransaction":
		fn, found := lookupMethod(tv.Type, sel.Sel.Name)
		return found && inORM(fn) && fn.Type().(*types.Signature).Params().Len() == 2
	}
	return false
}

// isORMType reports whether t is a pkg/orm transaction handle, or embeds one.
func isORMType(t types.Type) bool {
	if namedIn(t, ormPath, "Client", "Tx", "Context") {
		return true
	}
	for _, m := range []string{"RunTxReadOnly", "BeginDetached", "Dialect"} {
		if fn, ok := lookupMethod(t, m); ok && inORM(fn) {
			return true
		}
	}
	return false
}

func lookupMethod(t types.Type, name string) (*types.Func, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
	fn, ok := obj.(*types.Func)
	return fn, ok
}

func inORM(fn *types.Func) bool { return fn.Pkg() != nil && fn.Pkg().Path() == ormPath }

// takesTxOptions reports whether fn has orm's new opener shape:
// (ctx, orm.TxPolicy, fn).
func takesTxOptions(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	return ok && sig.Params().Len() == 3 && namedIn(sig.Params().At(1).Type(), ormPath, "TxPolicy", "TxOptions")
}

func namedIn(t types.Type, path string, names ...string) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != path {
		return false
	}
	for _, name := range names {
		if n.Obj().Name() == name {
			return true
		}
	}
	return false
}

// txOptionsLit returns e as an orm.TxOptions composite literal, or nil.
func (r *txRewriter) txOptionsLit(e ast.Expr) *ast.CompositeLit {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok || !r.isTxOptionsLit(lit) {
		return nil
	}
	return lit
}

func (r *txRewriter) isTxOptionsLit(lit *ast.CompositeLit) bool {
	if t := r.info.TypeOf(lit); t != nil && namedIn(t, ormPath, "TxOptions") {
		return true
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "TxOptions" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := r.info.Uses[id].(*types.PkgName)
	return ok && pn.Imported().Path() == ormPath
}

// oldIsolationNames are the pre-v0.1.47 Isolation constants, by value.
// IsolationDefault (0) meant SERIALIZABLE; the other three kept their
// values under their new names.
var oldIsolationNames = map[string]int{
	"IsolationDefault": 0, "IsolationSerializable": oldSerializable,
	"IsolationRepeatableRead": oldRepeatableRead, "IsolationReadCommitted": oldReadCommitted,
}

// isORMPackage reports whether x names the imported pkg/orm.
func (r *txRewriter) isORMPackage(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := r.info.Uses[id].(*types.PkgName)
	return ok && pn.Imported().Path() == ormPath
}

// The old isolation enum: IsolationDefault (0) meant SERIALIZABLE.
const (
	oldSerializable   = 1
	oldRepeatableRead = 2
	oldReadCommitted  = 3
)

// mapTxOptionsLit spells lit out under the new rules, keeping what it meant:
// an unset level was SERIALIZABLE, an unset (≤ 0) MaxAttempts was 7, and
// BeginDetached ignored MaxAttempts (one attempt). ok is false when lit is
// already valid (nothing to do) or cannot be decided (note says why).
// typed is true where the literal must stay an orm.TxOptions (a variable, a
// field, a return value) rather than any policy an opener takes.
func (r *txRewriter) mapTxOptionsLit(lit *ast.CompositeLit, detached, typed bool) (text, desc, note string, ok bool) {
	level, levelKnown := 0, true
	readOnly, readOnlySrc := false, ""
	attempts, attemptsSrc := 0, ""
	for _, elt := range lit.Elts {
		kv, isKV := elt.(*ast.KeyValueExpr)
		key, isIdent := ast.Expr(nil), false
		if isKV {
			key = kv.Key
			_, isIdent = key.(*ast.Ident)
		}
		if !isKV || !isIdent {
			return "", "", fmt.Sprintf("%s: %s — positional orm.TxOptions fields; name them (Isolation, ReadOnly, MaxAttempts) and re-run", r.site(lit), oneLine(r.text(lit))), false
		}
		switch key.(*ast.Ident).Name {
		case "Isolation":
			level, levelKnown = r.isolationValue(kv.Value)
		case "ReadOnly":
			if b, isConst := r.boolValue(kv.Value); isConst {
				readOnly = b
			} else {
				readOnlySrc = oneLine(r.text(kv.Value))
			}
		case "MaxAttempts":
			if n, isConst := r.intValue(kv.Value); isConst {
				attempts = n
			} else {
				attemptsSrc = oneLine(r.text(kv.Value))
			}
		default:
			return "", "", fmt.Sprintf("%s: %s — unknown orm.TxOptions field; fix it by hand", r.site(lit), oneLine(r.text(lit))), false
		}
	}
	if !levelKnown {
		return "", "", fmt.Sprintf("%s: %s — the isolation is computed at run time; make sure it is never zero (unset is now rejected; it used to mean SERIALIZABLE)", r.site(lit), oneLine(r.text(lit))), false
	}
	if level < 0 || level > oldReadCommitted {
		return "", "", fmt.Sprintf("%s: %s — unknown isolation value %d; fix it by hand", r.site(lit), oneLine(r.text(lit)), level), false
	}
	attemptsValid := attemptsSrc == "" && attempts >= 1 && (!detached || attempts == 1)
	if level != 0 && attemptsValid {
		return "", "", "", false // already explicit
	}
	if level == 0 {
		level = oldSerializable
	}
	switch {
	case detached:
		if attemptsSrc != "" || attempts > 1 {
			note = fmt.Sprintf("%s: BeginDetached ignored MaxAttempts (%s) and ran once; it now requires 1, which the rewrite spells", r.site(lit), firstNonEmpty(attemptsSrc, fmt.Sprint(attempts)))
		}
		attempts, attemptsSrc = 1, ""
	case attemptsSrc != "":
		note = fmt.Sprintf("%s: MaxAttempts %s ≤ 0 used to mean %d attempts and is now rejected; make sure it is at least 1", r.site(lit), attemptsSrc, defaultTxMaxAttempts)
	case attempts <= 0:
		attempts = defaultTxMaxAttempts
	}
	text = r.policyExpr(level, attempts, attemptsSrc, readOnly, readOnlySrc, typed)
	desc = policyDesc(level, attempts, attemptsSrc, readOnly, readOnlySrc)
	return text, desc, note, true
}

// defaultTxMaxAttempts is orm.DefaultTxMaxAttempts.
const defaultTxMaxAttempts = 7

var levelVars = map[int]string{oldSerializable: "Serializable", oldRepeatableRead: "RepeatableRead", oldReadCommitted: "ReadCommitted"}

var levelNames = map[int]string{oldSerializable: "serializable", oldRepeatableRead: "repeatable read", oldReadCommitted: "read committed"}

// policyExpr is the shortest spelling of a policy: the level's var, with
// WithMaxAttempts when the budget differs from the var's own and
// WithReadOnly when it reads only.
func (r *txRewriter) policyExpr(level, attempts int, attemptsSrc string, readOnly bool, readOnlySrc string, typed bool) string {
	n := attemptsSrc
	if n == "" {
		n = fmt.Sprint(attempts)
		if attempts == defaultTxMaxAttempts {
			n = r.orm("DefaultTxMaxAttempts")
		}
	}
	if readOnlySrc != "" {
		return fmt.Sprintf("%s{Isolation: %s, ReadOnly: %s, MaxAttempts: %s}", r.orm("TxOptions"), r.orm(levelVars[level]), readOnlySrc, n)
	}
	s := r.orm(levelVars[level])
	if readOnly {
		s += ".WithReadOnly()"
	}
	own := defaultTxMaxAttempts
	if level == oldReadCommitted {
		own = 1
	}
	switch {
	case attemptsSrc != "" || attempts != own:
		s += ".WithMaxAttempts(" + n + ")"
	case typed && !readOnly:
		// A bare level is an orm.Isolation; where an orm.TxOptions is
		// declared, spell the struct.
		return fmt.Sprintf("%s{Isolation: %s, MaxAttempts: %s}", r.orm("TxOptions"), r.orm(levelVars[level]), n)
	}
	return s
}

func policyDesc(level, attempts int, attemptsSrc string, readOnly bool, readOnlySrc string) string {
	s := levelNames[level]
	switch {
	case readOnlySrc != "":
		s += " read only if " + readOnlySrc
	case readOnly:
		s += " read only"
		if level == oldSerializable {
			s += " deferrable"
		}
	}
	switch {
	case attemptsSrc != "":
		return s + ", " + attemptsSrc + " attempts"
	case attempts == 1:
		return s + ", 1 attempt"
	}
	return fmt.Sprintf("%s, %d attempts", s, attempts)
}

// The database/sql isolation levels pkg/orm can express.
var sqlLevels = map[int64]int{
	0: oldReadCommitted, // LevelDefault: the database's default, READ COMMITTED
	2: oldReadCommitted, // LevelReadCommitted
	4: oldRepeatableRead,
	6: oldSerializable,
}

// mapSQLOptions spells the *sql.TxOptions RunTransactionWithOptions and
// BeginTx took as orm.TxOptions. Both ran once.
func (r *txRewriter) mapSQLOptions(e ast.Expr) (text, desc, note string, ok bool) {
	if tv, has := r.info.Types[e]; has && tv.IsNil() {
		return r.orm("ReadCommitted"), "read committed, 1 attempt", "", true
	}
	u, isAddr := e.(*ast.UnaryExpr)
	if !isAddr || u.Op != token.AND {
		return "", "", "the options are computed at run time", false
	}
	lit, isLit := u.X.(*ast.CompositeLit)
	if !isLit || !namedIn(r.info.TypeOf(lit), sqlPath, "TxOptions") {
		return "", "", "the options are computed at run time", false
	}
	level, readOnly := oldReadCommitted, false
	for _, elt := range lit.Elts {
		kv, isKV := elt.(*ast.KeyValueExpr)
		if !isKV {
			return "", "", "positional sql.TxOptions fields", false
		}
		key, _ := kv.Key.(*ast.Ident)
		switch {
		case key != nil && key.Name == "Isolation":
			tv, has := r.info.Types[kv.Value]
			if !has || tv.Value == nil || tv.Value.Kind() != constant.Int {
				return "", "", "the isolation level is computed at run time", false
			}
			v, _ := constant.Int64Val(tv.Value)
			l, known := sqlLevels[v]
			if !known {
				return "", "", fmt.Sprintf("isolation level %s has no pkg/orm spelling", oneLine(r.text(kv.Value))), false
			}
			level = l
		case key != nil && key.Name == "ReadOnly":
			b, isConst := r.boolValue(kv.Value)
			if !isConst {
				return "", "", "ReadOnly is computed at run time", false
			}
			readOnly = b
		default:
			return "", "", "unknown sql.TxOptions field", false
		}
	}
	if readOnly && level == oldSerializable {
		note = "SERIALIZABLE READ ONLY now also runs DEFERRABLE (no predicate locks, cannot fail 40001; it may wait briefly at its first statement)"
	}
	return r.policyExpr(level, 1, "", readOnly, "", false), policyDesc(level, 1, "", readOnly, ""), note, true
}

// isolationValue is the old-enum value of an orm.Isolation expression.
func (r *txRewriter) isolationValue(e ast.Expr) (int, bool) {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if v, old := oldIsolationNames[x.Sel.Name]; old && r.isORMPackage(x.X) {
			return v, true
		}
	case *ast.Ident:
		if v, old := oldIsolationNames[x.Name]; old {
			return v, true
		}
	}
	tv, ok := r.info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Int || !namedIn(tv.Type, ormPath, "Isolation") {
		return 0, false
	}
	v, _ := constant.Int64Val(tv.Value)
	return int(v), true
}

func (r *txRewriter) boolValue(e ast.Expr) (bool, bool) {
	tv, ok := r.info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Bool {
		return false, false
	}
	return constant.BoolVal(tv.Value), true
}

func (r *txRewriter) intValue(e ast.Expr) (int, bool) {
	tv, ok := r.info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Int {
		return 0, false
	}
	v, _ := constant.Int64Val(tv.Value)
	return int(v), true
}

// implementation reports a project type that implements orm.Context with
// the old transaction methods (a test double): its methods must change, and
// which body each one needs is the author's call.
func (r *txRewriter) implementation(fd *ast.FuncDecl) {
	if fd.Recv == nil || fd.Name.Name != "RunTx" || fd.Type.Params.NumFields() != 2 {
		return
	}
	fn, ok := r.info.Defs[fd.Name].(*types.Func)
	if !ok {
		return
	}
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return
	}
	dialect, ok := lookupMethod(recv.Type(), "Dialect")
	if !ok {
		return
	}
	sig := dialect.Type().(*types.Signature)
	if sig.Results().Len() != 1 || !namedIn(sig.Results().At(0).Type(), ormPath, "Dialect") {
		return
	}
	name := types.TypeString(recv.Type(), func(*types.Package) string { return "" })
	if r.impls[name] {
		return
	}
	r.impls[name] = true
	r.rep.Next = append(r.rep.Next, fmt.Sprintf("%s: %s implements orm.Context with the old transaction methods — give it RunTx(ctx, policy orm.TxPolicy, fn), RunTransaction(ctx, policy orm.TxPolicy, fn) and BeginDetached(ctx, policy orm.TxPolicy), and delete RunTxWithOptions", r.site(fd), name))
}

// argsTail closes a call's rendering: ", fn)" when a function follows the
// options, ")" when they are the last argument.
func argsTail(call *ast.CallExpr) string {
	if len(call.Args) > 2 {
		return ", fn)"
	}
	return ")"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
