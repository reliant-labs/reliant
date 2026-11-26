package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// crewsServiceGo is a crews handler service.go whose Deps struct holds
// fields (one per line, as written) and imports imports.
func crewsServiceGo(imports []string, fields ...string) string {
	var b strings.Builder
	b.WriteString("package crews\n\nimport (\n")
	for _, imp := range imports {
		b.WriteString("\t" + imp + "\n")
	}
	b.WriteString(")\n\ntype Deps struct {\n")
	for _, f := range fields {
		b.WriteString("\t" + f + "\n")
	}
	b.WriteString("}\n\ntype Service struct {\n\tdeps Deps\n}\n")
	return b.String()
}

// generateCrewsWith writes serviceGo as the crews handler's service.go and
// generates its CRUD, returning the ops file and the service.go after.
func generateCrewsWith(t *testing.T, projectDir, handlerDir, serviceGo string) (ops, svc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}
	generateCrews(t, projectDir)
	ops = readTestFile(t, filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if _, err := parser.ParseFile(token.NewFileSet(), "ops.go", ops, 0); err != nil {
		t.Fatalf("ops file does not parse: %v\n%s", err, ops)
	}
	return ops, readTestFile(t, filepath.Join(handlerDir, "service.go"))
}

// storeCalls are every persistence call the crews quintet's ops make, over
// a Deps field named field.
func storeCalls(field string) []string {
	p := "s.deps." + field + "."
	return []string{
		p + "CreateCrew(ctx, entity)",
		p + "GetCrewByID(ctx, id)",
		p + "ListCrew(ctx, opts...)",
		p + "UpdateCrew(ctx, entity)",
		p + "DeleteCrew(ctx, id)",
	}
}

var depsDBField = regexp.MustCompile(`\tDB\s+orm\.Context`)

// TestCRUDOps_RoutesThroughDeclaredStore: a Deps field typed as the entity's
// generated store makes every op persist through it, and — with no op left
// on s.deps.DB — forge does not inject a DB field.
func TestCRUDOps_RoutesThroughDeclaredStore(t *testing.T) {
	projectDir, handlerDir := ownerTestHandlerDir(t)
	ops, svc := generateCrewsWith(t, projectDir, handlerDir,
		crewsServiceGo([]string{`"example.com/test/internal/db"`}, "Crews db.CrewStore"))
	for _, want := range storeCalls("Crews") {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must call %q:\n%s", want, ops)
		}
	}
	if strings.Contains(ops, "s.deps.DB") {
		t.Errorf("no op may reach s.deps.DB when the entity's store is declared:\n%s", ops)
	}
	if depsDBField.MatchString(svc) {
		t.Errorf("forge injected a DB field no op uses:\n%s", svc)
	}
}

// TestCRUDOps_StoreBesideDB: a handler keeping DB beside the store gets the
// store routing, and its DB field and service.go are left as written.
func TestCRUDOps_StoreBesideDB(t *testing.T) {
	projectDir, handlerDir := ownerTestHandlerDir(t)
	src := crewsServiceGo([]string{`"github.com/reliant-labs/forge/pkg/orm"`, `"example.com/test/internal/db"`},
		"DB    orm.Context", "Crews db.CrewStore")
	ops, svc := generateCrewsWith(t, projectDir, handlerDir, src)
	for _, want := range storeCalls("Crews") {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must call %q:\n%s", want, ops)
		}
	}
	if svc != src {
		t.Errorf("service.go changed:\n%s", svc)
	}
}

// TestCRUDOps_StoreSelection pins which Deps fields count as the entity's
// store. Anything else leaves the ops on s.deps.DB — and forge injects DB.
func TestCRUDOps_StoreSelection(t *testing.T) {
	cases := []struct {
		name    string
		imports []string
		fields  []string
		want    string // the field the ops route through; "" = s.deps.DB
	}{
		{"aliased import", []string{`orm2 "example.com/test/internal/db"`}, []string{"Crews orm2.CrewStore"}, "Crews"},
		{"first of two wins", []string{`"example.com/test/internal/db"`}, []string{"A db.CrewStore", "B db.CrewStore"}, "A"},
		{"optional dep", []string{`"example.com/test/internal/db"`}, []string{"// forge:optional-dep", "Crews db.CrewStore"}, ""},
		{"aggregate store", []string{`"example.com/test/internal/db"`}, []string{"All db.Store"}, ""},
		{"ext store", []string{`"example.com/test/internal/db"`}, []string{"Crews db.CrewExtStore"}, ""},
		{"another entity's store", []string{`"example.com/test/internal/db"`}, []string{"Jobs db.JobStore"}, ""},
		{"same name, other package", []string{`db "example.com/test/internal/fakes"`}, []string{"Crews db.CrewStore"}, ""},
		{"pointer", []string{`"example.com/test/internal/db"`}, []string{"Crews *db.CrewStore"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			projectDir, handlerDir := ownerTestHandlerDir(t)
			ops, svc := generateCrewsWith(t, projectDir, handlerDir, crewsServiceGo(c.imports, c.fields...))
			if c.want == "" {
				if !strings.Contains(ops, "db.CreateCrew(ctx, s.deps.DB, entity)") {
					t.Errorf("ops must stay on s.deps.DB:\n%s", ops)
				}
				if !depsDBField.MatchString(svc) {
					t.Errorf("ops on s.deps.DB need the DB field injected:\n%s", svc)
				}
				return
			}
			for _, want := range storeCalls(c.want) {
				if !strings.Contains(ops, want) {
					t.Errorf("ops must call %q:\n%s", want, ops)
				}
			}
		})
	}
}

// TestCRUDOps_RelocatedORMStore: with database.orm.package set, the store
// is the relocated package's; the project's own internal/db is not it.
func TestCRUDOps_RelocatedORMStore(t *testing.T) {
	projectDir, handlerDir := storeProject(t)
	ops, _ := generateCrewsWith(t, projectDir, handlerDir,
		crewsServiceGo([]string{`"` + storePath + `"`}, "Crews store.CrewStore"))
	for _, want := range storeCalls("Crews") {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must call %q:\n%s", want, ops)
		}
	}

	projectDir, handlerDir = storeProject(t)
	ops, _ = generateCrewsWith(t, projectDir, handlerDir,
		crewsServiceGo([]string{`"` + facadePath + `"`}, "Crews db.CrewStore"))
	if strings.Contains(ops, "s.deps.Crews") {
		t.Errorf("a facade type named like the store is not the ORM's store:\n%s", ops)
	}
}

// TestCRUDOps_CustomReadShapeShimOverStore: the wired custom-read-shape
// shim queries through the store too, and so imports no ORM package it
// would not use.
func TestCRUDOps_CustomReadShapeShimOverStore(t *testing.T) {
	projectDir, handlerDir := ownerTestHandlerDir(t)
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"),
		[]byte(crewsServiceGo([]string{`"example.com/test/internal/db"`}, "Crews db.CrewStore")), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, entities := ownerTestServiceAndEntity(false, true)
	// The response names its rows `results`, not `crews`: the custom read shape.
	svc.Messages = map[string][]MessageFieldDef{
		"ListCrewsRequest":  {},
		"ListCrewsResponse": {{Name: "results", ProtoType: "message", MessageType: "Crew"}},
	}
	var methods []CRUDMethod
	for _, m := range MatchCRUDMethods(svc, entities) {
		if m.Operation == "list" {
			methods = append(methods, m)
		}
	}
	if len(methods) != 1 {
		t.Fatalf("want the list rpc, matched %d", len(methods))
	}
	if _, err := GenerateCRUDHandlers(svc, methods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	shim := readTestFile(t, filepath.Join(handlerDir, "handlers_crud.go"))
	if !strings.Contains(shim, "forge:custom-read-shape") {
		t.Fatalf("fixture must hit the custom read shape:\n%s", shim)
	}
	if !strings.Contains(shim, "s.deps.Crews.ListCrew(ctx, opts...)") {
		t.Errorf("the custom-read-shape body must query the store:\n%s", shim)
	}
	if _, ok := importBindings(t, shim)["db"]; ok {
		t.Errorf("the shim names nothing in the ORM package, so must not import it:\n%s", shim)
	}
}

// TestComputeStoreDefaults reads the routed fields back from the generated
// ops: a store field the ops call through defaults to the real store, one
// they do not keeps its double.
func TestComputeStoreDefaults(t *testing.T) {
	projectDir, handlerDir := ownerTestHandlerDir(t)
	generateCrewsWith(t, projectDir, handlerDir, crewsServiceGo([]string{`"example.com/test/internal/db"`},
		"Crews db.CrewStore", "Jobs  db.JobStore"))
	orm := config.ORMConfig{}
	got := computeStoreDefaults(handlerDir, ormImportPathFor("example.com/test", orm), orm)
	if len(got) != 1 {
		t.Fatalf("want one store default (Crews; Jobs has no CRUD ops), got %+v", got)
	}
	d := got[0]
	if d.FieldName != "Crews" || d.Qual != "db" || d.Accessor != "Crews" || d.Interface != "db.CrewStore" ||
		len(d.ExtraImports) != 1 || d.ExtraImports[0].Path != "example.com/test/internal/db" {
		t.Errorf("store default = %+v", d)
	}

	stubs := []DepsAutoStub{{FieldName: "Crews"}, {FieldName: "Jobs"}}
	markRealStores(got, stubs, nil)
	if !stubs[0].RealStore || stubs[1].RealStore {
		t.Errorf("only Crews' double gives way to the real store: %+v", stubs)
	}
}

// TestComponentTestHelpers_StoreDefault: a handler with a routed store and
// no DB field still gets the test database (WithDB, NewMigratedTestDB) —
// the store default is built over it — but never fills a Deps.DB it does
// not have, and installs the real store, not the stub, by default.
func TestComponentTestHelpers_StoreDefault(t *testing.T) {
	data := goldenHelperData(false)
	data.HasDB = false
	data.UnfilledDeps = nil
	data.StoreDefaults = []DepsStoreDefault{{
		FieldName: "Orders", Interface: "db.OrderStore", Qual: "db", Accessor: "Orders",
		ExtraImports: []ExtraImport{{Path: "example.com/shop/internal/db", Alias: "db"}},
	}}
	data.AutoStubs = []DepsAutoStub{{
		FieldName: "Orders", StubType: "StubOrders", InterfaceQualified: "db.OrderStore",
		CrossPackage: true, RealStore: true,
	}}
	data.ExtraImports = data.StoreDefaults[0].ExtraImports
	out := string(renderProjectTemplate(t, "component_test_helpers.go.tmpl", data))
	if _, err := parser.ParseFile(token.NewFileSet(), "helpers_gen_test.go", out, 0); err != nil {
		t.Fatalf("helpers do not parse: %v\n%s", err, out)
	}
	for _, want := range []string{
		"deps.Orders = db.NewStore(cfg.database()).Orders()",
		"func WithDB(db orm.Context) TestOption",
		"func NewMigratedTestDB(t *testing.T) orm.Context",
		"type StubOrders struct",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"deps.DB = cfg.database()", "deps.Orders = &StubOrders{}"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("must not render %q:\n%s", unwanted, out)
		}
	}
}

// TestCRUDOps_OwnedEntityOverStore: a `forge:owner=` entity with a declared
// store keeps s.deps.DB as the Owned* helpers' transactor — so forge still
// injects DB — while the row reads and writes inside go through the store,
// which joins that transaction through ctx.
func TestCRUDOps_OwnedEntityOverStore(t *testing.T) {
	projectDir, handlerDir := ownerTestHandlerDir(t)
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"),
		[]byte(crewsServiceGo([]string{`"example.com/test/internal/db"`}, "Crews db.CrewStore")), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, entities := ownerTestServiceAndEntity(false, true)
	entities[0].Ownership = []schemadef.ResolvedOwnerTerm{{Kind: "user", Column: "company_id"}}
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	ops := readTestFile(t, filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if _, err := parser.ParseFile(token.NewFileSet(), "ops.go", ops, 0); err != nil {
		t.Fatalf("ops file does not parse: %v\n%s", err, ops)
	}
	for _, want := range []string{
		"crud.OwnedCreate(ctx, s.deps.DB, s.deps.Owners,",
		"s.deps.Crews.CreateCrew(ctx, entity)",
		"s.deps.Crews.GetCrewByID(ctx, id)",
		"s.deps.Crews.DeleteCrew(ctx, id)",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops missing %q:\n%s", want, ops)
		}
	}
	if regexp.MustCompile(`db\.\w+Crew\w*\(ctx, s\.deps\.DB`).MatchString(ops) {
		t.Errorf("no row call may stay on the free functions once the store is declared:\n%s", ops)
	}
	if svc := readTestFile(t, filepath.Join(handlerDir, "service.go")); !depsDBField.MatchString(svc) {
		t.Errorf("an owned op's transactor is s.deps.DB, so DB must be injected:\n%s", svc)
	}
}
