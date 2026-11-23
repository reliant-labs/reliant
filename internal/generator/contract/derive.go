// Contracts derived from a marked type (`//forge:contract` on a struct).
//
// A package used to state its contract twice: `type Service interface` in
// contract.go, and the same method set on the unexported struct that
// implements it. The contract linter already requires the implementation to
// export ONLY the contract's methods, so the interface is by construction the
// type's exported method set. Marking the type says so once:
//
//	//forge:contract
//	type service struct{ deps Deps }
//
// and `forge generate` writes contract_gen.go — `type Service interface`
// holding every exported method declared on the type (pointer and value
// receivers), each method's doc comment copied, plus
// `var _ Service = (*service)(nil)` — or a `service{}` value when every
// method has a value receiver, so the assertion names the method set that
// actually satisfies Service. mock_gen.go and middleware_gen.go are
// then generated from contract_gen.go exactly as from a hand-written
// contract.go: ParseContract reads it, together with the package's
// hand-declared dependency interfaces (contract.go, when one remains, and a
// `// forge:deps` deps.go), so their mocks keep being generated.
//
// Only DECLARED methods are part of the contract. Methods promoted from an
// embedded field are part of the type's method set but were never written
// as the package's API; the derived interface leaves them out, and the type
// still satisfies it.
package contract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/contractsrc"
	"github.com/reliant-labs/forge/internal/depsurface"
)

// DeriveOutcome is what SyncDerivedContract did to one package.
type DeriveOutcome int

const (
	// DeriveNone means the package has no marked type and no contract_gen.go.
	DeriveNone DeriveOutcome = iota
	// DeriveWritten means contract_gen.go was written from the marked type and
	// its bytes changed (or it was created).
	DeriveWritten
	// DeriveUnchanged means contract_gen.go is current; the render matched.
	DeriveUnchanged
	// DeriveRemoved means the marker is gone, so the stale contract_gen.go was.
	DeriveRemoved
	// DeriveKept means the marker is gone but contract_gen.go is the user's
	// (disowned, or bytes forge never certified), so it was left alone.
	DeriveKept
)

// SyncDerivedContract keeps dir's contract_gen.go in step with its
// `//forge:contract` type: (re)written while the type is marked, removed
// (journaled, so an aborted run restores it) once it is not. A package that
// declares its contract twice — a hand-written Service interface AND a
// marked type — is an error naming both.
//
// It runs before any other contract codegen reads the package, because
// mock_gen.go and middleware_gen.go are rendered from the file it writes. It
// works on syntax alone, so a freshly scaffolded package — which does not
// compile until contract_gen.go declares the Service its New returns — is
// derived like any other.
func SyncDerivedContract(dir string, opts Options) (DeriveOutcome, error) {
	derivedPath := filepath.Join(dir, contractsrc.DerivedFileName)
	if !contractsrc.HasMarkedType(dir) {
		return removeStaleDerived(dir, derivedPath, opts)
	}
	content, err := RenderDerivedContract(dir, opts.Resolver)
	if err != nil {
		return DeriveNone, err
	}
	if opts.ProjectRoot != "" {
		rel, relErr := filepath.Rel(opts.ProjectRoot, derivedPath)
		if relErr != nil {
			return DeriveNone, fmt.Errorf("compute project-relative path: %w", relErr)
		}
		out, werr := checksums.WriteDerivedFileOutcome(opts.ProjectRoot, rel, content, opts.Checksums, true)
		if werr != nil {
			return DeriveNone, fmt.Errorf("write %s: %w", rel, werr)
		}
		if !out.Changed {
			return DeriveUnchanged, nil
		}
		return DeriveWritten, nil
	}
	if prev, rerr := os.ReadFile(derivedPath); rerr == nil && bytes.Equal(prev, content) {
		return DeriveUnchanged, nil
	}
	if err := atomicfile.WriteFile(derivedPath, content, 0o644); err != nil {
		return DeriveNone, err
	}
	return DeriveWritten, nil
}

// removeStaleDerived deletes a contract_gen.go whose marker is gone, unless
// the path is the user's: disowned, or bytes forge never certified.
func removeStaleDerived(dir, derivedPath string, opts Options) (DeriveOutcome, error) {
	onDisk, err := os.ReadFile(derivedPath)
	if os.IsNotExist(err) {
		return DeriveNone, nil
	}
	if err != nil {
		return DeriveNone, fmt.Errorf("read %s: %w", derivedPath, err)
	}
	rel := displayPath(opts.ProjectRoot, derivedPath)
	if opts.Checksums.IsDisowned(rel) {
		return DeriveKept, nil
	}
	root := opts.ProjectRoot
	if root == "" {
		root = dir
	}
	if checksums.ClassifyPath(root, rel, onDisk) == checksums.NoMarker && !bytes.HasPrefix(onDisk, []byte(derivedHeaderFirstLine)) {
		return DeriveKept, nil
	}
	if err := removeJournaled(derivedPath); err != nil {
		return DeriveNone, err
	}
	return DeriveRemoved, nil
}

const derivedHeaderFirstLine = "// Code generated by forge. DO NOT EDIT."

// RenderDerivedContract renders contract_gen.go for the package in dir from
// its `//forge:contract` type. r names the package's unaliased imports; nil
// builds a Resolver over dir only when the Go convention cannot.
func RenderDerivedContract(dir string, r *Resolver) ([]byte, error) {
	d, err := deriveContract(dir, r)
	if err != nil {
		return nil, err
	}
	return d.render()
}

// derived is a parsed derivation, ready to render.
type derived struct {
	pkg      string
	typeName string
	typeFile string
	typeDoc  []string
	// valueLit is the composite literal syntax the type accepts (a struct,
	// array, slice or map), so a value-receiver assertion can spell `T{}`.
	valueLit bool
	methods  []derivedMethod
	imports  []ImportDef
}

type derivedMethod struct {
	name string
	doc  []string
	sig  string // "(ctx context.Context) error" — the FuncType minus `func`
	// pointer is true for a pointer receiver: then only *T's method set
	// holds the method.
	pointer bool
}

// CheckMarkedContract returns dir's `//forge:contract` type, refusing every
// declaration the derived interface could not express: no marked type, two
// of them, an alias, a generic type, a type named Service, and a package
// that ALSO hand-writes its contract interface. `forge lint` reports the same
// refusals before `forge generate` would hit them.
func CheckMarkedContract(dir string) (contractsrc.MarkedType, error) {
	rel := dir
	marked := contractsrc.MarkedTypes(dir)
	switch {
	case len(marked) == 0:
		return contractsrc.MarkedType{}, fmt.Errorf("%s: no //forge:contract type", rel)
	case len(marked) > 1:
		var where []string
		for _, m := range marked {
			where = append(where, fmt.Sprintf("%s (%s:%d)", m.Name, m.File, m.Line))
		}
		return contractsrc.MarkedType{}, fmt.Errorf("%s: %d types carry //forge:contract (%s); a package derives ONE Service contract — keep the marker on the type New returns", rel, len(marked), strings.Join(where, ", "))
	}
	m := marked[0]
	switch {
	case m.Alias:
		return contractsrc.MarkedType{}, fmt.Errorf("%s:%d: //forge:contract is on the alias %s, which declares no methods of its own; put the marker on the defined type", filepath.Join(rel, m.File), m.Line, m.Name)
	case m.Generic:
		return contractsrc.MarkedType{}, fmt.Errorf("%s:%d: //forge:contract is on the generic type %s; a generic type cannot implement the non-generic Service interface by itself — mark a non-generic type, or hand-write the interface in contract.go", filepath.Join(rel, m.File), m.Line, m.Name)
	case m.Constrained:
		return contractsrc.MarkedType{}, fmt.Errorf("%s:%d: //forge:contract is on %s in %s, which build constraints leave out of some builds; %s is built in every one, so move the type to a file every target builds, or hand-write the interface in contract.go", filepath.Join(rel, m.File), m.Line, m.Name, m.File, contractsrc.DerivedFileName)
	case m.Name == contractsrc.InterfaceName:
		return contractsrc.MarkedType{}, fmt.Errorf("%s:%d: the //forge:contract type is named %s, the name of the interface forge derives from it; rename the type (conventionally the unexported `service`)", filepath.Join(rel, m.File), m.Line, m.Name)
	}
	if hw := contractsrc.HandWrittenContracts(dir); len(hw) > 0 {
		h := hw[0]
		return contractsrc.MarkedType{}, fmt.Errorf("%s declares its contract twice: the hand-written interface %s (%s:%d) and the //forge:contract type %s (%s:%d). Keep one: delete the interface and let forge generate derive it into %s, or remove the marker and keep contract.go",
			rel, h.Name, h.File, h.Line, m.Name, m.File, m.Line, contractsrc.DerivedFileName)
	}

	return m, nil
}

// fileImports is one source file's imports, as a derived method's
// qualifiers resolve against them.
type fileImports struct {
	byName  map[string]ImportDef // local name → import
	unnamed []ImportDef          // unaliased, name to resolve
	dot     bool
}

func newFileImports(file *ast.File) *fileImports {
	fi := &fileImports{byName: map[string]ImportDef{}}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch {
		case imp.Name == nil:
			fi.unnamed = append(fi.unnamed, ImportDef{Path: p})
		case imp.Name.Name == ".":
			fi.dot = true
		case imp.Name.Name == "_":
		default:
			fi.byName[imp.Name.Name] = ImportDef{Path: p, Name: imp.Name.Name, Explicit: true}
		}
	}
	return fi
}

// deriveContract parses dir and collects the marked type's exported method
// set, refusing every shape the derived interface could not express.
func deriveContract(dir string, r *Resolver) (*derived, error) {
	m, err := CheckMarkedContract(dir)
	if err != nil {
		return nil, err
	}
	c := &deriveCollector{
		dir:     dir,
		marked:  m,
		fset:    token.NewFileSet(),
		d:       &derived{typeName: m.Name, typeFile: m.File},
		perFile: map[string]*fileImports{},
	}
	for _, name := range contractsrc.SourceFiles(dir) {
		if err := c.collectFile(name); err != nil {
			return nil, err
		}
	}
	imports, err := c.resolveImports(r)
	if err != nil {
		return nil, err
	}
	c.d.imports = imports
	return c.d, nil
}

// deriveCollector gathers one derivation across the package's files: the
// marked type's doc and shape, its exported methods, and every package
// qualifier those methods' signatures spell.
type deriveCollector struct {
	dir       string
	marked    contractsrc.MarkedType
	fset      *token.FileSet
	d         *derived
	perFile   map[string]*fileImports
	qualified []qualifierUse
}

// qualifierUse is one `pkg.` a derived method's signature spells, with the
// file whose imports bind it.
type qualifierUse struct {
	file, qualifier, method string
}

// collectFile folds one source file into the derivation.
func (c *deriveCollector) collectFile(name string) error {
	path := filepath.Join(c.dir, name)
	file, err := parser.ParseFile(c.fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if c.d.pkg == "" {
		c.d.pkg = file.Name.Name
	}
	if collides := topLevelServiceDecl(file); collides != "" {
		return fmt.Errorf("%s declares a %s named %s, the name of the interface forge derives from the //forge:contract type %s; rename it", path, collides, contractsrc.InterfaceName, c.marked.Name)
	}
	fi := newFileImports(file)
	c.perFile[name] = fi
	constrained := contractsrc.BuildConstrained(c.dir, name, file)
	for _, decl := range file.Decls {
		switch dd := decl.(type) {
		case *ast.GenDecl:
			if name == c.marked.File && dd.Tok == token.TYPE {
				c.collectTypeDecl(dd)
			}
		case *ast.FuncDecl:
			if err := c.collectMethod(name, path, fi, dd, constrained); err != nil {
				return err
			}
		}
	}
	return nil
}

// collectTypeDecl records the marked type's doc prose and whether it takes
// a composite literal.
func (c *deriveCollector) collectTypeDecl(gd *ast.GenDecl) {
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name.Name != c.marked.Name {
			continue
		}
		doc := ts.Doc
		if doc == nil && len(gd.Specs) == 1 {
			doc = gd.Doc
		}
		c.d.typeDoc = proseLines(doc)
		switch ts.Type.(type) {
		case *ast.StructType, *ast.ArrayType, *ast.MapType:
			c.d.valueLit = true
		}
	}
}

// collectMethod records fn when it is an exported method declared on the
// marked type, with every package qualifier its signature spells. One in a
// build-constrained file is refused: the derived interface would name a
// method some builds of the type do not have.
func (c *deriveCollector) collectMethod(name, path string, fi *fileImports, fn *ast.FuncDecl, constrained bool) error {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() {
		return nil
	}
	if receiverTypeName(fn.Recv.List[0].Type) != c.marked.Name {
		return nil
	}
	if constrained {
		return fmt.Errorf("%s: %s.%s is declared in a file build constraints leave out of some builds; %s is built in every one, so declare the method in a file every target builds, or make it unexported", path, c.marked.Name, fn.Name.Name, contractsrc.DerivedFileName)
	}
	if fi.dot {
		return fmt.Errorf("%s: %s.%s is declared in a file with a dot import; a derived contract cannot tell its package's types from the dot-imported ones — import that package by name", path, c.marked.Name, fn.Name.Name)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, c.fset, fn.Type); err != nil {
		return fmt.Errorf("render %s.%s: %w", c.marked.Name, fn.Name.Name, err)
	}
	c.d.methods = append(c.d.methods, derivedMethod{
		name:    fn.Name.Name,
		doc:     methodDocLines(fn.Doc),
		sig:     strings.TrimPrefix(buf.String(), "func"),
		pointer: isPointerReceiver(fn.Recv.List[0].Type),
	})
	ast.Inspect(fn.Type, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			c.qualified = append(c.qualified, qualifierUse{file: name, qualifier: id.Name, method: fn.Name.Name})
		}
		return false
	})
	return nil
}

// resolveImports resolves each qualifier against the imports of the file
// its method is declared in. Aliased imports keep their alias; an unaliased
// one binds its package's declared name, which the convention gives for the
// standard library and the toolchain (the Resolver) for the rest.
func (c *deriveCollector) resolveImports(r *Resolver) ([]ImportDef, error) {
	needed := map[string]ImportDef{} // local name → import
	for _, q := range c.qualified {
		fi := c.perFile[q.file]
		imp, ok := fi.resolve(q.qualifier, r)
		if !ok && r == nil {
			r = NewResolver(filepath.Join(c.dir, c.marked.File))
			imp, ok = fi.resolve(q.qualifier, r)
		}
		if !ok {
			return nil, fmt.Errorf("%s: %s.%s names %s., which no import of %s binds", filepath.Join(c.dir, q.file), c.marked.Name, q.method, q.qualifier, q.file)
		}
		if prev, seen := needed[q.qualifier]; seen && prev.Path != imp.Path {
			return nil, fmt.Errorf("%s: the methods of %s use %s for two packages (%s and %s) in different files; contract_gen.go needs one name per package — alias one of the imports", c.dir, c.marked.Name, q.qualifier, prev.Path, imp.Path)
		}
		needed[q.qualifier] = imp
	}
	imports := make([]ImportDef, 0, len(needed))
	for _, imp := range needed {
		imports = append(imports, imp)
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
	return imports, nil
}

// resolve returns the import a qualifier q binds in this file.
func (fi *fileImports) resolve(q string, r *Resolver) (ImportDef, bool) {
	if imp, ok := fi.byName[q]; ok {
		return imp, true
	}
	for _, imp := range fi.unnamed {
		if r.packageName(imp.Path) == q {
			return ImportDef{Path: imp.Path, Name: q}, true
		}
	}
	return ImportDef{}, false
}

// topLevelServiceDecl reports what a file declares under the derived
// interface's name other than an interface (HandWrittenContracts reports
// those): "type", "func", "var" or "const"; "" for nothing.
func topLevelServiceDecl(file *ast.File) string {
	for _, decl := range file.Decls {
		switch dd := decl.(type) {
		case *ast.FuncDecl:
			if dd.Recv == nil && dd.Name.Name == contractsrc.InterfaceName {
				return "func"
			}
		case *ast.GenDecl:
			for _, spec := range dd.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == contractsrc.InterfaceName {
						if _, isIface := s.Type.(*ast.InterfaceType); !isIface {
							return "type"
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == contractsrc.InterfaceName {
							return strings.ToLower(dd.Tok.String())
						}
					}
				}
			}
		}
	}
	return ""
}

// assertionValue is the value contract_gen.go asserts implements Service,
// taken from the method set that holds the derived methods: `(*T)(nil)` when
// any of them has a pointer receiver (only *T's method set has it), and a T
// value when every one has a value receiver — `T{}` for a type that takes a
// composite literal, `*new(T)` for any other.
func (d *derived) assertionValue() string {
	for _, m := range d.methods {
		if m.pointer {
			return "(*" + d.typeName + ")(nil)"
		}
	}
	if len(d.methods) == 0 {
		// No methods: an empty interface, which *T satisfies as well as T;
		// keep the conventional spelling.
		return "(*" + d.typeName + ")(nil)"
	}
	if d.valueLit {
		return d.typeName + "{}"
	}
	return "*new(" + d.typeName + ")"
}

// isPointerReceiver reports whether a receiver type is `*T` (or `*T[...]`).
func isPointerReceiver(expr ast.Expr) bool {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.StarExpr:
			return true
		default:
			return false
		}
	}
}

// receiverTypeName is the base type name of a method receiver: `s *service`,
// `s service` and a generic `s *service[T]` all name "service".
func receiverTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// methodDocLines is a method's doc comment, raw, for the interface method —
// `// forge:no-observe` included, since ParseContract reads it there. Only
// `//go:` compiler directives are dropped: they apply to the function body,
// and an interface method has none.
func methodDocLines(cg *ast.CommentGroup) []string {
	if cg == nil {
		return nil
	}
	var out []string
	for _, c := range cg.List {
		if strings.HasPrefix(c.Text, "//go:") {
			continue
		}
		out = append(out, c.Text)
	}
	return out
}

// proseLines is the type's doc comment minus its directives (the
// //forge:contract marker itself, //go:, //nolint), for the interface doc.
func proseLines(cg *ast.CommentGroup) []string {
	if cg == nil {
		return nil
	}
	var out []string
	for _, c := range cg.List {
		if !strings.HasPrefix(c.Text, "//") {
			out = append(out, c.Text)
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if strings.HasPrefix(body, "forge:") || strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(body, "nolint") {
			continue
		}
		out = append(out, c.Text)
	}
	// Trim the blank `//` lines a removed directive leaves at either end.
	for len(out) > 0 && strings.TrimSpace(strings.TrimPrefix(out[len(out)-1], "//")) == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && strings.TrimSpace(strings.TrimPrefix(out[0], "//")) == "" {
		out = out[1:]
	}
	return out
}

func (d *derived) render() ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", derivedHeaderFirstLine)
	b.WriteString("// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)\n")
	fmt.Fprintf(&b, "// Source: the //forge:contract type %s in %s.\n", d.typeName, d.typeFile)
	b.WriteString("//\n")
	fmt.Fprintf(&b, "// To change the contract, change the exported methods of %s and re-run\n", d.typeName)
	b.WriteString("// \"forge generate\": this file, mock_gen.go and middleware_gen.go follow.\n")
	fmt.Fprintf(&b, "package %s\n\n", d.pkg)
	if len(d.imports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range d.imports {
			fmt.Fprintf(&b, "\t%s\n", imp.Declaration())
		}
		b.WriteString(")\n\n")
	}
	fmt.Fprintf(&b, "// %s is the contract of package %s: the exported methods of %s,\n", contractsrc.InterfaceName, d.pkg, d.typeName)
	fmt.Fprintf(&b, "// derived by forge generate from its //forge:contract marker.\n")
	if len(d.typeDoc) > 0 {
		b.WriteString("//\n")
		for _, l := range d.typeDoc {
			b.WriteString(l + "\n")
		}
	}
	fmt.Fprintf(&b, "type %s interface {\n", contractsrc.InterfaceName)
	for i, m := range d.methods {
		if i > 0 && len(m.doc) > 0 {
			b.WriteString("\n")
		}
		for _, l := range m.doc {
			b.WriteString("\t" + l + "\n")
		}
		fmt.Fprintf(&b, "\t%s%s\n", m.name, m.sig)
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "var _ %s = %s\n", contractsrc.InterfaceName, d.assertionValue())

	out, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("gofmt %s: %w\n---\n%s", contractsrc.DerivedFileName, err, b.String())
	}
	return out, nil
}

// contractFiles returns the files a contract at contractPath is read from:
// the file itself and, for a derived contract_gen.go, the package's
// hand-declared dependency interfaces — contract.go when one remains beside
// the marked type, and a `// forge:deps` deps.go. Their interfaces are
// mocked with Service's, exactly as when they share a hand-written
// contract.go.
func contractFiles(contractPath string) []string {
	files := []string{contractPath}
	if filepath.Base(contractPath) != contractsrc.DerivedFileName {
		return files
	}
	dir := filepath.Dir(contractPath)
	if p := filepath.Join(dir, contractsrc.FileName); fileExists(p) {
		files = append(files, p)
	}
	if depsurface.HasDepsMarker(dir) {
		files = append(files, filepath.Join(dir, depsurface.DepsFileName))
	}
	return files
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
