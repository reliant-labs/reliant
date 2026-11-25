package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// CRUD ops over a declared store.
//
// The generated ops used to reach persistence only through the ORM's free
// functions — db.CreateWidget(ctx, s.deps.DB, e) — so the one dependency a
// CRUD handler could have was `DB orm.Context`, and the only way to test an
// owned service's CRUD path was a real Postgres. The generated
// <Entity>Store (internal/db/<entity>_store_gen.go) is that same CRUD
// surface shaped as an interface, with the handle bound; composition
// already constructs it for any Deps field that names it, and the test
// harness already builds a double for it.
//
// So the selector is the type, as everywhere else in forge: a handler
// whose Deps declares a field of the ORM package's `<Entity>Store` gets
// ops for that entity that call `s.deps.<Field>.<Method>(ctx, …)`. An
// entity with no such field keeps the free-function call over s.deps.DB,
// byte for byte (TestCRUDOps_DBShapeRendersUnchanged), so the change is
// additive: nothing a project already declares changes meaning except a
// store field a CRUD entity's ops can now use — which was the same
// generated CRUD surface over the same database.
//
// The store's methods are the delegates' names minus the handle
// (plan_orm_store_gen.go), so the ops call the same function either way.

// crudStoreFields maps each entity name to the Deps field in handlerDir
// typed as that entity's generated store, `<orm>.<Entity>Store`, where
// <orm> is an import of ormImportPath. The aggregate `Store` (a struct
// bound to one handle, not mockable) and an `<Entity>ExtStore` (hand-written
// queries, not the CRUD surface) never match. An optional field
// (`forge:optional-dep`) never matches either: an op cannot fall back when
// it is nil. When two fields name one store, the first declared wins.
func crudStoreFields(handlerDir, ormImportPath string) map[string]string {
	fields, err := ParseServiceDeps(handlerDir)
	if err != nil || len(fields) == 0 {
		return nil
	}
	imports, _ := collectImports(handlerDir)
	out := map[string]string{}
	for _, f := range fields {
		if f.Optional || f.Name == "" {
			continue
		}
		qual, typeName, ok := strings.Cut(strings.TrimSpace(f.Type), ".")
		if !ok || imports[qual] != ormImportPath {
			continue
		}
		entity, isStore := strings.CutSuffix(typeName, "Store")
		if !isStore || entity == "" || strings.HasSuffix(entity, "Ext") || !token.IsIdentifier(typeName) {
			continue
		}
		if _, taken := out[entity]; !taken {
			out[entity] = f.Name
		}
	}
	return out
}

// ormImportPathFor is the import path of the project's generated ORM.
func ormImportPathFor(modulePath string, orm config.ORMConfig) string {
	return modulePath + "/" + orm.PackageDir()
}

// crudOpsStoreFieldRefs returns the Deps fields the handler's generated
// ops file (handlers_crud_ops_gen.go) calls through, other than DB: the
// store fields GenerateCRUDHandlers routed an entity's ops over. The test
// harness reads it to know which store fields the scaffolded real-database
// tests reach, and so must default to a real store rather than a double.
// It reads what was generated rather than recomputing the choice, so the
// two can never disagree.
func crudOpsStoreFieldRefs(handlerDir string) map[string]bool {
	path := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "deps" {
			return true
		}
		if recv, ok := inner.X.(*ast.Ident); ok && recv.Name == "s" && sel.Sel.Name != "DB" {
			out[sel.Sel.Name] = true
		}
		return true
	})
	return out
}

// DepsStoreDefault is a Deps field the test harness defaults to the real
// generated store over its test database:
// `<Qual>.NewStore(cfg.database()).<Accessor>()`, the expression compose.go
// builds over the ORM.
type DepsStoreDefault struct {
	// FieldName is the Deps field ("Widgets").
	FieldName string
	// Interface is the field's type as the harness spells it, for the
	// generated comment ("db.WidgetStore").
	Interface string
	// Qual is the identifier the harness imports the ORM package as.
	Qual string
	// Accessor is the aggregate Store's accessor for the entity
	// ("Widgets"), naming.Pluralize of the entity as
	// plan_orm_store_gen.go names it.
	Accessor string
	// ExtraImports is the ORM package's import.
	ExtraImports []ExtraImport
}

// computeStoreDefaults is the harness half of CRUD ops over a declared
// store: every store field (crudStoreFields) the generated ops file calls
// through (crudOpsStoreFieldRefs). A store field the ops do not use keeps
// the double the harness gave it before — nothing about it changed.
func computeStoreDefaults(handlerDir, ormImport string, orm config.ORMConfig) []DepsStoreDefault {
	routed := crudOpsStoreFieldRefs(handlerDir)
	if len(routed) == 0 {
		return nil
	}
	fields := crudStoreFields(handlerDir, ormImport)
	entities := make([]string, 0, len(fields))
	for entity := range fields {
		entities = append(entities, entity)
	}
	sort.Strings(entities)
	qual := orm.PackageName()
	var out []DepsStoreDefault
	for _, entity := range entities {
		field := fields[entity]
		if !routed[field] {
			continue
		}
		out = append(out, DepsStoreDefault{
			FieldName:    field,
			Interface:    qual + "." + entity + "Store",
			Qual:         qual,
			Accessor:     naming.Pluralize(entity),
			ExtraImports: []ExtraImport{{Path: ormImport, Alias: qual}},
		})
	}
	return out
}

// markRealStores flags the doubles of the fields a store default fills, so
// NewTest<Svc> installs the real store instead of them.
func markRealStores(defaults []DepsStoreDefault, stubs []DepsAutoStub, mocks []DepsAutoMock) {
	filled := map[string]bool{}
	for _, d := range defaults {
		filled[d.FieldName] = true
	}
	for i := range stubs {
		stubs[i].RealStore = filled[stubs[i].FieldName]
	}
	for i := range mocks {
		mocks[i].RealStore = filled[mocks[i].FieldName]
	}
}
