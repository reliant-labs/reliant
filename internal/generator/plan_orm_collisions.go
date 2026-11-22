package generator

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// ormOutput is one file the ORM projection is about to write into
// internal/db, rendered but not yet written.
type ormOutput struct {
	file   string // base name, e.g. "task_orm_gen.go"
	entity string // "" for the store file, which serves every entity
	src    []byte
}

// ormNameCollision is one package-level name the ORM would declare that a
// file the project owns in internal/db already declares.
type ormNameCollision struct {
	name   string
	at     string // "internal/db/models.go:119"
	entity string
	table  string
	file   string // the ORM file that declares it
}

// checkORMNameCollisions refuses an ORM projection that would redeclare a name
// the project already declares in internal/db, before anything is written.
//
// internal/db is ONE Go package. forge projects each entity into it — the
// struct (`Task`), its delegates (`CreateTask`, `ListTask`, …), its
// `TaskStore` and the aggregate `Store`/`NewStore` — and a project adopting
// forge usually already has a data layer there. reliant's `type Task =
// core.Task` alias is the measured case: the projection wrote task_orm_gen.go
// beside it, the validate build failed on `Task redeclared in this block`,
// and generate reverted everything it wrote, naming neither the declaration
// nor the way out.
//
// The ORM's package and names stay fixed (one convention every generated
// consumer and owned file relies on); what this adds is the answer. Each
// collision names the project's declaration and the entity behind it, and the
// error names the three ways to resolve it: keep that table off forge's ORM
// (`COMMENT ON TABLE … 'forge:no-entity'`, the per-table opt-out — the
// incremental path off a hand-written or sqlc data layer), hand the name to
// forge, or opt the whole package out with `//forge:no-orm`.
//
// A file the ORM itself owns is not the project's: this run's outputs, stale
// ORM files the sweep reclaims, and the retired forge-written types.go.
// Internal test files (package db) share the namespace and are read too.
func checkORMNameCollisions(dbDir, relDir string, entities []config.PlanEntity, outputs []ormOutput) error {
	declaredBy := map[string]ormOutput{} // name → the ORM output declaring it
	for _, out := range outputs {
		for _, name := range packageLevelNames(out.src) {
			if _, seen := declaredBy[name]; !seen {
				declaredBy[name] = out
			}
		}
	}
	tables := map[string]string{}
	for _, ent := range entities {
		table := ent.TableName
		if table == "" {
			table = naming.Pluralize(naming.ToSnakeCase(ent.Name))
		}
		tables[ent.Name] = table
	}

	entries, err := os.ReadDir(dbDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dbDir, err)
	}
	var collisions []ormNameCollision
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(dbDir, name)
		body, rerr := os.ReadFile(path)
		if rerr != nil || ormOwnsFile(name) {
			continue
		}
		file, perr := parser.ParseFile(fset, path, body, parser.SkipObjectResolution)
		if perr != nil || strings.HasSuffix(file.Name.Name, "_test") {
			continue // a broken file is the validate build's to report; an external test package shares no names
		}
		for _, d := range packageLevelDecls(file) {
			out, hit := declaredBy[d.Name]
			if !hit {
				continue
			}
			collisions = append(collisions, ormNameCollision{
				name:   d.Name,
				at:     fmt.Sprintf("%s/%s:%d", relDir, name, fset.Position(d.Pos()).Line),
				entity: out.entity,
				table:  tables[out.entity],
				file:   out.file,
			})
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Slice(collisions, func(i, j int) bool { return collisions[i].at < collisions[j].at })
	return &ORMCollisionError{collisions: collisions, dir: relDir}
}

// ORMCollisionError is the refusal checkORMNameCollisions returns: the names
// the project already declares, and how to resolve each entity.
type ORMCollisionError struct {
	collisions []ormNameCollision
	dir        string // the ORM package directory, e.g. "internal/db"
}

// Entities returns the entities whose names collide, sorted.
func (e *ORMCollisionError) Entities() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range e.collisions {
		if c.entity != "" && !seen[c.entity] {
			seen[c.entity] = true
			out = append(out, c.entity)
		}
	}
	sort.Strings(out)
	return out
}

func (e *ORMCollisionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "forge's ORM would redeclare names your %s already declares (one Go package, so both cannot compile):\n", e.dir)
	tables := map[string]string{}
	for _, c := range e.collisions {
		if c.entity == "" {
			fmt.Fprintf(&b, "  - %s, declared at %s — forge declares it in %s/%s for every entity\n", c.name, c.at, e.dir, c.file)
			continue
		}
		fmt.Fprintf(&b, "  - %s, declared at %s — forge declares it in %s/%s for entity %s (table %s)\n", c.name, c.at, e.dir, c.file, c.entity, c.table)
		tables[c.entity] = c.table
	}
	b.WriteString("Resolve it one of these ways:\n")
	fmt.Fprintf(&b, "  - give the projection a package of its own: `database: {orm: {package: internal/store}}` in forge.yaml — your %s keeps its names, and the generated rows, stores and CRUD wiring move to internal/store;\n", e.dir)
	for _, ent := range e.Entities() {
		table := tables[ent]
		fmt.Fprintf(&b, "  - keep %s off forge's ORM while your code owns it: in a migration, `COMMENT ON TABLE %s IS '%s'` — "+
			"the table is then plain schema, with no store and no CRUD wiring (a `// forge:custom` rpc does not do this: "+
			"the store is projected from every table, whatever its rpcs);\n",
			table, table, schemadef.TableMarkerNoEntity)
	}
	b.WriteString("  - hand the name to forge: delete or rename your declaration (sqlc output: a `rename:` in sqlc.yaml) and use the generated one;\n")
	fmt.Fprintf(&b, "  - or opt the whole data layer out with `//forge:no-orm: <why>` in %s's package doc.", e.dir)
	return b.String()
}

// ormOwnsFile reports whether an internal/db file is the ORM's own output
// rather than the project's: a file name this run rewrites or sweeps.
func ormOwnsFile(name string) bool {
	return strings.HasSuffix(name, "_orm_gen.go") || strings.HasSuffix(name, "_store_gen.go") || name == storeGenFile
}

// packageLevelNames returns the package-level identifiers src declares.
func packageLevelNames(src []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var names []string
	for _, id := range packageLevelDecls(file) {
		names = append(names, id.Name)
	}
	return names
}

// packageLevelDecls returns the identifiers file declares at package scope:
// types, vars, consts and receiver-less funcs. Methods, `_` and init are not
// package-scope names.
func packageLevelDecls(file *ast.File) []*ast.Ident {
	var ids []*ast.Ident
	keep := func(id *ast.Ident) {
		if id != nil && id.Name != "_" {
			ids = append(ids, id)
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name != "init" {
				keep(d.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					keep(s.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						keep(n)
					}
				}
			}
		}
	}
	return ids
}

// projectDeclaresName reports whether a file the project owns in the ORM
// package dbDir declares name at package level — the test for a delegate
// forge emits only when the project has not written its own.
func projectDeclaresName(dbDir, name string) bool {
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		file := e.Name()
		if e.IsDir() || !strings.HasSuffix(file, ".go") {
			continue
		}
		path := filepath.Join(dbDir, file)
		body, rerr := os.ReadFile(path)
		if rerr != nil || ormOwnsFile(file) {
			continue
		}
		f, perr := parser.ParseFile(fset, path, body, parser.SkipObjectResolution)
		if perr != nil || strings.HasSuffix(f.Name.Name, "_test") {
			continue
		}
		for _, id := range packageLevelDecls(f) {
			if id.Name == name {
				return true
			}
		}
	}
	return false
}

// storeMethodKey is how a method of the aggregate Store is keyed among
// package-level declarations: "Store.<Method>". A method is not a package
// scope name, but two declarations of one Store method do not compile either.
func storeMethodKey(method string) string { return storeTypeName + "." + method }

// storeTypeName is the aggregate store's type (renderStoreAggregate).
const storeTypeName = "Store"

// displayDeclName is a declaration key as a reader spells it: the bare name,
// or `func (Store) PlansExt` for a Store method.
func displayDeclName(key string) string {
	if method, ok := strings.CutPrefix(key, storeTypeName+"."); ok {
		return "func (" + storeTypeName + ") " + method
	}
	return key
}

// fileDeclarations is packageLevelDecls plus the methods file declares on
// the aggregate Store, keyed by storeMethodKey.
func fileDeclarations(file *ast.File) []*ast.Ident {
	ids := packageLevelDecls(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || recvTypeName(fn.Recv.List[0].Type) != storeTypeName {
			continue
		}
		ids = append(ids, &ast.Ident{NamePos: fn.Name.NamePos, Name: storeMethodKey(fn.Name.Name)})
	}
	return ids
}

// declaredNames is fileDeclarations' names for a rendered source.
func declaredNames(src []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var names []string
	for _, id := range fileDeclarations(file) {
		names = append(names, id.Name)
	}
	return names
}

// projectDeclarations maps every name a file the project owns in the ORM
// package dbDir declares (fileDeclarations) to where: "internal/db/x.go:12".
// It is how a generated name that only joins the projection later — an
// ext store — yields to a declaration the project made first instead of
// failing its build.
func projectDeclarations(dbDir, relDir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(dbDir, name)
		body, rerr := os.ReadFile(path)
		if rerr != nil || ormOwnsFile(name) {
			continue
		}
		file, perr := parser.ParseFile(fset, path, body, parser.SkipObjectResolution)
		if perr != nil || strings.HasSuffix(file.Name.Name, "_test") {
			continue
		}
		for _, id := range fileDeclarations(file) {
			if _, seen := out[id.Name]; !seen {
				out[id.Name] = fmt.Sprintf("%s/%s:%d", relDir, name, fset.Position(id.Pos()).Line)
			}
		}
	}
	return out
}

// recvTypeName is a receiver's base type name: `s Store`, `s *Store`.
func recvTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}
