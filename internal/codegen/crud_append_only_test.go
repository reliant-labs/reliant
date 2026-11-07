package codegen

import (
	"strings"
	"testing"
)

// appendOnlyCRUDEntity is a ledger table declared `forge:append-only`.
func appendOnlyCRUDEntity() EntityDef {
	return EntityDef{
		Name:       "Payment",
		TableName:  "payments",
		PkField:    "id",
		PkGoType:   "string",
		AppendOnly: true,
		Fields: []EntityField{
			{Name: "id", GoName: "ID", GoType: "string", Kind: FieldKindScalar},
			{Name: "amount_cents", GoName: "AmountCents", GoType: "int64", Kind: FieldKindScalar},
		},
	}
}

// The generated CRUD ops call the package-level db.Update<Entity> /
// db.Delete<Entity> delegates, which forge no longer emits for an
// append-only table. So an Update or Delete RPC declared against such a
// table is a CONTRADICTION between two things the user owns: the proto RPC
// and the table's `forge:append-only` comment.
//
// Forge cannot resolve it, and the failure mode if it tries is the worst
// available one — a generated file referencing `db.UpdatePayment`, which
// surfaces as `undefined: db.UpdatePayment` in forge-owned code that the
// user is told never to edit. Name the contradiction at generate time
// instead, in terms of the two declarations that disagree.
func TestBuildCRUDTemplateData_AppendOnlyEntityRejectsUpdateRPC(t *testing.T) {
	svc := ServiceDef{
		Name: "BillingService",
		Methods: []Method{
			{Name: "UpdatePayment", InputType: "UpdatePaymentRequest", OutputType: "UpdatePaymentResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdatePaymentRequest": {
				{Name: "payment", ProtoType: "message", MessageType: "Payment"},
			},
		},
	}
	methods := MatchCRUDMethods(svc, []EntityDef{appendOnlyCRUDEntity()})
	if len(methods) != 1 {
		t.Fatalf("fixture must produce one matched update method, got %d", len(methods))
	}

	_, err := buildCRUDTemplateData(svc, methods, nil, "example.com/test")
	if err == nil {
		t.Fatal("an Update RPC against an append-only table must fail the generate; " +
			"emitting the op would reference db.UpdatePayment, which forge does not generate — " +
			"an `undefined:` error inside a forge-owned file the user must not edit")
	}
	for _, want := range []string{"UpdatePayment", "append-only", "payments"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the two declarations that disagree; %q missing from: %v", want, err)
		}
	}
}

// Same for Delete, and the message must be as specific.
func TestBuildCRUDTemplateData_AppendOnlyEntityRejectsDeleteRPC(t *testing.T) {
	svc := ServiceDef{
		Name: "BillingService",
		Methods: []Method{
			{Name: "DeletePayment", InputType: "DeletePaymentRequest", OutputType: "DeletePaymentResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"DeletePaymentRequest": {{Name: "id", ProtoType: "string"}},
		},
	}
	methods := MatchCRUDMethods(svc, []EntityDef{appendOnlyCRUDEntity()})
	if len(methods) != 1 {
		t.Fatalf("fixture must produce one matched delete method, got %d", len(methods))
	}

	_, err := buildCRUDTemplateData(svc, methods, nil, "example.com/test")
	if err == nil {
		t.Fatal("a Delete RPC against an append-only table must fail the generate")
	}
	if !strings.Contains(err.Error(), "DeletePayment") {
		t.Errorf("the error must name the offending RPC; got: %v", err)
	}
}

// The read/insert half of the quintet is exactly what append-only keeps, so
// it must generate normally. This is the assertion that separates a correct
// gate from one that refused every RPC on the table.
func TestBuildCRUDTemplateData_AppendOnlyEntityAllowsCreateGetList(t *testing.T) {
	svc := ServiceDef{
		Name: "BillingService",
		Methods: []Method{
			{Name: "CreatePayment", InputType: "CreatePaymentRequest", OutputType: "CreatePaymentResponse"},
			{Name: "GetPayment", InputType: "GetPaymentRequest", OutputType: "GetPaymentResponse"},
			{Name: "ListPayments", InputType: "ListPaymentsRequest", OutputType: "ListPaymentsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"CreatePaymentRequest": {{Name: "amount_cents", ProtoType: "int64"}},
			"GetPaymentRequest":    {{Name: "id", ProtoType: "string"}},
			"ListPaymentsRequest":  {{Name: "page_size", ProtoType: "int32"}, {Name: "page_token", ProtoType: "string"}},
		},
	}
	methods := MatchCRUDMethods(svc, []EntityDef{appendOnlyCRUDEntity()})
	if len(methods) != 3 {
		t.Fatalf("expected create/get/list to match, got %d", len(methods))
	}
	if _, err := buildCRUDTemplateData(svc, methods, nil, "example.com/test"); err != nil {
		t.Fatalf("append-only removes the MUTATING verbs only; create/get/list must still generate: %v", err)
	}
}

// An ordinary entity keeps every verb — the gate is per-entity.
func TestBuildCRUDTemplateData_MutableEntityKeepsUpdateRPC(t *testing.T) {
	ent := appendOnlyCRUDEntity()
	ent.Name = "Invoice"
	ent.TableName = "invoices"
	ent.AppendOnly = false

	svc := ServiceDef{
		Name: "BillingService",
		Methods: []Method{
			{Name: "UpdateInvoice", InputType: "UpdateInvoiceRequest", OutputType: "UpdateInvoiceResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdateInvoiceRequest": {{Name: "invoice", ProtoType: "message", MessageType: "Invoice"}},
		},
	}
	methods := MatchCRUDMethods(svc, []EntityDef{ent})
	if len(methods) != 1 {
		t.Fatalf("fixture must produce one matched update method, got %d", len(methods))
	}
	if _, err := buildCRUDTemplateData(svc, methods, nil, "example.com/test"); err != nil {
		t.Fatalf("a mutable entity's Update RPC must still generate: %v", err)
	}
}
