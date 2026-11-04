package codegen

import (
	"strings"
	"testing"
)

// A born list page linked EVERY row to `/<slug>/<id>` — but that route exists
// only when forge generates a detail page, and forge generates one only when
// the service has a Get RPC. Found dogfooding forge on control-plane's
// operator console: the billing service serves ListUsageEvents and no
// GetUsageEvent, so no detail page was written, and every row click on the
// born UsageEvents list was a 404.
//
// The row link is therefore owed to the detail page, not to the list: with a
// Get RPC the row is a link; without one it is plain, inert data (no
// onRowClick, so <Resource> renders no button role, tab stop or pointer
// cursor either), and the router hook that only the link used is not
// imported at all — an unused import is an eslint error on a file forge just
// wrote. The audit columns the list normally omits ("the detail page is where
// they belong") stay, because there is no detail page for them to belong to.

// usageEventPageForTest builds the control-plane shape: a List RPC with no
// filter fields, cursor pagination, and — when withGet is false — no Get RPC.
func usageEventPageForTest(t *testing.T, withGet bool) PageTemplateData {
	t.Helper()
	svc := ServiceDef{
		Name:      "BillingService",
		Package:   "services.billing.v1",
		ProtoFile: "proto/services/billing/v1/billing.proto",
		Methods: []Method{
			{Name: "ListUsageEvents", InputType: "ListUsageEventsRequest", OutputType: "ListUsageEventsResponse",
				InputTypeFQ: "services.billing.v1.ListUsageEventsRequest"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListUsageEventsResponse": {
				{Name: "usage_events", ProtoType: "[]message", MessageType: "services.billing.v1.UsageEvent"},
				{Name: "next_page_token", ProtoType: "string"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"services.billing.v1.ListUsageEventsRequest": {
				{Name: "page_size", Kind: "int32"},
				{Name: "page_token", Kind: "string"},
			},
			"services.billing.v1.UsageEvent": {
				{Name: "id", Kind: "string"},
				{Name: "model", Kind: "string"},
				{Name: "created_at", Kind: "string"},
			},
		},
	}
	if withGet {
		svc.Methods = append(svc.Methods, Method{
			Name: "GetUsageEvent", InputType: "GetUsageEventRequest", OutputType: "GetUsageEventResponse",
		})
	}
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    "UsageEvent",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "model", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "created_at", ProtoType: "string", Kind: FieldKindScalar},
		},
	}, svc)
	return page
}

func TestListPage_RowLinksOnlyToAGeneratedDetailPage(t *testing.T) {
	const (
		nextRowLink = `onRowClick={(item) => router.push(entityViewHref("usage-events", item.id))}`
		viteRowLink = "onRowClick={(item) => void navigate({ to: `/usage-events/$id`, params: { id: String(item.id) } })}"
	)
	for _, tc := range []struct {
		name    string
		withGet bool
	}{
		{name: "no Get RPC, so no detail page", withGet: false},
		{name: "Get RPC, so a detail page", withGet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := usageEventPageForTest(t, tc.withGet)
			next := renderPageTemplate(t, "pages", "list-page.tsx.tmpl", page)
			vite := renderPageTemplate(t, "vite-spa-pages", "list-page.tsx.tmpl", page)

			if got := strings.Contains(next, nextRowLink); got != tc.withGet {
				t.Errorf("Next.js list page: row link present = %v, want %v:\n%s", got, tc.withGet, next)
			}
			if got := strings.Contains(vite, viteRowLink); got != tc.withGet {
				t.Errorf("Vite list page: row link present = %v, want %v:\n%s", got, tc.withGet, vite)
			}

			if tc.withGet {
				return
			}
			// No link, so nothing the link needed: an unused router hook
			// is an eslint error in the file forge just wrote.
			for _, bad := range []string{"onRowClick", "useRouter", "router.", "entityViewHref"} {
				if strings.Contains(next, bad) {
					t.Errorf("Next.js list page without a detail page still has %q:\n%s", bad, next)
				}
			}
			for _, bad := range []string{"onRowClick", "useNavigate", "navigate(", "@tanstack/react-router"} {
				if strings.Contains(vite, bad) {
					t.Errorf("Vite list page without a detail page still has %q:\n%s", bad, vite)
				}
			}
			for _, out := range []string{next, vite} {
				if strings.Contains(out, "links to the detail page") {
					t.Errorf("list page comment still promises a detail page:\n%s", out)
				}
			}
		})
	}
}

// The list omits the id and the audit timestamps on the grounds that the
// detail page shows them. With no detail page that ground is gone, and
// omitting them would make them unreachable anywhere in the UI — for an
// append-only event log, created_at is the column that matters most.
func TestAttachEntityMeta_ListKeepsAuditColumnsWithoutADetailPage(t *testing.T) {
	has := func(cols []EntityPageField, name string) bool {
		for _, c := range cols {
			if c.Name == name {
				return true
			}
		}
		return false
	}

	noDetail := usageEventPageForTest(t, false)
	for _, kept := range []string{"id", "createdAt", "model"} {
		if !has(noDetail.ListColumns, kept) {
			t.Errorf("without a detail page the list must keep %q; got %+v", kept, noDetail.ListColumns)
		}
	}

	withDetail := usageEventPageForTest(t, true)
	for _, omitted := range []string{"id", "createdAt"} {
		if has(withDetail.ListColumns, omitted) {
			t.Errorf("with a detail page the list must still omit %q; got %+v", omitted, withDetail.ListColumns)
		}
	}
}
