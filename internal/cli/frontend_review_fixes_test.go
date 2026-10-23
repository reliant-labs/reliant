package cli

// Render-level tests for the frontend generated-code review fixes.
// These pin template OUTPUT (string/parse assertions on rendered TS), not
// the live npm toolchain — the fast loop the velocity rules require.

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// renderHooksForTest renders hooks.ts.tmpl for a 3-RPC CRUD-ish service.
func renderHooksForTest(t *testing.T, workspaces bool) string {
	t.Helper()
	svc := codegen.ServiceDef{
		Name:      "TaskService",
		Package:   "demo.v1",
		ProtoFile: "proto/services/tasks/v1/tasks.proto",
		Methods: []codegen.Method{
			{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"},
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"},
			{Name: "CreateTask", InputType: "CreateTaskRequest", OutputType: "CreateTaskResponse"},
			{Name: "SendReport", InputType: "SendReportRequest", OutputType: "SendReportResponse"},
		},
	}
	data := codegen.ServiceDefToHookData(svc)
	data.Workspaces = workspaces
	if workspaces {
		data.APIPackage = "@demo/api"
	}

	content, err := templates.FrontendTemplates().Get("hooks.ts.tmpl")
	if err != nil {
		t.Fatalf("read hooks template: %v", err)
	}
	tmpl, err := template.New("hooks.ts.tmpl").Funcs(templates.FuncMap()).Parse(string(content))
	if err != nil {
		t.Fatalf("parse hooks template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render hooks template: %v", err)
	}
	return buf.String()
}

// TestFrontendLintConfigConsistency is the cheap, string-level guard for
// F12: the emitted toolchain config must keep the strictness knobs the
// generated code is written against. The full `npm run lint` /
// `tsc --noEmit` pass over a scaffolded project is the (slow) end-to-end
// verification; this test catches config regressions in milliseconds.
func TestFrontendLintConfigConsistency(t *testing.T) {
	// tsconfig: noUncheckedIndexedAccess must be on — generated code is
	// written to satisfy it (typed Records, guarded index access).
	tsconfig, err := templates.FrontendTemplates().Render("nextjs/tsconfig.json.tmpl", templates.FrontendTemplateData{})
	if err != nil {
		t.Fatalf("render tsconfig: %v", err)
	}
	if !strings.Contains(string(tsconfig), `"noUncheckedIndexedAccess": true`) {
		t.Errorf("nextjs tsconfig must enable noUncheckedIndexedAccess:\n%s", tsconfig)
	}

	// eslint config: alias classification + default-export scoping that
	// the generated files rely on for a zero-warning pristine lint.
	eslintCfg, err := templates.FrontendTemplates().Get("nextjs/eslint.config.mjs")
	if err != nil {
		t.Fatalf("read eslint config: %v", err)
	}
	cfg := string(eslintCfg)
	for _, want := range []string{
		`"import/internal-regex": "^@/"`,
		`"src/app/**/*.{ts,tsx}"`,
		`"src/components/ui/**/*.{ts,tsx}"`,
		`"src/mocks/scenarios/**/*.{ts,tsx}"`,
		`"import/no-default-export": "off"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("nextjs eslint config missing %q", want)
		}
	}

	// Generated hooks: type-only imports must come last (the import/order
	// "type" group) and value imports first — string-level proxy for the
	// import/order rule on the highest-traffic generated file.
	hooks := renderHooksForTest(t, false)
	lastValueImport := strings.LastIndex(hooks, "\nimport { ")
	firstTypeImport := strings.Index(hooks, "\nimport type { ")
	if firstTypeImport >= 0 && lastValueImport > firstTypeImport {
		t.Errorf("hooks file has a value import after a type import (violates import/order type-group-last):\n%s", hooks)
	}

	// format-utils: the badge variant map must be explicit — no hash-to-color
	// scheme — and REGISTRABLE, with a substring-inference + neutral fallback
	// so domain statuses don't fall through to grey. enumLabel strips the enum
	// TYPE prefix so labels read "Payment Captured", not "Order Status …".
	fu, err := templates.FrontendTemplates().Get("shared/src/lib/format-utils.ts")
	if err != nil {
		t.Fatalf("read format-utils: %v", err)
	}
	if strings.Contains(string(fu), "charCodeAt") {
		t.Errorf("format-utils still hashes enum values to badge colors")
	}
	for _, want := range []string{
		"export function registerStatusVariants", // domain-status extension seam
		`return "neutral"`,                       // unrecognized statuses fall through to grey
		"export function enumLabel",              // type-prefix-stripping label helper
	} {
		if !strings.Contains(string(fu), want) {
			t.Errorf("format-utils missing %q", want)
		}
	}
	// userMessage/stripServerFraming moved to @reliantlabs/forge-web-runtime, next
	// to normalizeError, which strips the identical backend framing — the two
	// copies used to carry comments promising to keep each other in sync, and
	// the scaffold-once copy could never be corrected once a project shipped.
	for _, gone := range []string{"export function userMessage", "export function stripServerFraming"} {
		if strings.Contains(string(fu), gone) {
			t.Errorf("format-utils re-declares %q — error framing is a wire contract owned by @reliantlabs/forge-web-runtime, not a per-project scaffold", gone)
		}
	}
	// inferVariant substring-matched status strings as a last-resort semantic
	// guess, and it inverted on the commonest English negation prefix:
	// "inactive" CONTAINS "active", so a retired record rendered success-green.
	// Same for incomplete/disabled/unhealthy/unverified/disconnected. This
	// assertion used to REQUIRE the function — the bug was pinned by the test
	// meant to protect the file. Unrecognized statuses now fall through to
	// neutral: grey says "nobody declared this" and is recoverable, green says
	// "this is fine" and is a lie.
	if strings.Contains(string(fu), "function inferVariant") {
		t.Errorf("format-utils re-introduces inferVariant — status colour is an exact-match lookup (registerStatusVariants) or neutral, never a substring guess")
	}

	// query-client: the single error-toast chokepoint with typed opt-out.
	qc, err := templates.FrontendTemplates().Get("shared/src/lib/query-client.ts")
	if err != nil {
		t.Fatalf("read query-client: %v", err)
	}
	for _, want := range []string{"MutationCache", "silenceErrorToast", "emitToast({ message: userMessage(error)"} {
		if !strings.Contains(string(qc), want) {
			t.Errorf("query-client missing %q", want)
		}
	}
}

// crudPageDataForTest builds a fully-enriched PageTemplateData (CRUD RPCs +
// entity field metadata) the way generateFrontendPages does.
func crudPageDataForTest(t *testing.T) codegen.PageTemplateData {
	t.Helper()
	svc := codegen.ServiceDef{
		Name:      "TaskService",
		Package:   "demo.v1",
		ProtoFile: "proto/services/tasks/v1/tasks.proto",
		Methods: []codegen.Method{
			{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"},
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"},
			{Name: "CreateTask", InputType: "CreateTaskRequest", OutputType: "CreateTaskResponse"},
			{Name: "UpdateTask", InputType: "UpdateTaskRequest", OutputType: "UpdateTaskResponse"},
			{Name: "DeleteTask", InputType: "DeleteTaskRequest", OutputType: "DeleteTaskResponse"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"CreateTaskRequest": {
				{Name: "title", ProtoType: "string"},
				{Name: "status", ProtoType: "string"},
				{Name: "done", ProtoType: "bool"},
			},
			"UpdateTaskRequest": {
				{Name: "id", ProtoType: "string"},
				{Name: "title", ProtoType: "string"},
				{Name: "status", ProtoType: "string"},
				{Name: "done", ProtoType: "bool"},
			},
		},
	}
	pages := codegen.ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	entity := codegen.EntityDef{
		Name:      "Task",
		PkField:   "id",
		ProtoFile: "proto/db/v1/tasks.proto",
		Fields: []codegen.EntityField{
			{Name: "id", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "title", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "status", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "done", ProtoType: "bool", Kind: codegen.FieldKindScalar},
			{Name: "created_at", ProtoType: "google.protobuf.Timestamp", Kind: codegen.FieldKindTimestamp},
			{Name: "metadata", ProtoType: "message", Kind: codegen.FieldKindMessage}, // must be skipped
		},
	}
	codegen.AttachEntityMeta(&page, entity, svc)
	return page
}

func renderPageForTest(t *testing.T, tmplName string, data codegen.PageTemplateData) string {
	t.Helper()
	return renderPageInDirForTest(t, "pages", tmplName, data)
}

// renderPageInDirForTest renders a page template out of a named tree —
// "pages" (Next.js) or "vite-spa-pages" — so an invariant that must hold for
// both browser trees can be asserted against both.
func renderPageInDirForTest(t *testing.T, dir, tmplName string, data codegen.PageTemplateData) string {
	t.Helper()
	tmpl, err := loadPageTemplate(dir, tmplName)
	if err != nil {
		t.Fatalf("load %s/%s: %v", dir, tmplName, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render %s/%s: %v", dir, tmplName, err)
	}
	return buf.String()
}

// TestPageTemplates_TypedColumnsNoReflection pins F1: the generator knows
// the entity's fields, so pages declare typed columns/rows instead of
// casting proto messages to Record<string, unknown> and reflecting.
func TestPageTemplates_TypedColumnsNoReflection(t *testing.T) {
	page := crudPageDataForTest(t)

	list := renderPageForTest(t, "list-page.tsx.tmpl", page)
	for _, want := range []string{
		`import type { Task } from "@/gen/db/v1/tasks_pb";`,
		// Adopts the runtime <Resource> container + tristate adapter — the
		// tristate ladder + pagination are owned once, not re-hand-rolled.
		`import { useQueryResource } from "@/hooks/use-query-resource";`,
		`import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";`,
		`const columns: ResourceColumn<Task>[] = [`,
		`header: "Title",`,
		`cell: (item) => formatValue(item.title),`,
		// status is enum-like (a string, not a proto enum) → <StatusBadge>
		// with the raw field. No enum object to pass: there is no TS enum
		// behind a plain string column. The proto-enum branch is pinned by
		// TestPageTemplates_EnumColumnsRenderThroughStatusBadge.
		`cell: (item) => <StatusBadge value={item.status} />,`,
		`import { StatusBadge } from "@/components/status-badge";`,
		// typed container + row key + navigation
		`<Resource<Task>`,
		`rowKey={(item) => String(item.id)}`,
		`router.push(entityViewHref("tasks", item.id))`,
	} {
		if !strings.Contains(list, want) {
			t.Errorf("list page missing %q:\n%s", want, list)
		}
	}
	// No reflection hedges — and no CLIENT-SIDE filtering / hard page cap:
	// filtering + pagination now happen SERVER-SIDE via the List RPC, so the
	// browser never fetches one capped page and filters it locally.
	for _, banned := range []string{
		"as Record<string, unknown>", "Object.keys(", "Object.values(", "$typeName", "?? data;",
		"items.filter(", "pageSize: 200", "const searchFields =",
	} {
		if strings.Contains(list, banned) {
			t.Errorf("list page reflects/hedges or client-filters (%q):\n%s", banned, list)
		}
	}
	// The skipped message-kind field must not become a column.
	if strings.Contains(list, `header: "Metadata"`) || strings.Contains(list, "item.metadata") {
		t.Errorf("list page rendered a column for a message-kind field:\n%s", list)
	}

	detail := renderPageForTest(t, "detail-page.tsx.tmpl", page)
	for _, want := range []string{
		"const item = data?.task;",
		`label: "Created At",`,
		"value: formatValue(item.createdAt),",
		"message={userMessage(error)}",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail page missing %q:\n%s", want, detail)
		}
	}
	for _, banned := range []string{"as Record<string, unknown>", "Object.entries(", "?? data;", "useQueryClient", "invalidateQueries"} {
		if strings.Contains(detail, banned) {
			t.Errorf("detail page still contains %q (reflection hedge or redundant invalidation):\n%s", banned, detail)
		}
	}

	edit := renderPageForTest(t, "edit-page.tsx.tmpl", page)
	for _, want := range []string{
		"const item = data?.task;",
		"title: String(item.title ?? \"\"),",
		"done: Boolean(item.done),",
		"meta: { silenceErrorToast: true },",
		"message={userMessage(mutation.error)}",
	} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit page missing %q:\n%s", want, edit)
		}
	}
	for _, banned := range []string{"as Record<string, unknown>", "as Parameters<typeof mutation.mutate>[0]"} {
		if strings.Contains(edit, banned) {
			t.Errorf("edit page still contains %q:\n%s", banned, edit)
		}
	}

	create := renderPageForTest(t, "create-page.tsx.tmpl", page)
	for _, want := range []string{
		"meta: { silenceErrorToast: true },",
		"message={userMessage(mutation.error)}",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create page missing %q:\n%s", want, create)
		}
	}
	if strings.Contains(create, "as Parameters<typeof mutation.mutate>[0]") {
		t.Errorf("create page still casts the mutate payload:\n%s", create)
	}
}

// enumColumnPageForTest builds a PageTemplateData whose entity carries a REAL
// proto enum column (not an enum-like string), so the badge branch that has to
// hand <StatusBadge> the enum object is exercised.
func enumColumnPageForTest(t *testing.T) codegen.PageTemplateData {
	t.Helper()
	svc := codegen.ServiceDef{
		Name:      "OrderService",
		Package:   "services.orders.v1",
		ProtoFile: "proto/services/orders/v1/orders.proto",
		Methods: []codegen.Method{
			{Name: "ListOrders", InputType: "ListOrdersRequest", OutputType: "ListOrdersResponse",
				InputTypeFQ: "services.orders.v1.ListOrdersRequest"},
			{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"ListOrdersResponse": {
				{Name: "orders", ProtoType: "[]message", MessageType: "services.orders.v1.Order"},
			},
		},
		Schemas: map[string][]codegen.SchemaFieldDef{
			"services.orders.v1.Order": {
				{Name: "id", Kind: "string"},
				{Name: "status", Kind: "enum", TypeName: "services.orders.v1.OrderStatus"},
			},
		},
		SchemaFiles: map[string]string{
			"services.orders.v1.Order": "proto/services/orders/v1/orders.proto",
		},
		Enums: map[string][]string{
			"services.orders.v1.OrderStatus": {"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_PENDING"},
		},
	}
	pages := codegen.ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	codegen.AttachEntityMeta(&page, codegen.EntityDef{
		Name:      "Order",
		PkField:   "id",
		ProtoFile: "proto/services/orders/v1/orders.proto",
		Fields: []codegen.EntityField{
			{Name: "id", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: codegen.FieldKindEnum,
				MessageType: "services.orders.v1.OrderStatus"},
		},
	}, svc)
	return page
}

// TestPageTemplates_EnumColumnsRenderThroughStatusBadge pins the fix for the
// bug 4 of 8 hand-written pages shipped: protobuf-es v2 makes `item.status` a
// runtime NUMBER, and a badge fed that number rendered "1" instead of
// "Pending". The reverse map now lives in <StatusBadge> — which takes the raw
// field plus the ENUM OBJECT — so the born page and the component's own
// docstring describe the SAME call, and neither reverse-maps by hand.
func TestPageTemplates_EnumColumnsRenderThroughStatusBadge(t *testing.T) {
	page := enumColumnPageForTest(t)

	for _, tc := range []struct {
		tmpl string
		cell string
	}{
		{tmpl: "list-page.tsx.tmpl", cell: `cell: (item) => <StatusBadge value={item.status} enumType={ OrderStatus } />,`},
		{tmpl: "detail-page.tsx.tmpl", cell: `value: <StatusBadge value={item.status} enumType={ OrderStatus } />,`},
	} {
		// Both browser page trees must tell the SAME story — the vite-spa
		// detail page used to have no enum branch at all and rendered the
		// ordinal for every proto enum.
		for _, dir := range []string{"pages", "vite-spa-pages"} {
			rendered := renderPageInDirForTest(t, dir, tc.tmpl, page)
			for _, want := range []string{
				`import { StatusBadge } from "@/components/status-badge";`,
				`import { OrderStatus } from "@/gen/services/orders/v1/orders_pb";`,
				tc.cell,
			} {
				if !strings.Contains(rendered, want) {
					t.Errorf("%s/%s missing %q:\n%s", dir, tc.tmpl, want, rendered)
				}
			}
			// The hand reverse-map at the call site is exactly what the shared
			// component now owns — no page may re-grow it.
			for _, banned := range []string{
				"OrderStatus[item.status]",
				`import Badge from "@/components/ui/badge";`,
			} {
				if strings.Contains(rendered, banned) {
					t.Errorf("%s/%s reverse-maps the enum at the call site (%q):\n%s", dir, tc.tmpl, banned, rendered)
				}
			}
		}
	}
}

// TestBuildNavPages_FiltersOnEntitySet pins the F2 fix: nav/dashboard
// routes derive from the SAME entity set that gates page emission. A
// service with CRUD-shaped RPC names but no proto entity definition gets
// pages skipped — so it must get NO nav route either.
func TestBuildNavPages_FiltersOnEntitySet(t *testing.T) {
	services := []codegen.ServiceDef{
		{
			Name:      "TaskService",
			ProtoFile: "proto/services/tasks/v1/tasks.proto",
			Methods: []codegen.Method{
				{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"},
				{Name: "CreateTask", InputType: "CreateTaskRequest", OutputType: "CreateTaskResponse"},
				// CRUD-shaped, but "Report" has no entity definition —
				// the page generator skips it, so nav must too.
				{Name: "ListReports", InputType: "ListReportsRequest", OutputType: "ListReportsResponse"},
			},
		},
	}
	entities := []codegen.EntityDef{{Name: "Task"}}

	pages := buildNavPages(services, entities)
	if len(pages) != 1 {
		t.Fatalf("expected exactly 1 nav page (Task), got %d: %+v", len(pages), pages)
	}
	p := pages[0]
	if p.Slug != "tasks" || !p.HasCreate || p.ListHook != "useListTasks" || p.ItemsField != "tasks" || p.LabelSingular != "Task" {
		t.Errorf("unexpected nav page data: %+v", p)
	}
	if p.HooksModule == "" {
		t.Errorf("expected HooksModule to be populated, got %+v", p)
	}
}

// TestBuildNavPages_ControlPlaneEntitySet is the regression test for the
// nav-empties bug: the applied-schema entity projection (BuildSchemaEntities)
// names entities by the singular CRUD-RPC form (EntityDef.Name = "LLMKey"),
// while ExtractCRUDEntities re-derives the plural + kebab slug from the same
// RPC. The nav gate matches the two halves; if it matches on the raw
// lowercase NAME instead of the deterministic kebab SLUG, an acronym entity
// whose two name projections disagree on casing (proto-Go "LlmKey" vs CRUD
// "LLMKey") falls through the gate, its route is dropped, and on a project
// where EVERY admin entity is an acronym/aggregated List the ENTIRE nav
// regenerates empty — ALL_ROUTES = [] and every dashboard tile vanishes,
// with no error.
//
// This reproduces the real control-plane shape that triggered it: five
// admin entities sourced from five different services (LLMKey, Daemon,
// Plan, UsageEvent, User), several List-only (admin read views), and the
// entity set keyed by the singular projection — including the casing-
// divergent "LlmKey" form to prove the slug match is casing-proof. The
// assertion is the full route set is populated, not empty.
func TestBuildNavPages_ControlPlaneEntitySet(t *testing.T) {
	services := []codegen.ServiceDef{
		{Name: "LLMGatewayService", ProtoFile: "proto/controlplane/v1/llm_gateway_service.proto",
			Methods: []codegen.Method{
				{Name: "ListLLMKeys", InputType: "ListLLMKeysRequest", OutputType: "ListLLMKeysResponse"},
				{Name: "CreateLLMKey", InputType: "CreateLLMKeyRequest", OutputType: "CreateLLMKeyResponse"},
				{Name: "GetLLMKey", InputType: "GetLLMKeyRequest", OutputType: "GetLLMKeyResponse"},
			}},
		{Name: "DaemonService", ProtoFile: "proto/controlplane/v1/daemon_service.proto",
			Methods: []codegen.Method{
				{Name: "ListDaemons", InputType: "ListDaemonsRequest", OutputType: "ListDaemonsResponse"},
				{Name: "CreateDaemon", InputType: "CreateDaemonRequest", OutputType: "CreateDaemonResponse"},
			}},
		{Name: "BillingService", ProtoFile: "proto/controlplane/v1/billing_service.proto",
			Methods: []codegen.Method{
				{Name: "ListPlans", InputType: "ListPlansRequest", OutputType: "ListPlansResponse"},
			}},
		{Name: "BillingAdminService", ProtoFile: "proto/controlplane/v1/billing_admin.proto",
			Methods: []codegen.Method{
				{Name: "ListUsageEvents", InputType: "ListUsageEventsRequest", OutputType: "ListUsageEventsResponse"},
			}},
		{Name: "UserAdminService", ProtoFile: "proto/controlplane/v1/user_admin.proto",
			Methods: []codegen.Method{
				{Name: "ListUsers", InputType: "ListUsersRequest", OutputType: "ListUsersResponse"},
			}},
	}
	// The entity set as BuildSchemaEntities projects it: singular names from
	// the applied-schema join. "LlmKey" is the proto-Go-cased projection of
	// the same entity ExtractCRUDEntities derives as "LLMKey" from
	// ListLLMKeys — the casing divergence the slug-keyed gate must absorb.
	// (BuildSchemaEntities sorts by Name; order is irrelevant to the gate.)
	entities := []codegen.EntityDef{
		{Name: "Daemon", TableName: "daemons"},
		{Name: "LlmKey", TableName: "llm_keys"},
		{Name: "Plan", TableName: "plans"},
		{Name: "UsageEvent", TableName: "usage_events"},
		{Name: "User", TableName: "users"},
	}

	pages := buildNavPages(services, entities)

	gotSlugs := make(map[string]bool, len(pages))
	for _, p := range pages {
		gotSlugs[p.Slug] = true
	}
	wantSlugs := []string{"llm-keys", "daemons", "plans", "usage-events", "users"}
	if len(pages) != len(wantSlugs) {
		t.Fatalf("nav regenerated %d route(s), want %d (empty/partial nav = the regression): %+v",
			len(pages), len(wantSlugs), pages)
	}
	for _, s := range wantSlugs {
		if !gotSlugs[s] {
			t.Errorf("missing nav route %q — gate dropped a valid entity: got %+v", s, pages)
		}
	}
}

// TestDashboardGenTemplate_RealCountsAndCreateGating pins the dashboard
// half of F2: tiles wire the real list hook count (no static &mdash; stat
// card) and QuickActions only renders "Create X" for entities whose
// service actually has a Create RPC.
func TestDashboardGenTemplate_RealCountsAndCreateGating(t *testing.T) {
	data := templates.FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "demo",
		Pages: []templates.NavPageData{{
			Label: "Tasks", LabelLower: "tasks", LabelSingular: "Task", Slug: "tasks",
			HasCreate: true, ListHook: "useListTasks", HooksModule: "@/hooks/task-service-hooks",
			ItemsField: "tasks", ComponentIdent: "Tasks",
		}},
		NavHookImports: []templates.NavHookImport{{
			Module: "@/hooks/task-service-hooks", Symbols: []string{"useListTasks"},
		}},
	}
	rendered, err := templates.FrontendTemplates().Render(
		"nextjs/src/app/dashboard.tsx.tmpl", data)
	if err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	out := string(rendered)

	for _, want := range []string{
		`import { useListTasks } from "@/hooks/task-service-hooks";`,
		"const { data } = useListTasks({});",
		"const count = data?.tasks?.length;",
		"{count === undefined ? (",
		"animate-pulse",
		"String(count)",
		"ALL_ROUTES.filter((route) => route.hasCreate)",
		"New {route.labelSingular}",
		`"use client";`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "&mdash;") {
		t.Errorf("dashboard still renders the static &mdash; fake stat card:\n%s", out)
	}

	// The undefined branch must render a loading AFFORDANCE, never a value.
	// An em-dash (or a "0") in the not-yet-loaded slot is a fabricated stat:
	// it is indistinguishable from a real answer of "none", so a slow query
	// reads as an empty table. The skeleton span says "still loading" and
	// cannot be mistaken for data.
	start := strings.Index(out, "{count === undefined ? (")
	end := strings.Index(out, ") : (")
	if start < 0 || end < start {
		t.Fatalf("dashboard no longer renders a count===undefined branch at all:\n%s", out)
	}
	undefinedBranch := out[start:end]
	if !strings.Contains(undefinedBranch, "animate-pulse") {
		t.Errorf("the count===undefined branch must render a loading skeleton, not a placeholder value:\n%s", undefinedBranch)
	}
	for _, fabricated := range []string{"—", `"0"`, "&mdash;"} {
		if strings.Contains(undefinedBranch, fabricated) {
			t.Errorf("the count===undefined branch renders the fabricated value %q — an unloaded count must not be indistinguishable from a real one:\n%s", fabricated, undefinedBranch)
		}
	}

	// nav carries hasCreate so the dashboard can gate.
	navRendered, err := templates.FrontendTemplates().Render(
		"nextjs/src/components/nav.tsx.tmpl", data)
	if err != nil {
		t.Fatalf("render nav: %v", err)
	}
	if !strings.Contains(string(navRendered), `slug: "tasks", labelSingular: "Task", hasCreate: true`) {
		t.Errorf("nav missing hasCreate/labelSingular route fields:\n%s", navRendered)
	}
}

// TestDashboardGenTemplate_CountsTotalCountNotPageLength pins the count
// itself. forge's own generated dashboard read `data.tasks.length` — the
// length of the page it just fetched — so every tile in a project whose
// tables outgrow one page reports the PAGE SIZE, permanently. This file is
// the exemplar hand-written tiles are copied from, so it taught the exact
// page-capped-count habit the build charter spends twelve lines forbidding.
//
// When the List response declares total_count the tile must read it, and ask
// the server for one row rather than a whole page it never renders. When it
// does NOT, the length fallback stays (it is the only honest number
// available) and the emitted comment says how to get the real total.
func TestDashboardGenTemplate_CountsTotalCountNotPageLength(t *testing.T) {
	render := func(t *testing.T, page templates.NavPageData) string {
		t.Helper()
		rendered, err := templates.FrontendTemplates().Render(
			"nextjs/src/app/dashboard.tsx.tmpl", templates.FrontendTemplateData{
				FrontendName: "web",
				ProjectName:  "demo",
				Pages:        []templates.NavPageData{page},
				NavHookImports: []templates.NavHookImport{{
					Module: "@/hooks/task-service-hooks", Symbols: []string{"useListTasks"},
				}},
			})
		if err != nil {
			t.Fatalf("render dashboard: %v", err)
		}
		return string(rendered)
	}

	base := templates.NavPageData{
		Label: "Tasks", LabelLower: "tasks", LabelSingular: "Task", Slug: "tasks",
		HasCreate: true, ListHook: "useListTasks", HooksModule: "@/hooks/task-service-hooks",
		ItemsField: "tasks", ComponentIdent: "Tasks",
	}

	t.Run("total_count declared", func(t *testing.T) {
		withTotal := base
		withTotal.HasTotalCount = true
		withTotal.TotalCountField = "totalCount"
		withTotal.HasPageSize = true
		out := render(t, withTotal)

		if !strings.Contains(out, "const count = data?.totalCount;") {
			t.Errorf("tile must count with the server's total_count, not the fetched page:\n%s", out)
		}
		if strings.Contains(out, "data?.tasks?.length") {
			t.Errorf("tile still reads the page-capped length — this is the antipattern:\n%s", out)
		}
		if !strings.Contains(out, "const { data } = useListTasks({ pageSize: 1 });") {
			t.Errorf("tile must request one row (it renders a count, not the rows):\n%s", out)
		}
	})

	t.Run("total_count declared, no page_size on the request", func(t *testing.T) {
		noPageSize := base
		noPageSize.HasTotalCount = true
		noPageSize.TotalCountField = "totalCount"
		out := render(t, noPageSize)

		if !strings.Contains(out, "const count = data?.totalCount;") {
			t.Errorf("tile must still count with total_count:\n%s", out)
		}
		if !strings.Contains(out, "const { data } = useListTasks({});") {
			t.Errorf("a List request with no page_size field must stay empty (pageSize would not typecheck):\n%s", out)
		}
	})

	t.Run("no total_count on the response", func(t *testing.T) {
		out := render(t, base)

		if !strings.Contains(out, "const count = data?.tasks?.length;") {
			t.Errorf("without total_count the row length is the only honest count:\n%s", out)
		}
		if !strings.Contains(out, "total_count") {
			t.Errorf("the fallback must name the fix (declare total_count) in the emitted comment:\n%s", out)
		}
	})
}

// TestHooksTemplate_KeyFactory pins the F4/F5 fixes: a generated per-service
// query-key factory whose keys embed the protojson-normalized request
// (bigint-safe, type-normalized), entity-scoped invalidation for CRUD
// mutations, and whole-service fallback for non-CRUD mutations.
func TestHooksTemplate_KeyFactory(t *testing.T) {
	out := renderHooksForTest(t, false)

	for _, want := range []string{
		// Factory exists with service + entity scopes. The tuples themselves
		// are built by @reliantlabs/forge-web-runtime/service-hooks and pinned in
		// ITS suite (service-hooks.test.ts); what the generated file owns —
		// and what is asserted here — is the service name, which entities get
		// a scope, and which RPCs get a key.
		`const keys = serviceKeys("taskService");`,
		"export const taskServiceKeys = {",
		"all: keys.all,",
		`task: keys.entity("task"),`,
		// Query keys are declared per RPC with the request schema that hashes
		// them and the entity they are scoped under.
		`getTask: keys.query("getTask", GetTaskRequestSchema, "task"),`,
		`listTasks: keys.query("listTasks", ListTasksRequestSchema, "task"),`,
		// Hooks consume the factory — no hand-built key literals.
		"taskServiceKeys.getTask,",
		"taskServiceKeys.listTasks,",
		// CRUD mutation invalidates the ENTITY scope, not the service.
		"createMutationHook<typeof CreateTaskRequestSchema, CreateTaskResponse>(\n  taskServiceKeys.task,",
		// Non-CRUD mutation (SendReport) falls back to the service scope.
		"createMutationHook<typeof SendReportRequestSchema, SendReportResponse>(\n  taskServiceKeys.all,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered hooks missing %q:\n%s", want, out)
		}
	}

	// The hook-level error toast is gone — the MutationCache in
	// query-client.ts is the single chokepoint (F3).
	for _, banned := range []string{"getEventBus", "toast:show", "catch {"} {
		if strings.Contains(out, banned) {
			t.Errorf("rendered hooks still contain %q — toast policy must live only in query-client.ts", banned)
		}
	}
}

// TestMockTransport_MutableStoreAndNotFound pins F7: per-entity mutable
// Map stores (Create/Delete round-trip within a session), Get-miss →
// ConnectError NotFound instead of silently serving the first fixture.
func TestMockTransport_MutableStoreAndNotFound(t *testing.T) {
	entities := []codegen.MockTransportEntity{{
		EntityName:             "Task",
		EntityNamePlural:       "Tasks",
		EntitySlug:             "tasks",
		ServiceTypeName:        "demo.v1.TaskService",
		ListRPC:                "ListTasks",
		GetRPC:                 "GetTask",
		CreateRPC:              "CreateTask",
		UpdateRPC:              "UpdateTask",
		DeleteRPC:              "DeleteTask",
		HasList:                true,
		HasGet:                 true,
		HasCreate:              true,
		HasUpdate:              true,
		HasDelete:              true,
		ItemsField:             "tasks",
		PkFieldCamel:           "id",
		GetEntityFieldCamel:    "task",
		CreateEntityFieldCamel: "task",
		ImportPath:             "services/tasks/v1/tasks_pb",
		EntityImportPath:       "db/v1/tasks_pb",
		SchemaImport:           "TaskSchema",
		ListResponseType:       "ListTasksResponse",
		GetResponseType:        "GetTaskResponse",
		CreateResponseType:     "CreateTaskResponse",
	}}

	got := renderMockTransport(t, entities)

	// The mutable store, the store-backed List, and the NotFound-on-miss are
	// the ENGINE's behaviors now — asserted directly, against a real
	// protobuf runtime, in web-runtime/src/mock-transport.test.ts. What the
	// project file supplies is the data those behaviors run on, so that is
	// what is pinned here.
	for _, want := range []string{
		// the fixtures the session store is seeded from
		"fixtures: tasksMocks.tasks,",
		// the field the store keys by, and the CRUD arms that use it
		`pkField: "id",`,
		`list: { rpc: "ListTasks", responseSchema: ListTasksResponseSchema, itemsField: "tasks" }`,
		`get: { rpc: "GetTask", responseSchema: GetTaskResponseSchema, entityField: "task" }`,
		`create: { rpc: "CreateTask"`,
		`delete: { rpc: "DeleteTask" }`,
		// the entity schema the write paths build records with, imported
		// from the ENTITY's module (which differs from the service's here)
		"entitySchema: TaskSchema,",
		`from "@/gen/db/v1/tasks_pb"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mock transport missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "?? all[0]") {
		t.Errorf("mock transport still falls back to all[0] on get-miss:\n%s", got)
	}
}

// TestScenarioRpcsTemplate_TypedHandlerMap pins the typed scenario handler
// map: per-RPC keys with typed request params and MessageInitShape-typed
// returns (a snake_case payload fails tsc instead of rendering blank UI).
func TestScenarioRpcsTemplate_TypedHandlerMap(t *testing.T) {
	services := []codegen.ServiceDef{{
		Name:      "TaskService",
		Package:   "demo.v1",
		ProtoFile: "proto/services/tasks/v1/tasks.proto",
		Methods: []codegen.Method{
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"},
			{Name: "StreamTasks", InputType: "StreamTasksRequest", OutputType: "StreamTasksResponse", ServerStreaming: true},
		},
	}}
	data := codegen.BuildScenarioRPCData(services)

	content, err := templates.FrontendTemplates().Get("mocks/scenarios/scenario-rpcs.ts.tmpl")
	if err != nil {
		t.Fatalf("read scenario-rpcs template: %v", err)
	}
	tmpl, err := template.New("scenario-rpcs.ts.tmpl").Funcs(templates.FuncMap()).Parse(string(content))
	if err != nil {
		t.Fatalf("parse scenario-rpcs template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render scenario-rpcs: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		`"demo.v1.TaskService/GetTask"?: (req: GetTaskRequest) => UnaryReturn<MessageInitShape<typeof GetTaskResponseSchema>>;`,
		`import type { GetTaskRequest, GetTaskResponseSchema } from "@/gen/services/tasks/v1/tasks_pb";`,
		"[key: string]: ((req: never) => unknown) | undefined;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scenario-rpcs missing %q:\n%s", want, out)
		}
	}
	// Streaming RPCs are NOT in the typed map (no canonical return shape).
	if strings.Contains(out, "StreamTasks") {
		t.Errorf("scenario-rpcs should not type streaming RPCs:\n%s", out)
	}
}

// TestHooksTemplate_MutationComposeThenSpread pins the F9 fix: in BOTH the
// workspaces and non-workspaces branches, caller-supplied options must be
// destructured (onSuccess pulled out, the REST spread into useMutation) so a
// caller-supplied onSuccess can never REPLACE the composed
// invalidation+onSuccess handler. The shipped bug was `...options` spread
// AFTER the composed onSuccess in the workspaces branch — a caller's
// onSuccess silently disabled list invalidation (stale-list-after-save).
func TestHooksTemplate_MutationComposeThenSpread(t *testing.T) {
	for _, tc := range []struct {
		name       string
		workspaces bool
	}{
		{"workspaces", true},
		{"frontend-local", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderHooksForTest(t, tc.workspaces)

			// The compose-then-spread pattern ITSELF now lives in
			// @reliantlabs/forge-web-runtime/service-hooks, and is tested there
			// against a captured options object
			// ("composes the caller's onSuccess AFTER invalidation") — a
			// stronger check than the substring assertions this test used to
			// make, because it observes the actual composition rather than
			// the text that implements it.
			//
			// What remains generated, and is asserted here, is that every
			// mutation goes THROUGH that factory with an invalidation scope.
			// A hand-rolled useMutation reappearing in the template is the
			// regression this now guards: it would bypass the composition
			// and silently reintroduce the caller-overrides-invalidation bug.
			if strings.Contains(out, "useMutation({") {
				t.Errorf("rendered hooks hand-roll useMutation — mutations must go through "+
					"createMutationHook so caller onSuccess cannot replace the composed invalidation:\n%s", out)
			}
			if !strings.Contains(out, "createMutationHook<typeof CreateTaskRequestSchema, CreateTaskResponse>(") {
				t.Errorf("expected CreateTask to be built by createMutationHook:\n%s", out)
			}
			// Every createMutationHook call passes an invalidation scope as
			// its first argument — never omitted, never a bare literal.
			for _, want := range []string{
				"createMutationHook<typeof CreateTaskRequestSchema, CreateTaskResponse>(\n  taskServiceKeys.task,",
				"createMutationHook<typeof SendReportRequestSchema, SendReportResponse>(\n  taskServiceKeys.all,",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("expected mutation hook to declare its invalidation scope %q:\n%s", want, out)
				}
			}
		})
	}
}

// tsImportLocals returns every name a rendered TS file binds through its
// `import [type] { A, B as C } from "…"` statements, with a count each — a
// count above one is TS2300 "Duplicate identifier".
func tsImportLocals(src string) map[string]int {
	out := map[string]int{}
	for _, m := range tsNamedImport.FindAllStringSubmatch(src, -1) {
		for _, spec := range strings.Split(m[1], ",") {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			if _, alias, ok := strings.Cut(spec, " as "); ok {
				spec = alias
			}
			out[strings.TrimSpace(strings.TrimPrefix(spec, "type "))]++
		}
	}
	for _, m := range tsNamespaceImport.FindAllStringSubmatch(src, -1) {
		out[m[1]]++
	}
	return out
}

// tsNamedImport matches one `import [type] { … } from` statement, over lines;
// tsNamespaceImport one `import * as X from`.
var (
	tsNamedImport     = regexp.MustCompile(`(?s)import\s+(?:type\s+)?\{([^}]*)\}\s*from`)
	tsNamespaceImport = regexp.MustCompile(`import\s+(?:type\s+)?\*\s+as\s+(\w+)\s+from`)
)

func renderFrontendTemplate(t *testing.T, name string, data any) string {
	t.Helper()
	content, err := templates.FrontendTemplates().Get(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	tmpl, err := template.New(name).Funcs(templates.FuncMap()).Parse(string(content))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

// Two services each declaring `PingRequest` is ordinary proto (a message
// name is per package), but the project-wide scenario handler map imported
// every service's messages into one TS scope — `import type { PingRequest }`
// once per service — and the frontend failed `tsc`:
//
//	src/mocks/scenario-rpcs_gen.ts(15,60): error TS2300: Duplicate identifier 'PingRequest'.
//
// A clashing name is now imported `as <module>_<Name>` from each module and
// the handler map names that; a name only one module supplies is unchanged.
func TestScenarioRpcsTemplate_SameMessageNameInTwoServices(t *testing.T) {
	svc := func(name, pkg, file string) codegen.ServiceDef {
		return codegen.ServiceDef{Name: name, Package: pkg, ProtoFile: file, Methods: []codegen.Method{
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
			{Name: "Get" + strings.TrimSuffix(name, "Service"), InputType: "Get" + strings.TrimSuffix(name, "Service") + "Request", OutputType: "Get" + strings.TrimSuffix(name, "Service") + "Response"},
		}}
	}
	out := renderFrontendTemplate(t, "mocks/scenarios/scenario-rpcs.ts.tmpl", codegen.BuildScenarioRPCData([]codegen.ServiceDef{
		svc("AlphaService", "services.alpha.v1", "proto/services/alpha/v1/alpha.proto"),
		svc("BetaService", "services.beta.v1", "proto/services/beta/v1/beta.proto"),
	}))
	for name, n := range tsImportLocals(out) {
		if n > 1 {
			t.Errorf("scenario-rpcs imports %q %d times (TS2300 Duplicate identifier):\n%s", name, n, out)
		}
	}
	for _, want := range []string{
		`import type { GetAlphaRequest, GetAlphaResponseSchema, PingRequest as services_alpha_v1_alpha_pb_PingRequest, PingResponseSchema as services_alpha_v1_alpha_pb_PingResponseSchema } from "@/gen/services/alpha/v1/alpha_pb";`,
		`"services.alpha.v1.AlphaService/Ping"?: (req: services_alpha_v1_alpha_pb_PingRequest) => UnaryReturn<MessageInitShape<typeof services_alpha_v1_alpha_pb_PingResponseSchema>>;`,
		`"services.beta.v1.BetaService/Ping"?: (req: services_beta_v1_beta_pb_PingRequest) => UnaryReturn<MessageInitShape<typeof services_beta_v1_beta_pb_PingResponseSchema>>;`,
		// Unique names keep their own.
		`"services.beta.v1.BetaService/GetBeta"?: (req: GetBetaRequest) => UnaryReturn<MessageInitShape<typeof GetBetaResponseSchema>>;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scenario-rpcs missing %q:\n%s", want, out)
		}
	}
}

// The same scope rule inside ONE service's hooks file: a request type from
// another proto file that shares a name with one of the service's own is
// imported under its module's name, and every hook refers to that.
func TestHooksTemplate_SameMessageNameFromTwoModules(t *testing.T) {
	data := codegen.ServiceDefToHookData(codegen.ServiceDef{
		Name:      "AlphaService",
		Package:   "services.alpha.v1",
		ProtoFile: "proto/services/alpha/v1/alpha.proto",
		Methods: []codegen.Method{
			{Name: "GetThing", InputType: "GetThingRequest", OutputType: "Thing"},
			{Name: "GetSharedThing", InputType: "GetThingRequest", InputProtoFile: "proto/shared/v1/types.proto", OutputType: "Thing", OutputProtoFile: "proto/shared/v1/types.proto"},
		},
	})
	out := renderFrontendTemplate(t, "hooks.ts.tmpl", data)
	for name, n := range tsImportLocals(out) {
		if n > 1 {
			t.Errorf("hooks imports %q %d times (TS2300 Duplicate identifier):\n%s", name, n, out)
		}
	}
	for _, want := range []string{
		"createQueryHook<typeof services_alpha_v1_alpha_pb_GetThingRequestSchema, services_alpha_v1_alpha_pb_Thing>(",
		"createQueryHook<typeof shared_v1_types_pb_GetThingRequestSchema, shared_v1_types_pb_Thing>(",
		"const client = connectClient(AlphaService);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hooks missing %q:\n%s", want, out)
		}
	}
}

// mock-transport.ts imports every entity's response schemas into one scope
// too, and the entities belong to different services whose protos may name
// a response alike — a generic `GetResponse` in two services. Each row must
// name its own service's schema.
func TestMockTransportTemplate_SameResponseNameInTwoServices(t *testing.T) {
	entity := func(name, svc string) codegen.MockTransportEntity {
		lower := strings.ToLower(name)
		return codegen.MockTransportEntity{
			EntityName: name, EntityNamePlural: name + "s", EntitySlug: lower + "s",
			ServiceName: svc, ServiceTypeName: "services." + lower + ".v1." + svc,
			ListRPC: "List" + name + "s", GetRPC: "Get" + name, CreateRPC: "Create" + name,
			HasList: true, HasGet: true, HasCreate: true,
			ItemsField: "items", PkFieldCamel: "id", GetEntityFieldCamel: "item", CreateEntityFieldCamel: "item",
			ImportPath:   "services/" + lower + "/v1/" + lower + "_pb",
			SchemaImport: name + "Schema",
			// Generic names, declared by each service's own proto.
			ListResponseType: "ListResponse", GetResponseType: "GetResponse", CreateResponseType: "CreateResponse",
		}
	}
	out := renderMockTransport(t, []codegen.MockTransportEntity{entity("Thing", "ThingService"), entity("Widget", "WidgetService")})
	for name, n := range tsImportLocals(out) {
		if n > 1 {
			t.Errorf("mock-transport imports %q %d times (TS2300 Duplicate identifier):\n%s", name, n, out)
		}
	}
	for _, want := range []string{
		`import { CreateResponseSchema as services_thing_v1_thing_pb_CreateResponseSchema, GetResponseSchema as services_thing_v1_thing_pb_GetResponseSchema, ListResponseSchema as services_thing_v1_thing_pb_ListResponseSchema, ThingSchema } from "@/gen/services/thing/v1/thing_pb";`,
		`list: { rpc: "ListWidgets", responseSchema: services_widget_v1_widget_pb_ListResponseSchema, itemsField: "items" }`,
		`get: { rpc: "GetThing", responseSchema: services_thing_v1_thing_pb_GetResponseSchema, entityField: "item" }`,
		`create: { rpc: "CreateWidget", responseSchema: services_widget_v1_widget_pb_CreateResponseSchema,`,
		// The entity schemas are distinct names and keep their own.
		"entitySchema: WidgetSchema,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mock-transport missing %q:\n%s", want, out)
		}
	}
}

// One entity listed by two services — each declaring its own
// `ListThingsResponse` — is two dispatch rows over ONE fixture module. The
// rows name their own service's schema, and the fixture module is imported
// once: an import per row declared `thingsMocks` twice.
func TestMockTransportTemplate_EntityServedByTwoServices(t *testing.T) {
	row := func(svc string) codegen.MockTransportEntity {
		lower := strings.ToLower(strings.TrimSuffix(svc, "Service"))
		return codegen.MockTransportEntity{
			EntityName: "Thing", EntityNamePlural: "Things", EntitySlug: "things",
			ServiceName: svc, ServiceTypeName: "services." + lower + ".v1." + svc,
			ListRPC: "ListThings", HasList: true, ItemsField: "things", PkFieldCamel: "id",
			ImportPath: "services/" + lower + "/v1/" + lower + "_pb", SchemaImport: "ThingSchema",
			ListResponseType: "ListThingsResponse",
		}
	}
	out := renderMockTransport(t, []codegen.MockTransportEntity{row("AlphaService"), row("BetaService")})
	for name, n := range tsImportLocals(out) {
		if n > 1 {
			t.Errorf("mock-transport imports %q %d times (TS2300 Duplicate identifier):\n%s", name, n, out)
		}
	}
	for _, want := range []string{
		`import * as thingsMocks from "@/mocks/things_gen";`,
		`service: "services.alpha.v1.AlphaService",`,
		`list: { rpc: "ListThings", responseSchema: services_alpha_v1_alpha_pb_ListThingsResponseSchema, itemsField: "things" }`,
		`list: { rpc: "ListThings", responseSchema: services_beta_v1_beta_pb_ListThingsResponseSchema, itemsField: "things" }`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mock-transport missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "fixtures: thingsMocks.things,"); n != 2 {
		t.Errorf("want both rows seeded from the one fixture module, got %d:\n%s", n, out)
	}
}
