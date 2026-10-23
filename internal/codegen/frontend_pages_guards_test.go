package codegen

import (
	"strings"
	"testing"
)

// guardedInvoiceService is the shape the `forge:guards` marker exists for,
// reduced to its smallest honest form.
//
// The service has the CRUD quintet for Invoice AND a custom RecordPayment
// RPC that enforces a state machine: it refuses an overpayment, and it is
// the only thing allowed to move invoices.amount_paid_cents. The scaffolded
// edit page reasons from the ENTITY's column list and knows nothing about
// that, so without the marker it writes amount_paid_cents raw through
// UpdateInvoice — bypassing the guard and tripping the raw
// invoices_not_overpaid CHECK as a 500.
//
// Note what the marker has to carry and why inference cannot: RecordPayment's
// request names `invoice_id` and `amount_cents`, and the column it guards
// (`amount_paid_cents`) appears in that request NOWHERE. Meanwhile
// `amount_cents` — the field name a matcher WOULD latch onto — is a real,
// freely-editable column on the invoice. Name-matching would miss the guard
// and wrongly freeze the total, both silently.
func guardedInvoiceService(guardMarker string) ServiceDef {
	return ServiceDef{
		Name:      "InvoiceService",
		Package:   "billing.v1",
		ProtoFile: "proto/services/invoices/v1/invoices.proto",
		Methods: []Method{
			{Name: "ListInvoices", InputType: "ListInvoicesRequest", InputTypeFQ: "billing.v1.ListInvoicesRequest", OutputType: "ListInvoicesResponse"},
			{Name: "GetInvoice", InputType: "GetInvoiceRequest", InputTypeFQ: "billing.v1.GetInvoiceRequest", OutputType: "GetInvoiceResponse"},
			{Name: "CreateInvoice", InputType: "CreateInvoiceRequest", InputTypeFQ: "billing.v1.CreateInvoiceRequest", OutputType: "CreateInvoiceResponse"},
			{Name: "UpdateInvoice", InputType: "UpdateInvoiceRequest", InputTypeFQ: "billing.v1.UpdateInvoiceRequest", OutputType: "UpdateInvoiceResponse"},
			{Name: "RecordPayment", InputType: "RecordPaymentRequest", InputTypeFQ: "billing.v1.RecordPaymentRequest", OutputType: "RecordPaymentResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdateInvoiceRequest": {
				{Name: "invoice", ProtoType: "message", MessageType: "billing.v1.Invoice"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
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
				{Name: "amount_cents", Kind: "int64", Guards: guardTargetsForTest(guardMarker)},
			},
		},
	}
}

// guardTargetsForTest keeps the fixture usable from the "no marker" arm.
func guardTargetsForTest(target string) []string {
	if target == "" {
		return nil
	}
	return []string{target}
}

// guardedInvoiceEntity is the applied-schema half — it is what supplies the
// real TABLE NAME the marker's `invoices.amount_paid_cents` target resolves
// against.
func guardedInvoiceEntity() EntityDef {
	return EntityDef{
		Name:      "Invoice",
		TableName: "invoices",
		PkField:   "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "amount_cents", ProtoType: "int64", Kind: FieldKindScalar},
			{Name: "amount_paid_cents", ProtoType: "int64", Kind: FieldKindScalar},
		},
	}
}

func guardedInvoicePage(t *testing.T, guardMarker string) PageTemplateData {
	t.Helper()
	svc := guardedInvoiceService(guardMarker)
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, guardedInvoiceEntity(), svc)
	return page
}

func updateFieldNames(page PageTemplateData) []string {
	var names []string
	for _, f := range page.UpdateFields {
		names = append(names, f.ProtoName)
	}
	return names
}

func containsName(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestAttachEntityMeta_GuardedColumnLeavesUpdateMask is the whole point of
// the marker: a column a custom RPC guards must not be named in the edit
// page's update_mask, because the mask IS the write. UpdateFields is the
// mask's source (both edit templates render `paths: [...]` straight off it),
// so a guarded field left in this slice is a guaranteed raw write.
func TestAttachEntityMeta_GuardedColumnLeavesUpdateMask(t *testing.T) {
	page := guardedInvoicePage(t, "invoices.amount_paid_cents")

	got := updateFieldNames(page)
	if containsName(got, "amount_paid_cents") {
		t.Errorf("update mask still names the guarded column: UpdateFields = %v\n"+
			"  amount_paid_cents is guarded by RecordPayment; writing it raw through "+
			"UpdateInvoice bypasses the overpayment refusal", got)
	}

	// The SIBLING column on the same entity, which nothing guards, must
	// still be editable. A rule that freezes the whole entity the moment
	// one of its columns is guarded is worse than no rule: the author
	// loses the edit page and gets no diagnostic saying why.
	if !containsName(got, "amount_cents") {
		t.Errorf("unguarded sibling column dropped from the edit form: UpdateFields = %v", got)
	}
}

// TestAttachEntityMeta_GuardedColumnNamesItsRPC pins the SECOND half of the
// decision: the guarded column is surfaced on the page as a read-only row
// naming the RPC that owns it, not silently deleted.
//
// Deleting it reproduces the discoverability failure the marker exists to
// close. The user who lowered amount_cents below amount_paid_cents and got a
// 500 learns nothing from a field that simply is not there; a disabled row
// reading "managed by RecordPayment" is a pointer to the correct API at the
// exact moment they are looking for one.
func TestAttachEntityMeta_GuardedColumnNamesItsRPC(t *testing.T) {
	page := guardedInvoicePage(t, "invoices.amount_paid_cents")

	if len(page.GuardedFields) != 1 {
		t.Fatalf("GuardedFields = %+v, want exactly amount_paid_cents", page.GuardedFields)
	}
	g := page.GuardedFields[0]
	if g.ProtoName != "amount_paid_cents" {
		t.Errorf("guarded field = %q, want amount_paid_cents", g.ProtoName)
	}
	if g.GuardedBy != "RecordPayment" {
		t.Errorf("GuardedBy = %q, want RecordPayment — the row has to NAME the rpc that owns the column", g.GuardedBy)
	}
	if !page.HasGuardedFields {
		t.Error("HasGuardedFields = false; the edit template gates the read-only section on it")
	}
}

// TestAttachEntityMeta_NoGuardMarkerKeepsColumnWritable is the no-fire arm.
// Without the marker forge has no way to know the column is special, and it
// must behave exactly as it always has — the marker is opt-in, and a
// heuristic that froze columns without one is the inference approach that
// was already rejected.
func TestAttachEntityMeta_NoGuardMarkerKeepsColumnWritable(t *testing.T) {
	page := guardedInvoicePage(t, "")

	got := updateFieldNames(page)
	if !containsName(got, "amount_paid_cents") {
		t.Errorf("unmarked column dropped from the edit form: UpdateFields = %v", got)
	}
	if len(page.GuardedFields) != 0 {
		t.Errorf("GuardedFields = %+v, want none without a marker", page.GuardedFields)
	}
}

// TestAttachEntityMeta_GuardOnAnotherTableIsIgnored pins the resolution
// rule. The target is `<table>.<column>`, and the TABLE half is load-bearing
// — a guard declared against payments.amount_cents must not freeze
// invoices.amount_cents, which is a different column that merely shares a
// name. This is the precise pair that made name-matching unusable, so it is
// pinned rather than assumed.
func TestAttachEntityMeta_GuardOnAnotherTableIsIgnored(t *testing.T) {
	page := guardedInvoicePage(t, "payments.amount_cents")

	got := updateFieldNames(page)
	if !containsName(got, "amount_cents") {
		t.Errorf("a guard on payments.amount_cents froze invoices.amount_cents: UpdateFields = %v", got)
	}
	if len(page.GuardedFields) != 0 {
		t.Errorf("GuardedFields = %+v, want none — the guard names another table", page.GuardedFields)
	}
}

// TestAttachEntityMeta_ReadOnlyGuardedColumnStillNamesItsRPC pins the
// combination the write-policy decision table recommends for a lifecycle
// column: `forge:read-only` so the API refuses a client write, plus
// `forge:guards` so the edit page says which rpc owns it.
//
// The read-only filter used to drop the field from UpdateFields before the
// guard pass looked, so the two markers together rendered NOTHING — the
// column vanished from the edit page with no pointer to its rpc, which is
// the discoverability failure guards exists to close. Read-only alone still
// hides the field (nothing names an owner to point at); adding the guard is
// what turns it into a disabled row.
func TestAttachEntityMeta_ReadOnlyGuardedColumnStillNamesItsRPC(t *testing.T) {
	svc := guardedInvoiceService("invoices.amount_paid_cents")
	entityFields := svc.Schemas["billing.v1.Invoice"]
	for i := range entityFields {
		if entityFields[i].Name == "amount_paid_cents" {
			entityFields[i].ReadOnly = true
		}
	}
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, guardedInvoiceEntity(), svc)

	if got := updateFieldNames(page); containsName(got, "amount_paid_cents") {
		t.Errorf("a read-only column is in the update mask: UpdateFields = %v", got)
	}
	if len(page.GuardedFields) != 1 || page.GuardedFields[0].ProtoName != "amount_paid_cents" ||
		page.GuardedFields[0].GuardedBy != "RecordPayment" {
		t.Fatalf("GuardedFields = %+v, want one disabled amount_paid_cents row naming RecordPayment", page.GuardedFields)
	}
	if !page.HasGuardedFields {
		t.Error("HasGuardedFields = false; the edit template gates the guarded section on it")
	}

	// Read-only WITHOUT a guard stays hidden: there is no rpc to name.
	plain := guardedInvoiceService("")
	plainFields := plain.Schemas["billing.v1.Invoice"]
	for i := range plainFields {
		if plainFields[i].Name == "amount_paid_cents" {
			plainFields[i].ReadOnly = true
		}
	}
	plainPage := ExtractCRUDEntities(plain)[0]
	AttachEntityMeta(&plainPage, guardedInvoiceEntity(), plain)
	if len(plainPage.GuardedFields) != 0 || containsName(updateFieldNames(plainPage), "amount_paid_cents") {
		t.Errorf("read-only without a guard: GuardedFields = %+v, UpdateFields = %v; want neither to carry it",
			plainPage.GuardedFields, updateFieldNames(plainPage))
	}
}

// TestGuardTargets pins the marker's SYNTAX against the spellings a proto
// author actually writes: the documented trailing form, a leading full-line
// form, extra prose after the target, and a field carrying two guards.
func TestGuardTargets(t *testing.T) {
	cases := []struct {
		name    string
		comment string
		want    []string
	}{
		{"trailing", "forge:guards invoices.amount_paid_cents", []string{"invoices.amount_paid_cents"}},
		{"indented", "   forge:guards invoices.amount_paid_cents", []string{"invoices.amount_paid_cents"}},
		{"trailing prose", "forge:guards invoices.amount_paid_cents — refuses overpayment", []string{"invoices.amount_paid_cents"}},
		{"two targets", "forge:guards jobs.crew_id\nforge:guards jobs.scheduled_start", []string{"jobs.crew_id", "jobs.scheduled_start"}},
		{"no target is not a guard", "forge:guards", nil},
		{"unrelated prose", "the invoice total in cents", nil},
		{"longer marker is not this one", "forge:guardsmen invoices.x", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GuardTargets(tc.comment)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("GuardTargets(%q) = %v, want %v", tc.comment, got, tc.want)
			}
		})
	}
}

// TestGuardsMarkerIsRegistered keeps the marker out of the silent-typo trap
// the registry exists to close: a marker the lint check does not know about
// is reported as an unrecognized `forge:` token on every proto that uses it.
func TestGuardsMarkerIsRegistered(t *testing.T) {
	if !IsKnownProtoMarker(ProtoMarkerGuards) {
		t.Fatalf("%s is not in KnownProtoMarkers — `forge lint --proto-markers` would flag every real use of it as a typo", ProtoMarkerGuards)
	}
}
