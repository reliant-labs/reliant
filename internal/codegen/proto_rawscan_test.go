// File: internal/codegen/proto_rawscan_test.go
//
// Tests for the lightweight raw-proto scanner: marker detection (spacing
// variants, marker-precedes-message rule), top-level field capture in
// SchemaFieldDef vocabulary (scalars, optional/repeated, enums with
// values, nested/cross-package messages, maps, oneofs), rpc + service
// capture, and multi-file same-package scans.

package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const rawScanFixtureProto = `syntax = "proto3";

package services.tasks.v1;

import "forge/v1/forge.proto";
import "google/protobuf/timestamp.proto";

option go_package = "example.com/x/gen/services/tasks/v1;tasksv1";

service TasksService {
  option (forge.v1.service) = { name: "TasksService" };

  rpc SubmitOrder(SubmitOrderRequest) returns (SubmitOrderResponse) {
    option (forge.v1.method) = {
      auth_required: true
    };
  }
  rpc TailEvents(TailEventsRequest) returns (stream TailEventsResponse);
}

// forge:entity
message Order {
  string id = 1;
  string customer_id = 2;
  optional string note = 3;
  OrderStatus status = 4;
  repeated string tags = 5;
  google.protobuf.Timestamp placed_at = 6;
  Item primary_item = 7;
  map<string, int64> attrs = 8;
  oneof payment {
    string card_token = 9;
    string invoice_ref = 10;
  }
  shared.v1.Address address = 11;

  message Item {
    string sku = 1;
  }
}

//forge:entity — glued spelling, trailing prose
message Customer {
  string name = 1;
}

// A plain comment between marker and message keeps the marker alive.
// forge:entity
// (still applies to the next message)
message Warehouse {
  string region = 1;
}

message Unmarked {
  string x = 1;
}

// forge:entity
message CreateOrderRequest {
  string customer_id = 1;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_OPEN = 1;
  ORDER_STATUS_CLOSED = 2;
}
`

func writeRawScanFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScanRawProtoDir_MarkersAndFields(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/tasks.proto": rawScanFixtureProto})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}

	if scan.Package != "services.tasks.v1" {
		t.Errorf("Package = %q", scan.Package)
	}
	if scan.ServiceName != "TasksService" {
		t.Errorf("ServiceName = %q", scan.ServiceName)
	}

	marked := map[string]bool{}
	for _, m := range scan.Messages {
		marked[m.Name] = m.Marked
	}
	for name, want := range map[string]bool{
		"Order":              true,
		"Customer":           true, // glued //forge:entity spelling
		"Warehouse":          true, // comments between marker and message
		"Unmarked":           false,
		"CreateOrderRequest": true, // marked — the REFUSAL is the consumer's job, not the scanner's
	} {
		if got, ok := marked[name]; !ok || got != want {
			t.Errorf("message %s: marked = %v (found %v), want %v", name, got, ok, want)
		}
	}

	order, ok := scan.MessageByName("Order")
	if !ok {
		t.Fatal("Order not scanned")
	}
	byName := map[string]SchemaFieldDef{}
	for _, f := range order.Fields {
		byName[f.Name] = f
	}
	const pkg = "services.tasks.v1"
	checks := []struct {
		name string
		want SchemaFieldDef
	}{
		{"id", SchemaFieldDef{Name: "id", Kind: "string"}},
		{"note", SchemaFieldDef{Name: "note", Kind: "string", Optional: true}},
		{"status", SchemaFieldDef{Name: "status", Kind: "enum", TypeName: pkg + ".OrderStatus"}},
		{"tags", SchemaFieldDef{Name: "tags", Kind: "string", Repeated: true}},
		{"placed_at", SchemaFieldDef{Name: "placed_at", Kind: "message", TypeName: "google.protobuf.Timestamp"}},
		{"primary_item", SchemaFieldDef{Name: "primary_item", Kind: "message", TypeName: pkg + ".Order.Item"}},
		{"attrs", SchemaFieldDef{Name: "attrs", Kind: "map", MapKeyKind: "string", MapValueKind: "int64"}},
		{"card_token", SchemaFieldDef{Name: "card_token", Kind: "string", Oneof: "payment"}},
		{"invoice_ref", SchemaFieldDef{Name: "invoice_ref", Kind: "string", Oneof: "payment"}},
		{"address", SchemaFieldDef{Name: "address", Kind: "message", TypeName: "shared.v1.Address"}},
	}
	for _, c := range checks {
		got, ok := byName[c.name]
		if !ok {
			t.Errorf("Order field %q not captured", c.name)
			continue
		}
		// DeepEqual, not ==: SchemaFieldDef carries slice-typed fields
		// (Guards), so the struct is no longer comparable.
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Order field %q = %+v, want %+v", c.name, got, c.want)
		}
	}

	// Enum values in declaration order, keyed fully-qualified.
	values := scan.Enums[pkg+".OrderStatus"]
	if len(values) != 3 || values[0] != "ORDER_STATUS_UNSPECIFIED" || values[2] != "ORDER_STATUS_CLOSED" {
		t.Errorf("OrderStatus values = %v", values)
	}

	// RPCs with streaming detection.
	if !scan.DeclaresRPC("SubmitOrder") || !scan.DeclaresRPC("TailEvents") {
		t.Errorf("rpcs not captured: %+v", scan.RPCs)
	}
	for _, r := range scan.RPCs {
		if r.Name == "SubmitOrder" && r.Streaming {
			t.Error("SubmitOrder wrongly detected streaming")
		}
		if r.Name == "TailEvents" && !r.Streaming {
			t.Error("TailEvents streaming not detected")
		}
	}
}

// TestScanRawProtoDir_ReadOnlyMarker pins the FIELD-level
// `// forge:read-only` marker on the raw-scan path (the birth truth for a
// brand-new `// forge:entity` message): the LEADING full-line spelling, the
// TRAILING inline spelling, and correct binding on a multi-field inline line.
// A marked field stays a normal captured field — only its ReadOnly bit flips.
func TestScanRawProtoDir_ReadOnlyMarker(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/orders.proto": `syntax = "proto3";
package services.orders.v1;

// forge:entity
message Order {
  string id = 1;
  string customer = 2;
  // forge:read-only
  string status = 3;
  int64 amount = 4; // forge:read-only — trailing spelling
  string note = 5;
}

// forge:entity
message Ledger { string id = 1; string owner = 2; string balance = 3; // forge:read-only
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	order, ok := scan.MessageByName("Order")
	if !ok {
		t.Fatal("Order not scanned")
	}
	readOnly := map[string]bool{}
	for _, f := range order.Fields {
		readOnly[f.Name] = f.ReadOnly
	}
	for name, want := range map[string]bool{
		"id":       false,
		"customer": false,
		"status":   true, // leading full-line marker
		"amount":   true, // trailing inline marker
		"note":     false,
	} {
		if readOnly[name] != want {
			t.Errorf("Order field %q: ReadOnly = %v, want %v", name, readOnly[name], want)
		}
	}

	// A trailing marker binds to the LAST field on an inline-body line, not
	// every field: only `balance` is read-only, `id`/`owner` are not.
	ledger, ok := scan.MessageByName("Ledger")
	if !ok {
		t.Fatal("Ledger not scanned")
	}
	lset := map[string]bool{}
	for _, f := range ledger.Fields {
		lset[f.Name] = f.ReadOnly
	}
	for name, want := range map[string]bool{"id": false, "owner": false, "balance": true} {
		if lset[name] != want {
			t.Errorf("Ledger field %q: ReadOnly = %v, want %v", name, lset[name], want)
		}
	}
}

// TestScanRawProtoDir_ServerSetIsNotAReadOnlyMarker pins the removal:
// `// forge:server-set` is not a marker forge knows, so it attaches to
// nothing and leaves the field client-writable. It is also NOT collected as
// an unapplied read-only marker — the refusal names markers forge recognizes
// but could not place, and this is not one of them.
//
// The retirement is no longer SILENT, though: the same spelling lands in the
// separate RetiredMarkers ledger, which birth refuses on. See
// TestScanRawProtoDir_RetiredMarkerSitesAreCollected — this test pins only
// that the old marker was not resurrected as an alias.
func TestScanRawProtoDir_ServerSetIsNotAReadOnlyMarker(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/orders.proto": `syntax = "proto3";
package services.orders.v1;

// forge:entity
message Order {
  string id = 1;
  // forge:server-set
  string status = 2;
  int64 amount = 3; // forge:server-set
  string note = 4;
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	order, ok := scan.MessageByName("Order")
	if !ok {
		t.Fatal("Order not scanned")
	}
	for _, f := range order.Fields {
		if f.ReadOnly {
			t.Errorf("field %q: ReadOnly = true, want false — forge:server-set is no longer a marker", f.Name)
		}
	}
	if len(order.UnappliedReadOnlyMarkers) != 0 {
		t.Errorf("got %d unapplied read-only marker(s), want 0: %+v",
			len(order.UnappliedReadOnlyMarkers), order.UnappliedReadOnlyMarkers)
	}
}

// TestScanRawProtoDir_InlineBodies pins statement splitting: a message
// (or enum) whose body sits on ONE line must parse identically to its
// multi-line spelling. Before splitProtoStatements, inline bodies
// captured ZERO fields — and a captured-fieldless `// forge:entity`
// message birthed a COLUMN-LESS table, silently dropping every field
// (including the `optional` scalars the db skill promises become
// nullable columns).
func TestScanRawProtoDir_InlineBodies(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/w.proto": `syntax = "proto3";
package services.widget.v1;

// forge:entity
message Widget { string id = 1; string name = 2; optional string nickname = 3; repeated string tags = 4; }

enum Color { COLOR_UNSPECIFIED = 0; COLOR_RED = 1; }

message Holder { Widget widget = 1; map<string, int64> attrs = 2; }
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}

	w, ok := scan.MessageByName("Widget")
	if !ok {
		t.Fatal("inline-body Widget not scanned")
	}
	if !w.Marked {
		t.Error("marker before an inline-body message must still mark it")
	}
	byName := map[string]SchemaFieldDef{}
	for _, f := range w.Fields {
		byName[f.Name] = f
	}
	for _, c := range []struct {
		name string
		want SchemaFieldDef
	}{
		{"id", SchemaFieldDef{Name: "id", Kind: "string"}},
		{"name", SchemaFieldDef{Name: "name", Kind: "string"}},
		{"nickname", SchemaFieldDef{Name: "nickname", Kind: "string", Optional: true}},
		{"tags", SchemaFieldDef{Name: "tags", Kind: "string", Repeated: true}},
	} {
		got, ok := byName[c.name]
		if !ok {
			t.Errorf("inline-body field %q not captured", c.name)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("inline-body field %q = %+v, want %+v", c.name, got, c.want)
		}
	}

	// Inline enum bodies capture their values, in declaration order.
	if v := scan.Enums["services.widget.v1.Color"]; len(v) != 2 || v[0] != "COLOR_UNSPECIFIED" || v[1] != "COLOR_RED" {
		t.Errorf("inline enum values = %v", v)
	}

	// Message-typed and map fields on one line classify like multi-line.
	h, ok := scan.MessageByName("Holder")
	if !ok {
		t.Fatal("inline-body Holder not scanned")
	}
	hb := map[string]SchemaFieldDef{}
	for _, f := range h.Fields {
		hb[f.Name] = f
	}
	if got := hb["widget"]; got.Kind != "message" || got.TypeName != "services.widget.v1.Widget" {
		t.Errorf("inline message-typed field = %+v", got)
	}
	if got := hb["attrs"]; got.Kind != "map" || got.MapKeyKind != "string" || got.MapValueKind != "int64" {
		t.Errorf("inline map field = %+v", got)
	}
}

func TestScanRawProtoDir_MarkerMustPrecedeMessage(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/x.proto": `syntax = "proto3";
package p.v1;

// forge:entity

message Gap {
  string a = 1;
}

// forge:entity
enum NotAMessage {
  NOT_A_MESSAGE_UNSPECIFIED = 0;
}

message AfterEnum {
  string a = 1;
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	gap, _ := scan.MessageByName("Gap")
	if !gap.Marked {
		t.Error("blank lines between marker and message must not kill the marker")
	}
	after, _ := scan.MessageByName("AfterEnum")
	if after.Marked {
		t.Error("a marker consumed by an intervening declaration must not leak to a later message")
	}
}

// TestScanRawProtoDir_ValidateOptions proves the raw scanner captures a
// field's inline protovalidate rules onto SchemaFieldDef.Validate — the
// truth the born migration reads for a brand-new `// forge:entity` message.
func TestScanRawProtoDir_ValidateOptions(t *testing.T) {
	proto := `syntax = "proto3";
package shop.v1;
import "buf/validate/validate.proto";

// forge:entity
message Widget {
  string id = 1;
  int64 amount_cents = 2 [(buf.validate.field).int64.gte = 0];
  string name = 3 [(buf.validate.field).string.min_len = 2, (buf.validate.field).string.max_len = 64];
  string sku = 4 [(buf.validate.field).string.pattern = "^SKU-[0-9]+$"];
  string email = 5 [(buf.validate.field).string.email = true];
  string plain = 6;
}
`
	dir := writeRawScanFixture(t, map[string]string{"shop/v1/shop.proto": proto})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := scan.MessageByName("Widget")
	if !ok {
		t.Fatal("Widget not scanned")
	}
	byName := map[string]SchemaFieldDef{}
	for _, f := range m.Fields {
		byName[f.Name] = f
	}
	if v := byName["amount_cents"].Validate; v == nil || v.Gte != "0" {
		t.Errorf("amount_cents.Validate = %+v", byName["amount_cents"].Validate)
	}
	if v := byName["name"].Validate; v == nil || v.MinLen == nil || *v.MinLen != 2 || v.MaxLen == nil || *v.MaxLen != 64 {
		t.Errorf("name.Validate = %+v", byName["name"].Validate)
	}
	if v := byName["sku"].Validate; v == nil || v.Pattern != "^SKU-[0-9]+$" {
		t.Errorf("sku.Validate = %+v", byName["sku"].Validate)
	}
	if v := byName["email"].Validate; v == nil || !v.Email {
		t.Errorf("email.Validate = %+v", byName["email"].Validate)
	}
	if byName["plain"].Validate != nil {
		t.Errorf("plain must carry no constraints, got %+v", byName["plain"].Validate)
	}
}

// TestScanRawProtoDir_ValidateOptions_MultiLineBraced pins the regression the
// combined braced protovalidate form hit: a braced value with multiple bounds
// is idiomatically written across SEVERAL physical lines
// (`= {`\n`  gte: 1`\n`  lte: 12`\n`}`), so the `[...]` options block does not
// close on the field's own line. The scanner must read the options across the
// newlines — a single-line read dropped every rule and the born migration
// carried NO CHECK, while the dotted single-line forms kept working. Covers a
// `//` comment (with a brace) inside the block and a multi-line string pattern
// whose value embeds `//` and `[` too.
func TestScanRawProtoDir_ValidateOptions_MultiLineBraced(t *testing.T) {
	proto := `syntax = "proto3";
package shop.v1;
import "buf/validate/validate.proto";

// forge:entity
message Card {
  string id = 1;
  int32 exp_month = 2 [(buf.validate.field).int32 = {
    gte: 1
    lte: 12
  }];
  int64 amount_cents = 3 [(buf.validate.field).int64 = {
    gte: 0  // never negative {enforced}
  }];
  string code = 4 [(buf.validate.field).string = {
    min_len: 2
    max_len: 8
  }];
  string url_path = 5 [(buf.validate.field).string = {
    pattern: "^https?://[a-z0-9./-]+$"
  }];
  int64 dotted = 6 [(buf.validate.field).int64.gte = 1];
}
`
	dir := writeRawScanFixture(t, map[string]string{"shop/v1/shop.proto": proto})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := scan.MessageByName("Card")
	if !ok {
		t.Fatal("Card not scanned")
	}
	byName := map[string]SchemaFieldDef{}
	for _, f := range m.Fields {
		byName[f.Name] = f
	}
	// Multi-line braced int32 with TWO bounds — the reported defect.
	if v := byName["exp_month"].Validate; v == nil || v.Gte != "1" || v.Lte != "12" {
		t.Errorf("exp_month.Validate = %+v, want gte=1 lte=12", byName["exp_month"].Validate)
	}
	// Multi-line braced int64 with a `//` comment (carrying a brace) inside.
	if v := byName["amount_cents"].Validate; v == nil || v.Gte != "0" {
		t.Errorf("amount_cents.Validate = %+v, want gte=0", byName["amount_cents"].Validate)
	}
	// Multi-line braced string length.
	if v := byName["code"].Validate; v == nil || v.MinLen == nil || *v.MinLen != 2 || v.MaxLen == nil || *v.MaxLen != 8 {
		t.Errorf("code.Validate = %+v, want min_len=2 max_len=8", byName["code"].Validate)
	}
	// Multi-line braced pattern whose value embeds `//` and `[` — neither may
	// truncate the capture (string spans are preserved).
	if v := byName["url_path"].Validate; v == nil || v.Pattern != "^https?://[a-z0-9./-]+$" {
		t.Errorf("url_path.Validate = %+v, want the full pattern", byName["url_path"].Validate)
	}
	// Control: the single-line dotted form is unaffected.
	if v := byName["dotted"].Validate; v == nil || v.Gte != "1" {
		t.Errorf("dotted.Validate = %+v, want gte=1", byName["dotted"].Validate)
	}
}

func TestScanRawProtoDir_MultiFileAndMissingDir(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{
		"v1/svc.proto": `syntax = "proto3";
package p.v1;
service PService {
  rpc DoThing(DoThingRequest) returns (DoThingResponse);
}
message DoThingRequest { string a = 1; }
message DoThingResponse { string b = 1; }
`,
		"v1/entities.proto": `syntax = "proto3";
package p.v1;

// forge:entity
message Widget {
  string label = 1;
  Grade grade = 2;
}

enum Grade {
  GRADE_UNSPECIFIED = 0;
  GRADE_A = 1;
}
`,
	})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := scan.MessageByName("Widget")
	if !ok || !w.Marked {
		t.Fatalf("Widget marker lost in multi-file scan: %+v", w)
	}
	// Cross-file enum resolution within the same package.
	var grade SchemaFieldDef
	for _, f := range w.Fields {
		if f.Name == "grade" {
			grade = f
		}
	}
	if grade.Kind != "enum" || grade.TypeName != "p.v1.Grade" {
		t.Errorf("cross-file enum classification = %+v", grade)
	}
	if scan.ServiceName != "PService" {
		t.Errorf("ServiceName = %q", scan.ServiceName)
	}

	// Missing directory: empty scan, no error.
	empty, err := ScanRawProtoDir(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatalf("missing dir must not error: %v", err)
	}
	if len(empty.Messages) != 0 || len(empty.Files) != 0 {
		t.Errorf("missing dir must scan empty, got %+v", empty)
	}
}

// TestScanRawProtoDir_RetiredMarkerSitesAreCollected pins the retired-marker
// ledger: a spelling forge deliberately REMOVED (codegen.RemovedProtoMarkers)
// is not a marker any scanner honours, so it attaches to no field — but
// unlike arbitrary prose it has a definite meaning the author expects and a
// definite fix, so the scan must surface it rather than drop it.
//
// This is the ledger the read-only refusal could never populate: that one
// collects markers forge RECOGNIZES but could not place, and a retired
// spelling is recognized by nothing.
func TestScanRawProtoDir_RetiredMarkerSitesAreCollected(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/orders.proto": `syntax = "proto3";
package services.orders.v1;

// forge:entity
message Order {
  string id = 1;
  // forge:server-set
  string status = 2;
  int64 amount = 3; // forge:server-set
  string note = 4;
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	order, ok := scan.MessageByName("Order")
	if !ok {
		t.Fatal("Order not scanned")
	}
	// Both spellings — leading comment line and trailing inline — are sites.
	if len(order.RetiredMarkers) != 2 {
		t.Fatalf("got %d retired marker site(s), want 2: %+v",
			len(order.RetiredMarkers), order.RetiredMarkers)
	}
	for _, site := range order.RetiredMarkers {
		if site.Marker != "forge:server-set" {
			t.Errorf("site Marker = %q, want forge:server-set", site.Marker)
		}
		if site.ReplacedBy != ProtoMarkerReadOnly {
			t.Errorf("site ReplacedBy = %q, want %q", site.ReplacedBy, ProtoMarkerReadOnly)
		}
		if site.Line == 0 {
			t.Errorf("site %+v has no line number", site)
		}
	}
	// The retirement itself still holds: nothing became read-only.
	for _, f := range order.Fields {
		if f.ReadOnly {
			t.Errorf("field %q: ReadOnly = true, want false — forge:server-set is retired, not aliased", f.Name)
		}
	}
}

// TestScanRawProtoDir_EveryRetiredMarkerReachesTheLedger pins the registry
// against the scanner: every entry in RemovedProtoMarkers must actually be
// collected, so adding a future retirement to the map cannot silently fail
// to reach birth. Without this pin the map could grow an entry that only the
// lint check honours — reintroducing, for the new spelling, exactly the
// silence this ledger closed for forge:server-set.
func TestScanRawProtoDir_EveryRetiredMarkerReachesTheLedger(t *testing.T) {
	for retired, replacement := range RemovedProtoMarkers {
		t.Run(retired, func(t *testing.T) {
			dir := writeRawScanFixture(t, map[string]string{"v1/x.proto": `syntax = "proto3";
package services.x.v1;

// forge:entity
message Thing {
  string id = 1;
  // ` + retired + `
  string status = 2;
}
`})
			scan, err := ScanRawProtoDir(dir)
			if err != nil {
				t.Fatalf("ScanRawProtoDir: %v", err)
			}
			thing, ok := scan.MessageByName("Thing")
			if !ok {
				t.Fatal("Thing not scanned")
			}
			if len(thing.RetiredMarkers) != 1 {
				t.Fatalf("retired marker %q reached the ledger %d time(s), want 1",
					retired, len(thing.RetiredMarkers))
			}
			if got := thing.RetiredMarkers[0].ReplacedBy; got != replacement {
				t.Errorf("ReplacedBy = %q, want %q", got, replacement)
			}
		})
	}
}

// TestScanRawProtoDir_RetiredMarkerLedgerIgnoresProse proves the ledger does
// not fire on the two shapes that must stay silent, which is the risk that
// matters: a spurious birth refusal breaks every project that builds today.
//
//   - Prose that merely CONTAINS `forge:` ("see the forge:read-only docs")
//     is a comment about forge, not a marker.
//   - Markers forge legitimately reads elsewhere — forge:mutation (frontend
//     hook generation, every generate) and forge:secret (descriptor path) —
//     are never retired spellings, and the raw scanner not consuming them is
//     not the same as them being wrong.
func TestScanRawProtoDir_RetiredMarkerLedgerIgnoresProse(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/orders.proto": `syntax = "proto3";
package services.orders.v1;

// forge:entity
// See the forge:read-only docs before editing; the forge:server-set spelling
// was retired, so do not use it in new protos.
message Order {
  string id = 1;
  // forge:secret
  string token = 2;
  int64 amount = 3; // forge:mutation is a method marker, not a field one
  string note = 4; // a note about forge: markers generally
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	order, ok := scan.MessageByName("Order")
	if !ok {
		t.Fatal("Order not scanned")
	}
	if len(order.RetiredMarkers) != 0 {
		t.Errorf("prose and other-pass markers must not populate the retired ledger, got %d: %+v",
			len(order.RetiredMarkers), order.RetiredMarkers)
	}
}
