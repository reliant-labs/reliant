package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// compositiongraph_cmd.go — the command-tree half of `forge fix
// composition-graph`: serve.go, server.go, the retired per-component
// subcommands, root.go and main.go; then the e2e harness, deploy/kcl and the
// scan for anything still naming the legacy composition.

// stockServerSpec is server.go's ServeSpec literal, whitespace-normalized.
var stockServerSpec = map[string]string{
	"Mount":           "(*app.Components).MountAll",
	"Workers":         "(*app.Components).AllWorkers",
	"Operators":       "(*app.Components).AllOperators",
	"RequireComplete": "true",
}

func (p *cgPlan) planCmdTree() {
	if f := p.files[p.tree+"/serve.go"]; f != nil {
		p.planServe(f)
	}
	if f := p.files[p.tree+"/server.go"]; f != nil {
		p.planServer(f)
	}
	for _, g := range groupDirs {
		p.planGroup(g)
	}
	if f := p.files[p.tree+"/root.go"]; f != nil {
		p.planRoot(f)
	}
	if f := p.files[path.Dir(p.tree)+"/main.go"]; f != nil {
		p.planMain(f)
	}
}

func (p *cgPlan) planServe(f *cgFile) {
	_, hasSpec := lookupType(f.ast, "ServeSpec")
	if !hasSpec {
		return
	}
	ts, _ := lookupType(f.ast, "ServeSpec")
	if st, ok := ts.Type.(*ast.StructType); ok {
		for _, fld := range st.Fields.List {
			for _, n := range fld.Names {
				if !stockServeSpec[n.Name] {
					p.block(f, n.Pos(), "ServeSpec.%s is process composition the graph expresses as REACH: a root that does not reach the component it guards never builds it. Declare a root per process that sets it (`//forge:root <name> command=<cmd>`), and make what it switched a provider only the right roots reach — migrations/v0.1.47, \"ServeSpec knobs\"", n.Name)
				}
			}
		}
	}
	retiring := map[string]bool{"Serve": true, "type ServeSpec": true, "type MountFunc": true, "MountNone": true, "serverkitConfig": true}
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) || retiring[declName(d)] {
			continue
		}
		p.block(f, d.Pos(), "%s: serve.go is re-scaffolded as runRoot, so this declaration needs a file of its own in %s first (and, if it shapes the chain or the edge, a home in providers.go's NewUserChain / NewAPIListener)", declName(d), p.tree)
	}
	p.remove(f.rel, true)
}

func lookupType(f *ast.File, name string) (*ast.TypeSpec, bool) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			if ts := s.(*ast.TypeSpec); ts.Name.Name == name {
				return ts, true
			}
		}
	}
	return nil, false
}

func (p *cgPlan) planServer(f *cgFile) {
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) {
			continue
		}
		if declName(d) != "newServerCmd" {
			p.block(f, d.Pos(), "%s: server.go is retired (the `server` command is generated per root), so this needs a file of its own first", declName(d))
		}
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || !isSelector(cl.Type, "ServeSpec") {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, val := exprString(f, kv.Key), exprString(f, kv.Value)
			if want, stock := stockServerSpec[key]; !stock || want != val {
				p.block(f, kv.Pos(), "the `server` command sets ServeSpec.%s: %s — the default root runs every service, worker and operator; to serve less, declare `//forge:root server` in internal/app with exactly the nodes it should run", key, val)
			}
		}
		return false
	})
	p.remove(f.rel, false)
}

func isSelector(e ast.Expr, name string) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == name
	case *ast.Ident:
		return x.Name == name
	}
	return false
}

// planGroup retires cmd/<bin>/cmd/<group>: every subcommand there must be
// forge's (a cobra command whose RunE hands Serve a typed spec).
func (p *cgPlan) planGroup(group string) {
	dir := p.tree + "/" + group
	entries, err := os.ReadDir(filepath.Join(p.dir, filepath.FromSlash(dir)))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		rel := dir + "/" + e.Name()
		if f := p.files[rel]; f != nil {
			p.checkGroupFile(f)
		}
		p.remove(rel, false)
	}
}

var groupCobraKeys = map[string]bool{"Use": true, "Short": true, "Long": true, "Args": true, "Aliases": true, "Example": true, "Hidden": true, "RunE": true}

func (p *cgPlan) checkGroupFile(f *cgFile) {
	for _, d := range f.ast.Decls {
		if isImports(d) || blankVar(d) {
			continue
		}
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "New") || !strings.HasSuffix(fd.Name.Name, "Cmd") {
			p.block(f, d.Pos(), "%s: the per-component subcommands are retired, so this needs a home outside %s first", declName(d), path.Dir(f.rel))
			continue
		}
		use, ok := p.stockGroupCmd(f, fd)
		if !ok {
			p.block(f, fd.Pos(), "%s customizes a per-component subcommand (flags, setup, a different spec), which the graph retires: a process is a root — declare `//forge:root <name>` and move what this command adds into runRoot or a provider", fd.Name.Name)
			continue
		}
		if use != "" {
			p.retired = append(p.retired, use)
		}
	}
}

// stockGroupCmd reports whether fd is forge's subcommand shape — `c :=
// &cobra.Command{…RunE: deps.Cmd = c; return cmd.Serve(…)}`,
// `config.RegisterFlags(c)`, `return c` — and returns its Use.
func (p *cgPlan) stockGroupCmd(f *cgFile, fd *ast.FuncDecl) (string, bool) {
	if fd.Body == nil {
		return "", false
	}
	use := ""
	for _, st := range fd.Body.List {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) != 1 {
				return "", false
			}
			u, ok := s.Rhs[0].(*ast.UnaryExpr)
			if !ok {
				return "", false
			}
			cl, ok := u.X.(*ast.CompositeLit)
			if !ok || !isSelector(cl.Type, "Command") {
				return "", false
			}
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					return "", false
				}
				key := exprString(f, kv.Key)
				if !groupCobraKeys[key] {
					return "", false
				}
				switch key {
				case "Use":
					if lit, ok := kv.Value.(*ast.BasicLit); ok {
						use, _ = strconv.Unquote(lit.Value)
						use = strings.Fields(use + " ")[0]
					}
				case "RunE":
					fl, ok := kv.Value.(*ast.FuncLit)
					if !ok || !servesOnly(fl) {
						return "", false
					}
				}
			}
		case *ast.ExprStmt:
			c, ok := s.X.(*ast.CallExpr)
			if !ok || !isSelector(c.Fun, "RegisterFlags") {
				return "", false
			}
		case *ast.ReturnStmt:
		default:
			return "", false
		}
	}
	return use, true
}

// servesOnly reports whether a RunE body only sets deps.Cmd and returns a
// Serve call.
func servesOnly(fl *ast.FuncLit) bool {
	for _, st := range fl.Body.List {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) != 1 || !isSelector(s.Lhs[0], "Cmd") {
				return false
			}
		case *ast.ReturnStmt:
			if len(s.Results) != 1 {
				return false
			}
			c, ok := s.Results[0].(*ast.CallExpr)
			if !ok || !isSelector(c.Fun, "Serve") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

type splice struct {
	start, end int
	text       string
}

func applySplices(src []byte, edits []splice) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	return out
}

// planRoot rewrites root.go: newRootCmd and Execute stop taking the
// retired group factories, and the `server` command becomes the roots'.
func (p *cgPlan) planRoot(f *cgFile) {
	off := func(pos token.Pos) int { return f.fset.Position(pos).Offset }
	var edits []splice
	dropGroupParam := func(fd *ast.FuncDecl) string {
		params := fd.Type.Params.List
		if len(params) == 0 {
			return ""
		}
		last := params[len(params)-1]
		if _, ok := last.Type.(*ast.Ellipsis); !ok || len(last.Names) != 1 {
			return ""
		}
		start := off(last.Pos())
		if len(params) > 1 {
			start = off(params[len(params)-2].End())
		}
		edits = append(edits, splice{start, off(last.End()), ""})
		return last.Names[0].Name
	}
	var sawServer bool
	for _, d := range f.ast.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || (fd.Name.Name != "newRootCmd" && fd.Name.Name != "Execute") {
			continue
		}
		group := dropGroupParam(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if isIdent(x.Fun, "newServerCmd") {
					edits = append(edits, splice{off(x.Pos()), off(x.End()), "rootCommands(deps)..."})
					sawServer = true
					return false
				}
				if isIdent(x.Fun, "newRootCmd") && x.Ellipsis.IsValid() && len(x.Args) == 2 {
					edits = append(edits, splice{off(x.Args[0].End()), off(x.Rparen), ""})
					return false
				}
			case *ast.BlockStmt:
				for _, st := range x.List {
					if r, ok := st.(*ast.RangeStmt); ok && group != "" && isIdent(r.X, group) {
						start, end := off(r.Pos()), off(r.End())
						for start > 0 && (f.src[start-1] == '\t' || f.src[start-1] == ' ') {
							start--
						}
						if end < len(f.src) && f.src[end] == '\n' {
							end++
						}
						edits = append(edits, splice{start, end, ""})
					}
				}
			}
			return true
		})
	}
	if !sawServer {
		p.block(f, token.NoPos, "root.go does not add newServerCmd(deps): add `root.AddCommand(rootCommands(deps)...)` where the server command is added, by hand, after converting")
		return
	}
	out, err := format.Source(applySplices(f.src, edits))
	if err != nil {
		p.block(f, token.NoPos, "root.go does not format after the rewrite (%v): make it by hand — newRootCmd(deps Deps), root.AddCommand(rootCommands(deps)...), Execute()", err)
		return
	}
	p.writes[f.rel] = out
}

// planMain drops the group constructors main.go hands cmd.Execute, and
// their imports.
func (p *cgPlan) planMain(f *cgFile) {
	groupPaths := map[string]bool{}
	for _, g := range groupDirs {
		groupPaths[p.module+"/"+p.tree+"/"+g] = true
	}
	var edits []splice
	ast.Inspect(f.ast, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if ok && isSelector(c.Fun, "Execute") && len(c.Args) > 0 {
			edits = append(edits, splice{f.fset.Position(c.Lparen).Offset + 1, f.fset.Position(c.Rparen).Offset, ""})
		}
		return true
	})
	src := applySplices(f.src, edits)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.rel, src, parser.ParseComments)
	if err != nil {
		p.block(f, token.NoPos, "main.go does not parse after the rewrite: %v", err)
		return
	}
	// Collected first: DeleteNamedImport edits file.Imports.
	var drop []*ast.ImportSpec
	for _, im := range file.Imports {
		if ipath, _ := strconv.Unquote(im.Path.Value); groupPaths[ipath] {
			drop = append(drop, im)
		}
	}
	for _, im := range drop {
		ipath, _ := strconv.Unquote(im.Path.Value)
		name := path.Base(ipath)
		if im.Name != nil {
			name = im.Name.Name
		}
		if len(packageRefs(file, name, nil)) > 0 {
			p.block(f, im.Pos(), "main.go still uses %s, a retired command group: drop the use by hand", ipath)
			return
		}
		astutil.DeleteNamedImport(fset, file, nameIf(im), ipath)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		p.block(f, token.NoPos, "main.go does not format after the rewrite: %v", err)
		return
	}
	out, err := format.Source(buf.Bytes())
	if err != nil {
		p.block(f, token.NoPos, "main.go does not format after the rewrite: %v", err)
		return
	}
	if !bytes.Equal(out, f.src) {
		p.writes[f.rel] = out
	}
}

func nameIf(im *ast.ImportSpec) string {
	if im.Name != nil {
		return im.Name.Name
	}
	return ""
}

// planLegacyGenerated deletes the generated files only the legacy
// composition has; the graph's generate writes routes_gen.go instead.
func (p *cgPlan) planLegacyGenerated() {
	for _, rel := range []string{"internal/app/mounts_services_gen.go"} {
		if _, err := os.Stat(filepath.Join(p.dir, filepath.FromSlash(rel))); err == nil {
			p.remove(rel, false)
		}
	}
}

// planE2E points the scaffolded e2e harness at `server`: the per-service
// subcommand it ran is retired.
func (p *cgPlan) planE2E() {
	if len(p.retired) == 0 || p.bin == "" {
		return
	}
	quoted := make([]string, len(p.retired))
	for i, r := range p.retired {
		quoted[i] = regexp.QuoteMeta(r)
	}
	re := regexp.MustCompile(`("(?:\./)?bin/` + regexp.QuoteMeta(p.bin) + `",\s*)"(` + strings.Join(quoted, "|") + `)"`)
	rels := make([]string, 0)
	for rel := range p.files {
		if strings.HasPrefix(rel, "e2e/") && strings.HasSuffix(rel, "_test.go") {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	for _, rel := range rels {
		src := p.current(rel)
		if out := re.ReplaceAll(src, []byte(`${1}"server"`)); !bytes.Equal(out, src) {
			p.writes[rel] = out
		}
	}
}

var kclArgs = regexp.MustCompile(`(?m)\b(?:args|command)\s*=\s*\[([^\]]*)\]`)

// planKCL names each workload declared to run a retired subcommand. It is a
// note, not a refusal: the scaffold declares one per service whether or not
// an environment deploys it, and only the project knows which do.
func (p *cgPlan) planKCL() {
	if len(p.retired) == 0 {
		return
	}
	retired := map[string]bool{}
	for _, r := range p.retired {
		retired[r] = true
	}
	root := filepath.Join(p.dir, "deploy", "kcl")
	_ = filepath.WalkDir(root, func(pth string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(pth, ".k") {
			return nil
		}
		src, err := os.ReadFile(pth)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(p.dir, pth)
		for _, m := range kclArgs.FindAllSubmatchIndex(src, -1) {
			for _, word := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(src[m[2]:m[3]], -1) {
				if name := string(word[1]); retired[name] {
					line := bytes.Count(src[:m[0]], []byte("\n")) + 1
					p.notes = append(p.notes, fmt.Sprintf("%s:%d declares a workload running `%s %s`, a subcommand the graph retires: if an environment deploys it, declare `//forge:root %s` in internal/app with the nodes that process runs (its command keeps the name) or run it as `server`; if none does, delete it", filepath.ToSlash(rel), line, p.bin, name, name))
				}
			}
		}
		return nil
	})
}

func refKey(rel string, line int) string { return fmt.Sprintf("%s:%d", rel, line) }

// scanReferences refuses the conversion while any file it keeps names a
// symbol it retires: the tree would not compile.
func (p *cgPlan) scanReferences() {
	appPath := p.module + "/internal/app"
	cmdPath := p.module + "/" + p.tree
	rels := make([]string, 0, len(p.files))
	for rel := range p.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if p.gone[rel] {
			continue
		}
		f := p.files[rel]
		if b, ok := p.writes[rel]; ok {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, rel, b, parser.ParseComments)
			if err != nil {
				continue
			}
			f = &cgFile{rel: rel, src: b, fset: fset, ast: file}
		}
		dir := path.Dir(rel)
		switch {
		case dir == "internal/app":
			p.scanIdents(f, legacyAppNames)
		case p.tree != "" && dir == p.tree:
			p.scanIdents(f, legacyCmdNames)
		}
		for _, im := range f.ast.Imports {
			ipath, _ := strconv.Unquote(im.Path.Value)
			var names map[string]bool
			switch {
			case ipath == appPath:
				names = legacyAppNames
			case p.tree != "" && ipath == cmdPath:
				names = legacyCmdNames
			default:
				continue
			}
			alias := path.Base(ipath)
			if im.Name != nil {
				alias = im.Name.Name
			}
			for _, sel := range packageRefs(f.ast, alias, nil) {
				if names[sel.Sel.Name] {
					p.block(f, sel.Pos(), "%s.%s is retired by the graph: rewrite this over the graph (a root, a node accessor, runRoot) before converting", alias, sel.Sel.Name)
				}
			}
		}
	}
}

// scanIdents reports, in a file of the package that declared names, each
// use of one (not a declaration, a field, a key or a selected member).
func (p *cgPlan) scanIdents(f *cgFile, names map[string]bool) {
	skip := map[*ast.Ident]bool{}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			skip[x.Sel] = true
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok {
				skip[id] = true
			}
		case *ast.Field:
			for _, id := range x.Names {
				skip[id] = true
			}
		case *ast.FuncDecl:
			skip[x.Name] = true
		case *ast.TypeSpec:
			skip[x.Name] = true
		case *ast.ValueSpec:
			for _, id := range x.Names {
				skip[id] = true
			}
		}
		return true
	})
	seen := map[string]bool{}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || skip[id] || !names[id.Name] {
			return true
		}
		line := f.fset.Position(id.Pos()).Line
		key := refKey(f.rel, line) + id.Name
		if seen[key] || p.skipRef[refKey(f.rel, line)] {
			return true
		}
		seen[key] = true
		p.block(f, id.Pos(), "%s is retired by the graph: rewrite this over the graph (a node accessor g.X(), a provider, a root) before converting", id.Name)
		return true
	})
}
