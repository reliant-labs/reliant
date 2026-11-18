package codegen

import (
	"strings"
	"testing"
)

// orgMemberGrantsService is the control-plane shape: a custom List + Update pair
// over a message whose primary key (user_id) is on the wire, but whose table's
// surrogate `id` is not.
func orgMemberGrantsService() ServiceDef {
	return ServiceDef{
		Name:      "AccessTokenService",
		Package:   "controlplane.v1",
		ProtoFile: "proto/services/access_token/v1/access_token.proto",
		Methods: []Method{
			{Name: "ListOrgMemberGrants", InputType: "ListOrgMemberGrantsRequest", OutputType: "ListOrgMemberGrantsResponse"},
			{Name: "UpdateOrgMemberGrants", InputType: "UpdateOrgMemberGrantsRequest", OutputType: "UpdateOrgMemberGrantsResponse"},
		},
		Schemas: map[string][]SchemaFieldDef{
			"controlplane.v1.OrgMemberGrants":              {{Name: "user_id", Kind: "string"}, {Name: "scopes", Kind: "string", Repeated: true}},
			"controlplane.v1.ListOrgMemberGrantsResponse":  {{Name: "members", Kind: "message", TypeName: "controlplane.v1.OrgMemberGrants", Repeated: true}},
			"controlplane.v1.UpdateOrgMemberGrantsRequest": {{Name: "user_id", Kind: "string"}, {Name: "scopes", Kind: "string", Repeated: true}},
		},
		SchemaFiles: map[string]string{"controlplane.v1.OrgMemberGrants": "proto/services/access_token/v1/access_token.proto"},
	}
}

func orgMemberGrantsEntity() EntityDef {
	return EntityDef{
		Name: "OrgMemberGrants", TableName: "org_member_grants", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "user_id", GoName: "UserId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "scopes", GoName: "Scopes", ProtoType: "string", GoType: "[]string", Kind: FieldKindScalar},
		},
	}
}

// Defect 1: an entity whose primary key is not on its wire message must not be
// projected into pages, nav or mocks.
func TestFrontendEntities_DropsEntityWhosePKIsNotOnTheWire(t *testing.T) {
	product := EntityDef{Name: "Product", TableName: "products", PkField: "id",
		Fields: []EntityField{{Name: "id", ProtoType: "string", Kind: FieldKindScalar}}}
	kept, dropped := FrontendEntities([]EntityDef{orgMemberGrantsEntity(), product})

	if len(kept) != 1 || kept[0].Name != "Product" {
		t.Fatalf("kept = %v, want only Product", kept)
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0], "OrgMemberGrants") || !strings.Contains(dropped[0], `"id"`) {
		t.Errorf("dropped = %v, want one reason naming OrgMemberGrants and the missing key", dropped)
	}
}

// An entity whose table holds a surrogate `id` but whose message exposes the
// conventional domain key (control-plane's UsageEvent) is coherent: the key
// resolves to usage_event_id. Dropping it would delete a working page.
func TestFrontendEntities_KeepsEntityKeyedByConventionalDomainID(t *testing.T) {
	usage := EntityDef{Name: "UsageEvent", TableName: "usage_events", PkField: "id",
		Fields: []EntityField{{Name: "usage_event_id", ProtoType: "string", Kind: FieldKindScalar}, {Name: "model", ProtoType: "string", Kind: FieldKindScalar}}}
	kept, dropped := FrontendEntities([]EntityDef{usage})
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("UsageEvent must stay: kept=%d dropped=%v", len(kept), dropped)
	}
	if got := mockPkFieldCamel(usage); got != "usageEventId" {
		t.Errorf("mock key = %q, want usageEventId", got)
	}
	page := PageTemplateData{}
	AttachEntityMeta(&page, usage, ServiceDef{})
	if page.PkFieldCamel != "usageEventId" {
		t.Errorf("page key = %q, want usageEventId (the page must key rows by a field the message has)", page.PkFieldCamel)
	}
}

func TestFrontendEntities_KeepsEntityWithNoWireInventory(t *testing.T) {
	legacy := EntityDef{Name: "Legacy", TableName: "legacies", PkField: "id"}
	if kept, dropped := FrontendEntities([]EntityDef{legacy}); len(kept) != 1 || len(dropped) != 0 {
		t.Errorf("an entity with no wire fields proves nothing and must be kept: kept=%v dropped=%v", kept, dropped)
	}
}

// Defect 2: an Update RPC with no Create RPC must name its own response schema,
// never the empty `Schema`.
func TestMockTransport_UpdateWithoutCreateUsesUpdateResponseSchema(t *testing.T) {
	svc := orgMemberGrantsService()
	svc.Schemas["controlplane.v1.UpdateOrgMemberGrantsResponse"] = []SchemaFieldDef{
		{Name: "member", Kind: "message", TypeName: "controlplane.v1.OrgMemberGrants"},
	}
	svc.Messages = map[string][]MessageFieldDef{
		"UpdateOrgMemberGrantsResponse": {{Name: "member", ProtoType: "message", MessageType: "OrgMemberGrants"}},
	}
	ent := orgMemberGrantsEntity()
	ent.PkField = "user_id"

	rows := ExtractMockTransportEntities([]ServiceDef{svc}, []EntityDef{ent})
	if len(rows) != 1 {
		t.Fatalf("want 1 transport entity, got %d", len(rows))
	}
	row := rows[0]
	if row.UpdateResponse() != "UpdateOrgMemberGrantsResponse" {
		t.Errorf("UpdateResponse() = %q, want the Update RPC's own response", row.UpdateResponse())
	}
	for _, g := range NewMockTransportTemplateData(rows).SchemaImportGroups {
		for _, sym := range g.Symbols {
			if sym == "Schema" {
				t.Errorf("import group %s carries the undefined symbol %q", g.ImportPath, sym)
			}
		}
	}
}

// Defect 3: no Get RPC, no edit page (it would call `use({ id })`).
func TestEditPage_NeedsAGetRPC(t *testing.T) {
	pages := ExtractCRUDEntities(orgMemberGrantsService())
	if len(pages) != 1 {
		t.Fatalf("want 1 page model, got %d", len(pages))
	}
	if !pages[0].HasUpdate || pages[0].HasGet {
		t.Fatalf("fixture drifted: HasUpdate=%v HasGet=%v", pages[0].HasUpdate, pages[0].HasGet)
	}
	if pages[0].EmitsEditPage() {
		t.Error("edit page would be emitted for an entity with no Get RPC")
	}
	withGet := pages[0]
	withGet.HasGet = true
	if !withGet.EmitsEditPage() {
		t.Error("an entity with Get and Update must get its edit page")
	}
}
