//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestE2EJSONBEntityConversion is the acceptance gate for STRUCTURED proto
// fields on entities — the json/jsonb siblings of the enum contract.
//
// Entity birth already emits a JSONB column for a `repeated <Message>`, a
// nested message, and a map of scalars (see internal/scaffold/entityproto.go).
// The CRUD conversion generator then refused to map any of them, emitting
//
//	// LineItems: unmapped (wire kind repeated_message, column JSONB)
//
// into the generated body and moving on. The two halves of the SAME
// generator disagreed, and the disagreement shipped as a comment: Create
// accepted line items and stored the column DEFAULT `[]`, List returned
// none, and every gate was green. Measured on a real run — an orders table
// rendered "No line items" on every row, forever.
//
// This test runs the REAL pipeline (buf compile + embedded postgres
// shadow-apply), and proves the round trip by executing Create -> Get ->
// List inside the generated app against real postgres. It also reads the
// raw column back so the STORED SHAPE is pinned, not just the Go values:
// proto field names as keys, so `line_items -> 0 ->> 'product_id'` is the
// spelling a SQL author gets.
func TestE2EJSONBEntityConversion(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "jsonapp", "--mod", "example.com/jsonapp", "--service", "orders")
	projectDir := filepath.Join(dir, "jsonapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
message OrderLineItem {
  string product_id = 1;
  int64 quantity = 2;
}

message ShippingAddress {
  string line1 = 1;
  string city = 2;
}

// forge:entity
message Order {
  string id = 1;
  string name = 2;
  repeated OrderLineItem line_items = 3;
  ShippingAddress ship_to = 4;
  optional ShippingAddress gift_to = 5;
  map<string, string> labels = 6;
  map<string, OrderLineItem> item_index = 7;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── birth: a column for every shape the generator maps, and a TODO
	// for the one it does not. The two halves must agree — a column with
	// no conversion is a field that is dead over the API. ──
	ordersUp := readBornMigrationE2E(t, projectDir, "orders")
	for _, want := range []string{
		"line_items JSONB NOT NULL DEFAULT '[]'", // repeated message
		"ship_to JSONB NOT NULL DEFAULT '{}'",    // singular message
		"gift_to JSONB",                          // optional message -> nullable
		"labels JSONB NOT NULL DEFAULT '{}'",     // map of scalars
	} {
		if !strings.Contains(ordersUp, want) {
			t.Errorf("orders birth migration should contain %q; got:\n%s", want, ordersUp)
		}
	}
	if !strings.Contains(ordersUp, `-- TODO: proto field "item_index" skipped`) {
		t.Errorf("a map of MESSAGES has no conversion, so birth must not create a column for it; got:\n%s", ordersUp)
	}
	if strings.Contains(ordersUp, "item_index JSONB") {
		t.Errorf("birth created a column the generator refuses to map; got:\n%s", ordersUp)
	}

	// ── projection: every json column is CONVERTED, in both directions ──
	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_ops_gen.go"))
	for _, want := range []string{
		"orm.MarshalJSONBList(m.LineItems)",                         // fromProto: repeated message
		"orm.UnmarshalJSONBList(string(e.LineItems), &m.LineItems)", // toProto: a JSONB column is []byte (#991)
		"orm.MarshalJSONBMessage(m.ShipTo)",                         // singular message
		"orm.UnmarshalJSONBMessage(string(e.ShipTo), &m.ShipTo)",    //
		"if m.GiftTo != nil {",                                      // nullable: SQL NULL for absent
		"orm.UnmarshalJSONBMessage(string(e.GiftTo), &m.GiftTo)",    // nullable: a nil []byte
		"orm.MarshalJSONBMap(m.Labels)",                             // map of scalars
		"orm.UnmarshalJSONBMap(string(e.Labels), &m.Labels)",        //
		"orm.MarshalJSONBList(req.LineItems)",                       // the CREATE path, too
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file should contain %q; got:\n%s", want, ops)
		}
	}
	// The comment is the defect. A field forge cannot map fails the
	// generate; a field it CAN map is converted. Neither ships a comment.
	if strings.Contains(ops, "unmapped (wire kind") {
		t.Errorf("no field may be dropped with an explanatory comment; ops:\n%s", ops)
	}
	// The create path must not stamp the column DEFAULT over a value the
	// request carried — that is how the line items were lost.
	if strings.Contains(ops, `e.LineItems = "[]"`) || strings.Contains(ops, `e.LineItems = []byte("[]")`) {
		t.Errorf("create must not overwrite a supplied value with the column DEFAULT; ops:\n%s", ops)
	}

	// ── generate x2: green and byte-for-byte idempotent ──
	writeJSONBRoundTripTest(t, projectDir)
	snaps := make([]map[string]string, 0, 2)
	for i := 0; i < 2; i++ {
		runCmd(t, projectDir, forgeBin, "generate")
		snaps = append(snaps, hashProjectTree(t, projectDir))
		runCmd(t, projectDir, "go", "build", "./...")
		runCmd(t, projectDir, "go", "vet", "./...")
	}
	if diff := diffTreeE2E(snaps[0], snaps[1]); diff != "" {
		t.Errorf("generate #2 is not idempotent vs #1 (file churn):\n%s", diff)
	}

	// ── runtime: the generated app's own tests prove the round trip ──
	runCmd(t, projectDir, "go", "test", "./internal/handlers/orders/")
}

// TestE2EJSONBUnmappableFieldFailsGenerate is Defect 2's gate: a proto
// field that HAS a column and no conversion must fail `forge generate`,
// naming the message, the field and the column. The generator used to
// write a comment about it and exit 0.
//
// The unmappable pairing here is one birth would never create (a map of
// messages over a hand-written JSONB column) — precisely the shape a hand
// migration produces, which is how the measured defect arrived.
func TestE2EJSONBUnmappableFieldFailsGenerate(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "badjson", "--mod", "example.com/badjson", "--service", "orders")
	projectDir := filepath.Join(dir, "badjson")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
message OrderLineItem {
  string product_id = 1;
}

// forge:entity
message Order {
  string id = 1;
  string name = 2;
  map<string, OrderLineItem> item_index = 3;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// Hand-add the column birth deliberately refused. This is the state a
	// human (or an LLM) reaches by writing the migration themselves.
	migDir := filepath.Join(projectDir, "db", "migrations")
	hand := filepath.Join(migDir, "99999999999999_add_item_index.up.sql")
	if err := os.WriteFile(hand, []byte("ALTER TABLE orders ADD COLUMN item_index JSONB NOT NULL DEFAULT '{}';\n"), 0o644); err != nil {
		t.Fatalf("write hand migration: %v", err)
	}

	out, err := runCmdCombined(projectDir, 10*time.Minute, forgeBin, "generate")
	if err == nil {
		t.Fatalf("forge generate must FAIL on a field with a column and no conversion; it exited 0:\n%s", out)
	}
	// The message must carry every fact needed to act: which message,
	// which field, which table and column, and what shape it is.
	for _, want := range []string{"Order", "item_index", "orders", "JSONB"} {
		if !strings.Contains(out, want) {
			t.Errorf("generate failure must name %q; got:\n%s", want, out)
		}
	}
}

// writeJSONBRoundTripTest drops a test into the generated app that reuses
// the born harness helpers (crudTestDB / crudTestCtx, same package) to
// drive Create -> Get -> List with structured values over the real
// generated stack and a real database, then reads the raw column back so
// the STORED SHAPE is pinned too.
func writeJSONBRoundTripTest(t *testing.T, projectDir string) {
	t.Helper()
	born := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_test.go"))
	// The test factory lives in the HANDLER package (helpers_gen_test.go),
	// reached through that package's own import alias — the pkg/app god
	// package that used to host app.NewTest<X> was retired with the old DI
	// unit. Capture the alias too so the injected test imports what the
	// born one does.
	m := regexp.MustCompile(`(\w+)\.NewTest(\w+)\(`).FindStringSubmatch(born)
	if m == nil {
		t.Fatalf("born handlers_crud_test.go carries no <pkg>.NewTest<X> helper call:\n%s", born)
	}
	pkgAlias := m[1]
	helper := m[2]
	pbImport := regexp.MustCompile(`pb "([^"]+)"`).FindStringSubmatch(born)
	if pbImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no pb import:\n%s", born)
	}
	svcImport := regexp.MustCompile(pkgAlias + ` "([^"]+)"`).FindStringSubmatch(born)
	if svcImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no %s handler-package import:\n%s", pkgAlias, born)
	}

	test := fmt.Sprintf(`package orders_test

// Written by forge's json/jsonb e2e (TestE2EJSONBEntityConversion):
// proves structured fields survive the executed Create -> Get -> List
// path, and that the stored document is the shape SQL expects.

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	pb "%s"
	%s "%s"
)

func TestJSONBStructuredRoundTrip(t *testing.T) {
	db := crudTestDB(t)
	svc := %s.NewTest%s(t, %s.WithDB(db))
	ctx := crudTestCtx()

	created, err := svc.CreateOrder(ctx, connect.NewRequest(&pb.CreateOrderRequest{
		Name: "jsonb-roundtrip",
		LineItems: []*pb.OrderLineItem{
			{ProductId: "prod-1", Quantity: 3},
			{ProductId: "prod-2", Quantity: 7},
		},
		ShipTo: &pb.ShippingAddress{Line1: "1 Main St", City: "Springfield"},
		Labels: map[string]string{"channel": "web"},
	}))
	if err != nil {
		t.Fatalf("create: %%v", err)
	}
	id := created.Msg.GetOrder().GetId()

	// The CREATE RESPONSE must already carry what the caller sent — the
	// measured defect stored the column DEFAULT and echoed nothing.
	if n := len(created.Msg.GetOrder().GetLineItems()); n != 2 {
		t.Fatalf("create response carried %%d line items, want 2", n)
	}

	got, err := svc.GetOrder(ctx, connect.NewRequest(&pb.GetOrderRequest{Id: id}))
	if err != nil {
		t.Fatalf("get: %%v", err)
	}
	items := got.Msg.GetOrder().GetLineItems()
	if len(items) != 2 {
		t.Fatalf("line items did not survive create->get: got %%d, want 2", len(items))
	}
	if items[0].GetProductId() != "prod-1" || items[0].GetQuantity() != 3 {
		t.Errorf("line item 0 changed: %%v", items[0])
	}
	if items[1].GetProductId() != "prod-2" || items[1].GetQuantity() != 7 {
		t.Errorf("line item 1 changed: %%v", items[1])
	}
	if c := got.Msg.GetOrder().GetShipTo().GetCity(); c != "Springfield" {
		t.Errorf("nested message did not survive create->get: city = %%q", c)
	}
	if got.Msg.GetOrder().GetGiftTo() != nil {
		t.Errorf("an absent optional message must read back nil, got %%v", got.Msg.GetOrder().GetGiftTo())
	}
	if l := got.Msg.GetOrder().GetLabels()["channel"]; l != "web" {
		t.Errorf("map did not survive create->get: labels = %%v", got.Msg.GetOrder().GetLabels())
	}

	// LIST is the half a hand-patch always misses: the measured fix
	// patched Create/Get/Update and left List returning empty arrays.
	listed, err := svc.ListOrders(ctx, connect.NewRequest(&pb.ListOrdersRequest{PageSize: 10}))
	if err != nil {
		t.Fatalf("list: %%v", err)
	}
	found := false
	for _, o := range listed.Msg.GetOrders() {
		if o.GetId() != id {
			continue
		}
		found = true
		if n := len(o.GetLineItems()); n != 2 {
			t.Errorf("list returned %%d line items for the order, want 2", n)
		}
	}
	if !found {
		t.Fatalf("created order not returned by list")
	}

	// The STORED DOCUMENT: proto field names as keys, so a SQL author's
	// line_items -> 0 ->> 'product_id' works.
	var productID string
	row := db.QueryRow(context.Background(),
		"SELECT line_items -> 0 ->> 'product_id' FROM orders WHERE id = $1", id)
	if err := row.Scan(&productID); err != nil {
		t.Fatalf("read the raw jsonb column: %%v", err)
	}
	if productID != "prod-1" {
		t.Errorf("stored document uses unexpected keys: product_id = %%q", productID)
	}
}
`, pbImport[1], pkgAlias, svcImport[1], pkgAlias, helper, pkgAlias)

	path := filepath.Join(projectDir, "internal", "handlers", "orders", "jsonb_roundtrip_e2e_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatalf("write jsonb round-trip test: %v", err)
	}
}
