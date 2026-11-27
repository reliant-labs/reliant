package opindex

import (
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// Op kinds.
const (
	// KindComponent is an internal package method wrapped by the generated
	// observability decorator (middleware_gen.go). Its span is INTERNAL and
	// named `<pkg>.<Method>`.
	KindComponent = "component"
	// KindRPC is a Connect rpc. Its otelconnect span is SERVER and named
	// `<proto package>.<Service>/<Method>`.
	KindRPC = "rpc"
)

// decoratorFile is the generated observability decorator a component
// package carries (internal/generator/contract/observed.go).
const decoratorFile = "middleware_gen.go"

// Op is one operation of the project: a name that is exactly the span name
// its telemetry carries, and where its code lives.
type Op struct {
	// Name is the span name: `<pkg>.<Method>` for a component,
	// `<proto package>.<Service>/<Method>` for an rpc.
	Name string `json:"name"`
	// Kind is KindComponent or KindRPC.
	Kind string `json:"kind"`
	// Package is the project-relative slash directory of the Go package
	// that implements the op ("internal/billing", "internal/handlers/todo").
	// Empty for an rpc whose handler package was not found.
	Package string `json:"package,omitempty"`
	// File and Line locate the implementation: the implementing method of a
	// component op, the handler method of an rpc. When none is found they
	// fall back to the interface method (component) or the proto rpc line.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Function is the implementing method as Go spells its receiver:
	// "(*service).Send", "(*Service).CreateTodo"; "Service.Send" when only
	// the interface method was found. Empty when nothing was.
	Function string `json:"function,omitempty"`
	// RPCService and RPCMethod are an rpc op's `rpc.service` / `rpc.method`
	// span attributes: "todo.v1.TodoService" and "CreateTodo".
	RPCService string `json:"rpc_service,omitempty"`
	RPCMethod  string `json:"rpc_method,omitempty"`
	// ProtoFile and ProtoLine locate an rpc op's declaration.
	ProtoFile string `json:"proto_file,omitempty"`
	ProtoLine int    `json:"proto_line,omitempty"`
}

// Index is every op of one project, read from its working tree, plus the
// per-package call graphs a diff is mapped through. Build it with [Build].
type Index struct {
	dir  string
	ops  map[string]Op
	pkgs map[string]*pkgIndex
	// rpcsByMessage maps a fully qualified proto message to the rpc ops
	// taking or returning it, so a message change in a shared types.proto
	// reaches rpcs declared in other files.
	rpcsByMessage map[string][]string
	warnings      []string
}

// pkgIndex is one Go package that has ops.
type pkgIndex struct {
	dir   string
	ops   []string            // op names, sorted
	entry map[string][]string // graph node -> ops it implements (impl or handler method)
	reach map[string][]string // graph node -> ops whose entry reaches it (entries included)
}

// Dir is the absolute project directory the index was built from.
func (ix *Index) Dir() string { return ix.dir }

// Ops returns every op, sorted by name.
func (ix *Index) Ops() []Op {
	out := make([]Op, 0, len(ix.ops))
	for _, op := range ix.ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the op named name (a span name).
func (ix *Index) Lookup(name string) (Op, bool) {
	op, ok := ix.ops[name]
	return op, ok
}

// ByPackage returns the ops implemented in the project-relative package
// directory dir, sorted by name.
func (ix *Index) ByPackage(dir string) []Op {
	p := ix.pkgs[cleanRel(dir)]
	if p == nil {
		return nil
	}
	out := make([]Op, 0, len(p.ops))
	for _, name := range p.ops {
		out = append(out, ix.ops[name])
	}
	return out
}

// Warnings lists what Build skipped and why: a Go or proto file it could
// not read or parse (a tree mid-edit). The ops of such a file are missing.
func (ix *Index) Warnings() []string { return ix.warnings }

// Build indexes the project at projectDir from its working tree.
//
// Component ops come from every package directory holding a generated
// middleware_gen.go: each decorator method passes its op name to the chain
// as a string literal, which is read back here, so the index names exactly
// what the running code names its spans. Rpc ops come from every .proto
// under proto/, and their handler methods from the handler package
// codegen.ResolveServiceComponent resolves for the service.
func Build(projectDir string) (*Index, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, fmt.Errorf("opindex: %s is not a directory", abs)
	}
	ix := &Index{dir: abs, ops: map[string]Op{}, pkgs: map[string]*pkgIndex{}, rpcsByMessage: map[string][]string{}}

	components, err := componentDirs(abs)
	if err != nil {
		return nil, err
	}
	protos := ix.scanProtos()

	// Every package that implements an op: components, plus the handler
	// package of each proto service.
	type rpcRef struct {
		op  Op
		rpc protoRPC
	}
	handlerRPCs := map[string][]rpcRef{} // handler dir -> rpcs
	var unresolved []Op
	resolved := map[string]string{} // proto service name -> handler dir
	for _, pf := range protos {
		for _, r := range pf.file.RPCs {
			op := Op{
				Name: pf.file.opName(r), Kind: KindRPC,
				File: pf.path, Line: r.Line,
				RPCService: pf.file.service(r), RPCMethod: r.Name,
				ProtoFile: pf.path, ProtoLine: r.Line,
			}
			for _, t := range []string{r.In, r.Out} {
				if t != "" {
					fq := qualify(pf.file.Package, t)
					ix.rpcsByMessage[fq] = appendUnique(ix.rpcsByMessage[fq], op.Name)
				}
			}
			dir, ok := resolved[r.Service]
			if !ok {
				if res, rerr := codegen.ResolveServiceComponent(abs, r.Service); rerr == nil && res.FromDisk {
					dir = ix.rel(res.Dir)
				}
				resolved[r.Service] = dir
			}
			if dir == "" {
				unresolved = append(unresolved, op)
				continue
			}
			op.Package = dir
			handlerRPCs[dir] = append(handlerRPCs[dir], rpcRef{op, r})
		}
	}

	dirs := map[string]bool{}
	for _, d := range components {
		dirs[d] = true
	}
	for d := range handlerRPCs {
		dirs[d] = true
	}
	sorted := make([]string, 0, len(dirs))
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	for _, dir := range sorted {
		pkg := ix.loadPackage(dir)
		p := &pkgIndex{dir: dir, entry: map[string][]string{}, reach: map[string][]string{}}
		for _, op := range pkg.componentOps() {
			op.Package = dir
			ix.add(p, op)
		}
		for _, ref := range handlerRPCs[dir] {
			op := ref.op
			if m, ok := pkg.methods[methodKey("Service", ref.rpc.Name)]; ok {
				op.File, op.Line, op.Function = m.file, m.decl.Start, m.decl.Display
				p.entry[m.decl.Key] = append(p.entry[m.decl.Key], op.Name)
			}
			ix.add(p, op)
		}
		for key, impl := range pkg.entries {
			p.entry[key] = append(p.entry[key], impl...)
		}
		if len(p.ops) == 0 {
			continue
		}
		sort.Strings(p.ops)
		for node, ops := range p.entry {
			for _, op := range ops {
				for _, n := range pkg.graph.reach(node) {
					p.reach[n] = appendUnique(p.reach[n], op)
				}
			}
		}
		for n := range p.reach {
			sort.Strings(p.reach[n])
		}
		for n := range p.entry {
			sort.Strings(p.entry[n])
		}
		ix.pkgs[dir] = p
	}
	for _, op := range unresolved {
		if _, dup := ix.ops[op.Name]; !dup {
			ix.ops[op.Name] = op
		}
	}
	sort.Strings(ix.warnings)
	return ix, nil
}

// add records op in the index and in its package, first declaration winning.
func (ix *Index) add(p *pkgIndex, op Op) {
	if _, dup := ix.ops[op.Name]; dup {
		return
	}
	ix.ops[op.Name] = op
	p.ops = append(p.ops, op.Name)
}

func (ix *Index) rel(abs string) string {
	r, err := filepath.Rel(ix.dir, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

func (ix *Index) warnf(format string, args ...any) {
	ix.warnings = append(ix.warnings, fmt.Sprintf(format, args...))
}

// componentDirs lists the project-relative directories holding a generated
// observability decorator. vendor, node_modules, testdata, the root gen/,
// and hidden or `_`-prefixed directories are not Go packages of the project
// (or are generated), and are skipped.
func componentDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // an unreadable subtree is not the index's to fail on
		}
		if !d.IsDir() {
			if d.Name() == decoratorFile {
				rel, _ := filepath.Rel(root, filepath.Dir(p))
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
			name == "vendor" || name == "node_modules" || name == "testdata" ||
			filepath.ToSlash(rel) == "gen" {
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

type scannedProto struct {
	path string // project-relative
	file protoFile
}

// scanProtos reads every .proto under proto/, in path order.
func (ix *Index) scanProtos() []scannedProto {
	root := filepath.Join(ix.dir, "proto")
	var out []scannedProto
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".proto") {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			ix.warnf("%s: %v", ix.rel(p), rerr)
			return nil
		}
		out = append(out, scannedProto{path: ix.rel(p), file: parseProto(src)})
		return nil
	})
	return out
}

// loadedPackage is one package directory, parsed.
type loadedPackage struct {
	dir string
	// methods are the hand-written methods, by "<Recv>.<Name>".
	methods map[string]located
	// ifaceMethods are interface methods declared in hand-written files, by
	// "<Iface>.<Name>" — where a component op falls back to when no concrete
	// type implements it in this package.
	ifaceMethods map[string]located
	// concrete are the non-interface types declared in hand-written files.
	concrete map[string]bool
	ctorType string
	// wrapped are the decorator methods read from middleware_gen.go.
	wrapped []wrappedMethod
	graph   *callGraph
	entries map[string][]string // filled by componentOps
}

type located struct {
	file string
	decl goDecl
}

// wrappedMethod is one decorator method and the op literal it passes to the
// observe chain.
type wrappedMethod struct {
	method, op, iface string
}

func methodKey(recv, name string) string { return recv + "." + name }

// loadPackage parses dir's non-test Go files. Generated files take no part
// in the graph or the implementation search (the decorator and the mock
// would otherwise implement every op); middleware_gen.go is read only for
// its op literals.
func (ix *Index) loadPackage(dir string) *loadedPackage {
	pkg := &loadedPackage{
		dir:          dir,
		methods:      map[string]located{},
		ifaceMethods: map[string]located{},
		concrete:     map[string]bool{},
		entries:      map[string][]string{},
	}
	absDir := filepath.Join(ix.dir, filepath.FromSlash(dir))
	entries, err := os.ReadDir(absDir)
	if err != nil {
		ix.warnf("%s: %v", dir, err)
		pkg.graph = buildGraph(nil)
		return pkg
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		rel := path.Join(dir, name)
		if dir == "." {
			rel = name
		}
		src, rerr := os.ReadFile(filepath.Join(absDir, name))
		if rerr != nil {
			ix.warnf("%s: %v", rel, rerr)
			continue
		}
		f, fset, decls, perr := parseGoDecls(rel, src)
		if perr != nil {
			ix.warnf("%s: %v", rel, perr)
			continue
		}
		if name == decoratorFile {
			pkg.wrapped = append(pkg.wrapped, decoratorOps(f)...)
			continue
		}
		if isGeneratedPath(rel) {
			continue
		}
		files = append(files, f)
		for _, d := range decls {
			if d.Kind == declMethod {
				if _, dup := pkg.methods[d.Key]; !dup {
					pkg.methods[d.Key] = located{rel, d}
				}
			}
		}
		pkg.scanTypes(f, fset, rel)
		if pkg.ctorType == "" {
			pkg.ctorType = constructorType(f)
		}
	}
	pkg.graph = buildGraph(files)
	return pkg
}

// scanTypes records f's concrete types and interface methods.
func (pkg *loadedPackage) scanTypes(f *ast.File, fset *token.FileSet, rel string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			it, isIface := ts.Type.(*ast.InterfaceType)
			if !isIface {
				pkg.concrete[ts.Name.Name] = true
				continue
			}
			for _, m := range it.Methods.List {
				for _, n := range m.Names {
					key := methodKey(ts.Name.Name, n.Name)
					pkg.ifaceMethods[key] = located{rel, goDecl{
						Key: key, Display: key, Kind: declMethod, Recv: ts.Name.Name, Name: n.Name,
						Start: fset.Position(m.Pos()).Line, End: fset.Position(m.End()).Line,
					}}
				}
			}
		}
	}
}

// componentOps resolves each decorator method to the method implementing it
// and records that method as the op's graph entry.
//
// The implementation is a method of that name on a concrete type of the
// package. Several types may have one (the service and a cache it wraps
// both have Get): the type the package constructor returns wins, then the
// first in file order. With none, the op points at the interface method.
func (pkg *loadedPackage) componentOps() []Op {
	var out []Op
	for _, w := range pkg.wrapped {
		op := Op{Name: w.op, Kind: KindComponent}
		var impl *located
		if pkg.ctorType != "" {
			if m, ok := pkg.methods[methodKey(pkg.ctorType, w.method)]; ok {
				impl = &m
			}
		}
		if impl == nil {
			var cands []located
			for _, m := range pkg.methods {
				if m.decl.Name == w.method && pkg.concrete[m.decl.Recv] && !strings.HasPrefix(m.decl.Recv, "forgeMiddleware") {
					cands = append(cands, m)
				}
			}
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].file != cands[j].file {
					return cands[i].file < cands[j].file
				}
				return cands[i].decl.Start < cands[j].decl.Start
			})
			if len(cands) > 0 {
				impl = &cands[0]
			}
		}
		switch {
		case impl != nil:
			op.File, op.Line, op.Function = impl.file, impl.decl.Start, impl.decl.Display
			pkg.entries[impl.decl.Key] = append(pkg.entries[impl.decl.Key], op.Name)
		default:
			if m, ok := pkg.ifaceMethods[methodKey(w.iface, w.method)]; ok {
				op.File, op.Line, op.Function = m.file, m.decl.Start, m.decl.Display
			}
		}
		out = append(out, op)
	}
	return out
}

// decoratorOps reads the op literals out of a generated decorator: every
// method whose body routes through `<recv>.chain.Run(ctx, "<op>", …)` or
// `<recv>.chain.Around(ctx, "<op>", …)`. A `// forge:no-observe` method,
// or a lifecycle method (Run, Drain, Close, Ready; exempt unless marked
// `// forge:observe`), delegates directly, has no span, and so is no op. The wrapped interface is
// the type of the decorator struct's `inner` field.
func decoratorOps(f *ast.File) []wrappedMethod {
	inner := map[string]string{} // decorator struct -> wrapped interface
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, fld := range st.Fields.List {
				if id, ok := fld.Type.(*ast.Ident); ok && len(fld.Names) == 1 && fld.Names[0].Name == "inner" {
					inner[ts.Name.Name] = id.Name
				}
			}
		}
	}
	var out []wrappedMethod
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil || len(fn.Recv.List) == 0 {
			continue
		}
		recv, _ := recvType(fn.Recv.List[0].Type)
		op := ""
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if op != "" {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Run" && sel.Sel.Name != "Around") {
				return true
			}
			if x, ok := sel.X.(*ast.SelectorExpr); !ok || x.Sel.Name != "chain" {
				return true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					op = s
				}
			}
			return true
		})
		if op != "" {
			out = append(out, wrappedMethod{method: fn.Name.Name, op: op, iface: inner[recv]})
		}
	}
	return out
}

// constructorType is the concrete type the package constructor (New, or a
// `// forge:constructor` func) builds, when f declares it: its declared
// result if that is a named type, else the composite literal it returns
// (`return &service{…}`, or a local assigned from one).
func constructorType(f *ast.File) string {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !codegen.IsComponentConstructor(fn) || fn.Body == nil {
			continue
		}
		locals := map[string]string{}
		found := ""
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if found != "" {
				return false
			}
			switch n := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && i < len(n.Rhs) {
						if t := literalType(n.Rhs[i]); t != "" {
							locals[id.Name] = t
						}
					}
				}
			case *ast.ReturnStmt:
				if len(n.Results) == 0 {
					return true
				}
				if t := literalType(n.Results[0]); t != "" {
					found = t
				} else if id, ok := n.Results[0].(*ast.Ident); ok {
					found = locals[id.Name]
				}
			}
			return true
		})
		if found != "" {
			return found
		}
		// No literal: the declared result (`func New() *service`). When that
		// is the contract interface no method is declared on it, and the
		// implementation search falls through to the other candidates.
		if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
			if t, _ := recvType(fn.Type.Results.List[0].Type); t != "" {
				return t
			}
		}
	}
	return ""
}

// literalType names the type of `&T{…}` or `T{…}`.
func literalType(e ast.Expr) string {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	if cl, ok := e.(*ast.CompositeLit); ok {
		if t, _ := recvType(cl.Type); t != "" {
			return t
		}
	}
	return ""
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// cleanRel normalizes a project-relative directory: slash-separated, no
// leading "./" or trailing "/", "." for the root.
func cleanRel(dir string) string {
	return path.Clean(filepath.ToSlash(dir))
}
