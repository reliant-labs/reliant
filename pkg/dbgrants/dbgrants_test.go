package dbgrants

import (
	"strings"
	"testing"
)

func fixture() []Table {
	return []Table{
		{Schema: "public", Name: "users", Columns: []Column{{Name: "id"}, {Name: "email"}, {Name: "api_token", Secret: true}}},
		{Schema: "billing", Name: "invoices", Columns: []Column{{Name: "total"}, {Name: "id"}}},
		{Schema: "public", Name: "vault", Columns: []Column{{Name: "blob", Secret: true}}},
	}
}

func TestSecretName(t *testing.T) {
	if got := SecretName("order-db"); got != "order-db-forge-ro" {
		t.Errorf("SecretName = %q, want order-db-forge-ro", got)
	}
}

// The same schema renders the same bytes whatever order it arrives in: the file
// is committed, and a diff that is only reordering is noise that hides a real
// change.
func TestRenderIsDeterministicAndOrderIndependent(t *testing.T) {
	a, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	rev := fixture()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	for _, t2 := range rev {
		for i, j := 0, len(t2.Columns)-1; i < j; i, j = i+1, j-1 {
			t2.Columns[i], t2.Columns[j] = t2.Columns[j], t2.Columns[i]
		}
	}
	b, err := Render(rev)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("order changed the output:\n%s\n---\n%s", a, b)
	}
}

// A secret column's name appears only in a comment, never in a place Postgres
// would read it as something to grant.
func TestRenderNeverListsASecretColumnAsReadable(t *testing.T) {
	script, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, "api_token") || strings.Contains(line, "blob") {
			t.Errorf("secret column outside a comment: %q", line)
		}
	}
	if !strings.Contains(script, "--   public.users: api_token") {
		t.Errorf("the withheld columns are not documented in the file:\n%s", script)
	}
}

// What the script can never do: no write privilege, no table-level SELECT, no
// sequence privilege, no membership, no grant to anyone but the role.
func TestRenderNeverGrantsMoreThanColumnSelect(t *testing.T) {
	script, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(script)
	for _, bad := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "WITH GRANT", "GRANT ALL", "PG_READ_ALL", "SUPERUSER", "BYPASSRLS", "CREATEROLE"} {
		if strings.Contains(upper, bad) {
			t.Errorf("the script mentions %s:\n%s", bad, script)
		}
	}
	for _, l := range strings.Split(script, "\n") {
		if strings.Contains(l, "GRANT SELECT") && !strings.Contains(l, "GRANT SELECT (%s) ON TABLE") {
			t.Errorf("a SELECT grant that is not column-scoped: %q", l)
		}
		if strings.Contains(l, "GRANT ") && !strings.Contains(l, "GRANT SELECT (%s)") && !strings.Contains(l, "GRANT USAGE ON SCHEMA") {
			t.Errorf("a grant that is neither column SELECT nor schema USAGE: %q", l)
		}
	}
}

// Every schema is converged by revoking first, so a table dropped from the
// declaration, or a column that became secret, loses its grant.
func TestRenderRevokesBeforeItGrants(t *testing.T) {
	script, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	revoke := strings.Index(script, "REVOKE ALL ON ALL TABLES")
	grant := strings.Index(script, "GRANT SELECT (")
	if revoke < 0 || grant < 0 || revoke > grant {
		t.Errorf("revoke at %d, grant at %d: the script must revoke first", revoke, grant)
	}
	if !strings.Contains(script, "REVOKE ALL ON ALL SEQUENCES") {
		t.Error("sequences are not revoked")
	}
}

// The script is a no-op without the role, so it is safe on a database that has
// none, and it checks by the role's name.
func TestRenderIsGuardedByTheRole(t *testing.T) {
	script, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "pg_roles WHERE rolname = 'forge_ro'") || !strings.Contains(script, "RETURN;") {
		t.Errorf("no role guard:\n%s", script)
	}
}

// A table whose every column is secret is declared with no readable column and
// is granted nothing.
func TestRenderAllSecretTableGrantsNothing(t *testing.T) {
	script, err := Render([]Table{{Schema: "public", Name: "vault", Columns: []Column{{Name: "blob", Secret: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "('public', 'vault', ARRAY[]::text[])") {
		t.Errorf("vault is not declared with an empty readable set:\n%s", script)
	}
}

// An empty schema still renders a valid, no-op script.
func TestRenderEmpty(t *testing.T) {
	script, err := Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "$forge_grants$") || strings.Contains(script, "('public'") {
		t.Errorf("unexpected content for an empty schema:\n%s", script)
	}
}

// Identifiers are data, never SQL: a quote, a dollar-quote terminator or a
// newline in a name cannot break out of the literal it is rendered into.
func TestRenderQuotesHostileIdentifiers(t *testing.T) {
	script, err := Render([]Table{{Schema: "public", Name: `we'ird"name`, Columns: []Column{{Name: `c'ol`}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `'we''ird"name'`) || !strings.Contains(script, `'c''ol'`) {
		t.Errorf("single quotes are not doubled:\n%s", script)
	}
	for _, bad := range []Table{
		{Schema: "public", Name: "a$forge_grants$b", Columns: []Column{{Name: "id"}}},
		{Schema: "public", Name: "t", Columns: []Column{{Name: "x\ny"}}},
		{Schema: "", Name: "t"},
		{Schema: "public", Name: ""},
	} {
		if _, err := Render([]Table{bad}); err == nil {
			t.Errorf("Render accepted %+v", bad)
		}
	}
	if _, err := RenderFor("evil$forge_grants$", nil); err == nil {
		t.Error("RenderFor accepted a role name that ends the dollar quote")
	}
}
