package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The read-only role's grants are projected from the applied schema and the
// wire messages' `forge:secret` markers. These pin the projection on a fixture
// schema with no database; the Postgres side (the role really cannot select the
// column) is pkg/migratekit/grants_pg_test.go.

func col(name string) schemadef.Column {
	return schemadef.Column{Name: name, Type: schemadef.CanonicalType("string")}
}

func fixtureTable(name string, cols ...string) schemadef.Table {
	t := schemadef.Table{Name: name, Schema: "public", PKCols: []string{"id"}}
	for _, c := range cols {
		t.Columns = append(t.Columns, col(c))
	}
	return t
}

// secretService is a service whose `Account` entity message marks api_token
// `forge:secret`, and which declares CRUD for it.
func secretService(entity string, methods []string, fields ...SchemaFieldDef) ServiceDef {
	svc := ServiceDef{Name: entity + "Service", Package: "app.v1", Schemas: map[string][]SchemaFieldDef{"app.v1." + entity: fields}}
	for _, m := range methods {
		svc.Methods = append(svc.Methods, Method{Name: m})
	}
	return svc
}

func secretFields(secret ...string) []SchemaFieldDef {
	var out []SchemaFieldDef
	for _, n := range []string{"id", "email", "api_token"} {
		f := SchemaFieldDef{Name: n, Kind: "string"}
		for _, s := range secret {
			if s == n {
				f.Secret = true
			}
		}
		out = append(out, f)
	}
	return out
}

func grantTableByName(t *testing.T, got []dbgrants.Table, name string) dbgrants.Table {
	t.Helper()
	for _, g := range got {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no grant table %q in %+v", name, got)
	return dbgrants.Table{}
}

func secretColumns(tb dbgrants.Table) string {
	var out []string
	for _, c := range tb.Columns {
		if c.Secret {
			out = append(out, c.Name)
		}
	}
	return strings.Join(out, ",")
}

// A secret field on the CRUD-joined entity withholds its column; every other
// column stays readable.
func TestReadOnlyGrantTables_SecretFieldOfACRUDEntityIsWithheld(t *testing.T) {
	tables := []schemadef.Table{fixtureTable("accounts", "id", "email", "api_token")}
	svc := secretService("Account", []string{"GetAccount", "ListAccounts"}, secretFields("api_token")...)
	got := ReadOnlyGrantTables(tables, []ServiceDef{svc})
	if s := secretColumns(grantTableByName(t, got, "accounts")); s != "api_token" {
		t.Errorf("secret columns = %q, want api_token only", s)
	}
}

// A table no service has CRUD rpcs for (every rpc `forge:custom`, or none)
// still has its secrets withheld: the marker lives on the wire message, and the
// message exists whether or not CRUD does.
func TestReadOnlyGrantTables_SecretOfATableWithNoCRUDIsWithheld(t *testing.T) {
	tables := []schemadef.Table{fixtureTable("accounts", "id", "email", "api_token")}
	svc := secretService("Account", nil, secretFields("api_token")...)
	got := ReadOnlyGrantTables(tables, []ServiceDef{svc})
	if s := secretColumns(grantTableByName(t, got, "accounts")); s != "api_token" {
		t.Errorf("secret columns = %q, want api_token: a table with no CRUD rpcs must not lose the marker", s)
	}
}

// An unmanaged service (`forge:unmanaged`) is still a service whose message can
// carry the marker.
func TestReadOnlyGrantTables_SecretInAnUnmanagedServiceIsWithheld(t *testing.T) {
	tables := []schemadef.Table{fixtureTable("accounts", "id", "email", "api_token")}
	svc := secretService("Account", []string{"GetAccount"}, secretFields("api_token")...)
	svc.Unmanaged = true
	got := ReadOnlyGrantTables(tables, []ServiceDef{svc})
	if s := secretColumns(grantTableByName(t, got, "accounts")); s != "api_token" {
		t.Errorf("secret columns = %q, want api_token", s)
	}
}

// The marker on one table's message does not leak onto another table that
// happens to share a column name.
func TestReadOnlyGrantTables_MarkerIsPerTable(t *testing.T) {
	tables := []schemadef.Table{
		fixtureTable("accounts", "id", "email", "api_token"),
		fixtureTable("audits", "id", "api_token"),
	}
	svc := secretService("Account", []string{"GetAccount"}, secretFields("api_token")...)
	got := ReadOnlyGrantTables(tables, []ServiceDef{svc})
	if s := secretColumns(grantTableByName(t, got, "audits")); s != "" {
		t.Errorf("audits secret columns = %q, want none: only Account's message marks a secret", s)
	}
}

// Every table is projected, with or without a message; views are not.
func TestReadOnlyGrantTables_EveryBaseTableAndNoView(t *testing.T) {
	view := fixtureTable("account_summaries", "id", "api_token")
	view.IsView = true
	tables := []schemadef.Table{fixtureTable("notes", "id", "body"), view}
	got := ReadOnlyGrantTables(tables, nil)
	if len(got) != 1 || got[0].Name != "notes" || secretColumns(got[0]) != "" {
		t.Errorf("got %+v, want only the base table notes, nothing secret", got)
	}
}

func TestReadOnlyGrantTables_DefaultsTheSchemaAndSorts(t *testing.T) {
	a, b := fixtureTable("b_table", "id"), fixtureTable("a_table", "id")
	a.Schema, b.Schema = "", "billing"
	got := ReadOnlyGrantTables([]schemadef.Table{a, b}, nil)
	if len(got) != 2 || got[0].Schema != "billing" || got[1].Schema != "public" {
		t.Errorf("got %+v, want billing.a_table then public.b_table", got)
	}
}

// The rendered file never lists a secret column in a GRANT, and lists the rest.
func TestGenerateReadOnlyGrants_RenderedScriptWithholdsTheSecret(t *testing.T) {
	tables := []schemadef.Table{fixtureTable("accounts", "id", "email", "api_token")}
	svc := secretService("Account", []string{"GetAccount"}, secretFields("api_token")...)
	script, err := dbgrants.Render(ReadOnlyGrantTables(tables, []ServiceDef{svc}))
	if err != nil {
		t.Fatal(err)
	}
	grantLine := ""
	for _, l := range strings.Split(script, "\n") {
		if strings.Contains(l, "'accounts'") {
			grantLine = l
		}
	}
	if !strings.Contains(grantLine, "'email'") || !strings.Contains(grantLine, "'id'") {
		t.Errorf("accounts row = %q, want id and email", grantLine)
	}
	if strings.Contains(grantLine, "api_token") {
		t.Errorf("the secret column is in the grant: %q", grantLine)
	}
}

// With no migrations there is nothing to project and nothing is written.
func TestGenerateReadOnlyGrants_NoMigrationsWritesNothing(t *testing.T) {
	dir := t.TempDir()
	wrote, err := GenerateReadOnlyGrants(dir, nil, nil)
	if err != nil || wrote {
		t.Fatalf("wrote=%v err=%v, want neither", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "grants")); !os.IsNotExist(err) {
		t.Errorf("db/grants exists with no migrations: %v", err)
	}
}

// The embed names the grants file only once it exists: `//go:embed` does not
// compile against an empty match, and the embed is rewritten when it appears.
func TestGenerateMigrateEmbedsTheGrantsOnlyWhenTheyExist(t *testing.T) {
	read := func(dir string) string {
		b, err := os.ReadFile(filepath.Join(dir, "db", "embed_gen.go"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	dir := t.TempDir()
	if err := GenerateMigrate(dir, "example.com/proj", true, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(dir); strings.Contains(got, "grants") {
		t.Errorf("embed names grants that do not exist:\n%s", got)
	}

	grants := filepath.Join(dir, filepath.FromSlash(dbgrants.GrantsPath))
	if err := os.MkdirAll(filepath.Dir(grants), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grants, []byte("-- grants\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateMigrate(dir, "example.com/proj", true, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(dir); !strings.Contains(got, "//go:embed migrations/*.sql grants\n") {
		t.Errorf("embed does not include the grants:\n%s", got)
	}
}
