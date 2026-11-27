package tmplfields

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
)

// typ is what the resolver knows of a Go type, as text/template sees it.
type typ struct {
	// desc names the type in a finding.
	desc string
	// handBuilt marks a type written for one caller: an anonymous struct, a
	// type declared inside a function, or one declared in a _test.go file.
	handBuilt bool
	// members is every field (promoted ones included) and method a template
	// can name on the type. nil means field access is OPEN: unchecked.
	members map[string]*member
	// elem is what `range` over the type binds dot to; nil when OPEN.
	elem *lazy
}

// open is the type of anything the resolver cannot pin down.
var open = &typ{desc: "an unresolved type"}

type member struct {
	// typ is a field's type, or a method's (first) result.
	typ    *lazy
	method bool
	result func(int) *typ // method results beyond the first
}

// lazy resolves a type on first use: field types are only resolved when a
// template walks into them, which also lets recursive types terminate.
type lazy struct {
	f    func() *typ
	t    *typ
	done bool
}

func (l *lazy) get() *typ {
	if l == nil {
		return open
	}
	if !l.done {
		l.done, l.t = true, open // a cycle back here sees open
		l.t = l.f()
	}
	return l.t
}

func resolved(t *typ) *lazy { return &lazy{t: t, done: true} }

type goFile struct {
	rel, dir string
	f        *ast.File
	test     bool
	imports  map[string]string // local name → import path, module packages only
}

type pkgKey struct {
	dir, name string
	tests     bool
}

// goPkg indexes one package's top-level declarations, lazily: a lookup of
// a name parses only the package's files whose source contains it, which is
// every file that can declare it. Parsing the whole of a large package (cli,
// codegen) for one type was most of the scan's time.
type goPkg struct {
	s         *scanner
	dir, name string
	tests     bool
	types     map[string][]*typeDecl
	methods   map[string][]*ast.FuncDecl
	funcs     map[string][]*ast.FuncDecl
	values    map[string][]valueDecl
	files     map[*ast.FuncDecl]*goFile
	scanned   map[string]bool // names whose files are indexed
	indexed   map[string]bool // files indexed
}

func (p *goPkg) typeDecls(name string) []*typeDecl       { p.index(name); return p.types[name] }
func (p *goPkg) methodDecls(recv string) []*ast.FuncDecl { p.index(recv); return p.methods[recv] }
func (p *goPkg) funcDecls(name string) []*ast.FuncDecl   { p.index(name); return p.funcs[name] }
func (p *goPkg) valueDecls(name string) []valueDecl      { p.index(name); return p.values[name] }

type typeDecl struct {
	spec *ast.TypeSpec
	file *goFile
	fn   *ast.FuncDecl // non-nil for a type declared inside a function
}

type valueDecl struct {
	spec    *ast.ValueSpec
	idx     int
	file    *goFile
	isConst bool
}

// scope is where an expression is resolved: its file, and the top-level
// function it sits in (nil at package level).
type scope struct {
	file *goFile
	fn   *ast.FuncDecl
}

type scanner struct {
	cfg     Config
	files   map[string][]byte
	dirs    map[string][]string
	fset    *token.FileSet
	parsed  map[string]*goFile
	pkgs    map[pkgKey]*goPkg
	pkgName map[string]string
	named   map[*ast.TypeSpec]*typ
	defs    map[*ast.FuncDecl]*funcDefs
	depth   int
}

func newScanner(files map[string][]byte, cfg Config) *scanner {
	s := &scanner{
		cfg:     cfg,
		files:   files,
		dirs:    map[string][]string{},
		fset:    token.NewFileSet(),
		parsed:  map[string]*goFile{},
		pkgs:    map[pkgKey]*goPkg{},
		pkgName: map[string]string{},
		named:   map[*ast.TypeSpec]*typ{},
		defs:    map[*ast.FuncDecl]*funcDefs{},
	}
	for rel := range files {
		d := path.Dir(rel)
		s.dirs[d] = append(s.dirs[d], rel)
	}
	for _, rels := range s.dirs {
		sort.Strings(rels)
	}
	return s
}

// parse returns rel's syntax tree, or nil when it does not parse (a
// template-shaped .go file, say): such a file contributes nothing.
func (s *scanner) parse(rel string) *goFile {
	if gf, ok := s.parsed[rel]; ok {
		return gf
	}
	var gf *goFile
	if f, err := parser.ParseFile(s.fset, rel, s.files[rel], parser.SkipObjectResolution); err == nil {
		gf = &goFile{rel: rel, dir: path.Dir(rel), f: f, test: strings.HasSuffix(rel, "_test.go")}
	}
	s.parsed[rel] = gf
	return gf
}

// dirOf maps an import path of this module to its module-relative dir.
func (s *scanner) dirOf(importPath string) (string, bool) {
	if importPath == s.cfg.ModulePath {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(importPath, s.cfg.ModulePath+"/"); ok {
		return rest, true
	}
	return "", false
}

// packageName is the name of the (non-test) package in dir, "" if none.
func (s *scanner) packageName(dir string) string {
	if n, ok := s.pkgName[dir]; ok {
		return n
	}
	name := ""
	for _, rel := range s.dirs[dir] {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if gf := s.parse(rel); gf != nil && !strings.HasSuffix(gf.f.Name.Name, "_test") && !isIgnored(gf.f) {
			name = gf.f.Name.Name
			break
		}
	}
	s.pkgName[dir] = name
	return name
}

// isIgnored reports a file under `//go:build ignore`, which no build sees.
func isIgnored(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.TrimSpace(c.Text) == "//go:build ignore" {
				return true
			}
		}
	}
	return false
}

func (s *scanner) importsOf(gf *goFile) map[string]string {
	if gf.imports != nil {
		return gf.imports
	}
	gf.imports = map[string]string{}
	for _, is := range gf.f.Imports {
		p, err := strconv.Unquote(is.Path.Value)
		if err != nil {
			continue
		}
		dir, inModule := s.dirOf(p)
		if !inModule {
			continue
		}
		name := ""
		if is.Name != nil {
			name = is.Name.Name
		} else {
			name = s.packageName(dir)
		}
		if name == "" || name == "_" || name == "." {
			continue
		}
		gf.imports[name] = p
	}
	return gf.imports
}

// pkg indexes the package named name in dir. tests includes its _test.go
// files, which only that package's own files can see.
func (s *scanner) pkg(dir, name string, tests bool) *goPkg {
	k := pkgKey{dir, name, tests}
	if p, ok := s.pkgs[k]; ok {
		return p
	}
	p := &goPkg{
		s: s, dir: dir, name: name, tests: tests,
		types:   map[string][]*typeDecl{},
		methods: map[string][]*ast.FuncDecl{},
		funcs:   map[string][]*ast.FuncDecl{},
		values:  map[string][]valueDecl{},
		files:   map[*ast.FuncDecl]*goFile{},
		scanned: map[string]bool{},
		indexed: map[string]bool{},
	}
	s.pkgs[k] = p
	return p
}

// index indexes every file of p whose source contains needle.
func (p *goPkg) index(needle string) {
	if p.scanned[needle] {
		return
	}
	p.scanned[needle] = true
	for _, rel := range p.s.dirs[p.dir] {
		if p.indexed[rel] || (!p.tests && strings.HasSuffix(rel, "_test.go")) || !bytes.Contains(p.s.files[rel], []byte(needle)) {
			continue
		}
		p.indexed[rel] = true
		gf := p.s.parse(rel)
		if gf == nil || gf.f.Name.Name != p.name {
			continue
		}
		for _, d := range gf.f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						p.types[sp.Name.Name] = append(p.types[sp.Name.Name], &typeDecl{spec: sp, file: gf})
					case *ast.ValueSpec:
						for i, n := range sp.Names {
							p.values[n.Name] = append(p.values[n.Name], valueDecl{spec: sp, idx: i, file: gf, isConst: d.Tok == token.CONST})
						}
					}
				}
			case *ast.FuncDecl:
				p.files[d] = gf
				if d.Recv == nil {
					p.funcs[d.Name.Name] = append(p.funcs[d.Name.Name], d)
					continue
				}
				if len(d.Recv.List) != 1 {
					continue
				}
				if recv := recvName(d.Recv.List[0].Type); recv != "" {
					p.methods[recv] = append(p.methods[recv], d)
				}
			}
		}
	}
}

func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.ParenExpr:
		return recvName(e.X)
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

func (s *scanner) pkgOf(gf *goFile) *goPkg {
	return s.pkg(gf.dir, gf.f.Name.Name, gf.test)
}

// localType finds a type declared inside fn, if exactly one has the name.
func localType(fn *ast.FuncDecl, gf *goFile, name string) (*typeDecl, bool) {
	if fn == nil || fn.Body == nil {
		return nil, false
	}
	var found []*ast.TypeSpec
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == name {
			found = append(found, ts)
		}
		return true
	})
	if len(found) != 1 {
		return nil, len(found) > 1
	}
	return &typeDecl{spec: found[0], file: gf, fn: fn}, false
}

// resolveType resolves a type expression written in sc.
func (s *scanner) resolveType(e ast.Expr, sc scope) *typ {
	if s.depth > 64 {
		return open
	}
	s.depth++
	defer func() { s.depth-- }()
	switch e := e.(type) {
	case *ast.ParenExpr:
		return s.resolveType(e.X, sc)
	case *ast.StarExpr:
		return s.resolveType(e.X, sc) // text/template dereferences pointers
	case *ast.Ident:
		if td, ambiguous := localType(sc.fn, sc.file, e.Name); td != nil {
			return s.namedType(td)
		} else if ambiguous {
			return open
		}
		if basicTypes[e.Name] || e.Name == "any" || e.Name == "error" {
			return open // predeclared: no fields (and no lookup through every file)
		}
		decls := s.pkgOf(sc.file).typeDecls(e.Name)
		if len(decls) == 1 {
			return s.namedType(decls[0])
		}
		return open // a builtin, a type parameter, or ambiguous
	case *ast.SelectorExpr:
		q, ok := e.X.(*ast.Ident)
		if !ok {
			return open
		}
		ip, ok := s.importsOf(sc.file)[q.Name]
		if !ok {
			return open
		}
		dir, _ := s.dirOf(ip)
		decls := s.pkg(dir, s.packageName(dir), false).typeDecls(e.Sel.Name)
		if len(decls) == 1 && ast.IsExported(e.Sel.Name) {
			return s.namedType(decls[0])
		}
		return open
	case *ast.StructType:
		t := &typ{desc: anonDesc(e), handBuilt: true}
		t.members = s.structMembers(e, sc)
		return t
	case *ast.ArrayType:
		elt := e.Elt
		return &typ{desc: "a slice", elem: &lazy{f: func() *typ { return s.resolveType(elt, sc) }}}
	case *ast.MapType:
		val := e.Value
		return &typ{desc: "a map", elem: &lazy{f: func() *typ { return s.resolveType(val, sc) }}}
	}
	return open // interface, func, chan, generic instantiation
}

func anonDesc(st *ast.StructType) string {
	var names []string
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			names = append(names, embeddedName(f.Type))
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return "struct{" + strings.Join(names, ", ") + "}"
}

func embeddedName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return embeddedName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return embeddedName(e.X)
	case *ast.IndexListExpr:
		return embeddedName(e.X)
	}
	return ""
}

// structMembers is st's fields, direct then promoted, or nil (OPEN) when an
// embedded field's type cannot be resolved: its promoted names are unknown.
func (s *scanner) structMembers(st *ast.StructType, sc scope) map[string]*member {
	members := map[string]*member{}
	var embedded []*typ
	for _, f := range st.Fields.List {
		ft := f.Type
		if len(f.Names) == 0 {
			name := embeddedName(ft)
			if name == "" {
				return nil
			}
			et := s.resolveType(ft, sc)
			if et.members == nil {
				return nil
			}
			members[name] = &member{typ: resolved(et)}
			embedded = append(embedded, et)
			continue
		}
		l := &lazy{f: func() *typ { return s.resolveType(ft, sc) }}
		for _, n := range f.Names {
			members[n.Name] = &member{typ: l}
		}
	}
	for _, et := range embedded {
		for name, m := range et.members {
			if _, ok := members[name]; !ok {
				members[name] = m
			}
		}
	}
	return members
}

// namedType resolves a declared type, memoised per declaration.
func (s *scanner) namedType(td *typeDecl) *typ {
	if t, ok := s.named[td.spec]; ok {
		return t
	}
	spec := td.spec
	pkgName := td.file.f.Name.Name
	t := &typ{desc: pkgName + "." + spec.Name.Name + " (" + td.file.dir + ")", handBuilt: td.fn != nil || td.file.test}
	if td.fn != nil {
		t.desc = spec.Name.Name + " (declared in " + td.fn.Name.Name + ", " + td.file.rel + ")"
	}
	s.named[spec] = t // a self-reference while resolving sees members == nil: OPEN
	if spec.TypeParams != nil {
		return t
	}
	sc := scope{file: td.file, fn: td.fn}
	if spec.Assign.IsValid() { // an alias is its target
		at := s.resolveType(spec.Type, sc)
		s.named[spec] = at
		return at
	}
	var methods map[string]*member
	if td.fn == nil {
		p := s.pkgOf(td.file)
		methods = map[string]*member{}
		for _, fd := range p.methodDecls(spec.Name.Name) {
			methods[fd.Name.Name] = &member{
				method: true,
				typ:    &lazy{f: func() *typ { return s.funcResult(fd, p.files[fd]) }},
				result: func(index int) *typ { return s.funcResultAt(fd, p.files[fd], index) },
			}
		}
	}
	switch u := spec.Type.(type) {
	case *ast.StructType:
		ms := s.structMembers(u, sc)
		if ms == nil {
			return t
		}
		for name, m := range methods {
			if _, ok := ms[name]; !ok {
				ms[name] = m
			}
		}
		t.members = ms
	case *ast.ArrayType:
		t.members = methods
		elt := u.Elt
		t.elem = &lazy{f: func() *typ { return s.resolveType(elt, sc) }}
	case *ast.MapType:
		// A map's `.key` is a lookup, so field access stays OPEN.
		val := u.Value
		t.elem = &lazy{f: func() *typ { return s.resolveType(val, sc) }}
	case *ast.Ident:
		if basicTypes[u.Name] {
			t.members = methods // a field of a string never resolves
		}
	}
	return t
}

var basicTypes = map[string]bool{
	"bool": true, "string": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

// funcResult is the type of a function's or method's first result — what
// text/template takes from a method, and what a call yields. A generic
// function, or one returning nothing, is OPEN.
func (s *scanner) funcResult(fd *ast.FuncDecl, gf *goFile) *typ {
	return s.funcResultAt(fd, gf, 0)
}

// funcResultAt counts result positions, including grouped named results.
func (s *scanner) funcResultAt(fd *ast.FuncDecl, gf *goFile, index int) *typ {
	ft := fd.Type
	if gf == nil || ft.TypeParams != nil || ft.Results == nil {
		return open
	}
	for _, field := range ft.Results.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		if index < n {
			return s.resolveType(field.Type, scope{file: gf})
		}
		index -= n
	}
	return open
}

// ---------------------------------------------------------------------------
// Values: what type an expression at a call site has.

const (
	defExpr   = iota // the def's type is expr's type
	defType          // the def's type is the type expression expr
	defRange         // the def is a range value over expr
	defOpen          // a def the resolver does not follow
	defResult        // the indexed result of a multi-value call
)

type def struct {
	from, to token.Pos // visible from the end of its statement to its scope's end
	kind     int
	expr     ast.Expr
	result   int // zero-based result position for defResult
}

type funcDefs struct {
	byName   map[string][]def
	assigned map[string]bool // names some `=`/op= statement assigns
}

func (s *scanner) funcDefs(fn *ast.FuncDecl) *funcDefs {
	if fd, ok := s.defs[fn]; ok {
		return fd
	}
	b := &defsBuilder{fn: fn, fd: &funcDefs{byName: map[string][]def{}, assigned: map[string]bool{}}}
	s.defs[fn] = b.fd
	b.params(fn.Type, fn.Recv, fn.Body)
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			b.stack = b.stack[:len(b.stack)-1]
			return true
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			b.params(n.Type, nil, n.Body)
		case *ast.AssignStmt:
			b.assign(n)
		case *ast.DeclStmt:
			b.decl(n)
		case *ast.RangeStmt:
			b.rangeStmt(n)
		}
		b.stack = append(b.stack, n)
		return true
	})
	return b.fd
}

// defsBuilder records one function's defs while ast.Inspect walks it.
type defsBuilder struct {
	fn    *ast.FuncDecl
	fd    *funcDefs
	stack []ast.Node // the walked node's ancestors
}

func (b *defsBuilder) add(id *ast.Ident, from, to token.Pos, kind int, e ast.Expr) {
	if id == nil || id.Name == "_" {
		return
	}
	b.fd.byName[id.Name] = append(b.fd.byName[id.Name], def{from: from, to: to, kind: kind, expr: e})
}

func (b *defsBuilder) assigned(exprs ...ast.Expr) {
	for _, e := range exprs {
		if id, ok := e.(*ast.Ident); ok {
			b.fd.assigned[id.Name] = true
		}
	}
}

// scopeEnd is where the scope of a def in the current statement ends.
func (b *defsBuilder) scopeEnd() token.Pos {
	for i := len(b.stack) - 1; i >= 0; i-- {
		switch n := b.stack[i].(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause, *ast.IfStmt, *ast.ForStmt,
			*ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.FuncLit:
			return n.End()
		}
	}
	return b.fn.End()
}

func (b *defsBuilder) params(ft *ast.FuncType, recv *ast.FieldList, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	for _, fl := range []*ast.FieldList{recv, ft.Params, ft.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				b.add(n, body.Lbrace, body.End(), defType, f.Type)
			}
		}
	}
}

func (b *defsBuilder) assign(n *ast.AssignStmt) {
	if n.Tok != token.DEFINE {
		b.assigned(n.Lhs...)
		return
	}
	end := b.scopeEnd()
	for i, l := range n.Lhs {
		id, _ := l.(*ast.Ident)
		switch {
		case len(n.Lhs) == len(n.Rhs):
			b.add(id, n.End(), end, defExpr, n.Rhs[i])
		case len(n.Rhs) == 1:
			b.multiValue(id, n.End(), end, n.Rhs[0], i)
		default:
			b.add(id, n.End(), end, defOpen, nil)
		}
	}
}

// multiValue preserves the result position instead of treating every variable
// as the call's first result. Comma-ok expressions only resolve their first value.
func (b *defsBuilder) multiValue(id *ast.Ident, from, to token.Pos, expr ast.Expr, index int) {
	if id == nil || id.Name == "_" {
		return
	}
	callExpr := expr
	for {
		p, ok := callExpr.(*ast.ParenExpr)
		if !ok {
			break
		}
		callExpr = p.X
	}
	if _, ok := callExpr.(*ast.CallExpr); ok {
		b.fd.byName[id.Name] = append(b.fd.byName[id.Name], def{
			from: from, to: to, kind: defResult, expr: callExpr, result: index,
		})
	} else if index == 0 {
		b.add(id, from, to, defExpr, expr)
	} else {
		b.add(id, from, to, defOpen, nil)
	}
}

func (b *defsBuilder) decl(n *ast.DeclStmt) {
	gd, ok := n.Decl.(*ast.GenDecl)
	if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
		return
	}
	end := b.scopeEnd()
	for _, sp := range gd.Specs {
		vs, ok := sp.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, id := range vs.Names {
			switch {
			case vs.Type != nil:
				b.add(id, vs.End(), end, defType, vs.Type)
			case len(vs.Values) == len(vs.Names):
				b.add(id, vs.End(), end, defExpr, vs.Values[i])
			case len(vs.Values) == 1:
				b.multiValue(id, vs.End(), end, vs.Values[0], i)
			default:
				b.add(id, vs.End(), end, defOpen, nil)
			}
		}
	}
}

func (b *defsBuilder) rangeStmt(n *ast.RangeStmt) {
	switch n.Tok {
	case token.DEFINE:
		k, _ := n.Key.(*ast.Ident)
		b.add(k, n.Body.Lbrace, n.End(), defOpen, nil)
		if v, ok := n.Value.(*ast.Ident); ok {
			b.add(v, n.Body.Lbrace, n.End(), defRange, n.X)
		}
	case token.ASSIGN:
		b.assigned(n.Key, n.Value)
	}
}

// lookup finds the def of name visible at pos in sc.fn.
func (s *scanner) lookup(name string, pos token.Pos, sc scope) (def, bool) {
	if sc.fn == nil {
		return def{}, false
	}
	var best def
	found := false
	for _, d := range s.funcDefs(sc.fn).byName[name] {
		if d.from <= pos && pos < d.to && (!found || d.from > best.from) {
			best, found = d, true
		}
	}
	return best, found
}

// valueType resolves the type of a value expression written in sc.
func (s *scanner) valueType(e ast.Expr, sc scope) *typ {
	if s.depth > 64 {
		return open
	}
	s.depth++
	defer func() { s.depth-- }()
	switch e := e.(type) {
	case *ast.ParenExpr:
		return s.valueType(e.X, sc)
	case *ast.CompositeLit:
		if e.Type == nil {
			return open
		}
		return s.resolveType(e.Type, sc)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return s.valueType(e.X, sc)
		}
	case *ast.StarExpr:
		return s.valueType(e.X, sc)
	case *ast.Ident:
		if d, ok := s.lookup(e.Name, e.Pos(), sc); ok {
			switch d.kind {
			case defExpr:
				return s.valueType(d.expr, sc)
			case defResult:
				if call, ok := d.expr.(*ast.CallExpr); ok {
					return s.callResultAt(call, sc, d.result)
				}
			case defType:
				return s.resolveType(d.expr, sc)
			case defRange:
				return s.valueType(d.expr, sc).elem.get()
			}
			return open
		}
		decls := s.pkgOf(sc.file).valueDecls(e.Name)
		if len(decls) != 1 {
			return open
		}
		vd := decls[0]
		psc := scope{file: vd.file}
		if vd.spec.Type != nil {
			return s.resolveType(vd.spec.Type, psc)
		}
		if len(vd.spec.Values) == len(vd.spec.Names) {
			return s.valueType(vd.spec.Values[vd.idx], psc)
		}
	case *ast.SelectorExpr:
		base := s.valueType(e.X, sc)
		if base.members == nil {
			return open
		}
		if m, ok := base.members[e.Sel.Name]; ok && !m.method {
			return m.typ.get()
		}
	case *ast.CallExpr:
		return s.callType(e, sc)
	case *ast.TypeAssertExpr:
		if e.Type != nil {
			return s.resolveType(e.Type, sc)
		}
	case *ast.IndexExpr:
		return s.valueType(e.X, sc).elem.get()
	}
	return open
}

// callType is what a call yields: a module function's or method's first
// result, or the type of a conversion to a module type. Anything else — a
// builtin, a func value, a function outside the module — is OPEN.
func (s *scanner) callType(call *ast.CallExpr, sc scope) *typ {
	return s.callResultAt(call, sc, 0)
}

func (s *scanner) callResultAt(call *ast.CallExpr, sc scope, index int) *typ {
	fromPkg := func(p *goPkg, name string) *typ {
		if fds := p.funcDecls(name); len(fds) == 1 {
			return s.funcResultAt(fds[0], p.files[fds[0]], index)
		}
		if tds := p.typeDecls(name); len(tds) == 1 && len(call.Args) == 1 && index == 0 {
			return s.namedType(tds[0])
		}
		return open
	}
	switch f := call.Fun.(type) {
	case *ast.ParenExpr:
		return s.callResultAt(&ast.CallExpr{Fun: f.X, Args: call.Args}, sc, index)
	case *ast.Ident:
		if _, local := s.lookup(f.Name, f.Pos(), sc); local {
			return open
		}
		if td, _ := localType(sc.fn, sc.file, f.Name); td != nil {
			if index == 0 {
				return s.namedType(td)
			}
			return open
		}
		return fromPkg(s.pkgOf(sc.file), f.Name)
	case *ast.SelectorExpr:
		if q, ok := f.X.(*ast.Ident); ok {
			if _, local := s.lookup(q.Name, q.Pos(), sc); !local {
				if ip, ok := s.importsOf(sc.file)[q.Name]; ok {
					if !ast.IsExported(f.Sel.Name) {
						return open
					}
					dir, _ := s.dirOf(ip)
					return fromPkg(s.pkg(dir, s.packageName(dir), false), f.Sel.Name)
				}
			}
		}
		base := s.valueType(f.X, sc)
		if m, ok := base.members[f.Sel.Name]; ok && m.method {
			if m.result != nil {
				return m.result(index)
			}
			if index == 0 {
				return m.typ.get()
			}
		}
	}
	return open
}

// stringValues is every constant string e can denote: a literal; a name
// bound once to one (a const, or a local nothing reassigns); or a range
// variable over a literal list of them, or a field of one over a literal list
// of structs (`for _, tc := range []struct{tmpl string}{{tmpl: "a.tmpl"}}`).
// ok is false when any value is not a literal.
func (s *scanner) stringValues(e ast.Expr, sc scope) ([]string, bool) {
	if s.depth > 64 {
		return nil, false
	}
	s.depth++
	defer func() { s.depth-- }()
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil, false
		}
		v, err := strconv.Unquote(e.Value)
		return []string{v}, err == nil
	case *ast.ParenExpr:
		return s.stringValues(e.X, sc)
	case *ast.Ident:
		if d, ok := s.lookup(e.Name, e.Pos(), sc); ok {
			if s.funcDefs(sc.fn).assigned[e.Name] {
				return nil, false
			}
			switch d.kind {
			case defExpr:
				return s.stringValues(d.expr, sc)
			case defRange:
				elems, _, ok := s.literalElems(d.expr, sc)
				if !ok {
					return nil, false
				}
				return s.eachString(elems, func(el ast.Expr) ([]string, bool) { return s.stringValues(el, sc) })
			}
			return nil, false
		}
		decls := s.pkgOf(sc.file).valueDecls(e.Name)
		if len(decls) != 1 || !decls[0].isConst || len(decls[0].spec.Values) != len(decls[0].spec.Names) {
			return nil, false
		}
		return s.stringValues(decls[0].spec.Values[decls[0].idx], scope{file: decls[0].file})
	case *ast.SelectorExpr:
		id, ok := e.X.(*ast.Ident)
		if !ok {
			return nil, false
		}
		d, ok := s.lookup(id.Name, id.Pos(), sc)
		if !ok || d.kind != defRange || s.funcDefs(sc.fn).assigned[id.Name] {
			return nil, false
		}
		elems, st, ok := s.literalElems(d.expr, sc)
		if !ok {
			return nil, false
		}
		return s.eachString(elems, func(el ast.Expr) ([]string, bool) {
			v, ok := fieldOf(el, e.Sel.Name, st)
			if !ok {
				return nil, false
			}
			return s.stringValues(v, sc)
		})
	}
	return nil, false
}

func (s *scanner) eachString(elems []ast.Expr, f func(ast.Expr) ([]string, bool)) ([]string, bool) {
	var out []string
	for _, el := range elems {
		vs, ok := f(el)
		if !ok {
			return nil, false
		}
		out = append(out, vs...)
	}
	return out, len(out) > 0
}

// literalElems is the elements of the literal slice, array or map (its
// values) that x is, directly or through a local bound once to it, and the
// element type when that is an anonymous struct (for positional fields).
func (s *scanner) literalElems(x ast.Expr, sc scope) ([]ast.Expr, *ast.StructType, bool) {
	if id, ok := x.(*ast.Ident); ok {
		d, ok := s.lookup(id.Name, id.Pos(), sc)
		if !ok || d.kind != defExpr || s.funcDefs(sc.fn).assigned[id.Name] {
			return nil, nil, false
		}
		x = d.expr
	}
	cl, ok := x.(*ast.CompositeLit)
	if !ok {
		return nil, nil, false
	}
	var elt ast.Expr
	switch t := cl.Type.(type) {
	case *ast.ArrayType:
		elt = t.Elt
	case *ast.MapType:
		elt = t.Value
	default:
		return nil, nil, false
	}
	st, _ := elt.(*ast.StructType)
	elems := make([]ast.Expr, 0, len(cl.Elts))
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			el = kv.Value
		}
		elems = append(elems, el)
	}
	return elems, st, true
}

// fieldOf is the value a struct literal el gives field, keyed or (for an
// anonymous struct type st) positional.
func fieldOf(el ast.Expr, field string, st *ast.StructType) (ast.Expr, bool) {
	if u, ok := el.(*ast.UnaryExpr); ok && u.Op == token.AND {
		el = u.X
	}
	cl, ok := el.(*ast.CompositeLit)
	if !ok || len(cl.Elts) == 0 {
		return nil, false
	}
	if _, keyed := cl.Elts[0].(*ast.KeyValueExpr); keyed {
		for _, e := range cl.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
					return kv.Value, true
				}
			}
		}
		return nil, false
	}
	if st == nil {
		return nil, false
	}
	i := 0
	for _, f := range st.Fields.List {
		names := f.Names
		if len(names) == 0 {
			i++
			continue
		}
		for _, n := range names {
			if n.Name == field && i < len(cl.Elts) {
				return cl.Elts[i], true
			}
			i++
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Call sites.

type callSite struct {
	pos                 string
	call                string
	accessor, arg, name string
	data                func() *typ
}

var (
	prefilterAccessor = []byte("Templates(")
	prefilterRender   = []byte(".Render(")
)

func (s *scanner) callSites(skipped map[string]int) ([]callSite, error) {
	tmplDir, _ := s.dirOf(s.cfg.TemplatesPkg)
	tmplName := path.Base(s.cfg.TemplatesPkg)
	rels := make([]string, 0, len(s.files))
	for rel, src := range s.files {
		if bytes.Contains(src, prefilterAccessor) && bytes.Contains(src, prefilterRender) {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	var sites []callSite
	for _, rel := range rels {
		gf := s.parse(rel)
		if gf == nil || isIgnored(gf.f) {
			continue
		}
		unqualified := gf.dir == tmplDir && gf.f.Name.Name == tmplName
		for _, d := range gf.f.Decls {
			fn, _ := d.(*ast.FuncDecl)
			sc := scope{file: gf, fn: fn}
			ast.Inspect(d, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				matched, reason, ok := s.matchSite(call, sc, unqualified)
				if !ok {
					return true
				}
				if reason != "" {
					skipped[reason]++
					return true
				}
				sites = append(sites, matched...)
				return true
			})
		}
	}
	return sites, nil
}

// matchSite recognises `<Accessor>(<arg>).Render(<name>, <data>)`, one
// site per template name it can denote. ok is false for any other call;
// reason is set for a Render call it cannot read.
func (s *scanner) matchSite(call *ast.CallExpr, sc scope, unqualified bool) (sites []callSite, reason string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != "Render" || len(call.Args) != 2 {
		return nil, "", false
	}
	inner, isCall := sel.X.(*ast.CallExpr)
	if !isCall {
		return nil, "", false
	}
	var accName string
	switch f := inner.Fun.(type) {
	case *ast.Ident:
		if !unqualified {
			return nil, "", false
		}
		accName = f.Name
	case *ast.SelectorExpr:
		q, isIdent := f.X.(*ast.Ident)
		if !isIdent || s.importsOf(sc.file)[q.Name] != s.cfg.TemplatesPkg {
			return nil, "", false
		}
		accName = f.Sel.Name
	default:
		return nil, "", false
	}
	acc, known := s.cfg.Accessors[accName]
	if !known {
		if strings.HasSuffix(accName, "Templates") {
			return nil, "unknown template category " + accName, true
		}
		return nil, "", false
	}
	args := []string{""}
	if acc.TakesArg {
		if len(inner.Args) != 1 {
			return nil, "", false
		}
		vs, isStr := s.stringValues(inner.Args[0], sc)
		if !isStr {
			return nil, "non-literal category argument", true
		}
		args = vs
	} else if len(inner.Args) != 0 {
		return nil, "", false
	}
	names, isStr := s.stringValues(call.Args[0], sc)
	if !isStr {
		return nil, "non-literal template name", true
	}
	dataExpr := call.Args[1]
	var data *typ
	resolve := func() *typ {
		if data == nil {
			data = s.valueType(dataExpr, sc)
		}
		return data
	}
	pos := position(s.fset, call.Pos())
	for _, arg := range args {
		callArg := ""
		if acc.TakesArg {
			callArg = strconv.Quote(arg)
		}
		for _, name := range names {
			sites = append(sites, callSite{
				pos:      pos,
				call:     accName + "(" + callArg + ").Render(" + strconv.Quote(name) + ", …)",
				accessor: accName,
				arg:      arg,
				name:     name,
				data:     resolve,
			})
		}
	}
	return sites, "", true
}
