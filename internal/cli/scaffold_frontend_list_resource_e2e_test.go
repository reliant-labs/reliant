//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestE2EScaffoldFrontendListResource is the RED→GREEN gate for the born
// list page adopting the forge frontend runtime <Resource> container so
// filtering + pagination happen SERVER-SIDE.
//
// Scenario: a fresh `--frontend dashboard` (Next.js) project with one CRUD
// entity whose List RPC exposes server-side filter fields — a free-text
// `search` and a same-package `OrderStatus` enum `status` filter — plus
// AIP-158 page_size/page_token pagination and a List response carrying
// next_page_token + total_count. `forge generate` scaffolds the list page,
// which must:
//
//   - import + use <Resource>/useQueryResource from the runtime;
//   - pass BOTH filters to the List RPC server-side (the search box drives
//     `search`, a typed <select> drives `status`) — with NO client-side
//     items.filter and NO hard `pageSize: 200` cap;
//   - thread page_token → next_page_token cursor pagination;
//   - surface the response's total_count.
//
// Then the FULL frontend build must be green: `npm run build` + `npm test`
// + `tsc --noEmit`.
//
// RED before this change: the list page fetched one (capped) page and
// filtered it in the browser (`items.filter(...)`), silently losing every
// row past the cap — the "fetch-and-compute-in-the-browser" defect.
//
// Module wiring uses the corpus-style local replaces (addCorpusForgePkgReplace)
// like the sibling frontend runtime e2e.
func TestE2EScaffoldFrontendListResource(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "ordersapp",
		"--mod", "example.com/ordersapp",
		"--frontend", "dashboard",
	)
	projectDir := filepath.Join(dir, "ordersapp")
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "scaffold", "service", "order")
	writeFileE2E(t, filepath.Join(projectDir, "proto", "services", "order", "v1", "order.proto"), orderCRUDProtoFilters)
	writeFileE2E(t, filepath.Join(projectDir, "db", "migrations", "0001_create_orders.up.sql"), ordersTableMigration)

	runCmd(t, projectDir, forgeBin, "generate")

	appDir := filepath.Join(projectDir, "frontends", "dashboard", "src", "app")
	listPath := filepath.Join(appDir, "orders", "page.tsx")
	assertPathExistsE2E(t, listPath)
	list := readFileE2E(t, listPath)

	// ── GREEN: the page consumes the runtime container + tristate adapter,
	//    passes every filter server-side, and walks the cursor. ──
	//
	// Every content check here is layout-insensitive (containsTSE2E): the
	// pages are born through the project's own prettier (#496), which owns
	// their line breaks, brace padding and trailing commas.
	for _, want := range []string{
		`import { useQueryResource } from "@/hooks/use-query-resource";`,
		`import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";`,
		`import { OrderStatus } from "@/gen/services/order/v1/order_pb";`,
		`const resource = useQueryResource(query);`,
		`<Resource<Order>`,
		`status={resource.status}`,
		`data={resource.status === "success" ? resource.data.orders : undefined}`,
		// Filters passed SERVER-SIDE to the List RPC.
		`const query = useListOrders({`,
		`pageToken: cursor.at(-1),`,
		`search: search || undefined,`,
		`status: status,`,
		// Typed enum <select> for the status filter.
		`<option value={String(OrderStatus.SHIPPED)}>Shipped</option>`,
		// Cursor pagination + real total.
		`onNextPage={() => setCursor((c) => [...c, nextToken])}`,
		`hasNextPage={Boolean(nextToken)}`,
		`resource.data.totalCount`,
	} {
		if !containsTSE2E(list, want) {
			t.Errorf("generated list page missing %q:\n%s", want, list)
		}
	}

	// ── The defect must be gone: no browser-side filtering, no hard cap. ──
	for _, bad := range []string{
		"items.filter(",
		"pageSize: 200",
		".filter((item)",
	} {
		if containsTSE2E(list, bad) {
			t.Errorf("generated list page STILL has the client-side/compute defect %q:\n%s", bad, list)
		}
	}

	// ── Filters live in the URL, so a deep link into a filtered list works. ──
	// Held in useState instead, `/orders?status=SHIPPED` renders the
	// UNFILTERED list with no error — the dead-link defect the forensics
	// found on all four of the dogfood dashboard's `?status=` tiles.
	for _, want := range []string{
		`import { useTypedSearchParams } from "@/lib/search-schemas";`,
		`const params = useTypedSearchParams(searchParamsSchema);`,
		`const search = params.q ?? "";`,
		`const status = statusOptions.find((member) => OrderStatus[member] === params.status);`,
		`setFilters({ status: picked === undefined ? undefined : OrderStatus[picked] });`,
		// useSearchParams() outside a Suspense boundary fails `next build`.
		`<Suspense fallback={<SkeletonLoader variant="table-row" count={5} />}>`,
	} {
		if !containsTSE2E(list, want) {
			t.Errorf("generated list page missing URL-filter wiring %q:\n%s", want, list)
		}
	}
	if containsTSE2E(list, `const [search, setSearch] = useState("")`) {
		t.Errorf("list page still holds its filters in useState — deep links stay dead:\n%s", list)
	}

	// ── Foreign keys render an <EntityPicker>, never a UUID text box. ──
	createPath := filepath.Join(appDir, "orders", "new", "page.tsx")
	assertPathExistsE2E(t, createPath)
	create := readFileE2E(t, createPath)
	if containsTSE2E(create, `{...register("customerId")}`) {
		t.Errorf("create form still renders a raw text input for a foreign key:\n%s", create)
	}
	for _, want := range []string{
		`import { EntityPicker } from "@/components/entity-picker";`,
		`useList={ useListCustomers }`,
		`itemsOf={(res) => res.customers}`,
		`optionLabel={(item) => String(item.name)}`,
	} {
		if !containsTSE2E(create, want) {
			t.Errorf("create page missing %q:\n%s", want, create)
		}
	}

	editPath := filepath.Join(appDir, "orders", "edit", "page.tsx")
	assertPathExistsE2E(t, editPath)
	edit := readFileE2E(t, editPath)
	for _, want := range []string{
		`import { EntityName } from "@/components/entity-name";`,
		`useGet={ useGetCustomer }`,
		`nameOf={(res) => res.customer?.name}`,
	} {
		if !containsTSE2E(edit, want) {
			t.Errorf("edit page missing the server-loaded FK label resolver %q:\n%s", want, edit)
		}
	}

	detailPath := filepath.Join(appDir, "orders", "view", "page.tsx")
	assertPathExistsE2E(t, detailPath)
	detail := readFileE2E(t, detailPath)
	if containsTSE2E(detail, "formatValue(item.customerId)") {
		t.Errorf("detail page still prints the raw foreign-key id:\n%s", detail)
	}
	if !containsTSE2E(detail, `id={item.customerId}`) {
		t.Errorf("detail page must resolve the foreign key through <EntityName>:\n%s", detail)
	}

	// ── The generated hook tests RUN. They used to ship as
	//    `<svc>-hooks.test.tsx.starter`, inert until someone renamed them,
	//    and nobody ever did — a 17 KB suite per service that had never
	//    executed once while `forge test` reported green. ──
	hooksDir := filepath.Join(projectDir, "frontends", "dashboard", "src", "hooks")
	assertPathExistsE2E(t, filepath.Join(hooksDir, "order-service-hooks.test.tsx"))
	if _, err := os.Stat(filepath.Join(hooksDir, "order-service-hooks.test.tsx.starter")); err == nil {
		t.Error("forge still emits an inert .starter test beside the live one")
	}

	// ── The build gate: real toolchain must be green. ──
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	// A bare `return` here used to drop the entire npm gate — install,
	// build, vitest, tsc — and still report PASS. requireTool makes the
	// same absence a hard failure in CI and a named skip on a laptop.
	requireTool(t, "node", "npm")

	webDir := filepath.Join(projectDir, "frontends", "dashboard")
	// The install resolves @reliantlabs/forge-web-runtime over a file: link
	// into the shared repo checkout and runs its prepare script there; build
	// it once up front so parallel tests do not race that bootstrap.
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")
	runCmdTimeout(t, webDir, 5*time.Minute, "npm", "run", "build")
	runCmdTimeout(t, webDir, 5*time.Minute, "npm", "test")
	runCmdTimeout(t, webDir, 2*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
}

// orderCRUDProtoFilters is a CRUD-entity proto whose List RPC declares
// server-side filter fields (a free-text `search` and a same-package
// `OrderStatus` enum filter) plus AIP-158 pagination, with a List response
// carrying next_page_token + total_count.
const orderCRUDProtoFilters = `syntax = "proto3";

package services.order.v1;

import "forge/v1/forge.proto";
import "google/protobuf/field_mask.proto";
import "google/protobuf/timestamp.proto";

option go_package = "example.com/ordersapp/gen/services/order/v1;orderv1";

// OrderService defines the order service RPCs.
service OrderService {
  option (forge.v1.service) = {
    name: "OrderService"
    version: "1.0.0"
    description: "order service"
  };

  // GetOrder retrieves an order by ID.
  rpc GetOrder(GetOrderRequest) returns (GetOrderResponse) {}

  // ListOrders returns a filtered, paginated list of orders.
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse) {}

  // CreateOrder creates a new order.
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse) {}

  // UpdateOrder updates an existing order.
  rpc UpdateOrder(UpdateOrderRequest) returns (UpdateOrderResponse) {}

  // GetCustomer retrieves a customer by ID.
  rpc GetCustomer(GetCustomerRequest) returns (GetCustomerResponse) {}

  // ListCustomers returns a paginated list of customers.
  rpc ListCustomers(ListCustomersRequest) returns (ListCustomersResponse) {}
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PENDING = 1;
  ORDER_STATUS_SHIPPED = 2;
  ORDER_STATUS_CANCELLED = 3;
}

// Order represents an order entity. customer_id is a FOREIGN KEY onto
// Customer -- the ordinary relational shape, and the one whose generated form
// used to render a raw text input for a UUID.
message Order {
  string id = 1;
  string customer_id = 2;
  OrderStatus status = 3;
  int64 amount_cents = 4;
  google.protobuf.Timestamp created_at = 5;
  string note = 6;
}

// Customer is the entity Order.customer_id points at.
message Customer {
  string id = 1;
  string name = 2;
}

message GetCustomerRequest {
  string id = 1;
}

message GetCustomerResponse {
  Customer customer = 1;
}

message ListCustomersRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional string search = 3;
}

message ListCustomersResponse {
  repeated Customer customers = 1;
  string next_page_token = 2;
  int32 total_count = 3;
}

message GetOrderRequest {
  string id = 1;
}

message GetOrderResponse {
  Order order = 1;
}

message ListOrdersRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional string search = 3;
  optional OrderStatus status = 4;
  string order_by = 5;
  bool descending = 6;
}

message ListOrdersResponse {
  repeated Order orders = 1;
  string next_page_token = 2;
  int32 total_count = 3;
}

message CreateOrderRequest {
  string customer_id = 1;
  int64 amount_cents = 2;
  string note = 3;
}

message CreateOrderResponse {
  Order order = 1;
}

message UpdateOrderRequest {
  Order order = 1;
  google.protobuf.FieldMask update_mask = 2;
}

message UpdateOrderResponse {
  Order order = 1;
}
`

// ordersTableMigration backs the Order entity with a real table — the
// search filter spans the string columns (customer) and the status filter
// names the status column, so `classifyEntityFilterField` clears both.
const ordersTableMigration = `CREATE TABLE customers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL REFERENCES customers(id),
    status TEXT NOT NULL DEFAULT 'ORDER_STATUS_UNSPECIFIED',
    amount_cents BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    note TEXT NOT NULL DEFAULT ''
);
`
