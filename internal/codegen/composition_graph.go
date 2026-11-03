// File: internal/codegen/composition_graph.go
//
// The composition graph (docs/design/composition-graph.md §3): every part of
// a process is a component built from its Deps, and the graph that wires them
// is typed, generated Go. This file is the graph's MODEL and DISCOVERY:
//
//   - nodes: every component forge already discovers (handlers, packages,
//     workers, operators), every PROVIDER func in internal/app's owned files
//     (`func New<X>(<X>Deps) (T, error)` produces T), and the nodes forge
//     generates itself — one route set per audience (<Audience>Routes) and,
//     when the project has operators, the controller manager (Operators);
//   - roots: structs in internal/app marked `//forge:root <name>`. Each field
//     is a node, resolved by type; a root's process builds exactly the
//     closure its fields reach. A project that declares none gets the
//     generated default root, Server: every runnable node nothing else
//     depends on — today's `server` command.
//
// Resolution (composition_graph_resolve.go) and emission
// (composition_graph_emit.go) build on what is discovered here.
//
// A project is in GRAPH mode unless its internal/app still holds the legacy
// composition — the owned `type Infra struct` / `func NewComponents` that
// providers.go and compose.go were before the graph. Those projects keep the
// legacy generators byte-for-byte until they migrate (`forge fix
// composition-graph`, internal/codemod; the rest of the path is the
// migrations/v1.0.0 skill).

package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// GraphComposition reports whether the project composes with the composition
// graph: true for a new project and for one whose internal/app has no legacy
// composition (`type Infra struct` or `func NewComponents`) in an owned file.
func GraphComposition(projectDir string) bool {
	appDir := filepath.Join(projectDir, "internal", "app")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(appDir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "NewComponents" {
					return false
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "Infra" {
						if _, isStruct := ts.Type.(*ast.StructType); isStruct {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

// nodeKind is where a graph node comes from.
type nodeKind int

const (
	nodeComponent nodeKind = iota // a handler / package / worker / operator package
	nodeProvider                  // a New<X>(<X>Deps) func in an owned internal/app file
	nodeRoutes                    // a generated <Audience>Routes route set
	nodeOperators                 // the generated controller manager
)

// lifecycle is the lifecycle methods a node's value has, read off its type by
// go/types: the method name to bind for each of lifecyclekit's four phases,
// "" for none.
type lifecycle struct {
	Run, Drain, Close, Ready string
}

func (l lifecycle) any() bool { return l.Run != "" || l.Drain != "" || l.Close != "" || l.Ready != "" }

// graphNode is one node of the composition graph.
type graphNode struct {
	// Name is the node's accessor on *Graph (g.<Name>()) and the suffix of
	// its compose.go constructor (newName). PascalCase, unique.
	Name string
	// LogName is the node's label in build errors and supervisor logs.
	LogName string
	Kind    nodeKind
	// Doc describes the node in one phrase, for generated comments.
	Doc string

	// Type is the Go type of the node's value as written in package app
	// ("*alpha.Service", "*APIListener", "*orm.Client").
	Type string
	// Imports are the alias → import path pairs Type and the constructor
	// call need in package app.
	Imports map[string]string

	// Ctor is the constructor call's function ("alpha.New", "NewDB"), and
	// DepsType its Deps literal type ("alpha.Deps", "DBDeps").
	Ctor     string
	DepsType string
	// DepsImport is the import path the Deps literal's selector names ("" for
	// a Deps type declared in package app).
	DepsImport string
	Fallible   bool
	// Wrapped components are constructed through their generated observe
	// middleware: `pkg.<MiddlewareCtor>(pkg.New(...))`.
	Wrapped        bool
	MiddlewareCtor string

	Deps      []DepsField
	DepsKeys  map[string]bool
	DepsFound bool
	// SrcImports are the imports the Deps struct's field types are written
	// against, for a node declared in package app (a provider's file, or a
	// generated route set's).
	SrcImports map[string]string

	Lifecycle lifecycle
	// NilGuard is true when the value can be nil (a pointer or an
	// interface), so the generated lifecycle literal checks it first.
	NilGuard bool

	// Comp is the discovered component behind a nodeComponent.
	Comp *BuildComponent
	// Role is a component's role root ("internal/workers", …); "" otherwise.
	Role string
	// Audience is a route set's audience.
	Audience string
	// Services are a route set's services, in Deps order.
	Services []*graphNode
	// RPCService is the canonical proto identity from the typed mount inventory.
	RPCService string
	// Pos orders providers by their declaration in internal/app.
	Pos int
	// File and Line locate a provider's func; Standby, AuthAudience and
	// RootOnly are its //forge:standby, //forge:audience and
	// //forge:root-only directives (graphProvider).
	File         string
	Line         int
	Standby      bool
	AuthAudience string
	RootOnly     []string
}

// graphRoot is one root: a struct in internal/app whose fields select the
// subgraph a process builds, or the generated default.
type graphRoot struct {
	// Name is the root's name — the `//forge:root <name>` argument.
	Name string
	// Command is the cobra command path that runs the root
	// (cmd/<bin>/cmd/roots_gen.go): the `command=a,b` option, else [Name].
	Command []string
	// Migrate is the `migrate=` option: what the root's process does about
	// the schema before it builds the root — migrateApply, migrateWait or
	// migrateNone. "" (no option) is the default: apply when the root
	// reaches the database, otherwise nothing.
	Migrate string
	// TypeName is the root struct's Go name: Build<TypeName> / Run<TypeName>.
	TypeName string
	// Short and Long are the help of the root's command, from the root
	// struct's doc comment (rootHelp): its first paragraph, and the rest.
	// Both empty without a doc comment: roots_gen.go describes the root.
	Short, Long string
	// Generated is the default root forge emits when none is declared.
	Generated bool
	Fields    []graphRootField
	// File/Line locate a declared root, for errors.
	File string
	Line int
}

// graphRootField is one field of a root, resolved to the node it holds.
type graphRootField struct {
	Name string
	Type string // as written
	Node *graphNode
}

// graphProvider is one provider func discovered in internal/app.
type graphProvider struct {
	Name     string // X in New<X>
	Result   string // T as written
	Fallible bool
	Deps     []DepsField
	DepsKeys map[string]bool
	Imports  map[string]string // the declaring file's imports, alias → path
	File     string
	Line     int
	Pos      int
	// Standby is `//forge:standby` on the provider func: a provider kept
	// ready for components to come, which `forge lint --graph` does not
	// require a root to reach.
	Standby bool
	// Audience is `//forge:audience <name>` on the provider func: the
	// trust domain an authenticator node authenticates, which `forge lint
	// --graph` compares with the audience of every service a listener
	// serves through a chain over it.
	Audience string
	// RootOnly is `//forge:root-only <root>[,<root>...]` on the provider
	// func: the roots whose closures alone may hold the node. Resolution
	// never fills another node's interface-typed field with it (only a
	// field of its very type), and generate fails when any other root
	// reaches it — a root-specific variant (the worker's privileged daemon
	// router) can never be handed to a process it was not built for.
	RootOnly []string
}

// rootDirectiveRE matches `//forge:root` with an optional name and options.
var rootDirectiveRE = regexp.MustCompile(`^//\s*forge:root(?:\s+([A-Za-z][\w-]*))?((?:\s+\S+)*)\s*$`)

// standbyDirectiveRE, audienceDirectiveRE and rootOnlyDirectiveRE read a
// provider func's doc.
var (
	standbyDirectiveRE  = regexp.MustCompile(`^//\s*forge:standby\b`)
	audienceDirectiveRE = regexp.MustCompile(`^//\s*forge:audience\s+([a-z][a-z0-9_-]*)\s*$`)
	rootOnlyDirectiveRE = regexp.MustCompile(`^//\s*forge:root-only\b(.*)$`)
	rootOnlyNameRE      = regexp.MustCompile(`^[A-Za-z][\w-]*$`)
)

// rootOnlyDirective is the roots a `//forge:root-only a,b` line names: nil
// without the directive, an error when it names none or a malformed one.
func rootOnlyDirective(doc *ast.CommentGroup) ([]string, error) {
	if doc == nil {
		return nil, nil
	}
	for _, c := range doc.List {
		m := rootOnlyDirectiveRE.FindStringSubmatch(c.Text)
		if m == nil {
			continue
		}
		var names []string
		for _, part := range strings.Split(m[1], ",") {
			name := strings.TrimSpace(part)
			if !rootOnlyNameRE.MatchString(name) {
				return nil, fmt.Errorf("%q: //forge:root-only names the roots that alone may reach the node, comma-separated (//forge:root-only worker or //forge:root-only api,worker)", strings.TrimSpace(c.Text))
			}
			names = append(names, name)
		}
		return names, nil
	}
	return nil, nil
}

// hasDirective reports whether doc carries a line matching re.
func hasDirective(doc *ast.CommentGroup, re *regexp.Regexp) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if re.MatchString(c.Text) {
			return true
		}
	}
	return false
}

// audienceDirective is the `//forge:audience <name>` argument, or "".
func audienceDirective(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.List {
		if m := audienceDirectiveRE.FindStringSubmatch(c.Text); m != nil {
			return m[1]
		}
	}
	return ""
}

// commandWordRE is one word of a root's command path.
var commandWordRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// isGraphGeneratedFile reports whether name is one of forge's generated
// internal/app files, which discovery never reads as owned declarations.
func isGraphGeneratedFile(name string) bool {
	return strings.HasSuffix(name, "_gen.go") || name == "compose.go"
}

// discoverAppDecls parses the owned internal/app files (everything except
// compose.go, *_gen.go and tests) for provider funcs and root structs.
func discoverAppDecls(projectDir string) (providers []graphProvider, roots []graphRoot, err error) {
	providers, roots, _, err = discoverAppDeclsAndGroups(projectDir)
	return providers, roots, err
}

// discoverAppDeclsAndGroups is discoverAppDecls, with the group commands'
// declared help (`//forge:group`, composition_graph_groups.go).
func discoverAppDeclsAndGroups(projectDir string) (providers []graphProvider, roots []graphRoot, groups map[string]groupHelp, err error) {
	groups = map[string]groupHelp{}
	appDir := filepath.Join(projectDir, "internal", "app")
	entries, rerr := os.ReadDir(appDir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, nil, groups, nil
		}
		return nil, nil, nil, rerr
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || isGraphGeneratedFile(n) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	pos := 0
	for _, n := range names {
		path := filepath.Join(appDir, n)
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("parse internal/app/%s: %w", n, perr)
		}
		rel := "internal/app/" + n
		structs, fileRoots, rerr := discoverFileStructsAndRoots(fset, file, rel)
		if rerr != nil {
			return nil, nil, nil, rerr
		}
		if gerr := discoverFileGroups(fset, file, rel, groups); gerr != nil {
			return nil, nil, nil, gerr
		}
		roots = append(roots, fileRoots...)
		imports := fileImportAliases(file)
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			x, results, ok := providerFuncShape(fd)
			if !ok {
				continue
			}
			st := structs[x+"Deps"]
			if st == nil {
				st = findAppStruct(projectDir, names, x+"Deps")
			}
			if st == nil {
				continue
			}
			rootOnly, rerr := rootOnlyDirective(fd.Doc)
			if rerr != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: New%s: %w", rel, fset.Position(fd.Pos()).Line, x, rerr)
			}
			pos++
			providers = append(providers, graphProvider{
				Name:     x,
				Result:   printType(fset, results[0]),
				Fallible: len(results) == 2,
				Deps:     collectDepsFields(fset, st),
				DepsKeys: depsStructKeys(st),
				Imports:  imports,
				File:     rel,
				Line:     fset.Position(fd.Pos()).Line,
				Pos:      pos,
				Standby:  hasDirective(fd.Doc, standbyDirectiveRE),
				Audience: audienceDirective(fd.Doc),
				RootOnly: rootOnly,
			})
		}
	}
	return providers, roots, groups, nil
}

// discoverFileStructsAndRoots indexes file's struct types by name and reads
// every `//forge:root` struct among them as a graphRoot.
func discoverFileStructsAndRoots(fset *token.FileSet, file *ast.File, rel string) (map[string]*ast.StructType, []graphRoot, error) {
	structs := map[string]*ast.StructType{}
	var roots []graphRoot
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			st, isStruct := ts.Type.(*ast.StructType)
			if !isStruct {
				continue
			}
			structs[ts.Name.Name] = st
			doc := ts.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			rootName, opts, isRoot := rootDirective(doc)
			if !isRoot {
				continue
			}
			r, err := graphRootFromStruct(fset, ts, st, rootName, opts, rel)
			if err != nil {
				return nil, nil, err
			}
			r.Short, r.Long = rootHelp(doc)
			roots = append(roots, r)
		}
	}
	return structs, roots, nil
}

// graphRootFromStruct builds the graphRoot a `//forge:root [name]` struct
// declares. Every field must be named: the field order is the stop order.
func graphRootFromStruct(fset *token.FileSet, ts *ast.TypeSpec, st *ast.StructType, rootName string, opts []string, rel string) (graphRoot, error) {
	if rootName == "" {
		rootName = naming.ToKebabCase(ts.Name.Name)
	}
	r := graphRoot{Name: rootName, TypeName: ts.Name.Name, File: rel, Line: fset.Position(ts.Pos()).Line}
	cmdPath, migrate, err := rootOptions(rootName, opts)
	if err != nil {
		return graphRoot{}, fmt.Errorf("%s:%d: root %s: %w", r.File, r.Line, ts.Name.Name, err)
	}
	r.Command, r.Migrate = cmdPath, migrate
	for _, f := range st.Fields.List {
		typ := printType(fset, f.Type)
		if len(f.Names) == 0 {
			return graphRoot{}, fmt.Errorf("%s:%d: root %s embeds %s; give each root field a name — the field order is the stop order", r.File, fset.Position(f.Pos()).Line, ts.Name.Name, typ)
		}
		for _, fn := range f.Names {
			r.Fields = append(r.Fields, graphRootField{Name: fn.Name, Type: typ})
		}
	}
	return r, nil
}

// providerFuncShape reports whether fd is a provider func — a top-level
// `New<X>(<X>Deps) T` or `New<X>(<X>Deps) (T, error)` with X exported — and
// returns X and its result types.
func providerFuncShape(fd *ast.FuncDecl) (string, []ast.Expr, bool) {
	if fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "New") || len(fd.Name.Name) == 3 {
		return "", nil, false
	}
	x := strings.TrimPrefix(fd.Name.Name, "New")
	if !ast.IsExported(x) {
		return "", nil, false
	}
	params := flattenFieldTypes(fd.Type.Params)
	if len(params) != 1 || !isIdent(params[0], x+"Deps") {
		return "", nil, false
	}
	results := flattenFieldTypes(fd.Type.Results)
	if len(results) == 0 || len(results) > 2 || (len(results) == 2 && !isIdent(results[1], "error")) {
		return "", nil, false
	}
	return x, results, true
}

// depsStructKeys is the set of keys a Deps struct literal may name: every
// named field, plus each embedded field's type name.
func depsStructKeys(st *ast.StructType) map[string]bool {
	keys := map[string]bool{}
	for _, f := range st.Fields.List {
		for _, fn := range f.Names {
			keys[fn.Name] = true
		}
		if len(f.Names) == 0 {
			if en := embeddedFieldName(f.Type); en != "" {
				keys[en] = true
			}
		}
	}
	return keys
}

// findAppStruct finds a struct declared in another owned internal/app file.
func findAppStruct(projectDir string, names []string, want string) *ast.StructType {
	for _, n := range names {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(projectDir, "internal", "app", n), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if st := findStructType(file, want); st != nil {
			return st
		}
	}
	return nil
}

// rootHelp is the help of a root's command, read off the root struct's doc
// comment, directives dropped: the first paragraph is the command's Short (on
// one line), the paragraphs after it its Long, line breaks kept. A doc of one
// paragraph has no Long, so `--help` prints the Short. The doc comment is the
// one place a root is described, so the command's help and the code's
// documentation cannot drift apart; notes for the code's readers that do not
// belong in `--help` go on the fields, or in a comment of their own above it.
func rootHelp(doc *ast.CommentGroup) (short, long string) {
	if doc == nil {
		return "", ""
	}
	paras := strings.Split(strings.TrimSpace(doc.Text()), "\n\n")
	if paras[0] == "" {
		return "", ""
	}
	short = strings.Join(strings.Fields(paras[0]), " ")
	for i, p := range paras[1:] {
		paras[i+1] = strings.TrimRight(p, "\n")
	}
	return short, strings.TrimSpace(strings.Join(paras[1:], "\n\n"))
}

// rootDirective reads `//forge:root [name] [command=a,b] [migrate=…]` off a
// type's doc comment: the root's name and its options, unparsed.
func rootDirective(doc *ast.CommentGroup) (name string, opts []string, ok bool) {
	if doc == nil {
		return "", nil, false
	}
	for _, c := range doc.List {
		if m := rootDirectiveRE.FindStringSubmatch(c.Text); m != nil {
			return m[1], strings.Fields(m[2]), true
		}
	}
	return "", nil, false
}

// The `migrate=` values: what a root's process does about the schema before
// it builds the root.
const (
	// migrateApply applies every pending migration (the default for a
	// root that reaches the database).
	migrateApply = "apply"
	// migrateWait applies nothing and blocks until the schema is current:
	// another process — a migrate=apply root, or a deploy job — applies it.
	migrateWait = "wait"
	// migrateNone leaves the schema alone (the default for a root that
	// never reaches the database).
	migrateNone = "none"
)

// rootOptions parses a root's options: the cobra command path that runs it,
// and its migrate policy. `command=server,api` is `<bin> server api`; with no
// command option the path is the root's name. Command words are lower-case
// cobra names. `migrate=apply|wait|none` is the policy; with no migrate
// option it is "" — the default, decided by reach (graphRootView.Migrate).
func rootOptions(name string, opts []string) (path []string, migrate string, err error) {
	path = []string{name}
	for _, o := range opts {
		key, val, found := strings.Cut(o, "=")
		switch {
		case found && key == "command":
			path = strings.Split(val, ",")
		case found && key == "migrate":
			switch val {
			case migrateApply, migrateWait, migrateNone:
				migrate = val
			default:
				return nil, "", fmt.Errorf("migrate=%s is not a migrate policy: migrate=apply (the process applies the migrations), migrate=wait (it waits until another process has), or migrate=none (it leaves the schema alone)", val)
			}
		default:
			return nil, "", fmt.Errorf("unknown //forge:root option %q: the options are command=<word>[,<word>…], the command path that runs the root, and migrate=apply|wait|none, what its process does about the schema", o)
		}
	}
	for _, w := range path {
		if !commandWordRE.MatchString(w) {
			return nil, "", fmt.Errorf("command word %q is not a cobra command name: lower-case letters, digits and dashes, starting with a letter", w)
		}
	}
	return path, migrate, nil
}

// fileImportAliases maps each import's name in file (explicit alias, or the
// path's last element) to its path. Blank and dot imports are skipped.
func fileImportAliases(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			name = imp.Name.Name
		} else if isMajorVersionSuffix(name) {
			trimmed := strings.TrimSuffix(path, "/"+name)
			name = trimmed[strings.LastIndex(trimmed, "/")+1:]
		}
		out[name] = path
	}
	return out
}

// typeExprImports returns the alias → path pairs a type expression's
// package selectors name, looked up in imports. A selector whose alias is not
// in imports is reported in unknown.
func typeExprImports(expr string, imports map[string]string) (map[string]string, []string) {
	out := map[string]string{}
	var unknown []string
	e, err := parser.ParseExpr(expr)
	if err != nil {
		return out, nil
	}
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if p, ok := imports[id.Name]; ok {
				out[id.Name] = p
			} else {
				unknown = append(unknown, id.Name)
			}
		}
		return false
	})
	return out, unknown
}

// audienceRoutesName is the generated route set's node name for an audience:
// user → UserRoutes, daemon → DaemonRoutes.
func audienceRoutesName(audience string) string {
	return naming.ToPascalCase(audience) + "Routes"
}

// logName is a node's label in errors, logs, the readiness report and the
// start plan: its accessor in snake_case, split on the words a person reads
// (GitHubAccess → github_access; naming.ToSnakeCaseWords).
func logName(name string) string { return naming.ToSnakeCaseWords(name) }
