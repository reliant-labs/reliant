package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// appendOnlyStoreEntities pairs an append-only ledger with an ordinary
// mutable entity, because the defect is a DIFFERENCE that only a pair can
// show: the append-only store must lose its mutators while the mutable one
// beside it keeps them. Asserting either alone would pass against a
// generator that had simply stopped emitting Update/Delete for everyone.
func appendOnlyStoreEntities() []config.PlanEntity {
	return []config.PlanEntity{
		{
			Name:       "Payment",
			AppendOnly: true,
			Timestamps: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "invoice_id", Type: "string", NotNull: true},
				{Name: "amount_cents", Type: "int64", NotNull: true},
			},
		},
		{
			Name: "Invoice",
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "amount_cents", Type: "int64", NotNull: true},
			},
		},
	}
}

func appendOnlyStoreSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, appendOnlyStoreEntities(), nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", storeGenFile))
	if err != nil {
		t.Fatalf("store file was not generated: %v", err)
	}
	return string(b)
}

// storeInterfaceBody returns the method block of one entity's store
// interface, so an assertion cannot accidentally match the ADAPTER's
// methods (which carry the same names) elsewhere in the file.
func storeInterfaceBody(t *testing.T, src, iface string) string {
	t.Helper()
	idx := strings.Index(src, "type "+iface+" interface {")
	if idx < 0 {
		t.Fatalf("%s must exist in the generated store file", iface)
	}
	rest := src[idx:]
	return rest[:strings.Index(rest, "\n}")]
}

// An append-only entity's immutability must be UNREPRESENTABLE in Go, not
// merely refused at runtime.
//
// forge honoured the marker everywhere it was visible — no Update/Delete
// RPC, no update/delete frontend hook, and a birth-migration trigger that
// rejects both at the database — and then emitted PaymentStore
// byte-identical in shape to every mutable entity's store. So the
// immutability of a money ledger was defended only by a postgres trigger:
// `DeletePayment` type-checked fine, and the failure could only ever
// surface as a 500 at runtime.
//
// Measured, in the audited build that found this: the agent wrote
// `panic("payments are append-only; Delete must never be called")` into a
// test fake — hand-defending an invariant the generator had declined to
// encode in the type.
func TestStoreInterfaces_AppendOnlyEntityHasNoMutators(t *testing.T) {
	src := appendOnlyStoreSource(t)
	body := storeInterfaceBody(t, src, "PaymentStore")

	for _, forbidden := range []string{
		"UpdatePayment(",
		"UpdatePaymentMasked(",
		"DeletePayment(",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("PaymentStore must not declare %s — an append-only ledger's immutability "+
				"has to be unrepresentable in the type, not merely refused by a DB trigger.\ngot:\n%s",
				forbidden, body)
		}
	}

	// The read surface is untouched: append-only removes the mutators, not
	// the entity. A store you cannot insert into or read from is not a
	// narrower store, it is a broken one.
	for _, want := range []string{
		"CreatePayment(ctx context.Context, msg *Payment) error",
		"GetPaymentByID(ctx context.Context, id string) (*Payment, error)",
		"ListPayment(ctx context.Context, opts ...orm.QueryOption) ([]*Payment, error)",
		"CountPayment(ctx context.Context, opts ...orm.QueryOption) (int64, error)",
		"WithTx(tx orm.Context) PaymentStore",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("append-only must remove the MUTATORS only; %q is missing.\ngot:\n%s", want, body)
		}
	}
}

// The adapter has to lose the same three methods. Leaving them on the
// concrete type would keep `paymentStore{}.DeletePayment(...)` compiling —
// the interface would document an invariant the implementation still
// offered to break.
func TestStoreInterfaces_AppendOnlyAdapterHasNoMutators(t *testing.T) {
	src := appendOnlyStoreSource(t)

	for _, forbidden := range []string{
		"func (s paymentStore) UpdatePayment(",
		"func (s paymentStore) UpdatePaymentMasked(",
		"func (s paymentStore) DeletePayment(",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the adapter must not implement %s — an invariant the interface hides but the "+
				"concrete type still exposes is not an invariant", forbidden)
		}
	}
}

// The guard must be SCOPED to the marked entity. This is the assertion that
// distinguishes a correct gate from a generator that regressed every store.
func TestStoreInterfaces_MutableEntityKeepsItsMutators(t *testing.T) {
	src := appendOnlyStoreSource(t)
	body := storeInterfaceBody(t, src, "InvoiceStore")

	for _, want := range []string{
		"UpdateInvoice(ctx context.Context, msg *Invoice, opts ...crud.UpdateOption) error",
		"UpdateInvoiceMasked(ctx context.Context, msg *Invoice, fields []string, opts ...crud.UpdateOption) error",
		"DeleteInvoice(ctx context.Context, id string) error",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("an ordinary entity beside an append-only one must keep %q — the gate is "+
				"per-entity, not global.\ngot:\n%s", want, body)
		}
	}
}

// appendOnlyORMSource returns one entity's generated <entity>_orm_gen.go,
// which is where the PACKAGE-LEVEL delegates live. The store interface and
// the delegates are written by the same call but into different files, so
// a test that only reads store_gen.go cannot see half the surface.
func appendOnlyORMSource(t *testing.T, snake string) string {
	t.Helper()
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, appendOnlyStoreEntities(), nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", snake+"_orm_gen.go"))
	if err != nil {
		t.Fatalf("ORM file for %s was not generated: %v", snake, err)
	}
	return string(b)
}

// Narrowing the store interface closed ONE of the two routes to a mutating
// write. The package-level delegates are the other, and they are exported:
// `db.DeletePayment(ctx, tx, id)` type-checked fine against an append-only
// ledger even after the interface lost the method, so the guarantee the db
// skill states as a compile error was still only a runtime SQLSTATE P0001.
//
// An invariant enforced on one of two available routes is not an invariant;
// it is a convention with a longer error message.
func TestPlanORM_AppendOnlyEntityHasNoPackageLevelMutators(t *testing.T) {
	src := appendOnlyORMSource(t, "payment")

	for _, forbidden := range []string{
		"func UpdatePayment(",
		"func UpdatePaymentMasked(",
		"func DeletePayment(",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("append-only Payment must not get a package-level %s — the store interface "+
				"already omits it, and an exported delegate reopens exactly the write the "+
				"interface was narrowed to forbid", forbidden)
		}
	}

	// Reads and Create survive, along with the repo the delegates close
	// over. Append-only means immutable, not invisible.
	for _, want := range []string{
		"func CreatePayment(ctx context.Context, db orm.Context, msg *Payment) error",
		"func GetPaymentByID(ctx context.Context, db orm.Context, id string) (*Payment, error)",
		"func ListPayment(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*Payment, error)",
		"func CountPayment(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (int64, error)",
		"var paymentRepo = crud.NewRepo[Payment]()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("append-only removes the MUTATING delegates only; %q is missing.\ngot:\n%s", want, src)
		}
	}
}

// Scoped per-entity, same as the interface gate: a generator that simply
// stopped emitting the delegates for everyone would pass the test above.
func TestPlanORM_MutableEntityKeepsItsPackageLevelMutators(t *testing.T) {
	src := appendOnlyORMSource(t, "invoice")

	for _, want := range []string{
		"func UpdateInvoice(ctx context.Context, db orm.Context, msg *Invoice, opts ...crud.UpdateOption) error",
		"func UpdateInvoiceMasked(ctx context.Context, db orm.Context, msg *Invoice, fields []string, opts ...crud.UpdateOption) error",
		"func DeleteInvoice(ctx context.Context, db orm.Context, id string) error",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("an ordinary entity beside an append-only one must keep %q.\ngot:\n%s", want, src)
		}
	}
}

// The repo-ext seam's doc comment names the delegates a user can reuse. On
// an append-only entity it named two that no longer exist, which sends the
// reader to look for a symbol forge deliberately refuses to emit.
func TestPlanORM_AppendOnlyRepoExtSeamDoesNotAdvertiseMutators(t *testing.T) {
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, appendOnlyStoreEntities(), nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", "payment_repo_ext.go"))
	if err != nil {
		t.Fatalf("repo-ext seam was not scaffolded: %v", err)
	}
	src := string(b)
	for _, forbidden := range []string{"UpdatePayment", "DeletePayment"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the repo-ext seam must not point an append-only entity at %s; forge does not emit it", forbidden)
		}
	}
	if !strings.Contains(src, "CreatePayment") {
		t.Errorf("the seam should still name the delegates that DO exist.\ngot:\n%s", src)
	}
}

// The aggregate keeps its accessor either way: an append-only entity is
// still an entity, and a reporting service spanning tables must still be
// able to READ it. Dropping it from the aggregate would make append-only
// mean "invisible" rather than "immutable".
func TestStoreInterfaces_AggregateStillExposesAppendOnlyEntities(t *testing.T) {
	src := appendOnlyStoreSource(t)

	if !strings.Contains(src, "Payments() PaymentStore") {
		t.Error("the aggregate must still expose an append-only entity's store; append-only removes writes, not reads")
	}
	if !strings.Contains(src, "var _ PaymentStore = paymentStore{}") {
		t.Error("the append-only adapter still needs its compile-time assertion, or a drift between it and the narrowed interface is invisible")
	}
}
