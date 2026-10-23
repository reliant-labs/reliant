package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/codegen"
)

// harnessmocks.go — `forge fix harness-mocks`, the v0.1.47 move of the test
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
		Version: "v0.1.47",
		Short:   "Rename the harness's Stub<Field> doubles to the forge mocks that replace them (one double per interface since v0.1.47)",
		Long: `Point your handler tests at the forge mock the test harness now installs.

forge v0.1.47 stops synthesizing a Stub<Field> in helpers_gen_test.go for a Deps
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
		Run:     runHarnessMocks,
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
		if err := rewriteHarnessMockDir(dir, d, module+"/"+d, byDir[d], opts, &rep); err != nil {
			return rep, err
		}
	}
	if rep.Changed() {
		rep.Verify = []string{"forge generate", "go vet ./internal/handlers/...", "task test"}
	}
	return rep, nil
}

// rewriteHarnessMockDir rewrites one handler directory's test files.
func rewriteHarnessMockDir(root, rel, importPath string, renames []codegen.HarnessMockRename, opts Options, rep *Report) error {
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
		if hf == nil {
			continue // an external test file that does not import the handler
		}
		for stub := range hf.embeddedStubs(byStub) {
			if embedded[hf.f.Name.Name] == nil {
				embedded[hf.f.Name.Name] = map[string]bool{}
			}
			embedded[hf.f.Name.Name][stub] = true
		}
		files = append(files, testFile{fileRel, p, src, hf})
	}
	for _, tf := range files {
		if !opts.Scope.Contains(tf.rel) {
			continue
		}
		out, lines, next, err := tf.hf.rewrite(tf.src, byStub, embedded[tf.hf.f.Name.Name])
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

// parseHarnessFile parses one test file; nil for an external test file
// that does not import the handler, which can name none of its stubs.
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
		hf.handlerName = hf.imported[importPath]
		if hf.handlerName == "" {
			return nil, nil
		}
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
func (hf *harnessFile) rewrite(src []byte, byStub map[string]codegen.HarnessMockRename, embedded map[string]bool) ([]byte, []string, []string, error) {
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
			// embedded stub's FIELD when the package embeds one.
			if embedded[x.Sel.Name] {
				renameField(x.Sel)
			}
			ast.Inspect(x.X, visit)
			return false
		case *ast.KeyValueExpr:
			// A composite-literal key names a field, not a type.
			if id, ok := x.Key.(*ast.Ident); ok && embedded[id.Name] {
				renameField(id)
			} else if !ok {
				ast.Inspect(x.Key, visit)
			}
			ast.Inspect(x.Value, visit)
			return false
		case *ast.Ident:
			if stub, ok := hf.stubRef(x, byStub); ok {
				renameType(x, stub)
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			for stub, r := range byStub {
				if strings.Contains(x.Value, stub+".") {
					next = append(next, fmt.Sprintf("line %d expects %s's error text; an unset mock method returns contractkit.MockNotSet (\"%s.<Method>Func not set\") — update the expected text",
						fset.Position(x.Pos()).Line, stub, mockFieldName(r)))
				}
			}
		}
		return true
	}
	ast.Inspect(hf.f, visit)
	sort.Strings(next)
	if len(edits) == 0 {
		return nil, nil, next, nil
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	if len(toImport) > 0 {
		var err error
		if out, err = addHarnessImports(hf.filename, out, toImport); err != nil {
			return nil, nil, nil, err
		}
	}
	var lines []string
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%s (%d)", k, n))
	}
	sort.Strings(lines)
	return out, lines, next, nil
}

// addHarnessImports adds each import to src and gofmts the result.
func addHarnessImports(filename string, src []byte, imports []codegen.ExtraImport) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
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
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
