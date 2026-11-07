package codegen

import (
	"go/format"
	"strings"
	"testing"
)

// A factory for an append-only entity must not offer post-insert
// overrides.
//
// The ordinary factory inserts the row, loads it, applies the override
// closures to the loaded struct, and writes it back with db.Update<Entity>.
// On an append-only table that final write is exactly what the birth
// migration's trigger exists to reject, so the generated harness compiled
// and then failed at runtime — and only for the entity whose invariant is
// the most expensive to get wrong. Worse, the store no longer declares the
// mutator at all, so the same line stops compiling.
//
// The fix is to emit the factory WITHOUT the override seam rather than to
// drop the factory: a ledger row is still the thing a test needs to create.
// Tests that need a specific value insert it themselves.
func TestRenderEntityFactoryFile_AppendOnlyEntityHasNoOverrideSeam(t *testing.T) {
	specs := []entityFactorySpec{
		{
			goName:     "Payment",
			lower:      "payment",
			appendOnly: true,
			rootSQL:    "INSERT INTO \"payments\" (id, amount_cents) VALUES ($1, 100);",
		},
		{
			goName:  "Invoice",
			lower:   "invoice",
			rootSQL: "INSERT INTO \"invoices\" (id, amount_cents) VALUES ($1, 100);",
		},
	}

	out := renderEntityFactoryFile("github.com/acme/shop/internal/db", factoryGroup{pkg: "billing", specs: specs})
	if _, err := format.Source(out); err != nil {
		t.Fatalf("rendered factories_gen_test.go is not valid Go: %v\n---\n%s", err, out)
	}
	s := string(out)

	for _, forbidden := range []string{
		"db.UpdatePayment(",
		"type PaymentOverride func(*db.Payment)",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("append-only factory must not emit %q — that write is rejected by the table's "+
				"own trigger, and the generated store no longer declares the mutator at all", forbidden)
		}
	}

	if !strings.Contains(s, "func NewPayment(t testing.TB, database orm.Context) *db.Payment") {
		t.Error("an append-only entity still needs a factory to insert a row; only the override seam goes away")
	}

	// The mutable entity beside it keeps the full seam — the narrowing is
	// per-entity, not a global regression of the factory generator.
	for _, want := range []string{
		"type InvoiceOverride func(*db.Invoice)",
		"db.UpdateInvoice(context.Background(), database, row)",
		"func NewInvoice(t testing.TB, database orm.Context, overrides ...InvoiceOverride) *db.Invoice",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("an ordinary entity must keep %q", want)
		}
	}
}
