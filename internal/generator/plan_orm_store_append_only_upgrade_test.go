// File: internal/generator/plan_orm_store_append_only_upgrade_test.go
//
// The whole upgrade path in one test, against a real postgres: a table that
// was append-only BEFORE forge wrote COMMENT ON TABLE must still generate a
// store with no mutators.
//
// The sibling tests each own one seam — schemadef detects the guard
// trigger, codegen projects it to the plan, plan_orm_store_gen omits the
// mutators. Each can pass while the path as a whole is broken, because the
// thing that breaks an upgrade is a fact dropped at a boundary nobody
// tests. This starts where a real project starts, at applied SQL, and ends
// where the user's compiler does.

package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// preCommentAppendOnlySQL is a birth migration as forge WROTE IT before the
// catalog declaration landed: the guard trigger, and no COMMENT ON TABLE.
// It is the exact on-disk shape of every append-only table that predates
// the change, which is what makes it the fixture for the upgrade.
const preCommentAppendOnlySQL = `
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

CREATE TABLE invoices (
    id TEXT PRIMARY KEY,
    amount_cents BIGINT NOT NULL
);
`

// entityDefFromTable mirrors what codegen.buildEntityDef derives from an
// introspected table, limited to the fields this test's assertions depend
// on. It goes through schemadef.DetectConventions — the same call the real
// projection makes — rather than setting AppendOnly directly, because a
// test that asserts the flag it set itself would pass against a broken
// detector.
func entityDefFromTable(name string, table schemadef.Table) codegen.EntityDef {
	conv := schemadef.DetectConventions(table)
	def := codegen.EntityDef{
		Name:       name,
		TableName:  table.Name,
		SoftDelete: conv.SoftDelete,
		Timestamps: conv.Timestamps,
		AppendOnly: conv.AppendOnly,
	}
	for _, c := range table.Columns {
		def.Columns = append(def.Columns, codegen.EntityColumn{
			Name:    c.Name,
			Type:    string(c.Type),
			IsPK:    c.IsPK,
			NotNull: c.NotNull,
		})
	}
	return def
}

// TestAppendOnlyUpgrade_PreCommentTableStillGeneratesAnImmutableStore is the
// regression that matters for the upgrade: without the trigger fallback the
// applied schema reads as ordinary, and a money ledger gets back
// UpdatePayment / UpdatePaymentMasked / DeletePayment — every one of which
// the database rejects at runtime.
func TestAppendOnlyUpgrade_PreCommentTableStillGeneratesAnImmutableStore(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to real postgres; skipped under -short")
	}

	migDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(migDir, "00001_create_payments.up.sql"), []byte(preCommentAppendOnlySQL), 0o644); err != nil {
		t.Fatal(err)
	}

	tables, err := schemadef.ApplyAndIntrospect(migDir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}

	byName := map[string]schemadef.Table{}
	for _, tbl := range tables {
		byName[tbl.Name] = tbl
	}
	payments, ok := byName["payments"]
	if !ok {
		t.Fatalf("payments was not introspected; got %v", tables)
	}
	// Pin the premise. If a future change starts writing the comment into
	// this fixture the test would still pass, while proving nothing about
	// the upgrade it exists to cover.
	if payments.AppendOnlyDeclared() {
		t.Fatal("fixture must reproduce the PRE-comment shape: guard trigger, no catalog declaration")
	}

	root := t.TempDir()
	entities := []config.PlanEntity{
		codegen.EntityDefToPlanEntity(entityDefFromTable("Payment", payments)),
		codegen.EntityDefToPlanEntity(entityDefFromTable("Invoice", byName["invoices"])),
	}
	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", storeGenFile))
	if err != nil {
		t.Fatalf("store file was not generated: %v", err)
	}
	src := string(b)

	body := storeInterfaceBody(t, src, "PaymentStore")
	for _, forbidden := range []string{"UpdatePayment(", "UpdatePaymentMasked(", "DeletePayment("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("a table that was append-only before the catalog declaration existed must still "+
				"lose %s — otherwise the upgrade silently drops the guarantee for exactly the tables "+
				"that had it longest.\ngot:\n%s", forbidden, body)
		}
	}

	// Scoped, as ever: the mutable table beside it keeps everything.
	invoice := storeInterfaceBody(t, src, "InvoiceStore")
	for _, want := range []string{"UpdateInvoice(", "DeleteInvoice("} {
		if !strings.Contains(invoice, want) {
			t.Errorf("the ordinary entity must keep %s.\ngot:\n%s", want, invoice)
		}
	}

	// The package-level delegates are the second route to the same write,
	// and they are exported. A trigger-only table that loses its store
	// mutators but keeps `db.DeletePayment` has gained a better error
	// message, not the compile error the db skill promises.
	ormBytes, err := os.ReadFile(filepath.Join(root, "internal", "db", "payment_orm_gen.go"))
	if err != nil {
		t.Fatalf("payment ORM file was not generated: %v", err)
	}
	orm := string(ormBytes)
	for _, forbidden := range []string{"func UpdatePayment(", "func UpdatePaymentMasked(", "func DeletePayment("} {
		if strings.Contains(orm, forbidden) {
			t.Errorf("a pre-comment append-only table must also lose the package-level %s; "+
				"the interface gate alone leaves the exported delegate open", forbidden)
		}
	}
	if !strings.Contains(orm, "func CreatePayment(") || !strings.Contains(orm, "func GetPaymentByID(") {
		t.Errorf("the read/insert delegates must survive the upgrade.\ngot:\n%s", orm)
	}

	invoiceORM, err := os.ReadFile(filepath.Join(root, "internal", "db", "invoice_orm_gen.go"))
	if err != nil {
		t.Fatalf("invoice ORM file was not generated: %v", err)
	}
	for _, want := range []string{"func UpdateInvoice(", "func DeleteInvoice("} {
		if !strings.Contains(string(invoiceORM), want) {
			t.Errorf("the ordinary table must keep its package-level %s", want)
		}
	}
}
