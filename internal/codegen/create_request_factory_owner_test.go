package codegen

import (
	"context"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scaffolded lifecycle test creates its child under the CALLER's owner —
// the scoped handler stamps tenant_id from the claims — while the factory
// seeded the child's parents under owners the seed planner invented. With
// any same-owner guard (here the trigger a multi-tenant app uses in place of
// a composite (tenant_id, id) foreign key), every scoped create failed.
//
// The schema: tenants; strategies owned by a tenant; hypotheses owned by a
// tenant and pointing at a strategy, which must be the same tenant's.
const ownedSchema = `
CREATE TABLE tenants (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    slug TEXT NOT NULL UNIQUE
);
CREATE TABLE strategies (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    tenant_id TEXT NOT NULL REFERENCES tenants (id),
    name TEXT NOT NULL
);
COMMENT ON COLUMN strategies.tenant_id IS 'forge:owner';
CREATE TABLE hypotheses (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    tenant_id TEXT NOT NULL REFERENCES tenants (id),
    strategy_id TEXT NOT NULL REFERENCES strategies (id),
    claim TEXT NOT NULL
);
COMMENT ON COLUMN hypotheses.tenant_id IS 'forge:owner';
CREATE FUNCTION enforce_same_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM strategies s WHERE s.id = NEW.strategy_id AND s.tenant_id = NEW.tenant_id) THEN
        RAISE EXCEPTION 'hypothesis % crosses tenants', NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER hypotheses_same_tenant BEFORE INSERT ON hypotheses
    FOR EACH ROW EXECUTE FUNCTION enforce_same_tenant();
`

func ownedHypothesisMethods() (ServiceDef, []CRUDMethod) {
	entity := EntityDef{
		Name: "Hypothesis", TableName: "hypotheses", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "tenant_id", GoName: "TenantId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "strategy_id", GoName: "StrategyId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "claim", GoName: "Claim", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}
	svc := ServiceDef{
		Name: "GrowthService", Package: "growth.v1", GoPackage: "example.com/test/gen/proto/services/growth/v1",
		PkgName: "growthv1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreateHypothesisRequest": {
				{Name: "tenant_id", ProtoType: "string"},
				{Name: "strategy_id", ProtoType: "string"},
				{Name: "claim", ProtoType: "string"},
			},
		},
	}
	return svc, []CRUDMethod{{
		Method: MethodTemplateData{Name: "CreateHypothesis", InputType: "CreateHypothesisRequest", OutputType: "CreateHypothesisResponse"},
		Entity: entity, Operation: "create",
	}}
}

func TestCreateRequestFactory_SeedsParentsUnderTheCallersOwner_PG(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	projectDir := t.TempDir()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(ownedSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, methods := ownedHypothesisMethods()
	fx, err := loadCRUDTestFixtures(projectDir, methods)
	if err != nil || fx == nil {
		t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
	}
	defer fx.close()
	fx.ensurePlan("hypotheses")
	specs := buildCreateRequestSpecs(context.Background(), svc, methods, fx)
	if len(specs) != 1 || specs[0].failure != "" {
		t.Fatalf("specs = %+v", specs)
	}
	s := specs[0]
	if !s.ownerAware || len(s.ownerCells) == 0 || s.ownerField != "TenantId" {
		t.Fatalf("an owned entity's factory must be owner-aware: aware=%v cells=%v field=%q", s.ownerAware, s.ownerCells, s.ownerField)
	}

	// The planned rows, as planned, fail the guard for a child under the
	// caller's owner: the reproduction.
	ctx := context.Background()
	db := fx.shadow.DB()
	childUnder := func(parentSQL string) error {
		return execRolledBack(ctx, db,
			sqlStep{query: parentSQL},
			sqlStep{query: `INSERT INTO tenants (id, slug) VALUES ('tenant-1', 'tenant-1') ON CONFLICT DO NOTHING`},
			sqlStep{
				query: `INSERT INTO hypotheses (id, tenant_id, strategy_id, claim) VALUES ('h1', 'tenant-1', $1, 'c')`,
				args:  []any{strategyRow0(t, s)},
			})
	}
	if err := childUnder(s.parentSQL); err == nil || !strings.Contains(err.Error(), "crosses tenants") {
		t.Fatalf("expected the planned parents to violate the same-tenant guard, got %v", err)
	}
	// Under FactoryOwner the parents move to the caller's owner, and the
	// same child is accepted. rewriteOwnerLiterals is what the emitted
	// withFactoryOwner does, verbatim.
	if err := childUnder(rewriteOwnerLiterals(s.parentSQL, s.ownerCells, "tenant-1")); err != nil {
		t.Fatalf("parents seeded under the caller's owner must satisfy the guard: %v\n%s",
			err, rewriteOwnerLiterals(s.parentSQL, s.ownerCells, "tenant-1"))
	}

	out := renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "growth", pbImport: svc.GoPackage, creates: specs,
	})
	content := string(out)
	for _, want := range []string{
		"func NewCreateHypothesisRequest(t testing.TB, database orm.Context, variant int, opts ...FactoryOption) *pb.CreateHypothesisRequest {",
		"seedFactoryParents(t, database, withFactoryOwner(createHypothesisRequestParentSQL, createHypothesisRequestParentOwners, owner))",
		"req.TenantId = owner",
		"func FactoryOwner(owner string) FactoryOption {",
		"\t\"strings\"\n",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("owner-aware factory file missing %q:\n%s", want, content)
		}
	}
	// The emitted helper and the copy executed above are one body.
	src, err := os.ReadFile("create_request_factory.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "func rewriteOwnerLiterals(sql string, cells [][2]int, owner string) string {\n"+rewriteOwnerLiteralsBody+"}\n") ||
		!strings.Contains(content, "func withFactoryOwner(sql string, cells [][2]int, owner string) string {\n"+rewriteOwnerLiteralsBody+"}\n") {
		t.Errorf("the emitted withFactoryOwner drifted from rewriteOwnerLiterals:\n%s", content)
	}
	if _, err := format.Source(out); err != nil {
		t.Errorf("factory file is not valid Go: %v\n%s", err, content)
	}
}

// strategyRow0 is the strategy id variant 0 of the request references.
func strategyRow0(t *testing.T, s createRequestSpec) string {
	t.Helper()
	for _, f := range s.fields {
		if f.ProtoName == "StrategyId" {
			return strings.Trim(f.TestValue, `"`)
		}
	}
	t.Fatalf("request has no StrategyId field: %+v", s.fields)
	return ""
}

// A factory whose request touches no owned table keeps its plain signature,
// so FactoryOwner on it is a compile error rather than a silent no-op.
func TestCreateRequestFactory_UnownedKeepsPlainSignature(t *testing.T) {
	spec := createRequestSpec{funcName: "NewCreateWidgetRequest", inputType: "CreateWidgetRequest", entity: "Widget"}
	out := string(renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "widgets", pbImport: "example.com/test/gen/widgets/v1", creates: []createRequestSpec{spec},
	}))
	if !strings.Contains(out, "func NewCreateWidgetRequest(t testing.TB, database orm.Context, variant int) *pb.CreateWidgetRequest {") {
		t.Errorf("unowned factory signature changed:\n%s", out)
	}
	if strings.Contains(out, "FactoryOwner") || strings.Contains(out, "\"strings\"") {
		t.Errorf("a file with no owner-aware factory must not declare the owner option:\n%s", out)
	}
}

// A table owned by its own key (COMMENT ON COLUMN tenants.id IS
// 'forge:owner') with no FK parents is owner-aware, but its factory has no
// parents to seed and no owner field to set. It must not declare an unused
// owner variable, which failed `forge generate`'s compile check:
// "declared and not used: owner".
func TestCreateRequestFactory_SelfOwnedWithoutParentsCompiles(t *testing.T) {
	spec := createRequestSpec{
		funcName: "NewCreateTenantRequest", inputType: "CreateTenantRequest", entity: "Tenant",
		ownerAware: true,
		fields:     []CRUDTestFieldData{{ProtoName: "Slug", TestValue: `"a"`, TestValue2: `"b"`}},
	}
	out := string(renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "tenants", pbImport: "example.com/test/gen/tenants/v1", creates: []createRequestSpec{spec},
	}))
	if !strings.Contains(out, "func NewCreateTenantRequest(t testing.TB, database orm.Context, variant int, opts ...FactoryOption) *pb.CreateTenantRequest {") {
		t.Fatalf("self-owned factory must still take FactoryOwner:\n%s", out)
	}
	if strings.Contains(out, "owner := factoryOwner(opts)") {
		t.Errorf("a factory with no parents and no owner field must not declare owner:\n%s", out)
	}
	if _, err := format.Source([]byte(out)); err != nil {
		t.Errorf("rendered factory file does not parse: %v\n%s", err, out)
	}
}

// FactoryOwner rewrites owner cells, not owner-shaped text: a cell outside
// the owner-linked columns keeps its value even when it equals a planned
// owner (a tenant whose slug is its id), and a literal with an owner value
// inside it is not touched at all.
func TestOwnerCellRanges_RewritesOnlyOwnerLinkedCells(t *testing.T) {
	const sql = `INSERT INTO "tenants" ("id", "slug") VALUES
    ('tenant-a', 'tenant-a'),
    ('tenant-b', 'it''s tenant-b; really')
ON CONFLICT ("id") DO NOTHING;
INSERT INTO "audit" DEFAULT VALUES;
INSERT INTO "strategies" ("id", "tenant_id", "name", "uid") VALUES
    ('s-0', 'tenant-a', 'tenant-a', ARRAY['tenant-a', 'x']),
    ('s-1', 'tenant-b'::text, 'n(1)', ARRAY['y'])
ON CONFLICT ("id") DO NOTHING;`
	linked := map[string]map[string]bool{
		`"tenants"`:    {"id": true},
		`"strategies"`: {"tenant_id": true},
	}
	planned := map[string]bool{"'tenant-a'": true, "'tenant-b'": true}
	cells, err := ownerCellRanges(sql, linked, planned)
	if err != nil {
		t.Fatal(err)
	}
	got := rewriteOwnerLiterals(sql, cells, "o'1")
	want := `INSERT INTO "tenants" ("id", "slug") VALUES
    ('o''1', 'tenant-a'),
    ('o''1', 'it''s tenant-b; really')
ON CONFLICT ("id") DO NOTHING;
INSERT INTO "audit" DEFAULT VALUES;
INSERT INTO "strategies" ("id", "tenant_id", "name", "uid") VALUES
    ('s-0', 'o''1', 'tenant-a', ARRAY['tenant-a', 'x']),
    ('s-1', 'o''1'::text, 'n(1)', ARRAY['y'])
ON CONFLICT ("id") DO NOTHING;`
	if got != want {
		t.Errorf("rewrite touched a cell that is not an owner cell:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if rewriteOwnerLiterals(sql, cells, "") != sql {
		t.Error("an empty owner must leave the SQL as planned")
	}
}

// A seed statement the lexer cannot read is an error, never a silent no-op.
func TestOwnerCellRanges_RejectsUnreadableSQL(t *testing.T) {
	for _, sql := range []string{
		`DELETE FROM "tenants";`,
		`INSERT INTO "tenants" ("id") VALUES
    ('a', 'b')
ON CONFLICT DO NOTHING;`,
		`INSERT INTO "tenants" ("id") VALUES
    ('unterminated)
ON CONFLICT DO NOTHING;`,
	} {
		if _, err := ownerCellRanges(sql, nil, map[string]bool{"'a'": true}); err == nil {
			t.Errorf("expected an error for:\n%s", sql)
		}
	}
}
