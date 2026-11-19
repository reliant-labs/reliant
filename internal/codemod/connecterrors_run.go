package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// connecterrors_run.go — one run of `forge fix connect-errors`: load the
// module, decide each file's rewrites against the reader analysis
// (connecterrors_readers.go), write what --scope allows, and report with the
// positions of the files as they end up on disk.

// connectErrorTokens pre-filter the files worth type-checking.
var connectErrorTokens = [][]byte{[]byte("NewError"), []byte("CodeOf"), []byte("connect.Error")}

func runConnectErrors(dir string, opts Options) (Report, error) {
	candidates := map[string]bool{}
	err := ownedGoFiles(dir, func(rel string, src []byte) error {
		// A wrapper an earlier run left (svcerr.ToConnect) is in a file that
		// may no longer import connect at all.
		if bytes.Contains(src, []byte(svcerrPath)) && bytes.Contains(src, []byte("ToConnect(")) {
			candidates[filepath.Join(dir, filepath.FromSlash(rel))] = true
			return nil
		}
		if !bytes.Contains(src, []byte(connectPath)) {
			return nil
		}
		for _, tok := range connectErrorTokens {
			if bytes.Contains(src, tok) {
				candidates[filepath.Join(dir, filepath.FromSlash(rel))] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	modPath := goModulePath(dir)
	if modPath == "" || len(candidates) == 0 {
		return Report{}, nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Report{}, err
	}
	// ownedGoFiles reported paths under dir; the loader reports absolute ones.
	abs := make(map[string]bool, len(candidates))
	for p := range candidates {
		a, err := filepath.Abs(p)
		if err != nil {
			return Report{}, err
		}
		abs[a] = true
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:   absDir,
		Env:   goexec.Env(),
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return Report{}, fmt.Errorf("load the project's packages: %w", err)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })

	ce := &connectErrorsRun{absDir: absDir, modPath: modPath, scope: opts.Scope, candidates: abs,
		out: map[string][]byte{}, src: map[string][]byte{}}
	if err := ce.run(pkgs); err != nil {
		return Report{}, err
	}
	if opts.Write {
		paths := make([]string, 0, len(ce.out))
		for path := range ce.out {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		ce.lines = map[string]func(int) int{}
		for _, path := range paths {
			ce.out[path] = formatForProject(absDir, path, ce.out[path])
			if err := writeFile(path, ce.out[path]); err != nil {
				return Report{}, err
			}
			ce.lines[ce.relOf(path)] = lineMapper(ce.src[path], ce.out[path])
		}
	}
	return ce.report(), nil
}

// ceFile is one candidate file and the package variant it was read from.
type ceFile struct {
	pkg  *packages.Package
	file *ast.File
	path string // absolute
	rel  string // slash-separated, relative to the project
	test bool
}

// ceSite is a position in the tree the run read: line is that file's line
// before any rewrite. connectErrorsRun.at prints it as it is on disk.
type ceSite struct {
	rel  string
	line int
}

// ceDone is one rewrite: where, and "old → new".
type ceDone struct {
	at   ceSite
	text string
}

type connectErrorsRun struct {
	absDir     string
	modPath    string
	scope      *Scope
	candidates map[string]bool
	out        map[string][]byte // path → the rewritten file
	src        map[string][]byte // path → the file as read
	// lines maps a written file's line as read to its line on disk; nil
	// until the run writes (a dry run's report reads the files as they are).
	lines map[string]func(int) int

	rewrites []ceDone
	leftHint []leftSite // sites left alone, with why
	// kept: handler packages whose rewrites stay *connect.Error, with
	// every reader that can see their errors.
	kept map[string][]ceBlock
	// outOfScope: test rewrites to svcerr.Code the scope did not allow.
	outOfScope []ceDone
	bareUsed   bool
}

// pkgPathOf is the import path a package variant belongs to: the test
// variant of p and p's external test package both count as p.
func pkgPathOf(p *packages.Package) string {
	return strings.TrimSuffix(p.PkgPath, "_test")
}

func (c *connectErrorsRun) relOf(path string) string {
	rel, err := filepath.Rel(c.absDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// at prints a site as file:line of the file on disk once the run ends.
func (c *connectErrorsRun) at(s ceSite) string {
	line := s.line
	if m := c.lines[s.rel]; m != nil {
		line = m(line)
	}
	return fmt.Sprintf("%s:%d", s.rel, line)
}

// relPkg names a package of this module by its directory.
func (c *connectErrorsRun) relPkg(path string) string {
	if rel, ok := strings.CutPrefix(path, c.modPath+"/"); ok {
		return rel
	}
	return path
}

func (c *connectErrorsRun) run(pkgs []*packages.Package) error {
	files := c.collectFiles(pkgs)
	idx := newReaderIndex(c, files, pkgs)
	bare, err := c.rewriteSources(files, idx)
	if err != nil {
		return err
	}
	return c.rewriteTests(files, idx, bare)
}

// collectFiles lists each candidate file once: the test variant of a package
// re-lists its non-test files, and the first variant seen carries the same
// syntax.
func (c *connectErrorsRun) collectFiles(pkgs []*packages.Package) []ceFile {
	var files []ceFile
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.TypesInfo == nil || p.Types == nil {
			continue
		}
		for _, f := range p.Syntax {
			tf := p.Fset.File(f.Pos())
			if tf == nil {
				continue
			}
			path := tf.Name()
			if !c.candidates[path] || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, ceFile{pkg: p, file: f, path: path, rel: c.relOf(path), test: strings.HasSuffix(path, "_test.go")})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files
}

// rewriteSources rewrites every in-scope non-test file's connect.NewError
// calls, and returns the packages that got a bare svcerr kind.
func (c *connectErrorsRun) rewriteSources(files []ceFile, idx *readerIndex) (map[string]bool, error) {
	c.kept = map[string][]ceBlock{}
	bare := map[string]bool{}
	blocks := map[string][]ceBlock{}
	for _, f := range files {
		if f.test || !c.scope.Contains(f.rel) {
			continue
		}
		pkg := pkgPathOf(f.pkg)
		handler := isHandlerFile(f.rel)
		if _, done := blocks[pkg]; handler && !done {
			blocks[pkg] = idx.blockers(pkg)
		}
		r, err := newCERewriter(f)
		if err != nil {
			return nil, err
		}
		r.allowBare = handler && len(blocks[pkg]) == 0
		r.rewriteNewErrors()
		c.leftHint = append(c.leftHint, r.left...)
		if handler && len(blocks[pkg]) > 0 && (len(r.edits) > 0 || r.keptWrapper) {
			c.kept[pkg] = blocks[pkg]
		}
		if len(r.edits) == 0 {
			continue
		}
		out, err := r.apply()
		if err != nil {
			return nil, err
		}
		c.out[f.path], c.src[f.path] = out, r.src
		c.rewrites = append(c.rewrites, r.done...)
		if r.usedBare {
			c.bareUsed = true
			bare[pkg] = true
		}
	}
	return bare, nil
}

// rewriteTests rewrites connect.CodeOf to svcerr.Code in the tests of the
// packages that may now hand them a bare svcerr kind where a *connect.Error
// used to be: the ones that got one, and every package importing one (its
// code or its tests). A package whose rewrites all stayed *connect.Error
// changed no type, and its tests are left alone. A test outside --scope is
// listed instead of rewritten.
func (c *connectErrorsRun) rewriteTests(files []ceFile, idx *readerIndex, bare map[string]bool) error {
	affected := map[string]bool{}
	for pkg := range bare {
		affected[pkg] = true
		for q := range idx.importers[pkg] {
			affected[q] = true
		}
	}
	for _, f := range files {
		if !f.test || !affected[pkgPathOf(f.pkg)] {
			continue
		}
		r, err := newCERewriter(f)
		if err != nil {
			return err
		}
		r.rewriteCodeOf()
		if len(r.edits) == 0 {
			continue
		}
		if !c.scope.Contains(f.rel) {
			c.outOfScope = append(c.outOfScope, r.done...)
			continue
		}
		out, err := r.apply()
		if err != nil {
			return err
		}
		c.out[f.path], c.src[f.path] = out, r.src
		c.rewrites = append(c.rewrites, r.done...)
	}
	return nil
}

func (c *connectErrorsRun) report() Report {
	var rep Report
	for _, d := range c.rewrites {
		rep.Rewrites = append(rep.Rewrites, c.at(d.at)+": "+d.text)
	}
	used := map[string]bool{}
	for _, l := range c.leftHint {
		rep.Next = append(rep.Next, "left as written: "+c.at(l.at)+": "+l.call+" — "+l.why)
		used[l.why] = true
	}
	for _, why := range leftReasons {
		if used[why] {
			rep.Next = append(rep.Next, why+": "+leftLegend[why])
		}
	}
	kept := make([]string, 0, len(c.kept))
	for pkg := range c.kept {
		kept = append(kept, pkg)
	}
	sort.Strings(kept)
	for _, pkg := range kept {
		blocks := c.kept[pkg]
		var b strings.Builder
		readers := "1 in-process reader sees"
		if len(blocks) > 1 {
			readers = fmt.Sprintf("%d in-process readers see", len(blocks))
		}
		fmt.Fprintf(&b, "%s: its rewrites stay *connect.Error (svcerr.ToConnect) — %s its errors as a *connect.Error:", pkg, readers)
		for _, bl := range blocks {
			b.WriteString("\n     - " + c.renderBlock(bl))
		}
		b.WriteString("\n   Switch each to svcerr.Code(err) (an errors.As or As-style helper into a *connect.Error to svcerr.Code or the svcerr.Is* predicates), then re-run forge fix connect-errors: it drops the wrappers once none is left.")
		rep.Next = append(rep.Next, b.String())
	}
	if len(c.outOfScope) > 0 {
		var b strings.Builder
		b.WriteString("outside --scope, so left as written — a package these tests import now returns bare svcerr kinds, and connect.CodeOf reads a bare kind as unknown:")
		for _, d := range c.outOfScope {
			b.WriteString("\n     - " + c.at(d.at) + ": " + d.text)
		}
		b.WriteString("\n   svcerr.Code reads the same code as connect.CodeOf for a *connect.Error, so the switch is safe whenever that file's owner makes it: a run with the file in --scope makes it.")
		rep.Next = append(rep.Next, b.String())
	}
	if rep.Changed() {
		if c.bareUsed {
			rep.Next = append(rep.Next,
				"A bare svcerr kind reaches the client with its code through svcerr.Interceptor, which forge's chain builders (serverkit.NewChain, observe.Chain, observe.DefaultMiddlewares) install innermost. A handler chain you assemble by hand must end with svcerr.Interceptor(), or the client reads unknown.",
				"In-process readers of a *connect.Error (connect.CodeOf, errors.As or an As-style helper into one) kept a package's rewrites *connect.Error when they are in the package or its tests, in a package whose code imports it, or in one that calls it through an interface production code hands one of its types; a read of a Connect client's error is not one. A reader further away is not traced: if handler errors travel further in process, check those readers — svcerr.Code(err) reads the same code for a *connect.Error and a bare kind.")
		}
		rep.Verify = []string{"go build ./...", "go vet ./...", "go test ./...", "forge lint --conventions"}
	}
	return rep
}

// lineMapper maps a line of before to the line of after holding the same
// text — or, inside a changed run of lines, the matching line of the run
// that replaced it — by a line diff of the two. Rewrites collapse a call's
// lines, gofmt moves an import: a site's own line survives as the start of
// its replacement.
func lineMapper(before, after []byte) func(int) int {
	a := strings.SplitAfter(string(before), "\n")
	b := strings.SplitAfter(string(after), "\n")
	ops := difflib.NewMatcherWithJunk(a, b, false, nil).GetOpCodes()
	return func(line int) int {
		i := line - 1
		for _, op := range ops {
			if i < op.I1 || i >= op.I2 {
				continue
			}
			switch op.Tag {
			case 'e':
				return op.J1 + (i - op.I1) + 1
			case 'r':
				return op.J1 + min(i-op.I1, op.J2-op.J1-1) + 1
			default: // 'd': the line is gone; the next one stands in
				return op.J1 + 1
			}
		}
		return line
	}
}
