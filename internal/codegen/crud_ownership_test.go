package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// crud_ownership_test.go pins the projection of a table's `forge:owner=`
// declaration into the generated ops: every op routes through crud's
// Owned* helpers, the service gains its Owners dep, and the shim carries no
// second, hand-finished scope. An undeclared table is pinned unchanged.

func generateOwnedCRUD(t *testing.T, ownership []schemadef.ResolvedOwnerTerm, nullableOrg bool) (ops, shim, service string) {
	t.Helper()
	projectDir, handlerDir := ownerTestHandlerDir(t)
	svc, entities := ownerTestServiceAndEntity(false, true)
	entities[0].Ownership = ownership
	entities[0].Columns = append(entities[0].Columns, EntityColumn{Name: "org_id", Type: "string", NotNull: !nullableOrg})
	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(handlerDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), name, b, parser.AllErrors); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, b)
		}
		return string(b)
	}
	return read("handlers_crud_ops_gen.go"), read("handlers_crud.go"), read("service.go")
}

func TestOwnedEntity_EveryOpRoutesThroughOwnedHelpers(t *testing.T) {
	ops, shim, service := generateOwnedCRUD(t, []schemadef.ResolvedOwnerTerm{
		{Kind: "user", Column: "company_id"},
		{Kind: "org", Column: "org_id"},
	}, true)

	for _, want := range []string{
		"var crewOwnership = crud.Ownership{",
		`{Kind: "user", Column: "company_id"},`,
		`{Kind: "org", Column: "org_id"},`,
		"crud.OwnedCreate(ctx, s.deps.DB, s.deps.Owners, crewOwnership, \"id\", entity,",
		"crud.OwnedGet(ctx, s.deps.Owners, crewOwnership, \"id\", id, opts,",
		"opts, err := crud.OwnedList(ctx, s.deps.Owners, crewOwnership, opts)",
		"crud.OwnedUpdate(ctx, s.deps.DB, s.deps.Owners, crewOwnership, \"id\", entity.ID, entity, opts,",
		"crud.OwnedDelete(ctx, s.deps.DB, s.deps.Owners, crewOwnership, \"id\", id, opts,",
		// The stamp fills the first direct owner only when the row names none.
		`if e.CompanyID == "" && (e.OrgID == nil || *e.OrgID == "") {`,
		"e.CompanyID = owner",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must contain %q:\n%s", want, ops)
		}
	}
	for _, unwanted := range []string{"crud.ScopedGet(", "crud.ScopedUpdate(", "crud.ScopedDelete("} {
		if strings.Contains(ops, unwanted) {
			t.Errorf("an owned entity's op must not fall back to %s:\n%s", unwanted, ops)
		}
	}
	if !strings.Contains(shim, "Auth: SCOPED by the table's forge:owner= declaration") || strings.Contains(shim, "AUTHENTICATED, UNSCOPED") {
		t.Errorf("the shim must say the op scopes it, and never call it unscoped:\n%s", shim)
	}
	if strings.Contains(shim, "FORGE_SCAFFOLD") {
		t.Errorf("a table-level owner must not scaffold the column marker's wrapper:\n%s", shim)
	}
	if !strings.Contains(service, "Owners crud.OwnerResolver") || !strings.Contains(service, `"github.com/reliant-labs/forge/pkg/crud"`) {
		t.Errorf("Deps must gain Owners crud.OwnerResolver and its import:\n%s", service)
	}
}

func TestOwnedEntity_JoinPathHasNoStamp(t *testing.T) {
	ops, _, _ := generateOwnedCRUD(t, []schemadef.ResolvedOwnerTerm{{
		Kind: "user", Column: "company_id", Constraint: "crews_company_id_fkey",
		Hops: []schemadef.OwnerHop{{Schema: "cp", Table: "companies", Key: "id", Column: "owner_id"}},
	}}, false)
	for _, want := range []string{
		`{Kind: "user", Column: "company_id", Constraint: "crews_company_id_fkey", Hops: []crud.OwnerHop{{Schema: "cp", Table: "companies", Key: "id", Column: "owner_id"}}},`,
		"\t\t\t\tnil,\n",
		"// crewOwnership is table crews's `forge:owner=user:company_id>owner_id` declaration,",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops must contain %q:\n%s", want, ops)
		}
	}
}

func TestUnownedEntity_OpsUnchanged(t *testing.T) {
	ops, _, service := generateOwnedCRUD(t, nil, false)
	if strings.Contains(ops, "Owned") || strings.Contains(ops, "Ownership") {
		t.Errorf("an entity with no forge:owner= must render the unscoped ops:\n%s", ops)
	}
	if strings.Contains(service, "Owners") {
		t.Errorf("an unowned service must not gain an Owners dep:\n%s", service)
	}
}

// The stamp closure names the entity through the ORM qualifier the ops file
// binds (internal/store in a project whose ORM lives there, or an alias when
// `db` is taken), never a hard-coded `db.`.
func TestOwnerStamp_UsesTheOpsFilesORMQualifier(t *testing.T) {
	_, entities := ownerTestServiceAndEntity(false, true)
	e := entities[0]
	e.Ownership = []schemadef.ResolvedOwnerTerm{{Kind: "user", Column: "company_id"}}
	render := ownerStampRenderer(true, e)
	if render == nil {
		t.Fatal("an owned create with a direct owner column must stamp")
	}
	for qual, want := range map[string]string{"": "func(e *db." + e.Name + ",", "store": "func(e *store." + e.Name + ",", "ormdb": "func(e *ormdb." + e.Name + ","} {
		if got := render(qual); !strings.HasPrefix(got, want) {
			t.Errorf("qualifier %q: stamp = %q, want prefix %q", qual, got, want)
		}
	}
	if ownerStampRenderer(false, e) != nil {
		t.Error("an op that stamps nothing must not get a renderer")
	}
}
