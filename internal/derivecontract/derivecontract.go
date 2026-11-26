// Package derivecontract migrates one package from a hand-written contract
// to a derived one: `forge fix derive-contract <package-dir>`.
//
// Before, the package states its API twice — `type Service interface` in
// contract.go, and the same methods on the one type that implements it.
// After, the implementing type carries `//forge:contract`, and `forge
// generate` writes the interface into contract_gen.go from the type's
// exported methods (internal/generator/contract, derive.go). The migration
// is opt-in and per package, because only the author knows a package is
// single-implementation for good: a strategy interface with several
// implementations IS the abstraction and keeps its contract.go.
//
// The rewrite never changes the contract. The package is type-checked first
// (one that does not type-check is refused), and the implementation is the
// type checker's: the one named type whose method set, or whose pointer's,
// implements Service. It refuses every package where deriving would change
// the contract: no implementation or several, an interface that embeds
// another (derivation reads declared methods only), a differently-named
// contract interface (the derived one is always Service), an implementation
// that is generic, gets a Service method only by promotion, or exports a
// method Service does not have (the derived contract would grow it). And
// before anything is written, it type-checks the rewritten package with the
// contract_gen.go forge would derive, and compares that Service, method by
// method, with the one it replaces (typed.go).
//
// It writes only inside the package, and the package only inside the
// project, on symlink-resolved paths (safety.go).
//
// Plan computes the rewrite and Apply writes it as one transaction, so
// `--dry-run` and the tests read exactly what a run would write, and a
// failed write leaves every file as it was.
package derivecontract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/contractsrc"
	"github.com/reliant-labs/forge/internal/depsurface"
	"github.com/reliant-labs/forge/internal/pkgdirective"
)

// marker is the directive the migration puts on the implementing type.
const marker = "//" + contractsrc.Marker

// goDirective is the comment form gofmt treats as a directive
// (`//go:generate`, `//forge:contract`): no space after the slashes.
var goDirective = regexp.MustCompile(`^//[a-z0-9]+:[a-z0-9]`)

// Migration is the planned rewrite of one package. Nothing is on disk until
// Apply.
type Migration struct {
	// Dir is the package directory, project-relative and slash-separated.
	Dir string
	// Type is the implementing type that now carries //forge:contract, and
	// TypeFile the base name of the file declaring it.
	Type, TypeFile string
	// Methods are Service's methods, in their contract.go order.
	Methods []string
	// PointerOnly reports that only *Type implements Service (a method has
	// a pointer receiver), so contract_gen.go asserts `(*Type)(nil)`.
	PointerOnly bool
	// KeptContract reports that contract.go stays, holding what it declared
	// besides Service (the Deps struct, constants, functions).
	KeptContract bool
	// DepsFile is the base name of the file contract.go's remaining
	// dependency interfaces moved to ("deps.go"); "" when none moved.
	DepsFile string
	// Writes are the files to (re)write, Removes the ones to delete;
	// absolute paths.
	Writes  []FileWrite
	Removes []string
	// Rewrites describe the change, one human-readable line each.
	Rewrites []string
}

// FileWrite is one file's new content.
type FileWrite struct {
	Path    string
	Content []byte
}

// RefusedError is a package the migration will not rewrite, with every
// reason it found.
type RefusedError struct {
	Dir     string
	Reasons []string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s cannot derive its contract: %s", e.Dir, strings.Join(e.Reasons, "; "))
}

// writeFile and removeFile are Apply's two mutations, variables so a test
// can fail one partway through.
var (
	writeFile  = atomicfile.WriteFile
	removeFile = os.Remove
)

// original is one file's bytes before Apply touched it.
type original struct {
	path    string
	content []byte
	mode    os.FileMode
	existed bool
}

// Apply writes m as one transaction: every file first, each atomically,
// then the removals. When any step fails, every file already changed is
// put back — rewritten files restored to their original bytes, created
// ones deleted, removed ones recreated — before the error is returned, so a
// failed run never leaves a half-migrated package.
func Apply(m *Migration) error {
	backups := map[string]original{}
	for _, path := range append(writePaths(m), m.Removes...) {
		b := original{path: path, mode: 0o644}
		content, err := os.ReadFile(path)
		switch {
		case err == nil:
			b.content, b.existed = content, true
			if fi, err := os.Stat(path); err == nil {
				b.mode = fi.Mode().Perm()
			}
		case !os.IsNotExist(err):
			return fmt.Errorf("read %s before rewriting it: %w", path, err)
		}
		backups[path] = b
	}

	var done []original
	rollback := func(cause error) error {
		var failed []string
		for i := len(done) - 1; i >= 0; i-- {
			b := done[i]
			var err error
			if b.existed {
				err = atomicfile.WriteFile(b.path, b.content, b.mode)
			} else if rerr := os.Remove(b.path); rerr != nil && !os.IsNotExist(rerr) {
				err = rerr
			}
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", b.path, err))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("%w; restoring the package also failed for %s — `git diff` shows what is left", cause, strings.Join(failed, ", "))
		}
		return fmt.Errorf("%w; every file was restored, nothing changed", cause)
	}
	for _, w := range m.Writes {
		b := backups[w.Path]
		if err := writeFile(w.Path, w.Content, b.mode); err != nil {
			return rollback(fmt.Errorf("write %s: %w", w.Path, err))
		}
		done = append(done, b)
	}
	for _, path := range m.Removes {
		b := backups[path]
		if err := removeFile(path); err != nil && !os.IsNotExist(err) {
			return rollback(fmt.Errorf("remove %s: %w", path, err))
		}
		done = append(done, b)
	}
	return nil
}

func writePaths(m *Migration) []string {
	out := make([]string, 0, len(m.Writes))
	for _, w := range m.Writes {
		out = append(out, w.Path)
	}
	return out
}

// pkgSrc is the package's non-test, non-generated files, parsed once.
type pkgSrc struct {
	root   string // the project root: its golangci config and go.mod
	dir    string
	name   string // package clause
	fset   *token.FileSet
	files  []*srcFile
	byName map[string]*srcFile
	names  map[string]string // import path → declared name (toolchain)
	// nonStd reports an unaliased import outside the standard library: its
	// name is the toolchain's to give, so the derived render needs a
	// Resolver.
	nonStd bool
}

type srcFile struct {
	name string
	path string
	src  []byte
	file *ast.File
}

// serviceMethod is one method of contract.go's Service as written: its
// docs move, and its text is quoted in messages. Its type is the type
// checker's (typed.go).
type serviceMethod struct {
	name    string
	display string // as written, for messages
	doc     *ast.CommentGroup
	comment *ast.CommentGroup
}

type serviceDecl struct {
	file    *srcFile
	gd      *ast.GenDecl
	ts      *ast.TypeSpec
	doc     *ast.CommentGroup
	methods []serviceMethod
}

type typeDecl struct {
	name    string
	file    *srcFile
	gd      *ast.GenDecl
	ts      *ast.TypeSpec
	methods map[string]methodDecl
}

type methodDecl struct {
	fd   *ast.FuncDecl
	file *srcFile
}

// edit replaces src[start:end] with text; an insertion has start == end.
// Edits at one offset apply in the order they were added.
type edit struct {
	start, end int
	text       string
	seq        int
}

type edits map[string][]edit

func (e edits) add(file string, start, end int, text string) {
	e[file] = append(e[file], edit{start: start, end: end, text: text, seq: len(e[file])})
}

func applyEdits(src []byte, es []edit) []byte {
	sorted := append([]edit(nil), es...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start > sorted[j].start
		}
		return sorted[i].seq > sorted[j].seq
	})
	out := append([]byte(nil), src...)
	for _, e := range sorted {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	return out
}

// Plan computes the migration of the package at dir (absolute, or relative
// to root). It returns a *RefusedError, naming every reason, for a package
// the migration would change the contract of.
//
//nolint:funlen // One linear story: refuse, find, rewrite, verify.
func Plan(root, dir string) (*Migration, error) {
	given := dir
	if !filepath.IsAbs(given) {
		given = filepath.Join(root, dir)
	}
	given = filepath.Clean(given)
	display := given
	if r, err := filepath.Rel(root, given); err == nil {
		display = filepath.ToSlash(r)
	}
	rroot, err := resolve(root)
	if err != nil {
		return nil, fmt.Errorf("resolve the project root %s: %w", root, err)
	}
	abs, err := resolve(given)
	if err != nil {
		return nil, fmt.Errorf("%s is not a package directory", display)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a package directory", display)
	}
	if !within(rroot, abs) {
		return nil, &RefusedError{Dir: display, Reasons: []string{fmt.Sprintf("it resolves to %s, outside the project %s; the migration rewrites files only inside the project", abs, rroot)}}
	}
	rel := display
	if r, err := filepath.Rel(rroot, abs); err == nil {
		rel = filepath.ToSlash(r)
	}
	refuse := func(reasons ...string) error { return &RefusedError{Dir: rel, Reasons: reasons} }

	if reasons := preflight(abs); len(reasons) > 0 {
		return nil, refuse(reasons...)
	}
	// The compiler's view of the package, before any of it is trusted: a
	// package that does not type-check cannot be proven to keep its
	// contract.
	tp, errs, err := loadPackage(rroot, abs, nil)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, refuse(typeErrorReason("the package", errs))
	}
	p, err := parsePackage(rroot, abs, tp)
	if err != nil {
		return nil, err
	}
	if out := excludedSources(p, tp); len(out) > 0 {
		return nil, refuse(fmt.Sprintf("build constraints leave %s out of this build, and the derived contract reads every source file, so the migration cannot check what it would change in the builds that include them", strings.Join(out, ", ")))
	}
	svc, reasons := findService(p)
	if len(reasons) > 0 {
		return nil, refuse(reasons...)
	}
	_, origIface, reasons := typedService(tp)
	if len(reasons) > 0 {
		return nil, refuse(reasons...)
	}
	impl, pointerOnly, reasons := findImplementation(p, tp, svc)
	if len(reasons) > 0 {
		return nil, refuse(reasons...)
	}

	m := &Migration{Dir: rel, Type: impl.name, TypeFile: impl.file.name, PointerOnly: pointerOnly}
	for _, im := range svc.methods {
		m.Methods = append(m.Methods, im.name)
	}
	ed := edits{}
	cf := svc.file

	// 1. Service leaves contract.go.
	start, end := serviceRange(p, svc)
	ed.add(cf.name, start, end, "")

	// 2. Each method's doc moves onto the implementation, when it has none
	// of its own; a forge directive (method-level forge:no-observe) moves
	// either way, since the derived interface reads it there.
	var docMoved []string
	for _, im := range svc.methods {
		md := impl.methods[im.name]
		if moved := moveMethodDoc(p, ed, im, md); moved {
			docMoved = append(docMoved, im.name)
		}
	}

	// 3. The marker, and the interface's doc, go on the type.
	markType(p, ed, svc, impl)

	// 4. What is left of contract.go: kept, or its dependency interfaces
	// moved to deps.go and the file removed.
	outputs := map[string][]byte{} // base name → content; nil = removed
	contractSrc := applyEdits(cf.src, ed[cf.name])
	delete(ed, cf.name)
	settled, err := settleContract(p, cf, contractSrc, ed, m)
	if err != nil {
		return nil, err
	}
	for name, content := range settled {
		outputs[name] = content
	}

	// 5. Every other edited file: comment edits only, so gofmt alone —
	// its imports are untouched and its grouping is the author's.
	for name, es := range ed {
		f := p.byName[name]
		out, err := format.Source(applyEdits(f.src, es))
		if err != nil {
			return nil, fmt.Errorf("format %s: %w", f.path, err)
		}
		outputs[name] = out
	}

	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)

	// 6. Every file the migration writes or removes — and contract_gen.go,
	// which the run writes next — resolves inside the package.
	touched := []string{filepath.Join(abs, contractsrc.DerivedFileName)}
	for _, name := range names {
		touched = append(touched, filepath.Join(abs, name))
	}
	if out := escapes(abs, touched); len(out) > 0 {
		return nil, refuse(out...)
	}

	// 7. Verify before writing anything: type-check the rewritten package
	// with its derived contract, and compare that with Service.
	if reasons, err := verify(p, tp, origIface, outputs); err != nil {
		return nil, err
	} else if len(reasons) > 0 {
		return nil, refuse(reasons...)
	}
	for _, name := range names {
		path := filepath.Join(abs, name)
		if outputs[name] == nil {
			m.Removes = append(m.Removes, path)
			continue
		}
		m.Writes = append(m.Writes, FileWrite{Path: path, Content: outputs[name]})
	}

	m.Rewrites = append([]string{fmt.Sprintf("%s/%s: //forge:contract on %s — its exported methods are the contract now", rel, impl.file.name, impl.name)}, m.Rewrites...)
	if len(docMoved) > 0 {
		m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s: Service's method docs moved onto %s (%s)", rel, impl.name, strings.Join(docMoved, ", ")))
	}
	return m, nil
}

// preflight refuses the packages the migration does not apply to at all.
func preflight(dir string) []string {
	if mt := contractsrc.MarkedTypes(dir); len(mt) > 0 {
		return []string{fmt.Sprintf("it already derives its contract from the //forge:contract type %s (%s:%d)", mt[0].Name, mt[0].File, mt[0].Line)}
	}
	if _, err := os.Stat(filepath.Join(dir, contractsrc.FileName)); err != nil {
		return []string{"it has no contract.go, so there is no hand-written Service to migrate"}
	}
	if mk, ok := pkgdirective.Find(dir, "forge:exclude-contract"); ok {
		return []string{fmt.Sprintf("%s:%d opts it out of contract generation (//forge:exclude-contract), and a derived contract is generated code; remove the directive first if the package should have one", filepath.Base(mk.File), mk.Line)}
	}
	var reasons []string
	hw := contractsrc.HandWrittenContracts(dir)
	found := false
	for _, h := range hw {
		switch {
		case h.Name != contractsrc.InterfaceName:
			reasons = append(reasons, fmt.Sprintf("its contract interface is %s (%s:%d), and a derived contract is always named %s: rename the interface and its uses first, or keep the hand-written contract", h.Name, h.File, h.Line, contractsrc.InterfaceName))
		case h.File != contractsrc.FileName:
			reasons = append(reasons, fmt.Sprintf("its %s interface is declared in %s, not contract.go; move it to contract.go first", h.Name, h.File))
		default:
			found = true
		}
	}
	if len(reasons) == 0 && !found {
		reasons = append(reasons, "contract.go declares no `type Service interface`: there is no contract to derive")
	}
	return reasons
}

// parsePackage parses the package's source files for the rewrite; tp, its
// type-checked load, names every package it imports.
func parsePackage(root, dir string, tp *packages.Package) (*pkgSrc, error) {
	p := &pkgSrc{root: root, dir: dir, fset: token.NewFileSet(), byName: map[string]*srcFile{}, names: map[string]string{}}
	for _, imp := range tp.Types.Imports() {
		p.names[imp.Path()] = imp.Name()
	}
	for _, name := range contractsrc.SourceFiles(dir) {
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(p.fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if p.name == "" {
			p.name = file.Name.Name
		}
		f := &srcFile{name: name, path: path, src: src, file: file}
		p.files = append(p.files, f)
		p.byName[name] = f
		for _, imp := range file.Imports {
			ip, err := strconv.Unquote(imp.Path.Value)
			if err == nil && imp.Name == nil && !isStdlib(ip) {
				p.nonStd = true
			}
		}
	}
	return p, nil
}

// findService returns contract.go's Service, refusing a generic one and one
// that embeds an interface.
func findService(p *pkgSrc) (*serviceDecl, []string) {
	cf := p.byName[contractsrc.FileName]
	if cf == nil {
		return nil, []string{"contract.go did not parse as part of the package"}
	}
	var svc *serviceDecl
	for _, decl := range cf.file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != contractsrc.InterfaceName {
				continue
			}
			if _, ok := ts.Type.(*ast.InterfaceType); ok {
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				svc = &serviceDecl{file: cf, gd: gd, ts: ts, doc: doc}
			}
		}
	}
	if svc == nil {
		return nil, []string{"contract.go declares no `type Service interface`: there is no contract to derive"}
	}
	var reasons []string
	if svc.ts.TypeParams != nil && len(svc.ts.TypeParams.List) > 0 {
		reasons = append(reasons, "Service is generic; a derived contract is not")
	}
	it := svc.ts.Type.(*ast.InterfaceType)
	for _, field := range it.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok || len(field.Names) == 0 {
			reasons = append(reasons, fmt.Sprintf("Service embeds %s; a derived contract lists the implementation's declared methods only — write the embedded methods into Service first, or keep the hand-written contract", render(field.Type)))
			continue
		}
		for _, n := range field.Names {
			svc.methods = append(svc.methods, serviceMethod{
				name:    n.Name,
				display: strings.TrimPrefix(render(ft), "func"),
				doc:     field.Doc,
				comment: field.Comment,
			})
		}
	}
	if len(svc.methods) == 0 && len(reasons) == 0 {
		reasons = append(reasons, "Service declares no methods, so no type can be told apart as its implementation")
	}
	return svc, reasons
}

// typeSpec is a type declaration with the GenDecl holding it.
type typeSpec struct {
	gd *ast.GenDecl
	ts *ast.TypeSpec
}

// typeSpecs are file's non-interface type declarations.
func typeSpecs(f *srcFile) []typeSpec {
	var out []typeSpec
	for _, decl := range f.file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, iface := ts.Type.(*ast.InterfaceType); !iface {
				out = append(out, typeSpec{gd: gd, ts: ts})
			}
		}
	}
	return out
}

// methodDecls are file's method declarations.
func methodDecls(f *srcFile) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, decl := range f.file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv != nil && len(fd.Recv.List) == 1 {
			out = append(out, fd)
		}
	}
	return out
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

// serviceRange is the byte range of the Service declaration in contract.go:
// its doc, the declaration (or, in a `type (...)` group, its spec), a
// trailing comment, and the rest of its last line.
func serviceRange(p *pkgSrc, svc *serviceDecl) (int, int) {
	src := svc.file.src
	var start, end token.Pos
	if len(svc.gd.Specs) == 1 {
		start, end = svc.gd.Pos(), svc.gd.End()
		if svc.gd.Doc != nil {
			start = svc.gd.Doc.Pos()
		}
	} else {
		start, end = svc.ts.Pos(), svc.ts.End()
	}
	if svc.ts.Doc != nil && svc.ts.Doc.Pos() < start {
		start = svc.ts.Doc.Pos()
	}
	if svc.ts.Comment != nil && svc.ts.Comment.End() > end {
		end = svc.ts.Comment.End()
	}
	s, e := p.fset.Position(start).Offset, p.fset.Position(end).Offset
	for s > 0 && (src[s-1] == ' ' || src[s-1] == '\t') {
		s--
	}
	if i := bytes.IndexByte(src[e:], '\n'); i >= 0 {
		e += i + 1
	} else {
		e = len(src)
	}
	return s, e
}

func commentLines(cg *ast.CommentGroup) []string {
	if cg == nil {
		return nil
	}
	out := make([]string, 0, len(cg.List))
	for _, c := range cg.List {
		out = append(out, c.Text)
	}
	return out
}

// isForgeDirective reports a whole-line forge directive, spaced or not.
func isForgeDirective(text string) bool {
	return strings.HasPrefix(pkgdirective.TrimCommentMarkers(text), "forge:")
}

// missingDirectives are the forge directives of from that have is without.
func missingDirectives(from []string, have *ast.CommentGroup) []string {
	present := map[string]bool{}
	for _, l := range commentLines(have) {
		present[pkgdirective.TrimCommentMarkers(l)] = true
	}
	var out []string
	for _, l := range from {
		if isForgeDirective(l) && !present[pkgdirective.TrimCommentMarkers(l)] {
			out = append(out, l)
		}
	}
	return out
}

// moveMethodDoc puts an interface method's doc on its implementation:
// all of it when the method has no doc, else only its forge directives. It
// reports whether anything moved.
func moveMethodDoc(p *pkgSrc, ed edits, im serviceMethod, md methodDecl) bool {
	doc := commentLines(im.doc)
	if len(doc) == 0 {
		doc = commentLines(im.comment)
	} else if im.comment != nil {
		doc = append(doc, missingDirectives(commentLines(im.comment), im.doc)...)
	}
	if len(doc) == 0 {
		return false
	}
	if md.fd.Doc == nil {
		at := p.fset.Position(md.fd.Pos()).Offset
		ed.add(md.file.name, at, at, strings.Join(doc, "\n")+"\n")
		return true
	}
	extra := missingDirectives(doc, md.fd.Doc)
	if len(extra) == 0 {
		return false
	}
	at := p.fset.Position(md.fd.Doc.End()).Offset
	ed.add(md.file.name, at, at, "\n"+strings.Join(extra, "\n"))
	return true
}

// markType adds //forge:contract to the implementation's doc. A type with
// no doc of its own takes Service's (its leading "Service" renamed to the
// type), so contract_gen.go keeps describing the contract; one with a doc
// keeps it, gaining only Service's forge directives (a package-level
// forge:no-observe on the interface reads from the marked type's doc).
func markType(p *pkgSrc, ed edits, svc *serviceDecl, t *typeDecl) {
	doc := t.ts.Doc
	if doc == nil && len(t.gd.Specs) == 1 {
		doc = t.gd.Doc
	}
	if doc != nil {
		lines := missingDirectives(commentLines(svc.doc), doc)
		last := doc.List[len(doc.List)-1].Text
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		// gofmt sets a doc's directives off with a blank `//` line; inside a
		// `type (...)` group it does not reformat, so write it that way.
		if !goDirective.MatchString(last) && strings.TrimSpace(last) != "//" {
			lines = append(lines, "//")
		}
		lines = append(lines, marker)
		at := p.fset.Position(doc.End()).Offset
		ed.add(t.file.name, at, at, "\n"+strings.Join(lines, "\n"))
		return
	}
	var lines []string
	for i, l := range commentLines(svc.doc) {
		if strings.HasPrefix(l, "//nolint") || strings.HasPrefix(l, "//go:") {
			continue // they described the interface declaration, not the type
		}
		if i == 0 {
			if rest, ok := strings.CutPrefix(l, "// "+contractsrc.InterfaceName+" "); ok {
				l = "// " + t.name + " " + rest
			}
		}
		lines = append(lines, l)
	}
	if len(lines) > 0 {
		lines = append(lines, "//")
	}
	lines = append(lines, marker)
	pos, indent := t.gd.Pos(), ""
	if len(t.gd.Specs) > 1 {
		pos, indent = t.ts.Pos(), "\t"
	}
	at := p.fset.Position(pos).Offset
	ed.add(t.file.name, at, at, strings.Join(lines, "\n"+indent)+"\n"+indent)
}

// settleContract decides what becomes of contract.go once Service is gone,
// given its edited source. It returns the files to write (nil content =
// remove) and may add edits for other files (the package comments contract.go
// carried).
//
// contract.go is removed when nothing but dependency interfaces (or nothing
// at all) is left in it: those move to deps.go, the file `forge fix
// narrow-deps` and `forge scaffold package` declare them in, which `forge
// generate` mocks beside the derived Service. When it still holds anything
// else — the Deps struct, constants, functions — it stays, as a companion
// of the marked type: its interfaces are mocked from there too. It also stays
// when moving it would change a build (a build constraint, a blank or dot
// import) or would clobber a deps.go that is not forge's.
//
//nolint:funlen,gocognit // The decision and its two outcomes read together.
func settleContract(p *pkgSrc, cf *srcFile, src []byte, ed edits, m *Migration) (map[string][]byte, error) {
	out := map[string][]byte{}
	rel := m.Dir + "/" + contractsrc.FileName
	fset := token.NewFileSet()
	nf, err := parser.ParseFile(fset, cf.path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("re-parse %s: %w", cf.path, err)
	}
	usedBefore := usedQualifiers(cf.file)

	var ifaceNames []string
	onlyIfaces := true
	var lastImport *ast.GenDecl
	for _, decl := range nf.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if ok && gd.Tok == token.IMPORT {
			lastImport = gd
			continue
		}
		if !ok || gd.Tok != token.TYPE {
			onlyIfaces = false
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if _, iface := ts.Type.(*ast.InterfaceType); !iface {
				onlyIfaces = false
			}
			ifaceNames = append(ifaceNames, ts.Name.Name)
		}
	}
	keepReason := ""
	switch {
	case !onlyIfaces:
		keepReason = "it still declares more than dependency interfaces"
	case hasBuildConstraint(nf):
		keepReason = "it has a build constraint"
	case hasSideEffectImport(nf):
		keepReason = "it has a blank or dot import"
	case len(ifaceNames) > 0 && fileExists(filepath.Join(p.dir, depsurface.DepsFileName)) && !depsurface.HasDepsMarker(p.dir):
		keepReason = "deps.go exists without the `// forge:deps` marker, so it is not forge's to extend"
	}
	if keepReason != "" {
		content, err := pruneImports(p, cf.path, src, usedBefore)
		if err != nil {
			return nil, err
		}
		out[contractsrc.FileName] = content
		m.KeptContract = true
		m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s: Service removed; the file stays, as the marked type's companion (%s)", rel, keepReason))
		return out, nil
	}

	// Removed. What follows the imports (or the package clause) is the
	// body: the dependency interfaces with their docs and any free-standing
	// comments, directives included.
	bodyStart := fset.Position(nf.Name.End()).Offset
	importsText := ""
	if lastImport != nil {
		for _, decl := range nf.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				importsText = string(src[fset.Position(gd.Pos()).Offset:fset.Position(lastImport.End()).Offset])
				break
			}
		}
		bodyStart = fset.Position(lastImport.End()).Offset
	}
	body := strings.TrimSpace(string(src[bodyStart:]))

	target := headerTarget(p, m)
	moveHeader(p, nf, src, fset, ed, target)
	out[contractsrc.FileName] = nil

	if len(ifaceNames) == 0 {
		// Free-standing comments only. Its package directives can live in
		// any file, so they go where the package comments went; its prose
		// described contract.go (the scaffold's dependency-interface
		// guide), so it goes with the file — and the report says so.
		var directives []string
		dropped := 0
		for _, cg := range nf.Comments {
			if fset.Position(cg.Pos()).Offset < bodyStart {
				continue
			}
			for _, c := range cg.List {
				if isForgeDirective(c.Text) {
					directives = append(directives, c.Text)
				} else if strings.TrimSpace(c.Text) != "//" {
					dropped++
				}
			}
		}
		if len(directives) > 0 {
			ed.add(target.name, len(target.src), len(target.src), "\n"+strings.Join(directives, "\n")+"\n")
		}
		note := ""
		if dropped > 0 {
			note = fmt.Sprintf(" (its %d free-standing comment line(s) described the file and were not kept; `git diff` shows them)", dropped)
		}
		m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s: removed — Service was all it declared%s", rel, note))
		return out, nil
	}

	depsPath := filepath.Join(p.dir, depsurface.DepsFileName)
	m.DepsFile = depsurface.DepsFileName
	var content []byte
	if depsurface.HasDepsMarker(p.dir) {
		prev, err := os.ReadFile(depsPath)
		if err != nil {
			return nil, err
		}
		merged, err := mergeIntoDeps(prev, body, cf.file, depsPath)
		if err != nil {
			return nil, err
		}
		pf, err := parser.ParseFile(token.NewFileSet(), depsPath, prev, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", depsPath, err)
		}
		for q := range usedQualifiers(pf) {
			usedBefore[q] = true
		}
		content = merged
		m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s/%s: gained %s from contract.go", m.Dir, depsurface.DepsFileName, strings.Join(ifaceNames, ", ")))
	} else {
		var b strings.Builder
		b.WriteString(depsurface.DepsFileMarker + "\n\npackage " + p.name + "\n\n")
		if importsText != "" {
			b.WriteString(importsText + "\n\n")
		}
		b.WriteString(body + "\n")
		content = []byte(b.String())
		m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s/%s: created with %s, moved from contract.go", m.Dir, depsurface.DepsFileName, strings.Join(ifaceNames, ", ")))
	}
	content, err = pruneImports(p, depsPath, content, usedBefore)
	if err != nil {
		return nil, err
	}
	out[depsurface.DepsFileName] = content
	m.Rewrites = append(m.Rewrites, fmt.Sprintf("%s: removed — Service is derived, and its dependency interfaces are in deps.go", rel))
	return out, nil
}

// mergeIntoDeps appends body to an existing deps.go, adding the imports of
// contract.go (pruneImports drops the ones nothing uses).
func mergeIntoDeps(prev []byte, body string, contractFile *ast.File, path string) ([]byte, error) {
	src := append(append([]byte(nil), prev...), []byte("\n"+body+"\n")...)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("merge into %s: %w", path, err)
	}
	for _, imp := range contractFile.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		astutil.AddNamedImport(fset, f, name, ip)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// headerTarget is the file contract.go's package comments move to: doc.go
// when the package has one, else the implementation's file.
func headerTarget(p *pkgSrc, m *Migration) *srcFile {
	if f := p.byName["doc.go"]; f != nil {
		return f
	}
	return p.byName[m.TypeFile]
}

// moveHeader carries contract.go's comments above its package clause —
// the package doc, with any package directive in it — to target. A line
// target's header already has (a license, forge's ownership banner) is not
// duplicated, nor is the old contract.go scaffold's own description line;
// the package doc joins target's own, or becomes it.
func moveHeader(p *pkgSrc, nf *ast.File, src []byte, fset *token.FileSet, ed edits, target *srcFile) {
	targetHeader := string(target.src[:p.fset.Position(target.file.Package).Offset])
	have := map[string]bool{}
	for _, l := range strings.Split(targetHeader, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var top []string
	doc := ""
	for _, cg := range nf.Comments {
		if cg.Pos() >= nf.Package {
			break
		}
		var lines []string
		for _, c := range cg.List {
			t := c.Text
			switch {
			case strings.Contains(t, "\n"):
				if strings.Contains(targetHeader, t) {
					continue
				}
			case have[t], strings.HasPrefix(t, "// yours:"), strings.HasPrefix(t, scaffoldContractDocLine):
				continue
			}
			lines = append(lines, t)
		}
		lines = trimBlankComments(lines)
		if len(lines) == 0 {
			continue
		}
		if cg == nf.Doc {
			doc = strings.Join(lines, "\n")
		} else {
			top = append(top, strings.Join(lines, "\n"))
		}
	}
	if len(top) > 0 {
		ed.add(target.name, 0, 0, strings.Join(top, "\n\n")+"\n\n")
	}
	if doc == "" {
		return
	}
	if target.file.Doc != nil {
		at := p.fset.Position(target.file.Doc.End()).Offset
		ed.add(target.name, at, at, "\n//\n"+doc)
		return
	}
	at := p.fset.Position(target.file.Package).Offset
	ed.add(target.name, at, at, doc+"\n")
}

// scaffoldContractDocLine opens the package comment forge's contract.go
// scaffold wrote before contracts could be derived; it describes the file
// being removed, so it does not move.
const scaffoldContractDocLine = "// Package contract; canonical pattern"

// trimBlankComments drops the bare `//` lines at either end of lines.
func trimBlankComments(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "//" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "//" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// pruneImports drops the imports src no longer uses that it used before
// (usedBefore): those only Service named. An import whose name is not known
// for certain is never dropped on a guess — if it is unused, the compiler
// says so — and goimports-style formatting follows the project's standard.
func pruneImports(p *pkgSrc, path string, src []byte, usedBefore map[string]bool) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	used := usedQualifiers(f)
	for _, imp := range append([]*ast.ImportSpec(nil), f.Imports...) {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name, local := "", p.nameOf(ip)
		if imp.Name != nil {
			name, local = imp.Name.Name, imp.Name.Name
		}
		if local == "_" || local == "." || used[local] || !usedBefore[local] {
			continue
		}
		astutil.DeleteNamedImport(fset, f, name, ip)
	}
	// A block left holding one import reads as `import "x"`.
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT && len(gd.Specs) == 1 && gd.Lparen.IsValid() {
			if is := gd.Specs[0].(*ast.ImportSpec); is.Doc == nil && is.Comment == nil {
				gd.Lparen, gd.Rparen = token.NoPos, token.NoPos
			}
		}
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return checksums.RewriteGoFormat(p.root).Format(path, buf.Bytes())
}

func hasBuildConstraint(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				return true
			}
		}
	}
	return false
}

func hasSideEffectImport(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Name != nil && (imp.Name.Name == "_" || imp.Name.Name == ".") {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
