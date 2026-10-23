//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EEnumEntityConversionAndFilter is the acceptance gate for proto
// enum fields on entities, end to end — the enum siblings of the
// optional-scalar contract. Entity birth stores enum fields as TEXT
// columns holding the proto enum VALUE NAMES (CHECK (col IN
// ('ORDER_STATUS_...'))), so the Go projections must speak names too:
//
//   - conversions: <entity>ToProto / <entity>FromProto map enum wire
//     fields <-> TEXT columns by NAME (String() toward the database,
//     pb.<Enum>_value[name] toward the wire). The old generator left
//     every enum field unmapped — dead over the API: rows born
//     UNSPECIFIED forever, updates writing "" into the CHECK. The
//     name->number direction is CHECKED: a stored name the current proto
//     no longer declares (renamed/removed enum value) returns an error
//     rather than reading the row back as UNSPECIFIED, so the unchecked
//     `pb.<Enum>(pb.<Enum>_value[col])` cast must never reappear;
//   - list filters: an enum-typed filter binds req.<F>.String(), never
//     the raw pb enum — the old `WhereEq("status", *req.Status)` hit
//     Postgres with `text = integer` and errored on the first call.
//
// The entity carries the full enum mix: a plain enum (NOT NULL, born at
// the first REAL member, its CHECK refusing the proto zero), an
// `optional` enum (nullable — NULL is its spelling for unset), and a
// `repeated` enum (TEXT[] of names); the hand-authored ListOrdersRequest carries an
// `optional OrderStatus status` filter. Runs the REAL pipeline (buf
// compile + embedded postgres shadow-apply), requires generate x2
// idempotent + build + vet green, and proves the runtime round-trip by
// running a Create -> Get -> List(filter) test inside the generated app
// against real postgres.
func TestE2EEnumEntityConversionAndFilter(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "enumapp", "--mod", "example.com/enumapp", "--service", "orders")
	projectDir := filepath.Join(dir, "enumapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author the enum, the marked entity, and OUR ListOrdersRequest (the
	// quintet completion keeps a message the user already declared, so
	// the enum filter field survives into the generated list handler).
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_DRAFT = 1;
  ORDER_STATUS_PLACED = 2;
  ORDER_STATUS_SHIPPED = 3;
}

// forge:entity
message Order {
  string id = 1;
  string name = 2;
  OrderStatus status = 3;
  optional OrderStatus fallback_status = 4;
  repeated OrderStatus history = 5;
}

message ListOrdersRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional OrderStatus status = 3;
  string order_by = 4;
  bool descending = 5;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── birth: enum columns store VALUE NAMES under a CHECK ──
	ordersUp := readBornMigrationE2E(t, projectDir, "orders")
	for _, want := range []string{
		"status TEXT NOT NULL DEFAULT 'ORDER_STATUS_DRAFT' CHECK (status IN (", // plain enum, born at the first REAL member
		"'ORDER_STATUS_PLACED'",                            // the CHECK vocabulary is the value names
		"fallback_status TEXT CHECK (fallback_status IN (", // optional enum -> nullable, no default
		"history TEXT[] NOT NULL DEFAULT '{}'",             // repeated enum -> TEXT[] of names
	} {
		if !strings.Contains(ordersUp, want) {
			t.Errorf("orders birth migration should contain %q; got:\n%s", want, ordersUp)
		}
	}
	// The proto zero is "the caller did not say" — a fact about a request,
	// never about a stored row — so no CHECK admits it. A row that held it
	// would be in a state the domain does not have, and every reader would
	// have to handle it.
	for _, line := range strings.Split(ordersUp, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, "CHECK (") && strings.Contains(line, "ORDER_STATUS_UNSPECIFIED") {
			t.Errorf("CHECK admits the proto zero sentinel: %s", strings.TrimSpace(line))
		}
	}

	// ── projection: conversions map the enum by NAME, both directions ──
	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_ops_gen.go"))
	for _, want := range []string{
		"if m.Status != 0 {\n\t\te.Status = m.Status.String()\n\t}", // fromProto: a value the caller sent stores its own name
		"if v, ok := pb.OrderStatus_value[e.Status]; ok {",          // toProto: name -> number, LOOKED UP not cast
		"m.Status = pb.OrderStatus(v)",
		`return nil, fmt.Errorf("corrupt enum value %q for column status", e.Status)`, // …and a miss is an error
		"if m.FallbackStatus != nil && *m.FallbackStatus != 0 {",                      // optional enum: unset -> NULL
		"if e.FallbackStatus != nil {",
		"if v, ok := pb.OrderStatus_value[*e.FallbackStatus]; ok {",
		`return nil, fmt.Errorf("corrupt enum value %q for column fallback_status", *e.FallbackStatus)`,
		"for _, v := range m.History {", // repeated enum: element-wise names
		"e.History = append(e.History, v.String())",
		"if v, ok := pb.OrderStatus_value[sv]; ok {",
		"m.History = append(m.History, pb.OrderStatus(v))",
		`return nil, fmt.Errorf("corrupt enum value %q for column history", sv)`,
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file should contain %q; got:\n%s", want, ops)
		}
	}
	// Unset (the proto zero) leaves the field empty — the born enum column is
	// ,nullzero, so Create omits it and the database applies its own DEFAULT
	// (F12.5, #990). The op must not paste the DEFAULT's literal into Go.
	if strings.Contains(ops, `e.Status = "ORDER_STATUS_DRAFT"`) {
		t.Errorf("the create op must leave an unset enum to the column DEFAULT, not paste its literal; ops:\n%s", ops)
	}
	// The unchecked cast is the bug the checked lookup replaced: it turns a
	// stored name the proto no longer declares into UNSPECIFIED, silently,
	// on the read path. It must never come back.
	if strings.Contains(ops, "pb.OrderStatus(pb.OrderStatus_value[") {
		t.Errorf("enum toProto must not use the unchecked map cast (unknown name reads back as UNSPECIFIED); ops:\n%s", ops)
	}
	if strings.Contains(ops, "unmapped (wire kind enum") {
		t.Errorf("enum entity fields must be mapped, not silently dropped; ops:\n%s", ops)
	}

	// ── list filter: bind the VALUE NAME, never the raw pb enum ──
	if !strings.Contains(ops, `orm.WhereEq("status", (*req.Status).String())`) {
		t.Errorf("optional enum filter must bind (*req.Status).String(); ops:\n%s", ops)
	}
	if strings.Contains(ops, `orm.WhereEq("status", *req.Status))`) {
		t.Errorf("enum filter must never bind the raw pb enum (Postgres text = integer); ops:\n%s", ops)
	}

	// ── generate x2: green and byte-for-byte idempotent ──
	writeEnumRoundTripTest(t, projectDir)
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

	// ── runtime: the generated app's own tests prove the round-trip ──
	// Runs the born lifecycle test AND the injected enum round-trip
	// (Create -> Get -> List(enum filter)) against real postgres: the
	// filter executing at all is the text-=-text proof, the Get proving
	// the status survived is the conversion proof.
	runCmd(t, projectDir, "go", "test", "./internal/handlers/orders/")
}

// writeEnumRoundTripTest drops a test file into the generated app that
// reuses the born harness helpers (crudTestDB / crudTestCtx, same
// package) to drive Create -> Get -> List(filter) with enum values over
// the real generated stack and a real database.
func writeEnumRoundTripTest(t *testing.T, projectDir string) {
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

// Written by forge's enum e2e (TestE2EEnumEntityConversionAndFilter):
// proves enum fields survive the executed Create -> Get -> List(filter)
// path — names in the database, numbers on the wire.

import (
	"testing"

	"connectrpc.com/connect"

	pb "%[1]s"
	%[3]s "%[4]s"
)

func TestEnumStatusRoundTrip(t *testing.T) {
	db := crudTestDB(t)
	svc := %[3]s.NewTest%[2]s(t, %[3]s.WithDB(db))
	ctx := crudTestCtx()

	fallback := pb.OrderStatus_ORDER_STATUS_DRAFT
	created, err := svc.CreateOrder(ctx, connect.NewRequest(&pb.CreateOrderRequest{
		Name:           "enum-roundtrip",
		Status:         pb.OrderStatus_ORDER_STATUS_PLACED,
		FallbackStatus: &fallback,
		History:        []pb.OrderStatus{pb.OrderStatus_ORDER_STATUS_DRAFT, pb.OrderStatus_ORDER_STATUS_PLACED},
	}))
	if err != nil {
		t.Fatalf("create: %%v", err)
	}

	got, err := svc.GetOrder(ctx, connect.NewRequest(&pb.GetOrderRequest{Id: created.Msg.GetOrder().GetId()}))
	if err != nil {
		t.Fatalf("get: %%v", err)
	}
	if s := got.Msg.GetOrder().GetStatus(); s != pb.OrderStatus_ORDER_STATUS_PLACED {
		t.Fatalf("status did not survive create->get: got %%v, want PLACED", s)
	}
	if s := got.Msg.GetOrder().GetFallbackStatus(); s != pb.OrderStatus_ORDER_STATUS_DRAFT {
		t.Fatalf("optional enum did not survive create->get: got %%v, want DRAFT", s)
	}
	if h := got.Msg.GetOrder().GetHistory(); len(h) != 2 || h[0] != pb.OrderStatus_ORDER_STATUS_DRAFT || h[1] != pb.OrderStatus_ORDER_STATUS_PLACED {
		t.Fatalf("repeated enum did not survive create->get: got %%v", h)
	}

	// The enum list filter must execute (TEXT = name, not text = integer)
	// and match by stored value name.
	match := pb.OrderStatus_ORDER_STATUS_PLACED
	listed, err := svc.ListOrders(ctx, connect.NewRequest(&pb.ListOrdersRequest{PageSize: 10, Status: &match}))
	if err != nil {
		t.Fatalf("list with enum filter: %%v", err)
	}
	if n := len(listed.Msg.GetOrders()); n != 1 {
		t.Fatalf("enum filter matched %%d rows, want 1", n)
	}
	miss := pb.OrderStatus_ORDER_STATUS_SHIPPED
	empty, err := svc.ListOrders(ctx, connect.NewRequest(&pb.ListOrdersRequest{PageSize: 10, Status: &miss}))
	if err != nil {
		t.Fatalf("list with non-matching enum filter: %%v", err)
	}
	if n := len(empty.Msg.GetOrders()); n != 0 {
		t.Fatalf("non-matching enum filter matched %%d rows, want 0", n)
	}
}

// TestEnumUnsetLandsTheSchemaDefault is the four-surfaces question asked of
// a RUNNING app: the CHECK does not admit the proto zero, so a Create that
// leaves the enum unset either lands the DEFAULT the migration declares or
// fails outright. proto3 gives a plain enum no presence, so "unset" here is
// indistinguishable on the wire from "explicitly UNSPECIFIED" — which is
// exactly why the schema, not the request, decides what it means.
func TestEnumUnsetLandsTheSchemaDefault(t *testing.T) {
	db := crudTestDB(t)
	svc := %[3]s.NewTest%[2]s(t, %[3]s.WithDB(db))
	ctx := crudTestCtx()

	created, err := svc.CreateOrder(ctx, connect.NewRequest(&pb.CreateOrderRequest{
		Name: "enum-unset",
	}))
	if err != nil {
		t.Fatalf("create with an unset enum: %%v", err)
	}
	got, err := svc.GetOrder(ctx, connect.NewRequest(&pb.GetOrderRequest{Id: created.Msg.GetOrder().GetId()}))
	if err != nil {
		t.Fatalf("get: %%v", err)
	}
	if s := got.Msg.GetOrder().GetStatus(); s != pb.OrderStatus_ORDER_STATUS_DRAFT {
		t.Fatalf("an unset enum stored %%v; the column DEFAULT (ORDER_STATUS_DRAFT) is what unset means", s)
	}
	// The nullable column spells unset as NULL, which reads back as the
	// proto zero — no sentinel is ever stored.
	if s := got.Msg.GetOrder().GetFallbackStatus(); s != pb.OrderStatus_ORDER_STATUS_UNSPECIFIED {
		t.Fatalf("an unset optional enum read back as %%v, want the zero (stored NULL)", s)
	}
}
`, pbImport[1], helper, pkgAlias, svcImport[1])

	path := filepath.Join(projectDir, "internal", "handlers", "orders", "enum_roundtrip_e2e_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatalf("write enum round-trip test: %v", err)
	}
}
