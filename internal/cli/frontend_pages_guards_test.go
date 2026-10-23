package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// guardedInvoicePageData builds the page model for an Invoice whose
// amount_paid_cents column is guarded by a custom RecordPayment RPC, with
// amount_cents beside it guarded by nothing.
//
// The pair is the whole test: one column comes off the write surface and its
// neighbour must not, and both are int64 money columns on the same entity so
// nothing but the marker distinguishes them.
func guardedInvoicePageData(t *testing.T) codegen.PageTemplateData {
	t.Helper()
	svc := codegen.ServiceDef{
		Name:      "InvoiceService",
		Package:   "billing.v1",
		ProtoFile: "proto/services/invoices/v1/invoices.proto",
		Methods: []codegen.Method{
			{Name: "ListInvoices", InputType: "ListInvoicesRequest", InputTypeFQ: "billing.v1.ListInvoicesRequest", OutputType: "ListInvoicesResponse"},
			{Name: "GetInvoice", InputType: "GetInvoiceRequest", InputTypeFQ: "billing.v1.GetInvoiceRequest", OutputType: "GetInvoiceResponse"},
			{Name: "UpdateInvoice", InputType: "UpdateInvoiceRequest", InputTypeFQ: "billing.v1.UpdateInvoiceRequest", OutputType: "UpdateInvoiceResponse"},
			{Name: "RecordPayment", InputType: "RecordPaymentRequest", InputTypeFQ: "billing.v1.RecordPaymentRequest", OutputType: "RecordPaymentResponse"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"UpdateInvoiceRequest": {
				{Name: "invoice", ProtoType: "message", MessageType: "billing.v1.Invoice"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
		Schemas: map[string][]codegen.SchemaFieldDef{
			"billing.v1.UpdateInvoiceRequest": {
				{Name: "invoice", Kind: "message", TypeName: "billing.v1.Invoice"},
				{Name: "update_mask", Kind: "message", TypeName: "google.protobuf.FieldMask"},
			},
			"billing.v1.Invoice": {
				{Name: "id", Kind: "string"},
				{Name: "amount_cents", Kind: "int64"},
				{Name: "amount_paid_cents", Kind: "int64"},
			},
			"billing.v1.RecordPaymentRequest": {
				{Name: "invoice_id", Kind: "string"},
				{Name: "amount_cents", Kind: "int64", Guards: []string{"invoices.amount_paid_cents"}},
			},
		},
	}
	pages := codegen.ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	codegen.AttachEntityMeta(&page, codegen.EntityDef{
		Name:      "Invoice",
		TableName: "invoices",
		PkField:   "id",
		Fields: []codegen.EntityField{
			{Name: "id", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "amount_cents", ProtoType: "int64", Kind: codegen.FieldKindScalar},
			{Name: "amount_paid_cents", ProtoType: "int64", Kind: codegen.FieldKindScalar},
		},
	}, svc)
	return page
}

// TestEditPage_GuardedColumnIsNotInTheMask renders both edit templates and
// checks the emitted TSX, not the page model — the mask is a literal the
// template writes, so the model being right is necessary and not sufficient.
//
// Both halves matter and they fail differently. A guarded path left in
// `paths: [...]` is the original defect: the form writes the column raw and
// trips the CHECK the custom RPC exists to avoid. An unguarded sibling
// DROPPED from the mask is the opposite failure, and a quieter one — the
// column silently stops being editable with no error at all.
func TestEditPage_GuardedColumnIsNotInTheMask(t *testing.T) {
	page := guardedInvoicePageData(t)

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := loadPageTemplate(kind, "edit-page.tsx.tmpl")
			if err != nil {
				t.Fatalf("load edit-page: %v", err)
			}
			var b strings.Builder
			if err := tmpl.Execute(&b, page); err != nil {
				t.Fatalf("render edit-page: %v", err)
			}
			edit := b.String()

			if strings.Contains(edit, `"amount_paid_cents"`) {
				t.Errorf("guarded column is still in the update_mask — the form writes it raw, bypassing RecordPayment:\n%s", edit)
			}
			if strings.Contains(edit, `register("amountPaidCents")`) {
				t.Errorf("guarded column still renders as an editable input:\n%s", edit)
			}
			if !strings.Contains(edit, `paths: ["amount_cents"]`) {
				t.Errorf("unguarded sibling column dropped from the mask:\n%s", edit)
			}

			// The guarded column is SHOWN, disabled, naming the rpc — the
			// decision the marker turns on. Omitting it entirely would
			// leave the reader who just got a 500 with nothing to go on.
			for _, want := range []string{"Managed elsewhere", "<code>RecordPayment</code>", "item?.amountPaidCents"} {
				if !strings.Contains(edit, want) {
					t.Errorf("edit page missing %q — the guarded row must name the rpc that owns the column:\n%s", want, edit)
				}
			}
		})
	}
}

// TestEditPage_NoGuardsRendersNoGuardedSection is the no-fire arm at the
// template level. A project using no guards must get byte-identical output
// to what it got before the marker existed — a stray "Managed elsewhere"
// heading on every scaffolded edit page would be a regression shipped to
// every user who never opted in.
func TestEditPage_NoGuardsRendersNoGuardedSection(t *testing.T) {
	page := aip134PageDataForTest(t)

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := loadPageTemplate(kind, "edit-page.tsx.tmpl")
			if err != nil {
				t.Fatalf("load edit-page: %v", err)
			}
			var b strings.Builder
			if err := tmpl.Execute(&b, page); err != nil {
				t.Fatalf("render edit-page: %v", err)
			}
			if strings.Contains(b.String(), "Managed elsewhere") {
				t.Errorf("unguarded entity rendered the guarded section:\n%s", b.String())
			}
		})
	}
}
