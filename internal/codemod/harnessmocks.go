package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/goexec"
)

// harnessmocks.go — `forge fix harness-mocks`, the v1.0.0 move of the test
// harness onto forge's contract mocks.
//
// A handler's helpers_gen_test.go used to synthesize a Stub<Field> for every
// interface-typed Deps field, even when forge already generated a
// Mock<Role> for that interface in a mock_gen.go. Now the harness installs
// the mock and declares no stub, so a test that built `&chat.StubPause{...}`
// stops compiling. The two doubles have the same shape — a <Method>Func
// field per method and an embedded contractkit.Recorder — so the rewrite is
// the type name alone (plus an import when the mock lives in the declaring
// package): a pure rename, which is why `forge project upgrade` runs it.

func init() {
	register(Codemod{
		Name:    "harness-mocks",
		Version: "v1.0.0",
		Short:   "Rename the harness's Stub<Field> doubles to the forge mocks that replace them (one double per interface since v1.0.0)",
		Long: `Point your handler tests at the forge mock the test harness now installs.

forge v1.0.0 stops synthesizing a Stub<Field> in helpers_gen_test.go for a Deps
field whose interface forge already mocks in a mock_gen.go — the declaring
package's (contract.go or a forge:deps deps.go), or the handler's own. The
harness installs that mock instead:

  Deps.Pause runs.PauseController   StubPause → runs.MockPauseController
  Deps.Narrow Narrow (deps.go)      StubNarrow → MockNarrow
  Deps.Estimates db.EstimateStore   StubEstimates → MockEstimateStore
                                    (the handler's Deps-discovered mock)

Both doubles have the same fields (<Method>Func, an embedded
contractkit.Recorder), so this renames the type in every test file of the
handler, qualifying it the way the file already reaches the package, and adds
the declaring package's import where a file lacks it. A test struct that embeds
a stub has its embedded field renamed with it (StubPause: → MockPauseController:,
w.StubPause → w.MockPauseController). Stub<Field> stays for
an interface no mock_gen.go has (a third-party or standard-library one).

One behaviour moves with the name: an unset method returns
contractkit.MockNotSet ("MockPauseController.PauseFunc not set") instead of
testkit.StubNotConfigured ("StubPause.PauseFunc not set: forge test stub not
configured ..."). Both are a non-nil error from every error-returning method;
a test that asserts on the old text is listed for you, not rewritten.`,
		Upgrade: true,
		Scoped:  true,
		Deprecated: "every project forge supports already uses the harness's forge mocks; " +
			"`forge project upgrade` runs this rewrite when a project still needs it",
		Run: runHarnessMocks,
	})
}

func runHarnessMocks(dir string, opts Options) (Report, error) {
	var rep Report
	renames, err := codegen.HarnessMockRenames(dir)
	if err != nil {
		return rep, err
	}
	if len(renames) == 0 {
		return rep, nil
	}
	module, err := codegen.GetModulePath(dir)
	if err != nil {
		return rep, fmt.Errorf("read the module path: %w", err)
	}
	byDir := map[string][]codegen.HarnessMockRename{}
	var dirs []string
	for _, r := range renames {
		if _, ok := byDir[r.Dir]; !ok {
			dirs = append(dirs, r.Dir)
		}
		byDir[r.Dir] = append(byDir[r.Dir], r)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if err := rewriteHarnessMockDir(dir, d, module, byDir[d], opts, &rep); err != nil {
			return rep, err
		}
	}
	if rep.Changed() {
		rep.Verify = []string{"forge generate", "go vet ./internal/handlers/...", "task test"}
	}
	return rep, nil
}

// rewriteHarnessMockDir rewrites one handler directory's test files.
func rewriteHarnessMockDir(root, rel, module string, renames []codegen.HarnessMockRename, opts Options, rep *Report) error {
	importPath := module + "/" + rel
	abs := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	byStub := map[string]codegen.HarnessMockRename{}
	for _, r := range renames {
		byStub[r.StubType] = r
	}
	type testFile struct {
		rel, path string
		src       []byte
		hf        *harnessFile
	}
	var files []testFile
	// embedded is, per package clause (the handler's own and its _test
	// package are two packages), every retired stub some struct in it
	// embeds: the embedded FIELD is named after the type, so its keys and
	// selectors are renamed with it. A struct in one file is used from
	// another, so this is gathered over the whole directory first.
	embedded := map[string]map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		p := filepath.Join(abs, e.Name())
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if isGenerated(src) {
			continue // helpers_gen_test.go: forge generate rewrites it
		}
		fileRel := path.Join(rel, e.Name())
		hf, err := parseHarnessFile(p, src, importPath)
		if err != nil {
			return fmt.Errorf("%s: %w", fileRel, err)
		}
		for stub := range hf.embeddedStubs(byStub) {
			if embedded[hf.f.Name.Name] == nil {
				embedded[hf.f.Name.Name] = map[string]bool{}
			}
			embedded[hf.f.Name.Name][stub] = true
		}
		files = append(files, testFile{fileRel, p, src, hf})
	}
	var parsed []*harnessFile
	for _, tf := range files {
		parsed = append(parsed, tf.hf)
	}
	fields := gatherHarnessFields(abs, importPath, parsed, byStub, embedded)
	for _, tf := range files {
		if !opts.Scope.Contains(tf.rel) {
			continue
		}
		out, lines, next, err := tf.hf.rewrite(tf.src, byStub, embedded[tf.hf.f.Name.Name], fields)
		if err != nil {
			return fmt.Errorf("%s: %w", tf.rel, err)
		}
		for _, n := range next {
			rep.Next = append(rep.Next, tf.rel+": "+n)
		}
		if out == nil {
			continue
		}
		for _, l := range lines {
			rep.Rewrites = append(rep.Rewrites, tf.rel+": "+l)
		}
		if opts.Write {
			if err := writeFile(tf.path, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// harnessFile is one parsed test file of a handler directory.
type harnessFile struct {
	filename string
	fset     *token.FileSet
	f        *ast.File
	// external is a `package <handler>_test` file, which reaches the
	// handler's exported names through its import of it (handlerName); an
	// in-package file names them bare.
	external    bool
	handlerName string
	imported    map[string]string // import path -> local name
	names       map[string]bool   // every local import name in the file
}

// parseHarnessFile parses one test file.
func parseHarnessFile(filename string, src []byte, importPath string) (*harnessFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	hf := &harnessFile{filename: filename, fset: fset, f: f, external: strings.HasSuffix(f.Name.Name, "_test"),
		imported: map[string]string{}, names: map[string]bool{}}
	for _, imp := range f.Imports {
		p, perr := strconv.Unquote(imp.Path.Value)
		if perr != nil {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		} else if p == importPath && hf.external {
			name = strings.TrimSuffix(f.Name.Name, "_test")
		}
		hf.imported[p] = name
		hf.names[name] = true
	}
	if hf.external {
		// "" when the file does not import the handler: it names none of
		// its stubs, but it can still reach the field of a struct another
		// file of its package embeds one in.
		hf.handlerName = hf.imported[importPath]
	}
	return hf, nil
}

// stubRef reports which retired stub the type expression e names, if any:
// `StubX` in the handler's package, `<handler>.StubX` from its _test
// package.
func (hf *harnessFile) stubRef(e ast.Expr, byStub map[string]codegen.HarnessMockRename) (string, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if _, ok := byStub[x.Name]; ok && !hf.external {
			return x.Name, true
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && hf.external && id.Name == hf.handlerName {
			if _, ok := byStub[x.Sel.Name]; ok {
				return x.Sel.Name, true
			}
		}
	}
	return "", false
}

// embeddedStubs is every retired stub a struct type in the file embeds.
func (hf *harnessFile) embeddedStubs(byStub map[string]codegen.HarnessMockRename) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(hf.f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if len(field.Names) > 0 {
				continue
			}
			t := field.Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			if stub, ok := hf.stubRef(t, byStub); ok {
				out[stub] = true
			}
		}
		return true
	})
	return out
}

// mockFieldName is the name of the field a struct embedding the mock gets.
func mockFieldName(r codegen.HarnessMockRename) string {
	return r.MockType[strings.LastIndex(r.MockType, ".")+1:]
}

// harnessEdit is one byte-range replacement.
type harnessEdit struct {
	start, end int
	text       string
}

// rewrite renames every reference to a retired stub in the file — the type,
// and, where the package embeds it (embedded), the field named after it. It
// returns the new source (nil when nothing changed), one line per rename,
// and what it could not decide.
func (hf *harnessFile) rewrite(src []byte, byStub map[string]codegen.HarnessMockRename, embedded map[string]bool, fields *harnessFields) ([]byte, []string, []string, error) {
	fset := hf.fset
	var edits []harnessEdit
	counts := map[string]int{}
	var toImport []codegen.ExtraImport
	var next []string
	spell := func(r codegen.HarnessMockRename) (string, bool) {
		if r.Import == nil {
			if hf.external {
				return hf.handlerName + "." + r.MockType, true
			}
			return r.MockType, true
		}
		mock := mockFieldName(r)
		if name, ok := hf.imported[r.Import.Path]; ok && name != "_" && name != "." {
			return name + "." + mock, true
		}
		if hf.names[r.Import.Alias] {
			next = append(next, fmt.Sprintf("%s → %s needs %q imported as %s, which this file already uses for another package — import it under another name and qualify the mock with it",
				r.StubType, r.MockType, r.Import.Path, r.Import.Alias))
			return "", false
		}
		toImport = append(toImport, *r.Import)
		hf.imported[r.Import.Path] = r.Import.Alias
		hf.names[r.Import.Alias] = true
		return r.Import.Alias + "." + mock, true
	}
	edit := func(n ast.Node, text, what string) {
		edits = append(edits, harnessEdit{fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset, text})
		counts[what]++
	}
	renameType := func(n ast.Expr, stub string) {
		r := byStub[stub]
		if text, ok := spell(r); ok {
			edit(n, text, stub+" → "+text)
		}
	}
	renameField := func(id *ast.Ident) {
		r := byStub[id.Name]
		field := mockFieldName(r)
		edit(id, field, "field "+id.Name+" → "+field)
	}
	// leave reports a reference spelled like the embedded stub's field
	// that the codemod cannot prove is that field.
	leave := func(id *ast.Ident) {
		r := byStub[id.Name]
		next = append(next, fmt.Sprintf("line %d: %s may be the embedded %s's field (now %s) or another struct's field of that name; rename it by hand if it is the embedded one",
			fset.Position(id.Pos()).Line, id.Name, id.Name, mockFieldName(r)))
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if stub, ok := hf.stubRef(x, byStub); ok {
				renameType(x, stub)
				return false
			}
			// The Sel of any other selector is a field, a method or another
			// package's name — never this package's stub type. It is the
			// embedded stub's FIELD only when the type checker resolves it
			// to that field; one it could not resolve is reported.
			if embedded[x.Sel.Name] {
				switch stub, ok := fields.resolved[posKey(hf.filename, fset.Position(x.Sel.Pos()).Offset)]; {
				case !ok:
					leave(x.Sel)
				case stub:
					renameField(x.Sel)
				}
			}
			ast.Inspect(x.X, visit)
			return false
		case *ast.CompositeLit:
			if x.Type != nil {
				ast.Inspect(x.Type, visit)
			}
			for _, elt := range x.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					ast.Inspect(elt, visit)
					continue
				}
				switch id, _ := kv.Key.(*ast.Ident); hf.keyAction(x, kv, byStub, embedded, fields) {
				case keyRename:
					renameField(id)
				case keyReport:
					leave(id)
				case keyVisit:
					ast.Inspect(kv.Key, visit)
				}
				ast.Inspect(kv.Value, visit)
			}
			return false
		case *ast.Ident:
			if stub, ok := hf.stubRef(x, byStub); ok {
				renameType(x, stub)
			}
		case *ast.BasicLit:
			next = append(next, hf.errorTextNotes(x, byStub)...)
		}
		return true
	}
	ast.Inspect(hf.f, visit)
	sort.Strings(next)
	if len(edits) == 0 {
		return nil, nil, next, nil
	}

	out, err := hf.fixImports(applyHarnessEdits(src, edits), toImport)
	if err != nil {
		return nil, nil, nil, err
	}
	return out, countLines(counts), next, nil
}

// countLines is one sorted "<rename> (<n>)" line per kind of rename.
func countLines(counts map[string]int) []string {
	var lines []string
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%s (%d)", k, n))
	}
	sort.Strings(lines)
	return lines
}

// errorTextNotes reports a string literal that expects a retired stub's
// unset-method error text, which the mock spells differently.
func (hf *harnessFile) errorTextNotes(x *ast.BasicLit, byStub map[string]codegen.HarnessMockRename) []string {
	if x.Kind != token.STRING {
		return nil
	}
	var notes []string
	for stub, r := range byStub {
		if strings.Contains(x.Value, stub+".") {
			notes = append(notes, fmt.Sprintf("line %d expects %s's error text; an unset mock method returns contractkit.MockNotSet (\"%s.<Method>Func not set\") — update the expected text",
				hf.fset.Position(x.Pos()).Line, stub, mockFieldName(r)))
		}
	}
	return notes
}

// applyHarnessEdits applies non-overlapping byte-range edits to src.
func applyHarnessEdits(src []byte, edits []harnessEdit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	return out
}

// fixImports adds each import to the rewritten src, removes every import the
// rewrite left unused (an external test whose only use of the handler was
// the stub it embedded), and gofmts the result.
func (hf *harnessFile) fixImports(src []byte, imports []codegen.ExtraImport) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, hf.filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, imp := range imports {
		name := imp.Alias
		if name == path.Base(imp.Path) {
			name = ""
		}
		astutil.AddNamedImport(fset, f, name, imp.Path)
	}
	before, after := importUses(hf.f), importUses(f)
	type unused struct{ name, path string }
	var drop []unused
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		local, ok := hf.imported[p]
		if !ok || local == "_" || local == "." || before[local] == 0 || after[local] > 0 {
			continue
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		drop = append(drop, unused{name, p})
	}
	for _, u := range drop {
		astutil.DeleteNamedImport(fset, f, u.name, u.path)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// importUses counts, per local name, the qualified identifiers (name.X) in f.
func importUses(f *ast.File) map[string]int {
	uses := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				uses[id.Name]++
			}
		}
		return true
	})
	return uses
}

// harnessFields is what the rewrite knows about struct fields spelled like a
// retired stub, gathered over a handler directory before any file changes.
type harnessFields struct {
	// embeds is, per "<package clause>.<type>" for every top-level struct
	// type of the directory's test files, the retired stubs it embeds.
	embeds map[string]map[string]bool
	// resolved is, per "<test file base name>:<offset>" of a selector's
	// field name or a struct literal's key spelled like an embedded stub,
	// whether the type checker resolved it to one of those embedded stub
	// fields (true) or to some other field (false). A name it could not
	// resolve is absent: the rewrite reports it rather than guess.
	resolved map[string]bool
}

// posKey names a position in one of the handler directory's files.
func posKey(filename string, offset int) string {
	return filepath.Base(filename) + ":" + strconv.Itoa(offset)
}

func gatherHarnessFields(abs, importPath string, files []*harnessFile, byStub map[string]codegen.HarnessMockRename, embedded map[string]map[string]bool) *harnessFields {
	hf := &harnessFields{embeds: map[string]map[string]bool{}, resolved: map[string]bool{}}
	// decls is where each embedded stub field is declared: the position
	// go/types gives the field's object.
	decls := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f.f, func(n ast.Node) bool {
			if st, ok := n.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					if len(field.Names) > 0 {
						continue
					}
					if _, isStub := f.stubRef(unstar(field.Type), byStub); isStub {
						decls[posKey(f.filename, f.fset.Position(field.Type.Pos()).Offset)] = true
						if id := embeddedIdent(field.Type); id != nil {
							decls[posKey(f.filename, f.fset.Position(id.Pos()).Offset)] = true
						}
					}
				}
			}
			return true
		})
		for _, decl := range f.f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if st, ok := ts.Type.(*ast.StructType); ok {
					hf.embeds[f.f.Name.Name+"."+ts.Name.Name] = f.structEmbeds(st, byStub)
				}
			}
		}
	}
	names := map[string]bool{}
	for _, set := range embedded {
		for name := range set {
			names[name] = true
		}
	}
	if len(names) > 0 {
		hf.resolve(abs, importPath, names, decls)
	}
	return hf
}

// resolve type-checks the handler's test packages and records, for every
// selector and struct literal key named like an embedded stub, whether it is
// that embedded field. Only the field's object can say so: a selector's
// receiver may be any struct, a dependency's included. A package that does
// not load leaves every such name unresolved, so each is reported.
func (hf *harnessFields) resolve(abs, importPath string, names, decls map[string]bool) {
	cfg := &packages.Config{
		// Type information survives errors: once forge generate retires
		// the stubs, the tests no longer compile, and the fields' objects
		// are still what decides.
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:   abs,
		Env:   goexec.Env(),
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return
	}
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		stubField := func(obj types.Object) bool {
			v, ok := obj.(*types.Var)
			if !ok || !v.IsField() || !v.Embedded() || v.Pkg() == nil {
				return false
			}
			if path := v.Pkg().Path(); path != importPath && path != importPath+"_test" {
				return false
			}
			at := p.Fset.Position(v.Pos())
			return at.IsValid() && decls[posKey(at.Filename, at.Offset)]
		}
		for _, file := range p.Syntax {
			filename := p.Fset.Position(file.Pos()).Filename
			if filepath.Dir(filename) != abs && !sameDir(filepath.Dir(filename), abs) {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if !names[x.Sel.Name] {
						return true
					}
					if sel := p.TypesInfo.Selections[x]; sel != nil && sel.Kind() == types.FieldVal {
						hf.resolved[posKey(filename, p.Fset.Position(x.Sel.Pos()).Offset)] = stubField(sel.Obj())
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok && names[id.Name] {
						if obj := p.TypesInfo.Uses[id]; obj != nil {
							hf.resolved[posKey(filename, p.Fset.Position(id.Pos()).Offset)] = stubField(obj)
						}
					}
				}
				return true
			})
		}
	}
}

// sameDir reports whether a and b name one directory (through symlinks).
func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// embeddedIdent is the identifier naming an embedded field of type e.
func embeddedIdent(e ast.Expr) *ast.Ident {
	switch t := unstar(e).(type) {
	case *ast.Ident:
		return t
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedIdent(t.X)
	case *ast.IndexListExpr:
		return embeddedIdent(t.X)
	}
	return nil
}

// structEmbeds is the retired stubs st embeds.
func (hf *harnessFile) structEmbeds(st *ast.StructType, byStub map[string]codegen.HarnessMockRename) map[string]bool {
	out := map[string]bool{}
	for _, field := range st.Fields.List {
		if len(field.Names) > 0 {
			continue
		}
		if stub, ok := hf.stubRef(unstar(field.Type), byStub); ok {
			out[stub] = true
		}
	}
	return out
}

// litEmbeds is the retired stubs the struct type of lit embeds; known is
// false when the literal's type cannot be read from the syntax (an elided
// type, or a type the directory's test files do not declare).
func (hf *harnessFile) litEmbeds(lit *ast.CompositeLit, byStub map[string]codegen.HarnessMockRename, fields *harnessFields) (map[string]bool, bool) {
	switch t := lit.Type.(type) {
	case *ast.StructType:
		return hf.structEmbeds(t, byStub), true
	case *ast.Ident:
		// A top-level struct type of this package's test files. Any other
		// (a function-local type, a non-test type) is not read here.
		e, ok := fields.embeds[hf.f.Name.Name+"."+t.Name]
		return e, ok
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return nil, false
		}
		if hf.external && id.Name == hf.handlerName {
			// The handler's test files are compiled into it; its other
			// types cannot embed a test-only stub.
			return fields.embeds[strings.TrimSuffix(hf.f.Name.Name, "_test")+"."+t.Sel.Name], true
		}
		// Another package's type cannot embed this package's test stub.
		return nil, true
	case *ast.MapType, *ast.ArrayType:
		return nil, true
	}
	return nil, false
}

// keysAreFields reports whether lit's keys can be field names: not a map or
// slice literal, whose keys are values.
func (hf *harnessFile) keysAreFields(lit *ast.CompositeLit) bool {
	switch lit.Type.(type) {
	case *ast.MapType, *ast.ArrayType:
		return false
	}
	return true
}

func unstar(e ast.Expr) ast.Expr {
	if star, ok := e.(*ast.StarExpr); ok {
		return star.X
	}
	return e
}

// keyAction is what the rewrite does with one key of a composite literal.
type keyAction int

const (
	keyVisit  keyAction = iota // not a field spelled like an embedded stub
	keyRename                  // the embedded stub's field
	keyKeep                    // another struct's field of the same name
	keyReport                  // cannot tell which without types
)

// keyAction decides kv's key: a key names a field of the literal's own type,
// so it is renamed only when that type is the one embedding the stub.
func (hf *harnessFile) keyAction(lit *ast.CompositeLit, kv *ast.KeyValueExpr, byStub map[string]codegen.HarnessMockRename, embedded map[string]bool, fields *harnessFields) keyAction {
	id, isIdent := kv.Key.(*ast.Ident)
	if !isIdent || !embedded[id.Name] || !hf.keysAreFields(lit) {
		return keyVisit
	}
	if stub, ok := fields.resolved[posKey(hf.filename, hf.fset.Position(id.Pos()).Offset)]; ok {
		if stub {
			return keyRename
		}
		return keyKeep
	}
	// Unresolved (the package did not load): the literal's type, read
	// from the syntax, decides when it can.
	embeds, known := hf.litEmbeds(lit, byStub, fields)
	switch {
	case known && embeds[id.Name]:
		return keyRename
	case known:
		return keyKeep
	}
	return keyReport
}
