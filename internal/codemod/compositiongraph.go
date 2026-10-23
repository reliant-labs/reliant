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
	"golang.org/x/tools/imports"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// compositiongraph.go — `forge fix composition-graph`: move a project off the
// legacy composition (the owned Infra / OpenInfra in providers.go, the
// Components / NewComponents bag in compose.go, lifecycle.go, ServeSpec and
// the per-service / per-worker / per-operator subcommands) onto the
// composition graph (docs/design/composition-graph.md).
//
// What it converts is the composition forge scaffolded and reconciled. The
// conversion is all or nothing:
//
//   - Infra's stock fields become the graph's inputs and stock providers
//     (Log → g.Log, Cfg → g.Cfg, DB → g.DB().DB, ORM → g.ORM(), the default
//     HTTP client → g.HTTPClient()); providers.go is re-scaffolded.
//   - Each NewComponents construction becomes a graph constructor method in
//     compose.go, its Deps values translated (infra.X → the graph's node,
//     c.X and producer locals → g.X()). When every value is the
//     conventional Logger/Config pair, compose.go is re-scaffolded instead.
//   - lifecycle.go, server.go and the cmd/<bin>/cmd/{services,workers,
//     operators} subcommands go; serve.go is re-scaffolded as runRoot;
//     root.go and main.go stop naming the retired commands; the e2e
//     harness runs `server`.
//
// Anything else — an Infra field forge did not put there, a ServeSpec knob,
// a hand-written command that calls Serve, code that names a retired symbol
// — is a shape only its author can map onto roots and providers. Each is
// reported with its file:line and the edit that clears it, and nothing is
// written: a conversion that compiles but quietly changes what a process
// serves (collapsing two deployment groups into one `server`, say) is worse
// than none. migrations/v0.1.47 is the manual path, with control-plane's
// ServeSpec knobs as the worked example.

func init() {
	register(Codemod{
		Name:    "composition-graph",
		Version: "v0.1.47",
		Short:   "Move a project off Infra / NewComponents / ServeSpec onto the composition graph",
		Long: `Convert the legacy composition to the composition graph.

A project scaffolded before the graph composes with an owned Infra struct and
OpenInfra (internal/app/providers.go), a Components bag built by NewComponents
(compose.go), lifecycle.go, ServeSpec (cmd/<bin>/cmd/serve.go) and one
subcommand per service, worker and operator. forge still generates for it
exactly as before. This codemod converts the composition forge scaffolded:

  - providers.go, compose.go and serve.go are re-scaffolded for the graph on
    the next ` + "`forge generate`" + ` (compose.go keeps every Deps value you set,
    translated: infra.Log → g.Log, infra.DB → g.DB().DB, c.Tasks → g.Tasks());
  - lifecycle.go, server.go and the per-component subcommands are retired,
    and root.go / main.go stop naming them;
  - the e2e harness runs ` + "`server`" + `, the one root a project has until it
    declares more.

The default root, ` + "`server`" + `, reaches every service, worker and operator,
which is what ` + "`<bin> server`" + ` ran. Anything the codemod cannot map without
deciding for you — an Infra field of your own, a ServeSpec knob, a command
that calls Serve, code that names Infra or Components — is listed with its
file:line and the edit that clears it, and then NOTHING is written. Clear
them (the migrations/v0.1.47 skill walks through each) and run it again.

It replaces files you own, so it runs only on a committed tree
(--allow-dirty overrides); review with git diff, undo with git checkout.`,
		CleanTree: true,
		Run:       runCompositionGraph,
	})
}

// The legacy names the graph retires, by the package that declared them.
var (
	legacyAppNames = map[string]bool{
		"Infra": true, "OpenInfra": true, "InfraOption": true, "NewComponents": true, "Components": true,
		"OperatorEntry": true, "RunOperators": true, "HasOperators": true, "MountFunc": true,
	}
	legacyCmdNames = map[string]bool{"Serve": true, "ServeSpec": true, "MountFunc": true, "MountNone": true, "newServerCmd": true}
	// stockInfraFields are the fields forge's providers.go template declared.
	stockInfraFields = map[string]bool{"Log": true, "Cfg": true, "DB": true, "ORM": true, "httpBase": true, "httpBaseOnce": true}
	// stockServeSpec are ServeSpec's template fields.
	stockServeSpec = map[string]bool{"Mount": true, "Workers": true, "Operators": true, "RequireComplete": true}
	// groupDirs are the retired per-component command groups.
	groupDirs = []string{"services", "workers", "operators"}
)

// cgPlan is the conversion, decided before anything is written.
type cgPlan struct {
	dir, module, bin, tree string

	deletes []string           // project-relative files to delete
	rebirth []string           // deleted files forge scaffolds again (birth record dropped)
	writes  map[string][]byte  // project-relative path → new content
	files   map[string]*cgFile // parsed owned Go files, by path
	gone    map[string]bool    // files deleted or wholly replaced
	retired []string           // retired subcommand names (`<bin> <name>`)
	blocked []string
	notes   []string
	skipRef map[string]bool // file:line references a rewrite already handles
}

type cgFile struct {
	rel  string
	src  []byte
	fset *token.FileSet
	ast  *ast.File
}

func (p *cgPlan) block(f *cgFile, pos token.Pos, format string, a ...any) {
	where := f.rel
	if pos.IsValid() {
		where = fmt.Sprintf("%s:%d", f.rel, f.fset.Position(pos).Line)
	}
	p.blocked = append(p.blocked, where+": "+fmt.Sprintf(format, a...))
}

func (p *cgPlan) remove(rel string, rebirth bool) {
	if p.gone[rel] {
		return
	}
	p.gone[rel] = true
	p.deletes = append(p.deletes, rel)
	if rebirth {
		p.rebirth = append(p.rebirth, rel)
	}
}

func runCompositionGraph(dir string, opts Options) (Report, error) {
	write := opts.Write
	if _, err := os.Stat(filepath.Join(dir, "internal", "app")); os.IsNotExist(err) {
		return handComposedReport(dir), nil
	}
	if codegen.GraphComposition(dir) {
		return Report{}, nil
	}
	module, err := codegen.GetModulePath(dir)
	if err != nil {
		return Report{}, err
	}
	p := &cgPlan{dir: dir, module: module, writes: map[string][]byte{}, files: map[string]*cgFile{}, gone: map[string]bool{}, skipRef: map[string]bool{}}
	if err := p.load(); err != nil {
		return Report{}, err
	}
	if p.tree == "" {
		p.blocked = append(p.blocked, "cmd/: no command tree at cmd/<bin>/cmd/root.go, which is where the graph writes roots_gen.go — move the cobra tree there first")
	} else {
		p.planCmdTree()
	}
	p.planProviders()
	p.planCompose()
	p.planLifecycle()
	p.planAPIKeyValidator()
	p.planLegacyGenerated()
	p.planE2E()
	p.planKCL()
	p.scanReferences()

	rep := Report{Verify: []string{"forge generate", "go build ./...", "forge lint --graph"}}
	if len(p.blocked) > 0 {
		sort.Strings(p.blocked)
		rep.Blocked = p.blocked
		rep.Next = []string{"clear each item above (`forge skill load migrations/v0.1.47` walks through them), then run `forge fix composition-graph` again — nothing was written"}
		rep.Verify = nil
		return rep, nil
	}

	for _, rel := range p.deletes {
		rep.Rewrites = append(rep.Rewrites, "delete "+rel+map[bool]string{true: " (forge scaffolds the graph's on the next generate)", false: ""}[contains(p.rebirth, rel)])
	}
	writes := make([]string, 0, len(p.writes))
	for rel := range p.writes {
		writes = append(writes, rel)
	}
	sort.Strings(writes)
	for _, rel := range writes {
		rep.Rewrites = append(rep.Rewrites, "rewrite "+rel)
	}
	rep.Next = append(rep.Next,
		"run `forge generate`: it scaffolds providers.go, serve.go (runRoot) and the generated graph (graph_gen.go, routes_gen.go, roots_gen.go), then `go build ./...` and `forge lint --graph`",
		fmt.Sprintf("review `git diff HEAD -- %s/serve.go`: an edit you made to the old Serve — an interceptor, CORS, edge middleware — now belongs in providers.go (NewUserChain, NewAPIListener) or in runRoot (process policy)", p.tree),
		"the one root is `server`, the generated default: every service, worker and operator, as `"+p.bin+" server` ran. Split a process by declaring a `//forge:root <name>` struct in internal/app (`forge skill load architecture`, \"Roots and commands\")",
	)
	if len(p.retired) > 0 {
		rep.Next = append(rep.Next, fmt.Sprintf("retired subcommands (one root runs them all now): `%s %s`", p.bin, strings.Join(p.retired, "`, `"+p.bin+" ")))
	}
	rep.Next = append(rep.Next, "deploy/kcl is untouched and still runs `server`; to give each workload exactly its root's config, bind it with `config_gen.root_of(\"server\")` and project its env with `config_gen.workloadEnvMap(cfg, w)` (migrations/v0.1.47 — this changes the rendered env)")
	rep.Next = append(rep.Next, p.notes...)
	if !write {
		return rep, nil
	}
	for _, rel := range writes {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), p.writes[rel]); err != nil {
			return Report{}, err
		}
	}
	for _, rel := range p.deletes {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			return Report{}, err
		}
	}
	for _, rel := range p.rebirth {
		checksums.ForgetScaffold(dir, rel)
	}
	for _, g := range groupDirs {
		removeIfEmpty(filepath.Join(dir, filepath.FromSlash(p.tree), g))
	}
	return rep, nil
}

// handComposedReport is the answer for a project with no internal/app: it
// composes by hand (its own mains over serverkit / lifecyclekit), so there is
// no legacy composition to convert — and no graph either. Adoption is the
// incremental path of the design (§6), one root at a time; say so rather
// than "already converted". A project with no command tree is a library or
// CLI and gets nothing.
func handComposedReport(dir string) Report {
	mains, _ := filepath.Glob(filepath.Join(dir, "cmd", "*", "main.go"))
	if len(mains) == 0 {
		return Report{}
	}
	next := []string{
		"no internal/app: this project composes its processes by hand, so there is no Infra / NewComponents composition to convert. The graph is adopted incrementally (docs/design/composition-graph.md §6): declare each process as a `//forge:root <name> command=<cmd>` struct in internal/app whose fields are the nodes its hand-written main builds today, give each node a provider (`func New<X>(<X>Deps) (T, error)`) or a component package, run `forge generate`, then delete the hand-written main",
	}
	if trees, _ := filepath.Glob(filepath.Join(dir, "cmd", "*", "cmd", "root.go")); len(trees) == 0 {
		// The binary whose cobra tree sits beside its main in a directory
		// of another name (reliant: cmd/reliant/commands/root.go).
		if elsewhere, _ := filepath.Glob(filepath.Join(dir, "cmd", "*", "*", "root.go")); len(elsewhere) > 0 {
			sort.Strings(elsewhere)
			tree, _ := filepath.Rel(dir, filepath.Dir(elsewhere[0]))
			bin := filepath.Base(filepath.Dir(filepath.Dir(elsewhere[0])))
			next = append(next, fmt.Sprintf("forge writes roots_gen.go and reads runRoot from the command tree at cmd/<bin>/cmd (root.go, serve.go); %s's is %s, so move it to cmd/%s/cmd (package name unchanged) before declaring roots", bin, filepath.ToSlash(tree), bin))
		}
	}
	return Report{Next: next}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// load parses the project's owned Go files and finds the command tree.
func (p *cgPlan) load() error {
	err := ownedGoFiles(p.dir, func(rel string, src []byte) error {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		p.files[rel] = &cgFile{rel: rel, src: src, fset: fset, ast: f}
		return nil
	})
	if err != nil {
		return err
	}
	roots, _ := filepath.Glob(filepath.Join(p.dir, "cmd", "*", "cmd", "root.go"))
	sort.Strings(roots)
	if len(roots) > 0 {
		rel, _ := filepath.Rel(p.dir, filepath.Dir(roots[0]))
		p.tree = filepath.ToSlash(rel)
		p.bin = filepath.Base(filepath.Dir(filepath.Dir(roots[0])))
	}
	return nil
}

// appFiles are the owned, non-test Go files of internal/app.
func (p *cgPlan) appFiles() []*cgFile {
	var out []*cgFile
	for rel, f := range p.files {
		if path.Dir(rel) == "internal/app" && !strings.HasSuffix(rel, "_test.go") {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func (p *cgPlan) findFunc(name string, recv bool) (*cgFile, *ast.FuncDecl) {
	for _, f := range p.appFiles() {
		for _, d := range f.ast.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name && (fd.Recv != nil) == recv {
				return f, fd
			}
		}
	}
	return nil, nil
}

func (p *cgPlan) findType(name string) (*cgFile, *ast.TypeSpec) {
	for _, f := range p.appFiles() {
		for _, d := range f.ast.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				if ts := s.(*ast.TypeSpec); ts.Name.Name == name {
					return f, ts
				}
			}
		}
	}
	return nil, nil
}

// recvType is the receiver's base type name ("Infra" for (i *Infra)).
func recvType(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// blankVar reports whether d is `var _ = …` / `var _ T` (an import keeper).
func blankVar(d ast.Decl) bool {
	gd, ok := d.(*ast.GenDecl)
	if !ok || gd.Tok != token.VAR {
		return false
	}
	for _, s := range gd.Specs {
		vs := s.(*ast.ValueSpec)
		for _, n := range vs.Names {
			if n.Name != "_" {
				return false
			}
		}
	}
	return true
}

func isImports(d ast.Decl) bool {
	gd, ok := d.(*ast.GenDecl)
	return ok && gd.Tok == token.IMPORT
}

// declName names a top-level declaration for a report.
func declName(d ast.Decl) string {
	switch x := d.(type) {
	case *ast.FuncDecl:
		if r := recvType(x); r != "" {
			return "(" + r + ")." + x.Name.Name
		}
		return x.Name.Name
	case *ast.GenDecl:
		for _, s := range x.Specs {
			switch sp := s.(type) {
			case *ast.TypeSpec:
				return "type " + sp.Name.Name
			case *ast.ValueSpec:
				if len(sp.Names) > 0 {
					return x.Tok.String() + " " + sp.Names[0].Name
				}
			}
		}
	}
	return "declaration"
}

// planProviders: Infra must hold only the fields forge's template declared,
// and providers.go only the template's declarations; then it is
// re-scaffolded.
func (p *cgPlan) planProviders() {
	f, ts := p.findType("Infra")
	if f == nil {
		return
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, fld := range st.Fields.List {
		for _, n := range fld.Names {
			if !stockInfraFields[n.Name] {
				p.block(f, n.Pos(), "Infra.%s (%s) is infrastructure forge did not scaffold: make it a provider in providers.go — `func New%s(%sDeps) (%s, error)`, built from the lines of OpenInfra that set it — so a root that does not reach it never opens it", n.Name, exprString(f, fld.Type), upperFirst(n.Name), upperFirst(n.Name), exprString(f, fld.Type))
			}
		}
		if len(fld.Names) == 0 {
			p.block(f, fld.Pos(), "Infra embeds %s: make what it provides a provider func in providers.go", exprString(f, fld.Type))
		}
	}
	stock := map[string]bool{"type Infra": true, "(Infra).DefaultClient": true, "(Infra).transport": true, "OpenInfra": true, "openDatabase": true}
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) || stock[declName(d)] {
			continue
		}
		p.block(f, d.Pos(), "%s: %s is re-scaffolded for the graph, so this declaration needs a file of its own in internal/app first (a provider, if it opens something: `func New<X>(<X>Deps) (T, error)`)", declName(d), f.rel)
	}
	if _, fd := p.findFunc("OpenInfra", false); fd != nil && (fd.Type.Params.NumFields() != 3 || hasVariadic(fd)) {
		p.block(f, fd.Pos(), "OpenInfra takes options (%s): what each option switches on is a component a root reaches or not — declare roots, and make what the option guarded a provider only those roots reach", exprString(f, fd.Type.Params))
	}
	p.remove(f.rel, f.rel == "internal/app/providers.go")
	if f.rel != "internal/app/providers.go" {
		if other, ok := p.files["internal/app/providers.go"]; ok {
			p.block(other, token.NoPos, "providers.go is re-scaffolded for the graph, and Infra lives in %s: move what providers.go declares into another file first", f.rel)
		} else {
			p.rebirth = append(p.rebirth, "internal/app/providers.go")
		}
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func hasVariadic(fd *ast.FuncDecl) bool {
	for _, fld := range fd.Type.Params.List {
		if _, ok := fld.Type.(*ast.Ellipsis); ok {
			return true
		}
	}
	return false
}

func exprString(f *cgFile, n ast.Node) string {
	start, end := f.fset.Position(n.Pos()).Offset, f.fset.Position(n.End()).Offset
	if start < 0 || end > len(f.src) || start > end {
		return "?"
	}
	return strings.Join(strings.Fields(string(f.src[start:end])), " ")
}

// cgNode is one NewComponents construction.
type cgNode struct {
	field, typ   string // Components field and its type
	alias, ctor  string
	fallible     bool
	wrap         string // "alias.NewWithMiddleware" when wrapped
	keys         []string
	values       map[string]string // translated
	conventional bool
}

// planCompose translates NewComponents into graph constructor methods.
func (p *cgPlan) planCompose() {
	f, fd := p.findFunc("NewComponents", false)
	if f == nil {
		return
	}
	fieldType := map[string]string{}
	if cf, ts := p.findType("Components"); cf != nil {
		if st, ok := ts.Type.(*ast.StructType); ok {
			for _, fld := range st.Fields.List {
				for _, n := range fld.Names {
					fieldType[n.Name] = exprString(cf, fld.Type)
				}
			}
		}
		if cf != f {
			p.block(cf, ts.Pos(), "Components is declared outside %s: move it beside NewComponents", f.rel)
		}
	}
	infraName, compName := "", "c"
	if fd.Type.Params.NumFields() == 1 && len(fd.Type.Params.List[0].Names) == 1 {
		infraName = fd.Type.Params.List[0].Names[0].Name
	}

	nodes := p.readConstructions(f, fd, infraName, compName)
	for _, n := range nodes {
		n.typ = fieldType[n.field]
		if n.typ == "" {
			p.block(f, fd.Pos(), "NewComponents sets c.%s, which Components does not declare", n.field)
		}
	}

	// Other declarations in compose.go: carried over unless they name the
	// legacy composition.
	var carried []string
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) || d == fd {
			continue
		}
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE && declName(d) == "type Components" {
			continue
		}
		if namesLegacy(d, legacyAppNames) {
			p.block(f, d.Pos(), "%s names the legacy composition (Infra / Components): rewrite it over the graph's nodes before converting", declName(d))
			continue
		}
		carried = append(carried, exprString0(f, d))
	}

	conventional := len(carried) == 0
	for _, n := range nodes {
		conventional = conventional && n.conventional
	}
	if conventional {
		p.remove(f.rel, f.rel == "internal/app/compose.go")
		return
	}
	src, err := renderGraphCompose(f, nodes, carried)
	if err != nil {
		p.block(f, token.NoPos, "the translated compose.go does not format (%v) — convert it by hand", err)
		return
	}
	p.gone[f.rel] = true
	p.writes[f.rel] = src
}

// readConstructions reads NewComponents' body as the constructions forge
// writes — `x, err := pkg.New(pkg.Deps{…})` / `c.X = …` — blocking on any
// other statement.
func (p *cgPlan) readConstructions(f *cgFile, fd *ast.FuncDecl, infraName, compName string) []*cgNode {
	// Pass 1: which local holds which component.
	producer := map[string]string{}
	for _, st := range fd.Body.List {
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if field := compSelector(as.Lhs[0], compName); field != "" {
				if id := wrappedIdent(as.Rhs[0]); id != "" {
					producer[id] = field
				}
			}
		}
	}

	var nodes []*cgNode
	pending := map[string]*cgNode{} // local → node awaiting its c.F assignment
	list := fd.Body.List
	for i := 0; i < len(list); i++ {
		st := list[i]
		switch s := st.(type) {
		case *ast.AssignStmt:
			if i == 0 && len(s.Lhs) == 1 && isIdent(s.Lhs[0], compName) && s.Tok == token.DEFINE {
				if !isEmptyComponentsLit(s.Rhs[0]) {
					p.block(f, s.Pos(), "NewComponents starts from %s, not an empty Components: each field set there is an edge compose.go has to name — write it as that node's Deps value", exprString(f, s.Rhs[0]))
				}
				continue
			}
			// x, err := alias.New(alias.Deps{…})
			if len(s.Lhs) == 2 && s.Tok == token.DEFINE && len(s.Rhs) == 1 && isIdent(s.Lhs[1], "err") {
				local, _ := s.Lhs[0].(*ast.Ident)
				n := p.ctorNode(f, s.Rhs[0], infraName, compName, producer)
				if n == nil || local == nil {
					p.block(f, s.Pos(), "NewComponents: `%s` is not a forge-shaped construction (`x, err := pkg.New(pkg.Deps{…})`); compose.go needs it as a graph constructor method — write it by hand after converting", exprString(f, s))
					continue
				}
				n.fallible = true
				pending[local.Name] = n
				if i+1 < len(list) {
					if _, isIf := list[i+1].(*ast.IfStmt); isIf {
						i++ // the `if err != nil { return … }` guard
					}
				}
				continue
			}
			// c.F = local | c.F = alias.Wrap(local) | c.F = alias.New(…) | c.F = alias.Wrap(alias.New(…))
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 && s.Tok == token.ASSIGN {
				field := compSelector(s.Lhs[0], compName)
				if field == "" {
					break
				}
				rhs, wrap := unwrap(f, s.Rhs[0])
				if id, ok := rhs.(*ast.Ident); ok && pending[id.Name] != nil {
					n := pending[id.Name]
					delete(pending, id.Name)
					n.field, n.wrap = field, wrap
					nodes = append(nodes, n)
					continue
				}
				if n := p.ctorNode(f, rhs, infraName, compName, producer); n != nil {
					n.field, n.wrap = field, wrap
					nodes = append(nodes, n)
					continue
				}
				p.block(f, s.Pos(), "NewComponents: `%s` is not a forge-shaped construction; write c.%s's graph constructor method by hand after converting", exprString(f, s), field)
				continue
			}
		case *ast.ReturnStmt:
			if len(s.Results) == 2 && isIdent(s.Results[0], compName) && isIdent(s.Results[1], "nil") {
				continue
			}
		}
		p.block(f, st.Pos(), "NewComponents: `%s` is wiring forge did not scaffold — express it as a Deps value in that node's graph constructor method, or a two-phase setter (`forge skill load architecture`)", firstLine(exprString(f, st)))
	}
	for local := range pending {
		p.block(f, fd.Pos(), "NewComponents constructs %s but never stores it on Components", local)
	}
	return nodes
}

// exprString0 is a node's exact source text (comments and layout kept).
func exprString0(f *cgFile, n ast.Node) string {
	start := f.fset.Position(n.Pos()).Offset
	if d, ok := n.(*ast.FuncDecl); ok && d.Doc != nil {
		start = f.fset.Position(d.Doc.Pos()).Offset
	}
	if d, ok := n.(*ast.GenDecl); ok && d.Doc != nil {
		start = f.fset.Position(d.Doc.Pos()).Offset
	}
	return string(f.src[start:f.fset.Position(n.End()).Offset])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	if len(s) > 80 {
		return s[:80] + " …"
	}
	return s
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isEmptyComponentsLit(e ast.Expr) bool {
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return false
	}
	cl, ok := u.X.(*ast.CompositeLit)
	return ok && isIdent(cl.Type, "Components") && len(cl.Elts) == 0
}

// compSelector returns F for `c.F`.
func compSelector(e ast.Expr, comp string) string {
	sel, ok := e.(*ast.SelectorExpr)
	if ok && isIdent(sel.X, comp) {
		return sel.Sel.Name
	}
	return ""
}

// wrappedIdent returns x for `x` or `alias.Wrap(x)`.
func wrappedIdent(e ast.Expr) string {
	if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 {
		e = call.Args[0]
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// unwrap peels a middleware constructor (`alias.Wrap(inner)` whose inner is
// a local or a constructor call), returning inner and "alias.Wrap".
func unwrap(f *cgFile, e ast.Expr) (ast.Expr, string) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return e, ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return e, ""
	}
	switch inner := call.Args[0].(type) {
	case *ast.Ident:
		return inner, exprString(f, sel)
	case *ast.CallExpr:
		if len(inner.Args) == 1 {
			if _, isLit := inner.Args[0].(*ast.CompositeLit); isLit {
				return inner, exprString(f, sel)
			}
		}
	}
	return e, ""
}

// ctorNode reads `alias.New(alias.Deps{K: V, …})`, translating each value.
func (p *cgPlan) ctorNode(f *cgFile, e ast.Expr, infra, comp string, producer map[string]string) *cgNode {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil
	}
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	alias, ok := fn.X.(*ast.Ident)
	if !ok {
		return nil
	}
	lit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return nil
	}
	if lt, ok := lit.Type.(*ast.SelectorExpr); !ok || !isIdent(lt.X, alias.Name) || lt.Sel.Name != "Deps" {
		return nil
	}
	n := &cgNode{alias: alias.Name, ctor: fn.Sel.Name, values: map[string]string{}, conventional: true}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			p.block(f, el.Pos(), "%s.Deps has an unkeyed value", alias.Name)
			continue
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			continue
		}
		v, ok := p.translate(f, kv.Value, infra, comp, producer)
		if !ok {
			continue
		}
		n.keys = append(n.keys, key.Name)
		n.values[key.Name] = v
		src := exprString(f, kv.Value)
		if !(key.Name == "Logger" && src == infra+".Log") && !(key.Name == "Config" && src == infra+".Cfg") {
			n.conventional = false
		}
	}
	return n
}

// translate rewrites one legacy Deps value onto the graph.
func (p *cgPlan) translate(f *cgFile, e ast.Expr, infra, comp string, producer map[string]string) (string, bool) {
	imported := map[string]bool{}
	for _, im := range f.ast.Imports {
		name := path.Base(strings.Trim(im.Path.Value, `"`))
		if im.Name != nil {
			name = im.Name.Name
		}
		imported[name] = true
	}
	ok := true
	out := astutil.Apply(cloneExpr(e), func(c *astutil.Cursor) bool {
		switch x := c.Node().(type) {
		case *ast.CallExpr:
			if sel, isSel := x.Fun.(*ast.SelectorExpr); isSel && isIdent(sel.X, infra) && sel.Sel.Name == "DefaultClient" && len(x.Args) == 0 {
				c.Replace(call("g", "HTTPClient"))
				return false
			}
		case *ast.SelectorExpr:
			switch {
			case isIdent(x.X, infra):
				switch x.Sel.Name {
				case "Log", "Cfg":
					c.Replace(&ast.SelectorExpr{X: ast.NewIdent("g"), Sel: ast.NewIdent(x.Sel.Name)})
				case "ORM":
					c.Replace(call("g", "ORM"))
				case "DB":
					c.Replace(&ast.SelectorExpr{X: call("g", "DB"), Sel: ast.NewIdent("DB")})
				default:
					p.block(f, e.Pos(), "a Deps value reads %s.%s, which is no graph node: make it a provider (see Infra above)", infra, x.Sel.Name)
					ok = false
				}
				return false
			case isIdent(x.X, comp):
				c.Replace(call("g", x.Sel.Name))
				return false
			}
			if id, isID := x.X.(*ast.Ident); isID && imported[id.Name] {
				return false // pkg.Name
			}
		case *ast.Ident:
			if field, isProducer := producer[x.Name]; isProducer {
				c.Replace(call("g", field))
				return false
			}
			if x.Name == infra || x.Name == comp {
				p.block(f, e.Pos(), "a Deps value hands on %s whole — a component receives the capabilities it uses, never the container (forgeconv-no-container-deps)", x.Name)
				ok = false
				return false
			}
			if x.Obj != nil {
				p.block(f, e.Pos(), "a Deps value reads the local %s: the graph has no locals — make it a node, or write the value by hand after converting", x.Name)
				ok = false
			}
		}
		return true
	}, nil)
	if !ok {
		return "", false
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), out); err != nil {
		return "", false
	}
	return buf.String(), true
}

func call(recv, method string) *ast.CallExpr {
	return &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent(recv), Sel: ast.NewIdent(method)}}
}

// cloneExpr re-parses an expression so it can be rewritten without the
// original file's positions.
func cloneExpr(e ast.Expr) ast.Expr {
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), e); err != nil {
		return e
	}
	out, err := parser.ParseExpr(buf.String())
	if err != nil {
		return e
	}
	return out
}

const graphComposeHeader = `// forge-scaffold (USER-OWNED, RECONCILED): the composition graph's wiring —
// one constructor method per node, holding the value expression for every
// field of that node's Deps. Those expressions are yours. Converted from the
// legacy NewComponents by ` + "`forge fix composition-graph`" + `; every
// ` + "`forge generate`" + ` adds the methods and Deps keys a node lacks, drops a key
// whose field is gone, and never touches a value.
`

// renderGraphCompose writes compose.go for nodes, in the graph's method
// shape, plus the declarations carried over from the legacy file.
func renderGraphCompose(f *cgFile, nodes []*cgNode, carried []string) ([]byte, error) {
	var b strings.Builder
	b.WriteString(graphComposeHeader)
	b.WriteString("package app\n\nimport (\n")
	for _, im := range f.ast.Imports {
		if im.Name != nil {
			fmt.Fprintf(&b, "\t%s %s\n", im.Name.Name, im.Path.Value)
		} else {
			fmt.Fprintf(&b, "\t%s\n", im.Path.Value)
		}
	}
	b.WriteString(")\n")
	for _, n := range nodes {
		var lit strings.Builder
		lit.WriteString(n.alias + ".Deps{\n")
		for _, k := range n.keys {
			fmt.Fprintf(&lit, "\t\t%s: %s,\n", k, n.values[k])
		}
		lit.WriteString("\t}")
		ctor := n.alias + "." + n.ctor + "(" + lit.String() + ")"
		fmt.Fprintf(&b, "\n// new%s constructs %s.\nfunc (g *Graph) new%s() (%s, error) {\n", n.field, n.field, n.field, n.typ)
		switch {
		case n.wrap != "" && n.fallible:
			fmt.Fprintf(&b, "\tsvc, err := %s\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn %s(svc), nil\n", ctor, n.wrap)
		case n.wrap != "":
			fmt.Fprintf(&b, "\treturn %s(%s), nil\n", n.wrap, ctor)
		case n.fallible:
			fmt.Fprintf(&b, "\treturn %s\n", ctor)
		default:
			fmt.Fprintf(&b, "\treturn %s, nil\n", ctor)
		}
		b.WriteString("}\n")
	}
	for _, c := range carried {
		b.WriteString("\n" + c + "\n")
	}
	return imports.Process("compose.go", []byte(b.String()), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8, FormatOnly: false})
}

// namesLegacy reports whether d mentions one of names as an identifier.
func namesLegacy(d ast.Node, names map[string]bool) bool {
	found := false
	ast.Inspect(d, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// planLifecycle: the file holding AllWorkers / RunOperators must be the
// template's — WrapWorker accessors and the stock operator bridge — and then
// goes: the graph supervises workers and runs operators itself.
func (p *cgPlan) planLifecycle() {
	f, _ := p.findFunc("AllWorkers", true)
	if f == nil {
		if f, _ = p.findFunc("RunOperators", false); f == nil {
			return
		}
	}
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) {
			continue
		}
		name := declName(d)
		switch name {
		case "type OperatorEntry", "type operatorMarker", "(operatorMarker).Name", "(OperatorEntry).Marker",
			"(Components).AllWorkers", "(Components).AllOperators", "HasOperators":
			continue
		case "RunOperators":
			p.checkRunOperators(f, d.(*ast.FuncDecl))
			continue
		}
		fd, ok := d.(*ast.FuncDecl)
		if ok && recvType(fd) == "Components" && strings.HasPrefix(fd.Name.Name, "Worker") && isWrapWorker(fd) {
			continue
		}
		if ok && recvType(fd) == "Components" && strings.HasPrefix(fd.Name.Name, "Operator") && isOperatorEntry(fd) {
			continue
		}
		p.block(f, d.Pos(), "%s is supervision forge did not scaffold (a lease, a wrapper, a custom accessor): the graph supervises each worker node from its own Run/Drain/Close — make the wrapper a component of its own (`type LeasedX struct{…}` with Run) a root reaches", name)
	}
	p.remove(f.rel, false)
}

func isWrapWorker(fd *ast.FuncDecl) bool {
	if fd.Body == nil || len(fd.Body.List) != 1 {
		return false
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	c, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(c.Args) != 2 {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "WrapWorker"
}

func isOperatorEntry(fd *ast.FuncDecl) bool {
	if fd.Body == nil || len(fd.Body.List) != 1 {
		return false
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	cl, ok := ret.Results[0].(*ast.CompositeLit)
	return ok && isIdent(cl.Type, "OperatorEntry") && len(cl.Elts) == 3
}

func (p *cgPlan) checkRunOperators(f *cgFile, fd *ast.FuncDecl) {
	ast.Inspect(fd, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Options" {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key := exprString(f, kv.Key)
			switch key {
			case "HealthProbeBindAddress":
			case "LeaderElectionID":
				if lit, ok := kv.Value.(*ast.BasicLit); ok {
					if v, err := strconv.Unquote(lit.Value); err == nil && v != codegen.LeaderElectionID(p.module) {
						p.notes = append(p.notes, fmt.Sprintf("operators: the graph's controller manager elects a leader as %q; RunOperators used %q, so during the first rollout an old and a new replica can both lead — roll it out with one replica, or keep the old ID by disowning graph_gen.go", codegen.LeaderElectionID(p.module), v))
					}
				}
			default:
				p.block(f, kv.Pos(), "RunOperators sets operatorkit.Options.%s, which the graph's controller manager does not take from the project: run the operators under a root of their own with a hand-written manager node (`forge skill load operators`)", key)
			}
		}
		return false
	})
}

// planAPIKeyValidator retypes auth.go's APIKeyValidator(*Infra) to the
// graph's *DB: NewUserAuth hands it the serving pool.
func (p *cgPlan) planAPIKeyValidator() {
	f, fd := p.findFunc("APIKeyValidator", false)
	if f == nil || fd.Type.Params.NumFields() != 1 {
		return
	}
	param := fd.Type.Params.List[0]
	star, ok := param.Type.(*ast.StarExpr)
	if !ok || !isIdent(star.X, "Infra") {
		return
	}
	if len(param.Names) == 1 && param.Names[0].Name != "_" {
		name := param.Names[0].Name
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && isIdent(sel.X, name) && sel.Sel.Name != "DB" {
				p.block(f, sel.Pos(), "APIKeyValidator reads %s.%s; the graph hands it the serving pool (*DB) only — take what else it needs as a field of UserAuthDeps in providers.go", name, sel.Sel.Name)
			}
			return true
		})
	}
	src := p.current(f.rel)
	off := f.fset.Position(star.X.Pos()).Offset
	end := f.fset.Position(star.X.End()).Offset
	p.writes[f.rel] = append(append(append([]byte(nil), src[:off]...), "DB"...), src[end:]...)
	p.skipRef[refKey(f.rel, f.fset.Position(star.X.Pos()).Line)] = true
}

// current is a file's bytes as this plan leaves them so far.
func (p *cgPlan) current(rel string) []byte {
	if b, ok := p.writes[rel]; ok {
		return b
	}
	return p.files[rel].src
}
