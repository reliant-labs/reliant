package generator

// Ext stores — <entity>_ext_store_gen.go, the interface over an entity's
// hand-written queries.
//
// ── The gap this closes ───────────────────────────────────────────────────
//
// <Entity>Store gives the GENERATED CRUD an interface shape. A query the
// builder cannot express lives in the scaffold-once <entity>_repo_ext.go
// seam, written — as the seam itself instructs — as a package function of
// the delegates' shape:
//
//	func GetPlanByThreadID(ctx context.Context, db orm.Context, threadID string) (*core.Plan, error)
//
// A function is not a type, so a consumer's Deps could not name it. The way
// out was the hand-written interface plus passthrough adapter that the
// service-layer skill forbids, one per entity, kept in step with the ext file
// by hand (reliant's facade retirement measured one per entity across every
// wave: internal/store/plan_repo_ext.go alone has seven such queries).
//
// ── Why generating it does not go stale ───────────────────────────────────
//
// <Entity>Store deliberately does not enumerate the ext queries: it is
// regenerated from the SCHEMA, and the queries are user code it would go
// stale against. This file is regenerated from the ext file ITSELF, every
// run, so it follows the queries the way mock_gen.go follows contract.go —
// add, change or remove one and the next generate tracks it. It is a
// separate interface rather than more methods on <Entity>Store, because a
// project's hand fakes of <Entity>Store must keep compiling (semver 1).
//
// ── What is collected ─────────────────────────────────────────────────────
//
// Exported package functions of exactly <entity>_repo_ext.go — the file forge
// scaffolds and names — whose first two parameters are a context.Context and
// forge's orm.Context (contract.CollectExtQueries; it shares derive.go's
// syntax-only import resolution and doc copying). Sibling files are not searched
// for query functions: `<entity>_repo_ext_*.go` would be ambiguous across entities (Plan's
// plan_repo_ext_x.go is also entity PlanRepoExt's seam), and the one file is
// the seam's documented home. An exported function that takes a handle but
// cannot be collected is named in one ℹ️ line per entity; a file that cannot
// be read is named and skipped, never a generate failure.
//
// ── Names, and the projects that already declared them ────────────────────
//
// The file declares <Entity>ExtStore, <entity>ExtStore, New<Entity>ExtStore
// and Store.<Entities>Ext. A project that already declares one of them in the
// ORM package — a hand-written adapter of the very shape this replaces is the
// likely case — keeps its own, gets no ext store for that entity and an ℹ️
// line naming its declaration, rather than a generate that stops compiling
// (plan_orm_collisions.go). So does an entity named <Entity>Ext, whose own
// store is <Entity>ExtStore in the same file name.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator/contract"
	"github.com/reliant-labs/forge/internal/naming"
)

// entityRepoExtFile is ent's scaffold-once custom-query seam.
func entityRepoExtFile(ent config.PlanEntity) string {
	return naming.ToSnakeCase(ent.Name) + "_repo_ext.go"
}

// entityExtStoreFile is ent's ext store file. It ends in _store_gen.go, so
// the ORM sweep reclaims it, the collision check treats it as forge's, and
// compose's store seam (codegen.parseStoreSeam) wires a `<pkg>.<Entity>ExtStore`
// Deps field through its Store accessor exactly as it wires <Entity>Store.
func entityExtStoreFile(ent config.PlanEntity) string {
	return naming.ToSnakeCase(ent.Name) + "_ext_store_gen.go"
}

// extStoreNames are the identifiers an entity's ext store declares.
type extStoreNames struct {
	iface    string // PlanExtStore
	adapter  string // planExtStore
	ctor     string // NewPlanExtStore
	accessor string // PlansExt, a method of Store
}

func extStoreNamesFor(ent config.PlanEntity) extStoreNames {
	iface := ent.Name + "ExtStore"
	return extStoreNames{
		iface:    iface,
		adapter:  lowerFirst(iface),
		ctor:     "New" + iface,
		accessor: naming.Pluralize(ent.Name) + "Ext",
	}
}

// declared lists the names as checkORMNameCollisions' declarations spell
// them: package-level names bare, the accessor as "Store.<Method>".
func (n extStoreNames) declared() []string {
	return []string{n.iface, n.adapter, n.ctor, storeMethodKey(n.accessor)}
}

// renderExtStores renders the ext store of every entity whose
// <entity>_repo_ext.go in dbDir holds a collectable query, and the ℹ️ notes
// for what it left out. outputs are the ORM's other renders this run, whose
// names an ext store must not redeclare.
func renderExtStores(dbDir, relDir, pkgName string, entities []config.PlanEntity, outputs []ormOutput) ([]ormOutput, []string) {
	generated := map[string]string{} // name → the ORM file declaring it
	for _, out := range outputs {
		for _, name := range declaredNames(out.src) {
			generated[name] = out.file
		}
	}
	var project map[string]string // name → "internal/db/x.go:12", read once, lazily
	var collector contract.ExtQueryCollector
	var exts []ormOutput
	var notes []string
	for _, ent := range entities {
		extFile := entityRepoExtFile(ent)
		path := filepath.Join(dbDir, extFile)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		rel := relDir + "/" + extFile
		names := extStoreNamesFor(ent)
		set, err := collector.Collect(path)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s %s, so there is no %s for it", rel, strings.ReplaceAll(strings.TrimPrefix(err.Error(), path+": "), path, rel), names.iface))
			continue
		}
		if set.Package != pkgName {
			notes = append(notes, fmt.Sprintf("%s declares package %s, not %s, so there is no %s for it", rel, set.Package, pkgName, names.iface))
			continue
		}
		if len(set.Skipped) > 0 {
			var parts []string
			for _, s := range set.Skipped {
				parts = append(parts, s.Name+" ("+s.Reason+")")
			}
			notes = append(notes, fmt.Sprintf("%s: %s leaves out %s — call them as package functions, or reshape them as func(ctx context.Context, db orm.Context, …)", rel, names.iface, strings.Join(parts, ", ")))
		}
		if len(set.Queries) == 0 {
			continue
		}
		if project == nil {
			project = projectDeclarations(dbDir, relDir)
		}
		if clash := extStoreClash(names, generated, project, relDir); clash != "" {
			notes = append(notes, fmt.Sprintf("%s: no %s for %s's queries — %s", rel, names.iface, ent.Name, clash))
			continue
		}
		exts = append(exts, ormOutput{file: entityExtStoreFile(ent), entity: ent.Name, src: renderEntityExtStoreFile(pkgName, ent, set)})
	}
	return exts, notes
}

// extStoreClash names the first of an ext store's identifiers something else
// already declares, "" when none does.
func extStoreClash(names extStoreNames, generated, project map[string]string, relDir string) string {
	for _, name := range names.declared() {
		if at, ok := project[name]; ok {
			return fmt.Sprintf("%s is already declared at %s; rename that to get the generated store", displayDeclName(name), at)
		}
		if file, ok := generated[name]; ok {
			return fmt.Sprintf("%s is already declared by forge's %s/%s", displayDeclName(name), relDir, file)
		}
	}
	return ""
}

// renderEntityExtStoreFile is ent's <entity>_ext_store_gen.go: the interface
// over set's queries, the adapter forwarding to them, its constructor, and
// the aggregate's accessor.
func renderEntityExtStoreFile(pkgName string, ent config.PlanEntity, set contract.ExtQuerySet) []byte {
	n := extStoreNamesFor(ent)
	extFile := entityRepoExtFile(ent)
	orm := set.ORMName

	var b strings.Builder
	writeORMGeneratedHeader(&b, "the exported query functions of "+extFile+" beside it.")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	// The standard library, then everything else: goimports' grouping, so
	// no formatter pass rewrites the file after its hash is stamped.
	b.WriteString("import (\n")
	for _, std := range []bool{true, false} {
		wrote := false
		for _, imp := range set.Imports {
			if isStdlibImport(imp.Path) == std {
				fmt.Fprintf(&b, "\t%s\n", imp.Declaration())
				wrote = true
			}
		}
		if wrote && std {
			b.WriteString("\n")
		}
	}
	b.WriteString(")\n\n")

	fmt.Fprintf(&b, "// %s is the hand-written queries of %s — each exported\n", n.iface, extFile)
	fmt.Fprintf(&b, "// func(ctx context.Context, db %s.Context, …) there — with the handle bound,\n", orm)
	b.WriteString("// so a service can declare it as a Deps field (forge wires and mocks it)\n")
	b.WriteString("// instead of hand-writing a passthrough adapter. It follows that file: add,\n")
	fmt.Fprintf(&b, "// change or remove a query there and re-run forge generate. %s's generated\n", ent.Name)
	fmt.Fprintf(&b, "// CRUD is %sStore.\n", ent.Name)
	fmt.Fprintf(&b, "type %s interface {\n", n.iface)
	for _, q := range set.Queries {
		for _, l := range q.Doc {
			b.WriteString("\t" + l + "\n")
		}
		fmt.Fprintf(&b, "\t%s%s\n\n", q.Name, q.String())
	}
	b.WriteString("\t// WithTx returns the same store bound to an explicit transaction handle.\n")
	b.WriteString("\t// Code using RunTx does not need it: called with the ctx RunTx hands its\n")
	b.WriteString("\t// fn, a query joins that transaction.\n")
	fmt.Fprintf(&b, "\tWithTx(tx %s.Context) %s\n", orm, n.iface)
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "// %s forwards each %s method to the package function of\n", n.adapter, n.iface)
	b.WriteString("// the same name, passing the handle it holds.\n")
	fmt.Fprintf(&b, "type %s struct{ db %s.Context }\n\n", n.adapter, orm)
	fmt.Fprintf(&b, "// %s returns %s's ext store bound to db: the *orm.Client for\n", n.ctor, ent.Name)
	b.WriteString("// ordinary use, or a transaction handle to scope every call to it.\n")
	fmt.Fprintf(&b, "func %s(db %s.Context) %s { return %s{db: db} }\n\n", n.ctor, orm, n.iface, n.adapter)
	for _, q := range set.Queries {
		writeExtForwarder(&b, n.adapter, q)
	}
	fmt.Fprintf(&b, "func (s %s) WithTx(tx %s.Context) %s { return %s{db: tx} }\n\n", n.adapter, orm, n.iface, n.adapter)
	fmt.Fprintf(&b, "var _ %s = %s{}\n\n", n.iface, n.adapter)
	fmt.Fprintf(&b, "// %s returns %s's ext store, bound to the aggregate's handle.\n", n.accessor, ent.Name)
	fmt.Fprintf(&b, "func (s Store) %s() %s { return %s{db: s.db} }\n", n.accessor, n.iface, n.adapter)
	return formatStoreSource(b.String())
}

// writeExtForwarder writes the adapter method for q: its signature, with a
// receiver no parameter or result name shadows, and one call to the package
// function with the handle put back in second place. A parameter named after
// the function itself is renamed here (not in the interface), or the call
// would name the parameter.
func writeExtForwarder(b *strings.Builder, adapter string, q contract.ExtQuery) {
	taken := map[string]bool{q.Name: true}
	for _, p := range q.Params {
		taken[p.Name] = true
	}
	for _, r := range q.Results {
		if r.Name != "" {
			taken[r.Name] = true
		}
	}
	recv := "s"
	for i := 2; taken[recv]; i++ {
		recv = fmt.Sprintf("s%d", i)
	}
	fwd := q
	fwd.Params = append([]contract.ExtParam(nil), q.Params...)
	args := make([]string, 0, len(q.Params)+1)
	for i, p := range fwd.Params {
		if p.Name == q.Name {
			name := p.Name + "Arg"
			for j := 2; taken[name]; j++ {
				name = fmt.Sprintf("%sArg%d", p.Name, j)
			}
			taken[name] = true
			fwd.Params[i].Name = name
		}
		arg := fwd.Params[i].Name
		if p.Variadic {
			arg += "..."
		}
		args = append(args, arg)
		if i == 0 {
			args = append(args, recv+".db")
		}
	}
	call := fmt.Sprintf("%s(%s)", q.Name, strings.Join(args, ", "))
	if len(q.Results) > 0 {
		call = "return " + call
	}
	fmt.Fprintf(b, "func (%s %s) %s%s {\n\t%s\n}\n\n", recv, adapter, q.Name, fwd.String(), call)
}

// isStdlibImport reports whether an import path is the standard library's:
// its first element has no dot (goimports' own test).
func isStdlibImport(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
