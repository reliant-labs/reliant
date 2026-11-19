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

// TestE2EScaffoldReadOnlyFieldRefusedOnClientUpdate is the end-to-end
// data-integrity gate for `// forge:read-only` and `// forge:computed` on the
// UPDATE path, proven at RUNTIME against real postgres through the executed
// generated stack (handlers -> pkg/crud -> internal/db -> database).
//
// Both markers promise "readable, not client-writable", and the born Create
// request honours that by omitting the field. The AIP-134 Update request,
// though, wraps the WHOLE entity, and the generated CRUD Update used to write
// it all back:
//
//   - a maskless full replace from a client that simply did not echo a
//     read-only field reset it — an unset enum to the column DEFAULT, a
//     timestamp to NULL, a computed total to 0 — with no error and no log;
//   - the same request carrying a value set a server-owned column directly,
//     bypassing the custom RPC that owns that state machine;
//   - an update_mask naming the column (`paths: ["status"]`) was accepted.
//
// This gate pins the fixed semantics:
//
//   - the repo-level masked write — the path a custom RPC uses — still
//     writes the read-only columns;
//   - a maskless client Update leaves them exactly as stored, whatever the
//     request carried, and the response reports the STORED values rather
//     than echoing the request;
//   - a client update_mask naming one is InvalidArgument (reason
//     unknown_field), and nothing is written;
//   - a client update_mask naming only editable columns still works.
//
// The row is read DIRECTLY, bypassing every forge layer, to observe the true
// stored values.
func TestE2EScaffoldReadOnlyFieldRefusedOnClientUpdate(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "roapp", "--mod", "example.com/roapp", "--service", "orders")
	projectDir := filepath.Join(dir, "roapp")
	addCorpusForgePkgReplace(t, projectDir)

	// An Order whose lifecycle is server-owned: an enum status (the unset
	// enum is the case that silently became the column DEFAULT), a nullable
	// timestamp (the case that silently became NULL), and a computed total
	// (the case that silently became 0).
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PENDING = 1;
  ORDER_STATUS_SHIPPED = 2;
}

// forge:entity
message Order {
  string id = 1;
  string customer = 2;
  int64 amount_cents = 3;
  OrderStatus status = 4;  // forge:read-only
  google.protobuf.Timestamp shipped_at = 5;  // forge:read-only
  int64 discount_cents = 6;  // forge:computed
  google.protobuf.Timestamp created_at = 7;
  google.protobuf.Timestamp updated_at = 8;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// The born Create request omits all three (unchanged behaviour, and the
	// premise of everything below).
	genProto := readFileE2E(t, protoPath)
	createReq := sliceBetweenCLI(genProto, "message CreateOrderRequest {", "}")
	for _, field := range []string{"status", "shipped_at", "discount_cents"} {
		if regexp.MustCompile(`\b` + field + `\b`).MatchString(createReq) {
			t.Errorf("CreateOrderRequest must omit the read-only field %q:\n%s", field, createReq)
		}
	}

	writeReadOnlyUpdateTest(t, projectDir)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "TestReadOnlyColumnsOnClientUpdate", "./internal/handlers/orders/")
}

// writeReadOnlyUpdateTest injects a test into the generated app that reuses
// the born harness helpers (crudTestDB, crudTestCtx — same package) and drives
// the real generated handlers.
func writeReadOnlyUpdateTest(t *testing.T, projectDir string) {
	t.Helper()
	born := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_test.go"))
	m := regexp.MustCompile(`(\w+)\.NewTest(\w+)\(`).FindStringSubmatch(born)
	if m == nil {
		t.Fatalf("born handlers_crud_test.go carries no <pkg>.NewTest<X> helper call:\n%s", born)
	}
	pkgAlias, helper := m[1], m[2]
	pbImport := regexp.MustCompile(`pb "([^"]+)"`).FindStringSubmatch(born)
	if pbImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no pb import:\n%s", born)
	}
	svcImport := regexp.MustCompile(pkgAlias + ` "([^"]+)"`).FindStringSubmatch(born)
	if svcImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no %s handler-package import:\n%s", pkgAlias, born)
	}
	dbImport := strings.TrimSuffix(svcImport[1], "/handlers/orders") + "/db"

	test := fmt.Sprintf(`package orders_test

// Written by forge's read-only update e2e
// (TestE2EScaffoldReadOnlyFieldRefusedOnClientUpdate).

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "%[1]s"
	dbpkg "%[2]s"
	%[3]s "%[4]s"
)

func TestReadOnlyColumnsOnClientUpdate(t *testing.T) {
	db := crudTestDB(t)
	svc := %[3]s.NewTest%[5]s(t, %[3]s.WithDB(db))
	ctx := crudTestCtx()

	type row struct {
		customer  string
		status    string
		shippedAt sql.NullTime
		discount  int64
	}
	read := func(id string) row {
		t.Helper()
		var r row
		if err := db.QueryRow(ctx,
			"SELECT customer, status, shipped_at, discount_cents FROM orders WHERE id = $1", id,
		).Scan(&r.customer, &r.status, &r.shippedAt, &r.discount); err != nil {
			t.Fatalf("read order row %%s: %%v", id, err)
		}
		return r
	}

	created, err := svc.CreateOrder(ctx, connect.NewRequest(&pb.CreateOrderRequest{
		Customer:    "acme",
		AmountCents: 1000,
	}))
	if err != nil {
		t.Fatalf("create order: %%v", err)
	}
	id := created.Msg.GetOrder().GetId()

	// ── app code owns the lifecycle: the repo-level masked write lands ──
	shippedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := dbpkg.UpdateOrderMasked(ctx, db, &dbpkg.Order{
		ID:            id,
		Status:        "ORDER_STATUS_SHIPPED",
		ShippedAt:     &shippedAt,
		DiscountCents: 25,
	}, []string{"status", "shipped_at", "discount_cents"}); err != nil {
		t.Fatalf("repo-level masked write of the read-only columns (the custom-RPC path): %%v", err)
	}
	stored := read(id)
	if stored.status != "ORDER_STATUS_SHIPPED" || !stored.shippedAt.Valid || stored.discount != 25 {
		t.Fatalf("repo-level masked write did not land: %%+v", stored)
	}
	wantStored := func(label string) {
		t.Helper()
		got := read(id)
		if got.status != "ORDER_STATUS_SHIPPED" {
			t.Errorf("%%s: status = %%q, want ORDER_STATUS_SHIPPED — a client Update wrote a read-only column", label, got.status)
		}
		if !got.shippedAt.Valid || !got.shippedAt.Time.Equal(shippedAt) {
			t.Errorf("%%s: shipped_at = %%v, want %%v — a client Update wrote a read-only column", label, got.shippedAt, shippedAt)
		}
		if got.discount != 25 {
			t.Errorf("%%s: discount_cents = %%d, want 25 — a client Update wrote a computed column", label, got.discount)
		}
	}

	// ── maskless full replace from a client that does not echo them ──
	resp, err := svc.UpdateOrder(ctx, connect.NewRequest(&pb.UpdateOrderRequest{
		Order: &pb.Order{Id: id, Customer: "acme-renamed", AmountCents: 2000},
	}))
	if err != nil {
		t.Fatalf("maskless update: %%v", err)
	}
	wantStored("maskless update omitting the read-only fields")
	if got := read(id).customer; got != "acme-renamed" {
		t.Errorf("maskless update did not write the editable column: customer = %%q", got)
	}
	if got := resp.Msg.GetOrder().GetStatus(); got != pb.OrderStatus_ORDER_STATUS_SHIPPED {
		t.Errorf("maskless update response status = %%v, want the STORED ORDER_STATUS_SHIPPED, not the request's", got)
	}
	if got := resp.Msg.GetOrder().GetDiscountCents(); got != 25 {
		t.Errorf("maskless update response discount_cents = %%d, want the STORED 25, not the request's", got)
	}

	// ── maskless full replace that tries to set them ──
	if _, err := svc.UpdateOrder(ctx, connect.NewRequest(&pb.UpdateOrderRequest{
		Order: &pb.Order{
			Id: id, Customer: "acme-renamed", AmountCents: 2000,
			Status: pb.OrderStatus_ORDER_STATUS_PENDING, DiscountCents: 999,
		},
	})); err != nil {
		t.Fatalf("maskless update carrying read-only values: %%v", err)
	}
	wantStored("maskless update carrying read-only values")

	// ── a client update_mask naming a read-only or computed column ──
	for _, path := range []string{"status", "shipped_at", "discount_cents"} {
		_, err := svc.UpdateOrder(ctx, connect.NewRequest(&pb.UpdateOrderRequest{
			Order:      &pb.Order{Id: id, Status: pb.OrderStatus_ORDER_STATUS_PENDING, DiscountCents: 999},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{path}},
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("update_mask [%%q]: code = %%v (err %%v), want InvalidArgument", path, connect.CodeOf(err), err)
			continue
		}
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Meta().Get(svcerr.ReasonHeader) != crud.ReasonUnknownField {
			t.Errorf("update_mask [%%q]: want reason %%q, got error %%v", path, crud.ReasonUnknownField, err)
		}
	}
	wantStored("masked update naming a read-only column")

	// ── a client update_mask naming only editable columns still works ──
	resp, err = svc.UpdateOrder(ctx, connect.NewRequest(&pb.UpdateOrderRequest{
		Order:      &pb.Order{Id: id, Customer: "masked", Status: pb.OrderStatus_ORDER_STATUS_PENDING},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"customer"}},
	}))
	if err != nil {
		t.Fatalf("masked update of an editable column: %%v", err)
	}
	if got := read(id).customer; got != "masked" {
		t.Errorf("masked update did not write customer: %%q", got)
	}
	wantStored("masked update of an editable column")
	if got := resp.Msg.GetOrder().GetStatus(); got != pb.OrderStatus_ORDER_STATUS_SHIPPED {
		t.Errorf("masked update response status = %%v, want the STORED ORDER_STATUS_SHIPPED, not the request's", got)
	}
}
`, pbImport[1], dbImport, pkgAlias, svcImport[1], helper)

	path := filepath.Join(projectDir, "internal", "handlers", "orders", "read_only_update_e2e_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatalf("write read-only update test: %v", err)
	}
}
