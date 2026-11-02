package codegen

import (
	"strings"
	"testing"
)

// listFilterSvcForTest builds a ServiceDef shaped like the REAL `forge
// generate` descriptor for an entity whose List RPC exposes server-side
// filter fields — the same fields the backend CRUD op filters on:
//
//   - a free-text `search` filter (backend: ILIKE across string columns),
//   - a same-package `OrderStatus` enum filter,
//   - an `optional bool expedited` filter,
//   - AIP-158 page_size/page_token pagination,
//   - a List response carrying next_page_token + total_count.
//
// The List request lives in the deep Schemas map (keyed by FQ name) so the
// enum filter's type name is available for the typed <select> projection;
// the List RESPONSE's cursor/count fields are read from the shallow Messages
// map (the same source crudMethodFacts uses to detect total_count).
func listFilterSvcForTest() ServiceDef {
	return ServiceDef{
		Name:      "OrderService",
		Package:   "services.orders.v1",
		ProtoFile: "proto/services/orders/v1/orders.proto",
		Methods: []Method{
			{Name: "ListOrders", InputType: "ListOrdersRequest", OutputType: "ListOrdersResponse",
				InputTypeFQ: "services.orders.v1.ListOrdersRequest"},
			{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"},
			{Name: "CreateOrder", InputType: "CreateOrderRequest", OutputType: "CreateOrderResponse",
				InputTypeFQ: "services.orders.v1.CreateOrderRequest"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListOrdersResponse": {
				{Name: "orders", ProtoType: "[]message", MessageType: "services.orders.v1.Order"},
				{Name: "next_page_token", ProtoType: "string"},
				{Name: "total_count", ProtoType: "int32"},
			},
			"CreateOrderRequest": {
				{Name: "customer", ProtoType: "string"},
				{Name: "amount_cents", ProtoType: "int64"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"services.orders.v1.ListOrdersRequest": {
				{Name: "page_size", Kind: "int32"},
				{Name: "page_token", Kind: "string"},
				{Name: "search", Kind: "string", Optional: true},
				{Name: "status", Kind: "enum", TypeName: "services.orders.v1.OrderStatus", Optional: true},
				{Name: "expedited", Kind: "bool", Optional: true},
				{Name: "order_by", Kind: "string"},
				{Name: "descending", Kind: "bool"},
			},
			"services.orders.v1.CreateOrderRequest": {
				{Name: "customer", Kind: "string"},
				{Name: "amount_cents", Kind: "int64"},
			},
			"services.orders.v1.Order": {
				{Name: "id", Kind: "string"},
				{Name: "customer", Kind: "string"},
				{Name: "status", Kind: "enum", TypeName: "services.orders.v1.OrderStatus"},
				{Name: "amount_cents", Kind: "int64"},
			},
		},
		SchemaFiles: map[string]string{
			"services.orders.v1.ListOrdersRequest":  "proto/services/orders/v1/orders.proto",
			"services.orders.v1.CreateOrderRequest": "proto/services/orders/v1/orders.proto",
			"services.orders.v1.Order":              "proto/services/orders/v1/orders.proto",
		},
		Enums: map[string][]string{
			"services.orders.v1.OrderStatus": {
				"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_PENDING", "ORDER_STATUS_SHIPPED",
			},
		},
	}
}

func listFilterPageForTest(t *testing.T) PageTemplateData {
	t.Helper()
	svc := listFilterSvcForTest()
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    "Order",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "customer", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: FieldKindEnum, MessageType: "services.orders.v1.OrderStatus"},
			{Name: "amount_cents", ProtoType: "int64", Kind: FieldKindScalar},
		},
	}, svc)
	return page
}

// TestAttachListMeta_ServerSideFilterProjection pins the projection of the
// List RPC's server-side filter + pagination shape onto the list page: the
// search filter drives the <Resource> box, enum/bool filters become discrete
// controls, page_token+next_page_token wire the cursor, and total_count is
// surfaced. This is what lets the born page filter/paginate SERVER-SIDE
// instead of fetching one capped page and filtering it in the browser.
func TestAttachListMeta_ServerSideFilterProjection(t *testing.T) {
	page := listFilterPageForTest(t)

	if page.SearchFilterField != "search" {
		t.Errorf("SearchFilterField = %q, want %q", page.SearchFilterField, "search")
	}
	if !page.HasCursorPagination {
		t.Errorf("HasCursorPagination = false, want true (page_token + next_page_token both present)")
	}
	if !page.HasPageSize {
		t.Errorf("HasPageSize = false, want true")
	}
	if page.NextTokenField != "nextPageToken" {
		t.Errorf("NextTokenField = %q, want %q", page.NextTokenField, "nextPageToken")
	}
	if !page.HasTotalCount || page.TotalCountField != "totalCount" {
		t.Errorf("HasTotalCount=%v TotalCountField=%q, want true/%q", page.HasTotalCount, page.TotalCountField, "totalCount")
	}

	// Exact (non-search) filters, in proto field order: status (enum) then
	// expedited (bool). order_by / descending / page_* are NOT filters.
	if len(page.ExactFilterFields) != 2 {
		t.Fatalf("ExactFilterFields = %+v, want [status(enum), expedited(bool)]", page.ExactFilterFields)
	}
	status := page.ExactFilterFields[0]
	if status.Name != "status" || status.NamePascal != "Status" || status.Kind != "enum" {
		t.Errorf("filter[0] = %+v, want status/Status/enum", status)
	}
	if status.EnumType != "OrderStatus" || status.EnumImport != "@/gen/services/orders/v1/orders_pb" {
		t.Errorf("filter[0] enum = %q from %q, want OrderStatus / @/gen/services/orders/v1/orders_pb", status.EnumType, status.EnumImport)
	}
	// The real members only: filtering on the zero matches no row, because
	// the born CHECK never admits it.
	wantVals := []PageEnumValue{
		{Ref: "OrderStatus.PENDING", Label: "Pending"},
		{Ref: "OrderStatus.SHIPPED", Label: "Shipped"},
	}
	if len(status.EnumValues) != len(wantVals) {
		t.Fatalf("filter[0] EnumValues = %+v, want %+v", status.EnumValues, wantVals)
	}
	for i, w := range wantVals {
		if status.EnumValues[i] != w {
			t.Errorf("filter[0] EnumValues[%d] = %+v, want %+v", i, status.EnumValues[i], w)
		}
	}
	expedited := page.ExactFilterFields[1]
	if expedited.Name != "expedited" || expedited.NamePascal != "Expedited" || expedited.Kind != "bool" {
		t.Errorf("filter[1] = %+v, want expedited/Expedited/bool", expedited)
	}

	if len(page.ListFilterEnumImports) != 1 ||
		page.ListFilterEnumImports[0].Path != "@/gen/services/orders/v1/orders_pb" ||
		len(page.ListFilterEnumImports[0].Types) != 1 ||
		page.ListFilterEnumImports[0].Types[0] != "OrderStatus" {
		t.Errorf("ListFilterEnumImports = %+v, want one OrderStatus import", page.ListFilterEnumImports)
	}
}

// TestListPage_RendersServerSideResource is the GREEN half: the rendered
// Next.js list page consumes the runtime <Resource>/useQueryResource,
// passes every filter SERVER-SIDE to the List RPC, and walks the cursor —
// with NO client-side items.filter and NO hard page cap.
func TestListPage_RendersServerSideResource(t *testing.T) {
	page := listFilterPageForTest(t)
	out := renderPageTemplate(t, "pages", "list-page.tsx.tmpl", page)

	for _, want := range []string{
		// Adopts the runtime container + tristate adapter.
		`import { useQueryResource } from "@/hooks/use-query-resource";`,
		`import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";`,
		`const resource = useQueryResource(query);`,
		`<Resource<Order>`,
		`status={resource.status}`,
		`data={resource.status === "success" ? resource.data.orders : undefined}`,
		// Enum filter imported + rendered as a typed <select>.
		`import { OrderStatus } from "@/gen/services/orders/v1/orders_pb";`,
		`<option value={String(OrderStatus.SHIPPED)}>Shipped</option>`,
		// Filter state lives in the URL, so a dashboard tile linking to
		// `?status=SHIPPED` lands on a FILTERED list. The token is the enum
		// MEMBER NAME, not its ordinal.
		`const statusOptions = [`,
		`const status = statusOptions.find((member) => OrderStatus[member] === params.status);`,
		`setFilters({ status: picked === undefined ? undefined : OrderStatus[picked] });`,
		`const params = useTypedSearchParams(searchParamsSchema);`,
		// Every filter passed to the List RPC (server-side).
		`const query = useListOrders({`,
		`pageSize: PAGE_SIZE,`,
		`pageToken: cursor.at(-1),`,
		`search: search || undefined,`,
		`status: status,`,
		`expedited: expedited,`,
		// Cursor pagination + real total.
		`onNextPage={() => setCursor((c) => [...c, nextToken])}`,
		`hasNextPage={Boolean(nextToken)}`,
		`resource.data.totalCount`,
		// <Resource>'s debounced search box is wired to the search filter.
		`filter={search}`,
		`onFilterChange={(value) => {`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered list page missing %q:\n%s", want, out)
		}
	}

	// The defect must be gone: no browser-side filtering, no hard page cap.
	for _, bad := range []string{
		"items.filter(",
		"pageSize: 200",
		".filter((item)",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("rendered list page still has the client-side/compute defect %q:\n%s", bad, out)
		}
	}
}

// TestViteSpaListPage_ServerSideResource pins the Vite SPA list page. The
// forge runtime is now framework-agnostic and emitted for Vite too, so the
// SPA adopts the SAME <Resource>/useQueryResource container as Next.js (no
// parallel, drift-prone table impl). Filtering + pagination stay server-side,
// navigation goes through tanstack-router, and there is NO client-side
// items.filter and NO hard page cap.
func TestViteSpaListPage_ServerSideResource(t *testing.T) {
	page := listFilterPageForTest(t)
	out := renderPageTemplate(t, "vite-spa-pages", "list-page.tsx.tmpl", page)

	for _, want := range []string{
		// Adopts the runtime container + tristate adapter.
		`import { useQueryResource } from "@/hooks/use-query-resource";`,
		`import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";`,
		`const resource = useQueryResource(query);`,
		`<Resource<Order>`,
		// Enum filter imported + rendered as a typed <select>.
		`import { OrderStatus } from "@/gen/services/orders/v1/orders_pb";`,
		`(Number(v) as OrderStatus)`,
		// Every filter passed to the List RPC (server-side), with the prior
		// page held while the next loads.
		`const query = useListOrders({`,
		`pageSize: PAGE_SIZE,`,
		`pageToken: cursor.at(-1),`,
		`search: search || undefined,`,
		`status: status,`,
		`expedited: expedited,`,
		`}, { placeholderData: keepPreviousData });`,
		// Real total + cursor pagination.
		`resource.data.totalCount`,
		`onNextPage={() => setCursor((c) => [...c, nextToken])}`,
		// Navigation goes through tanstack-router (typed route + params).
		`import { Link, useNavigate } from "@tanstack/react-router";`,
		"void navigate({ to: `/orders/$id`, params: { id: String(item.id) } })",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("vite-spa list page missing %q:\n%s", want, out)
		}
	}
	// The defect + the drifted parallel impl must be gone.
	for _, bad := range []string{
		"items.filter(",
		"pageSize: 200",
		// The old hand-rolled primitives / URL-state search must not return.
		`from "@/components/ui/table"`,
		"useSearch",
		"data?.nextPageToken",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("vite-spa list page still has %q:\n%s", bad, out)
		}
	}
}

// TestListPage_DegradesWithoutFilters pins graceful degradation: an entity
// whose List RPC exposes NO filter fields still adopts <Resource> (tristate
// + cursor pagination) — it just renders no filter controls and no search
// box, and never fabricates a filter.
func TestListPage_DegradesWithoutFilters(t *testing.T) {
	svc := ServiceDef{
		Name:      "WidgetService",
		Package:   "services.widgets.v1",
		ProtoFile: "proto/services/widgets/v1/widgets.proto",
		Methods: []Method{
			{Name: "ListWidgets", InputType: "ListWidgetsRequest", OutputType: "ListWidgetsResponse",
				InputTypeFQ: "services.widgets.v1.ListWidgetsRequest"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListWidgetsResponse": {
				{Name: "widgets", ProtoType: "[]message", MessageType: "services.widgets.v1.Widget"},
				{Name: "next_page_token", ProtoType: "string"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"services.widgets.v1.ListWidgetsRequest": {
				{Name: "page_size", Kind: "int32"},
				{Name: "page_token", Kind: "string"},
			},
			// The deep graph always holds the entity a List response
			// references; without it, ListWidgets is a custom rpc.
			"services.widgets.v1.Widget": {
				{Name: "id", Kind: "string"},
				{Name: "name", Kind: "string"},
			},
		},
	}
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    "Widget",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "name", ProtoType: "string", Kind: FieldKindScalar},
		},
	}, svc)

	if page.SearchFilterField != "" || len(page.ExactFilterFields) != 0 {
		t.Errorf("no filter fields expected, got search=%q exact=%+v", page.SearchFilterField, page.ExactFilterFields)
	}
	if !page.HasCursorPagination {
		t.Errorf("HasCursorPagination = false, want true (page_token + next_page_token present)")
	}
	if page.HasTotalCount {
		t.Errorf("HasTotalCount = true, want false (response declares no total_count)")
	}

	out := renderPageTemplate(t, "pages", "list-page.tsx.tmpl", page)
	for _, want := range []string{
		`import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";`,
		`<Resource<Widget>`,
		`onNextPage={() => setCursor((c) => [...c, nextToken])}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded list page missing %q:\n%s", want, out)
		}
	}
	// No filter box, no filter <select>, no total badge.
	for _, bad := range []string{
		"onFilterChange=",
		"<select",
		"resource.data.totalCount",
		"items.filter(",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("degraded list page unexpectedly has %q:\n%s", bad, out)
		}
	}
}

// A LIST page and a DETAIL page want different column sets. The detail view
// answers "everything about this record", so the id and the audit timestamps
// belong there. A table answers "which record do I want", and those three
// columns are pure noise in it — the row is already a link to the detail page.
//
// Measured: a 15-entity app generated 12 columns per list, all 30 of its CRUD
// pages were rewritten by hand, and every rewrite dropped exactly these.
func TestAttachEntityMeta_ListOmitsIdentityAndAuditColumns(t *testing.T) {
	svc := listFilterSvcForTest()
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    "Order",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "customer", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "created_at", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "updated_at", ProtoType: "string", Kind: FieldKindScalar},
		},
	}, svc)

	has := func(cols []EntityPageField, name string) bool {
		for _, c := range cols {
			if c.Name == name {
				return true
			}
		}
		return false
	}

	for _, omitted := range []string{"id", "createdAt", "updatedAt"} {
		if has(page.ListColumns, omitted) {
			t.Errorf("list columns must omit %q — it is machinery in a table, not data", omitted)
		}
		if !has(page.Columns, omitted) {
			t.Errorf("detail columns must KEEP %q — the detail page is where it is a real question", omitted)
		}
	}
	if !has(page.ListColumns, "customer") {
		t.Error("a real data column must survive the exclusion")
	}
}

// `billing_state` is a US state — free text with fifty values — and the badge
// heuristic matched it on the SUBSTRING "state", handing a postal abbreviation
// to StatusBadge with no enum behind it. A false badge asserts a closed set
// that does not exist, and has to be hand-corrected out of the born page; a
// missed badge merely renders the text the field already was.
func TestIsEnumLikeFieldName_MatchesWholeSegmentsNotSubstrings(t *testing.T) {
	for _, name := range []string{"status", "job_state", "payment_status", "lead_type", "user_role"} {
		if !isEnumLikeFieldName(name) {
			t.Errorf("%q names a closed value set and must badge", name)
		}
	}
	for _, name := range []string{"real_estate_id", "statement_id", "prototype_note", "typeahead_hint"} {
		if isEnumLikeFieldName(name) {
			t.Errorf("%q only CONTAINS an enum-ish fragment — badging it asserts a value set that does not exist", name)
		}
	}
}

// KNOWN GAP, pinned deliberately: `billing_state` is a US state — free text
// with fifty values — and it still badges, because "state" IS one of its
// segments and a segment match cannot tell it from `job_state`.
//
// The signal that separates them is not in the name at all: the proto declares
// it `string billing_state = 7 [(buf.validate.field).string.max_len = 2]`, and
// a closed set would have been declared an enum. EntityField carries no
// validate-rule metadata today, so the checker cannot see it.
//
// This test documents the boundary rather than asserting the wrong thing. When
// max_len (or any protovalidate rule) reaches EntityField, tighten
// isEnumLikeFieldName to treat a length-capped string as free text and flip
// this expectation.
func TestIsEnumLikeFieldName_KnownGap_AddressStateStillBadges(t *testing.T) {
	if !isEnumLikeFieldName("billing_state") {
		t.Skip("billing_state no longer badges — the gap is closed; fold this into the negative table above")
	}
}
