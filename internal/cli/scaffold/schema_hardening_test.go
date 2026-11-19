// File: internal/cli/scaffold/schema_hardening_test.go
//
// Unit tests for the birth-time schema-hardening derivations that live in
// the CLI layer: the append-only CRUD-piece filter (the wire half of the
// immutable-ledger marker).

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestReadOnlyOmittedFromCreateRequest pins the `// forge:read-only`
// request-omission: a read-only field is carried onto the entityField
// list (entityFieldsFromSchemaDefs) but EXCLUDED from the born
// Create<Entity>Request message, with the surviving fields renumbered
// contiguously (1,2,...) — no gap where the omitted field sat. The Update
// request (AIP-134 entity + mask) never enumerates fields, so it carries no
// per-field trace of the read-only field either.
func TestReadOnlyOmittedFromCreateRequest(t *testing.T) {
	defs := []codegen.SchemaFieldDef{
		{Name: "customer", Kind: "string"},
		{Name: "status", Kind: "string", ReadOnly: true}, // not client-writable
		{Name: "amount", Kind: "int64"},
	}
	fields, _ := entityFieldsFromSchemaDefs("services.orders.v1", defs)

	// The marker rides through onto the entityField list.
	var sawReadOnly bool
	for _, f := range fields {
		if f.Name == "status" && f.ReadOnly {
			sawReadOnly = true
		}
	}
	if !sawReadOnly {
		t.Fatalf("entityFieldsFromSchemaDefs dropped the ReadOnly bit: %+v", fields)
	}

	msgs := buildEntityCRUDMessagePieces("Order", fields)
	var createReq string
	for _, p := range msgs {
		if p.name == "CreateOrderRequest" {
			createReq = p.text
		}
	}
	if createReq == "" {
		t.Fatal("CreateOrderRequest piece not built")
	}
	if strings.Contains(createReq, "status") {
		t.Errorf("CreateOrderRequest must OMIT the read-only field:\n%s", createReq)
	}
	for _, keep := range []string{"customer", "amount"} {
		if !strings.Contains(createReq, keep) {
			t.Errorf("CreateOrderRequest dropped a client-settable field %q:\n%s", keep, createReq)
		}
	}
	// Field numbers stay contiguous across the omission (customer = 1,
	// amount = 2 — never a gap at the omitted status slot).
	if !strings.Contains(createReq, "customer = 1;") || !strings.Contains(createReq, "amount = 2;") {
		t.Errorf("CreateOrderRequest field numbers not contiguous after omission:\n%s", createReq)
	}
}

// TestReadOnlyMarkerStripsCreateFields proves the marker end to end through
// the birth path it actually travels: proto text → raw scan → entityField
// list → born Create<Entity>Request. A marked field must not reach the
// client-writable Create message, in either placement.
func TestReadOnlyMarkerStripsCreateFields(t *testing.T) {
	entityProto := func(marker string) string {
		return `syntax = "proto3";
package services.orders.v1;

// forge:entity
message Order {
  string id = 1;
  string customer = 2;
  // ` + marker + `
  string status = 3;
  int64 amount = 4; // ` + marker + `
  string note = 5;
}
`
	}

	createRequestFor := func(t *testing.T, proto string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "orders.proto"), []byte(proto), 0o644); err != nil {
			t.Fatal(err)
		}
		scan, err := codegen.ScanRawProtoDir(dir)
		if err != nil {
			t.Fatalf("ScanRawProtoDir: %v", err)
		}
		entity, ok := scan.MessageByName("Order")
		if !ok {
			t.Fatal("Order not scanned")
		}
		fields, _ := entityFieldsFromSchemaDefs("services.orders.v1", entity.Fields)
		for _, p := range buildEntityCRUDMessagePieces("Order", fields) {
			if p.name == "CreateOrderRequest" {
				return p.text
			}
		}
		t.Fatal("CreateOrderRequest piece not built")
		return ""
	}

	createReq := createRequestFor(t, entityProto("forge:read-only"))

	for _, omit := range []string{"status", "amount"} {
		if strings.Contains(createReq, omit) {
			t.Errorf("CreateOrderRequest must OMIT the read-only field %q:\n%s", omit, createReq)
		}
	}
	for _, keep := range []string{"customer", "note"} {
		if !strings.Contains(createReq, keep) {
			t.Errorf("CreateOrderRequest dropped the client-settable field %q:\n%s", keep, createReq)
		}
	}
}

func TestAppendOnlyFilter(t *testing.T) {
	rpcs := buildEntityCRUDRPCPieces("Order")
	msgs := buildEntityCRUDMessagePieces("Order", nil)

	// no-op when appendOnly is false
	if got := appendOnlyFilter(rpcs, "Order", false); len(got) != len(rpcs) {
		t.Fatalf("appendOnlyFilter(false) changed the piece count: %d vs %d", len(got), len(rpcs))
	}

	gotRPCs := appendOnlyFilter(rpcs, "Order", true)
	names := map[string]bool{}
	for _, p := range gotRPCs {
		names[p.name] = true
	}
	for _, keep := range []string{"CreateOrder", "GetOrder", "ListOrders"} {
		if !names[keep] {
			t.Errorf("append-only RPCs must keep %s", keep)
		}
	}
	for _, drop := range []string{"UpdateOrder", "DeleteOrder"} {
		if names[drop] {
			t.Errorf("append-only RPCs must drop %s", drop)
		}
	}

	gotMsgs := appendOnlyFilter(msgs, "Order", true)
	for _, p := range gotMsgs {
		if p.name == "UpdateOrderRequest" || p.name == "UpdateOrderResponse" ||
			p.name == "DeleteOrderRequest" || p.name == "DeleteOrderResponse" {
			t.Errorf("append-only messages must drop %s", p.name)
		}
	}
	// Create/Get/List envelopes survive.
	msgNames := map[string]bool{}
	for _, p := range gotMsgs {
		msgNames[p.name] = true
	}
	for _, keep := range []string{"CreateOrderRequest", "GetOrderResponse", "ListOrdersRequest", "ListOrdersResponse"} {
		if !msgNames[keep] {
			t.Errorf("append-only messages must keep %s", keep)
		}
	}
}

// THE FILTER SURFACE. A born List request must carry a facet for every field
// birth can filter mechanically — free-text search, each bool, each enum, and
// each <stem>_id reference.
//
// Enums and foreign keys used to be omitted, which left every generated list
// unfilterable on the two columns real callers filter by: a status and a
// parent. The documented workaround was to add the field by hand; the one
// people actually reached for was fetching a page and filtering client-side,
// which silently truncates past the page cap and produces numbers that are
// quietly wrong.
func TestListRequestScaffoldsEnumAndForeignKeyFacets(t *testing.T) {
	defs := []codegen.SchemaFieldDef{
		{Name: "customer_id", Kind: "string"},
		{Name: "status", Kind: "enum", TypeName: "services.orders.v1.OrderStatus"},
		{Name: "rush", Kind: "bool"},
		{Name: "notes", Kind: "string"},
	}
	fields, _ := entityFieldsFromSchemaDefs("services.orders.v1", defs)

	var listReq string
	for _, p := range buildEntityCRUDMessagePieces("Order", fields) {
		if p.name == "ListOrdersRequest" {
			listReq = p.text
		}
	}
	if listReq == "" {
		t.Fatal("ListOrdersRequest piece not built")
	}

	for _, want := range []string{
		// Typed as the enum, so an invalid value is a compile error at the
		// call site rather than a filter that matches nothing.
		"optional OrderStatus status",
		// Scoping a list to its parent is the other filter every caller needs.
		"optional string customer_id",
		"optional bool rush",
		"optional string search",
	} {
		if !strings.Contains(listReq, want) {
			t.Errorf("ListOrdersRequest is missing %q:\n%s", want, listReq)
		}
	}
}

// A NULLABLE reference is a facet like any other. The roofers Job carried
//
//	optional string crew_id = 7;  // forge:read-only
//
// and its born ListJobsRequest filtered by customer_id and property_id but
// not crew_id: the facet gate skipped every `optional` field, which is
// exactly the shape a reference takes while the relationship is not yet
// made (a job is unassigned until dispatch). The read-only marker played no
// part — it shapes the Create request, never the List one — so it rides
// along here to pin that too. Optional enums and bools were dropped the
// same way.
func TestListRequestScaffoldsFacetsForNullableFields(t *testing.T) {
	defs := []codegen.SchemaFieldDef{
		{Name: "customer_id", Kind: "string"},
		{Name: "crew_id", Kind: "string", Optional: true, ReadOnly: true},
		{Name: "priority", Kind: "enum", TypeName: "services.jobs.v1.JobPriority", Optional: true},
		{Name: "warranty", Kind: "bool", Optional: true},
		{Name: "site_notes", Kind: "string", Optional: true},
		{Name: "photo_ids", Kind: "string", Repeated: true},
	}
	fields, _ := entityFieldsFromSchemaDefs("services.jobs.v1", defs)

	var listReq string
	for _, p := range buildEntityCRUDMessagePieces("Job", fields) {
		if p.name == "ListJobsRequest" {
			listReq = p.text
		}
	}
	if listReq == "" {
		t.Fatal("ListJobsRequest piece not built")
	}
	for _, want := range []string{
		"optional string customer_id",
		"optional string crew_id",
		"optional JobPriority priority",
		"optional bool warranty",
		// A nullable text column is still text the search spans.
		"optional string search",
	} {
		if !strings.Contains(listReq, want) {
			t.Errorf("ListJobsRequest is missing %q:\n%s", want, listReq)
		}
	}
	// A repeated field has no single value to match exactly.
	if strings.Contains(listReq, "photo_ids") {
		t.Errorf("a repeated field must not get a facet:\n%s", listReq)
	}
}

// A plain text column is spanned by `search` and must NOT also get its own
// exact-match facet: an exact match on free text is not a filter anyone wants,
// and it would collide with the search span over the same column.
func TestListRequestGivesPlainTextNoExactFacet(t *testing.T) {
	defs := []codegen.SchemaFieldDef{{Name: "notes", Kind: "string"}}
	fields, _ := entityFieldsFromSchemaDefs("services.orders.v1", defs)

	var listReq string
	for _, p := range buildEntityCRUDMessagePieces("Order", fields) {
		if p.name == "ListOrdersRequest" {
			listReq = p.text
		}
	}
	if strings.Contains(listReq, "optional string notes") {
		t.Errorf("a free-text column must not get an exact-match facet:\n%s", listReq)
	}
	if !strings.Contains(listReq, "optional string search") {
		t.Errorf("a text column must still be spanned by search:\n%s", listReq)
	}
}
