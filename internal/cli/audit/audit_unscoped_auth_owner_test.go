package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// These tests pin the ARMING rule: `unscoped_auth` gates on a
// DECLARATION the developer wrote, never on a heuristic.
//
// The category has always diagnosed the hole correctly and always
// reported warn, on the reasoning that a fresh scaffold is entirely
// unscoped by construction so erroring would make forge's own output
// fail forge's own gate. That reasoning is sound and these tests keep
// it true — the fixture with no owner declaration must stay warn.
//
// What it missed is that "unscoped" and "unsafe" are different
// claims. A project that never says any data belongs to anyone has no
// ownership boundary to breach; a project that declares one has told forge
// the exact thing the fresh-scaffold argument assumed it could not know.
// `forge:owner` in a column's COMMENT is that declaration, and it is
// what moves the finding from advisory to gating.
//
// Both sides stay COMPUTED, matching the rest of this category: the
// owned table set comes from the migrations (the applied
// schema's own source of truth, the same text `forge lint`'s
// column-marker check reads), and the RPC→table mapping comes from
// codegen.ParseCRUDOperation plus naming — the identical derivation
// BuildSchemaEntities uses to join protos to tables. Nothing here greps
// prose.

// writeOwnerMigration lays down a migration declaring col on table as
// the owner of its rows, in the same COMMENT ON COLUMN form a real
// migration would carry.
func writeOwnerMigration(t *testing.T, projectDir, table, col string) {
	t.Helper()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TABLE " + table + " (\n" +
		"  id TEXT PRIMARY KEY,\n" +
		"  " + col + " TEXT NOT NULL,\n" +
		"  name TEXT NOT NULL\n" +
		");\n\n" +
		"COMMENT ON COLUMN " + table + "." + col + " IS '" + schemadef.ColumnMarkerOwner + "';\n"
	if err := os.WriteFile(filepath.Join(migDir, "20240101000000_init.up.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUnscopedAuth_NoOwnerDeclarationStaysAdvisory is the invariant the
// original warn-never-error decision protected, and the reason this gate
// is opt-in rather than heuristic. A project that declares no owner
// column — a fresh scaffold, an app whose rows belong to nobody — must still
// only warn, so forge's own output can clear forge's own gate.
func TestUnscopedAuth_NoOwnerDeclarationStaysAdvisory(t *testing.T) {
	src := handlerHeader + delegationBody("GetOrder")
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true}, src)

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusWarn {
		t.Fatalf("status = %q, want warn — no table declares %s, so no ownership boundary exists to breach and the finding stays advisory\nsummary: %s",
			cat.Status, schemadef.ColumnMarkerOwner, cat.Summary)
	}
	if names := unscopedMethods(t, cat); len(names) != 1 || names[0] != "GetOrder" {
		t.Fatalf("unscoped = %v, want [GetOrder] — the finding is still reported, just not gating", names)
	}
	if got := cat.Details["owner_scoped_tables"]; got != nil {
		if tables, ok := got.([]string); !ok || len(tables) != 0 {
			t.Errorf("owner_scoped_tables = %v, want empty — nothing declared one", got)
		}
	}
}

// TestUnscopedAuth_OwnerDeclarationArmsTheGate is the core new
// assertion. One column says "this data belongs to someone", and an
// authenticated RPC over that entity that never resolves the caller
// stops being advice and becomes a failure.
func TestUnscopedAuth_OwnerDeclarationArmsTheGate(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — customers.company_id is declared %s and GetCustomer reads no caller, so every signed-in user can read every other user's rows\nsummary: %s",
			cat.Status, schemadef.ColumnMarkerOwner, cat.Summary)
	}
	names := unscopedMethods(t, cat)
	if len(names) != 1 || names[0] != "GetCustomer" {
		t.Fatalf("unscoped = %v, want [GetCustomer]", names)
	}
	gating, ok := cat.Details["owner_scoped_unscoped_rpcs"].([]unscopedRPC)
	if !ok || len(gating) != 1 {
		t.Fatalf("owner_scoped_unscoped_rpcs = %v, want exactly one entry — the gating subset must be reported separately from the advisory set",
			cat.Details["owner_scoped_unscoped_rpcs"])
	}
	if gating[0].Entity != "Customer" || gating[0].Table != "customers" {
		t.Errorf("gating finding = %+v, want it to name the entity and table that armed it", gating[0])
	}
	tables, ok := cat.Details["owner_scoped_tables"].([]string)
	if !ok || len(tables) != 1 || tables[0] != "customers" {
		t.Errorf("owner_scoped_tables = %v, want [customers]", cat.Details["owner_scoped_tables"])
	}
}

// TestUnscopedAuth_UnrelatedEntityDoesNotArmTheGate keeps the gate
// narrow. Declaring an owner column on ONE table must not make every
// unscoped RPC in the project a failure — an RPC over a table with no
// owner column (a global product catalog) is still only advice.
func TestUnscopedAuth_UnrelatedEntityDoesNotArmTheGate(t *testing.T) {
	src := handlerHeader + delegationBody("GetProduct")
	dir := writeProject(t, "ShopService", map[string]bool{"GetProduct": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusWarn {
		t.Fatalf("status = %q, want warn — products declares no owner column, so GetProduct crosses no declared boundary\nsummary: %s",
			cat.Status, cat.Summary)
	}
	if got := cat.Details["owner_scoped_unscoped_rpcs"]; got != nil {
		if gating, ok := got.([]unscopedRPC); !ok || len(gating) != 0 {
			t.Errorf("owner_scoped_unscoped_rpcs = %v, want empty", got)
		}
	}
}

// TestUnscopedAuth_AcknowledgementSuppressesTheGate pins the escape
// hatch against the ERROR path, not just the warning. A gate with no
// way to say "this one is deliberate" is a gate people disable
// wholesale, and the acknowledgement still has to carry a reason.
func TestUnscopedAuth_AcknowledgementSuppressesTheGate(t *testing.T) {
	src := handlerHeader +
		"// " + AuthUnscopedOKDirective + " operator console; scoping lives in the RLS policy on customers.\n" +
		delegationBody("ListCustomers")
	dir := writeProject(t, "ShopService", map[string]bool{"ListCustomers": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — the RPC is acknowledged in code WITH a reason, which must suppress the error and not merely the warning\nsummary: %s",
			cat.Status, cat.Summary)
	}
	if got := cat.Details["owner_scoped_unscoped_rpcs"]; got != nil {
		if gating, ok := got.([]unscopedRPC); !ok || len(gating) != 0 {
			t.Errorf("owner_scoped_unscoped_rpcs = %v, want empty", got)
		}
	}
}

// TestUnscopedAuth_BareAcknowledgementDoesNotSuppressTheGate is the
// reason-required rule carried onto the gating path. A directive that
// says nothing is the unfalsifiable comment this whole category exists
// to replace, and it must not be a one-line gate bypass.
func TestUnscopedAuth_BareAcknowledgementDoesNotSuppressTheGate(t *testing.T) {
	src := handlerHeader +
		"// " + AuthUnscopedOKDirective + "\n" +
		delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — a reasonless directive is not an acknowledgement\nsummary: %s", cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_ScopedHandlerOverOwnedEntityIsClean is the other
// side of the gate: the fix actually clears it. A handler that resolves
// the caller over an owned entity reports ok.
func TestUnscopedAuth_ScopedHandlerOverOwnedEntityIsClean(t *testing.T) {
	src := handlerHeader + scopedBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — the handler resolves the caller\nsummary: %s", cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_ListPluralResolvesToTheOwnedTable pins the RPC→table
// derivation against the shape it is most likely to get wrong.
// ListCustomers names the entity in the plural; the table lookup has to
// singularize before pluralizing, the same way BuildSchemaEntities does,
// or every List RPC silently falls out of the gating set.
func TestUnscopedAuth_ListPluralResolvesToTheOwnedTable(t *testing.T) {
	src := handlerHeader + delegationBody("ListCustomers")
	dir := writeProject(t, "ShopService", map[string]bool{"ListCustomers": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusError {
		t.Fatalf("status = %q, want error — ListCustomers must resolve to the customers table\nsummary: %s", cat.Status, cat.Summary)
	}
}

// TestUnscopedAuth_GatingSummaryNamesTheBreach keeps the message
// actionable. A gate that fails without naming the declaration that
// armed it reads as forge being arbitrary, and the first response to
// that is to switch the gate off.
func TestUnscopedAuth_GatingSummaryNamesTheBreach(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)

	if !strings.Contains(cat.Summary, schemadef.ColumnMarkerOwner) {
		t.Errorf("summary = %q, want it to name %s — the declaration that armed the gate", cat.Summary, schemadef.ColumnMarkerOwner)
	}
	hint, _ := cat.Details["hint"].(string)
	if !strings.Contains(hint, AuthUnscopedOKDirective) {
		t.Errorf("hint = %q, want it to name the acknowledgement directive", hint)
	}
}

// TestOwnerScopedTables_ReadsOnlyTheDeclaration pins the scanner
// against the two ways it could over-report: a column comment carrying
// unrelated prose, and a marker whose name merely starts with the
// owner marker's text.
func TestOwnerScopedTables_ReadsOnlyTheDeclaration(t *testing.T) {
	sql := `
CREATE TABLE customers (id TEXT PRIMARY KEY, company_id TEXT NOT NULL, note TEXT);
COMMENT ON COLUMN customers.company_id IS 'forge:owner';
COMMENT ON COLUMN customers.note IS 'free text about who owns this';

CREATE TABLE products (id TEXT PRIMARY KEY, sku TEXT);
COMMENT ON COLUMN products.sku IS 'forge:owner-unrelated';
`
	got := ownerScopedTablesIn(sql)
	if len(got) != 1 || got["customers"] != "company_id" {
		t.Fatalf("owner tables = %v, want exactly {customers: company_id} — prose mentioning ownership is not a declaration, and forge:owner-unrelated is a different marker",
			got)
	}
}

// TestOwnerScopedTables_CarriesTheDeclaredColumn pins the column half of
// the map. The table name alone arms the gate; the COLUMN is what the
// remediation snippet names in its WHERE predicate, so losing it would
// leave the gate able to refuse but not to help.
func TestOwnerScopedTables_CarriesTheDeclaredColumn(t *testing.T) {
	sql := `
CREATE TABLE documents (id TEXT PRIMARY KEY, org_id TEXT NOT NULL);
COMMENT ON COLUMN documents.org_id IS 'forge:owner';
`
	got := ownerScopedTablesIn(sql)
	if got["documents"] != "org_id" {
		t.Fatalf("owner column for documents = %q, want org_id — the predicate in the offered wrapper names this column", got["documents"])
	}
}

// TestOwnerScopedTables_SchemaQualified: pg_dump qualifies and may quote
// the object of a COMMENT ON COLUMN. The owner declaration must still arm
// the gate for the table, and handler SQL naming the table qualified must
// still be recognised as reading it.
func TestOwnerScopedTables_SchemaQualified(t *testing.T) {
	got := ownerScopedTablesIn(`
CREATE TABLE public.documents (id TEXT PRIMARY KEY, org_id TEXT NOT NULL);
COMMENT ON COLUMN "public"."documents".org_id IS 'forge:owner';
`)
	if len(got) != 1 || got["documents"] != "org_id" {
		t.Fatalf("owner tables = %v, want exactly {documents: org_id}", got)
	}
	ev, found := classifyHandlerSQL([]string{`SELECT id FROM public.documents`}, got)
	if !found || len(ev.Tables) != 1 || ev.Tables[0] != "documents" {
		t.Fatalf("classifyHandlerSQL over a qualified FROM = %+v (found %v), want it to read documents", ev, found)
	}
}
