// File: pkg/schemadef/append_only_upgrade_test.go
//
// The UPGRADE half of forge:append-only detection.
//
// The COMMENT ON TABLE declaration only exists on tables born AFTER forge
// started writing it. Every append-only table that already existed has the
// guard trigger and no comment, so comment-only detection reads it as an
// ordinary table — and the store generator hands it back Update/Delete for
// a table postgres refuses every write to. The immutability guarantee would
// silently lapse for exactly the tables that had it longest.
//
// So the guard trigger is a declaration too, and these tests pin that it is
// read as one.

package schemadef

import "testing"

// appendOnlyGuardSQL is the guard forge has always written at birth: the
// trigger half, with no COMMENT ON TABLE. It reproduces a table born before
// the declaration landed.
const appendOnlyGuardSQL = `
CREATE TABLE payments (
    id TEXT PRIMARY KEY,
    invoice_id TEXT NOT NULL,
    amount_cents BIGINT NOT NULL
);
CREATE OR REPLACE FUNCTION payments_forbid_mutation() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'table payments is append-only: % is not permitted', TG_OP;
END;
$$;
CREATE TRIGGER payments_append_only
    BEFORE UPDATE OR DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION payments_forbid_mutation();
`

func introspectOne(t *testing.T, name, sql string) Table {
	t.Helper()
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_"+name+".up.sql", sql)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	for _, tbl := range tables {
		if tbl.Name == name {
			return tbl
		}
	}
	t.Fatalf("table %s was not introspected; got %+v", name, tables)
	return Table{}
}

// The regression that matters: a table that was append-only BEFORE forge
// wrote the catalog comment must still read as append-only after.
func TestAppendOnly_GuardTriggerWithoutCommentIsStillDetected(t *testing.T) {
	requireRealPG(t)
	tbl := introspectOne(t, "payments", appendOnlyGuardSQL)

	if tbl.Comment != "" {
		t.Fatalf("fixture must reproduce the PRE-comment shape; got comment %q", tbl.Comment)
	}
	if !tbl.AppendOnly() {
		t.Errorf("a table carrying forge's append-only guard trigger must read as append-only; triggers = %v", tbl.Triggers)
	}
	if !DetectConventions(tbl).AppendOnly {
		t.Error("DetectConventions must surface it — it is what the entity projection reads")
	}
}

// The guard must be recognized by forge's own trigger NAME, not by "has
// some trigger touching UPDATE". An updated_at stamper is BEFORE UPDATE and
// means the exact opposite: the table is mutable by design.
//
// The asymmetry of harm is what sets this precision. Missing the marker
// silently weakens a guarantee; inventing one DELETES Update/Delete from a
// generated store and breaks a build that was correct. A heuristic broad
// enough to catch a hand-written guard is broad enough to catch an audit
// trigger, so detection stays exact and a hand-written guard declares
// itself with the comment.
func TestAppendOnly_OrdinaryUpdateTriggerIsNotAGuard(t *testing.T) {
	requireRealPG(t)
	tbl := introspectOne(t, "invoices", `
CREATE TABLE invoices (
    id TEXT PRIMARY KEY,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE OR REPLACE FUNCTION invoices_touch_updated_at() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
CREATE TRIGGER invoices_set_updated_at
    BEFORE UPDATE ON invoices
    FOR EACH ROW EXECUTE FUNCTION invoices_touch_updated_at();
`)

	if len(tbl.Triggers) == 0 {
		t.Fatal("fixture must actually install a trigger, or this proves nothing")
	}
	if tbl.AppendOnly() {
		t.Errorf("an updated_at stamper must never read as an append-only guard; triggers = %v", tbl.Triggers)
	}
}

// A plain table has neither signal, so nothing opts it in.
func TestAppendOnly_PlainTableIsNotAppendOnly(t *testing.T) {
	requireRealPG(t)
	tbl := introspectOne(t, "crews", `CREATE TABLE crews (id TEXT PRIMARY KEY, name TEXT);`)

	if tbl.AppendOnly() {
		t.Error("a table declaring nothing must not be append-only")
	}
}
