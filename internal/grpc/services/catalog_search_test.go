// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// catalogSearchFixture is two integrations: one that needs a connection
// (acme, api_key) and one that takes none (http's shape).
var catalogSearchFixture = []string{`
id: acme
version: 1
display_name: Acme CRM
icon: acme
category: crm
keywords: [customers]
connection:
  base_url: https://api.acme.example
  connection_params:
    - name: region
      display_name: Region
      pattern: "[a-z]{2}"
      default_value: us
  auth:
    - api_key: { in: header, name: X-Api-Key, label: Acme key }
actions:
  - id: contact.create
    display_name: Create contact
    summary: Add a contact to Acme.
    description: Create a contact in the connected Acme account and return its id.
    keywords: [person, lead]
    placement: server
    mutates: true
    tool: { expose: true }
    params:
      type: object
      required: [email]
      properties: { email: { type: string } }
    request: { method: POST, path: /contacts }
    output:
      schema: { type: object, properties: { id: { type: string } } }
  - id: contact.get
    display_name: Get contact
    summary: Read one contact.
    placement: server
    request: { method: GET, path: /contacts/1 }
  - id: deal.list
    display_name: List deals
    summary: List open deals.
    placement: server
    request: { method: GET, path: /deals }
`, `
id: web
version: 1
display_name: Web
icon: globe
category: core
connection:
  allow_any_public_host: true
  auth_optional: true
  auth:
    - api_key: { label: API key }
actions:
  - id: fetch
    display_name: Fetch a URL
    summary: Fetch a public URL, for example a contact page.
    placement: server
    request: { method: GET, url: "{{ params.url }}" }
`}

type fakeCatalogConnections map[string][]*core.Connection

func (f fakeCatalogConnections) ListConnections(_ context.Context, userID, _ string) ([]*core.Connection, error) {
	return f[userID], nil
}

func newCatalogSearchTestService(t *testing.T) *CatalogService {
	t.Helper()
	var ms []*reliantv1.IntegrationManifest
	for _, d := range catalogSearchFixture {
		m, err := manifest.Parse([]byte(d), manifest.TrustCurated)
		require.NoError(t, err)
		ms = append(ms, m)
	}
	idx, err := catalogindex.Build(ms)
	require.NoError(t, err)
	conns := fakeCatalogConnections{
		"alice": {{IntegrationID: "acme", AuthKind: core.ConnectionAuthAPIKey, Status: core.ConnectionStatusActive}},
	}
	methods := func(id string) (connections.Integration, bool) {
		if id != "acme" {
			return connections.Integration{}, false
		}
		return connections.Integration{ID: "acme", Methods: []connections.MethodStatus{
			{Kind: connections.MethodAPIKey, Available: true, FieldLabels: map[string]string{"api_key": "Acme key"}},
		}}, true
	}
	return NewCatalogService(nil).WithCatalogSearch(catalogindex.NewService(idx, conns, nil), methods)
}

func asUser(user string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, user)
}

func entryRefs(entries []*reliantv1.CatalogEntrySummary) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.GetRef())
	}
	return out
}

func TestSearchCatalog_ReturnsRankedLightweightEntries(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	resp, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(&reliantv1.SearchCatalogRequest{Query: "contact"}))
	require.NoError(t, err)
	got := resp.Msg
	// Both acme contact actions reach the prefix tier; web/fetch only mentions
	// "contact" in its summary, so it ranks last even though bob can use it
	// and cannot use acme.
	require.Equal(t, []string{"acme/contact.create@1", "acme/contact.get@1", "web/fetch@1"}, entryRefs(got.GetEntries()))
	assert.EqualValues(t, 3, got.GetTotalSize())
	assert.Empty(t, got.GetNextPageToken())

	first := got.GetEntries()[0]
	assert.Equal(t, reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION, first.GetKind())
	assert.Equal(t, "contact.create", first.GetId())
	assert.Equal(t, "Create contact", first.GetDisplayName())
	assert.Equal(t, "Add a contact to Acme.", first.GetSummary())
	assert.Equal(t, &reliantv1.CatalogIntegration{Id: "acme", Version: 1, DisplayName: "Acme CRM", Icon: "acme", Category: "crm"},
		first.GetIntegration())
	assert.Equal(t, []reliantv1.ConnectionAuthKind{reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_API_KEY}, first.GetAuthKinds())
	assert.True(t, first.GetConnectionRequired())
	assert.False(t, first.GetConnected(), "bob has no acme connection")
	assert.True(t, first.GetMutates())

	web := got.GetEntries()[2]
	assert.False(t, web.GetConnectionRequired())
	assert.True(t, web.GetConnected())

	assert.Equal(t, []*reliantv1.CatalogFacet{{Value: "crm", Count: 2}, {Value: "core", Count: 1}}, got.GetCategoryFacets())
}

func TestSearchCatalog_ConnectedIsTheCallers(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	req := &reliantv1.SearchCatalogRequest{ConnectedOnly: true}

	alice, err := svc.SearchCatalog(asUser("alice"), connect.NewRequest(req))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"acme/contact.create@1", "acme/contact.get@1", "acme/deal.list@1", "web/fetch@1"},
		entryRefs(alice.Msg.GetEntries()))
	for _, e := range alice.Msg.GetEntries() {
		assert.True(t, e.GetConnected(), e.GetRef())
	}

	bob, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(req))
	require.NoError(t, err)
	assert.Equal(t, []string{"web/fetch@1"}, entryRefs(bob.Msg.GetEntries()))
}

func TestSearchCatalog_Pages(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	req := &reliantv1.SearchCatalogRequest{PageSize: 3}
	p1, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(req))
	require.NoError(t, err)
	require.Len(t, p1.Msg.GetEntries(), 3)
	require.NotEmpty(t, p1.Msg.GetNextPageToken())
	assert.EqualValues(t, 4, p1.Msg.GetTotalSize())

	req.PageToken = p1.Msg.GetNextPageToken()
	p2, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(req))
	require.NoError(t, err)
	assert.Len(t, p2.Msg.GetEntries(), 1)
	assert.Empty(t, p2.Msg.GetNextPageToken())
	assert.NotContains(t, entryRefs(p1.Msg.GetEntries()), p2.Msg.GetEntries()[0].GetRef())
}

func TestSearchCatalog_FiltersByKindAndCategory(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	resp, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(&reliantv1.SearchCatalogRequest{
		Kinds: []reliantv1.CatalogEntryKind{reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER},
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetEntries(), "no trigger types exist yet")

	resp, err = svc.SearchCatalog(asUser("bob"), connect.NewRequest(&reliantv1.SearchCatalogRequest{Category: "core"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"web/fetch@1"}, entryRefs(resp.Msg.GetEntries()))
}

func TestSearchCatalog_RejectsBadRequests(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	cases := map[string]*reliantv1.SearchCatalogRequest{
		"bad page token":   {PageToken: "not-a-token"},
		"negative size":    {PageSize: -1},
		"unspecified kind": {Kinds: []reliantv1.CatalogEntryKind{reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_UNSPECIFIED}},
		"query too long":   {Query: string(make([]byte, catalogindex.MaxQueryLen+1))},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(req))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

// ListCatalogIntegrations is the browse a picker opens on: one row per
// integration, the caller's usable ones first, with what a row needs to draw
// (name, icon, how many entries) and nothing heavier.
func TestListCatalogIntegrations_ListsIntegrationsConnectedFirst(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	listing := func(user string, req *reliantv1.ListCatalogIntegrationsRequest) *reliantv1.ListCatalogIntegrationsResponse {
		t.Helper()
		resp, err := svc.ListCatalogIntegrations(asUser(user), connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg
	}
	ids := func(msg *reliantv1.ListCatalogIntegrationsResponse) []string {
		out := []string{}
		for _, l := range msg.GetIntegrations() {
			out = append(out, l.GetIntegration().GetId())
		}
		return out
	}

	// bob has no Acme connection, so only Web (no credential) is usable.
	bob := listing("bob", &reliantv1.ListCatalogIntegrationsRequest{})
	assert.Equal(t, []string{"web", "acme"}, ids(bob))
	assert.EqualValues(t, 2, bob.GetTotalSize())
	acme := bob.GetIntegrations()[1]
	assert.Equal(t, "Acme CRM", acme.GetIntegration().GetDisplayName())
	assert.Equal(t, "acme", acme.GetIntegration().GetIcon())
	assert.Equal(t, "crm", acme.GetIntegration().GetCategory())
	assert.EqualValues(t, 3, acme.GetEntryCount())
	assert.False(t, acme.GetConnected())

	// alice has one, so Acme sorts with the usable integrations.
	alice := listing("alice", &reliantv1.ListCatalogIntegrationsRequest{})
	assert.Equal(t, []string{"acme", "web"}, ids(alice))
	assert.True(t, alice.GetIntegrations()[0].GetConnected())

	core := listing("bob", &reliantv1.ListCatalogIntegrationsRequest{Category: "core"})
	assert.Equal(t, []string{"web"}, ids(core))
	assert.ElementsMatch(t, []string{"core", "crm"}, func() []string {
		var out []string
		for _, f := range core.GetCategoryFacets() {
			out = append(out, f.GetValue())
		}
		return out
	}(), "facets ignore the category filter")

	triggers := listing("bob", &reliantv1.ListCatalogIntegrationsRequest{
		Kinds: []reliantv1.CatalogEntryKind{reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER},
	})
	assert.Empty(t, triggers.GetIntegrations(), "neither fixture integration declares a trigger type")

	p1 := listing("bob", &reliantv1.ListCatalogIntegrationsRequest{PageSize: 1})
	require.NotEmpty(t, p1.GetNextPageToken())
	p2 := listing("bob", &reliantv1.ListCatalogIntegrationsRequest{PageSize: 1, PageToken: p1.GetNextPageToken()})
	assert.Equal(t, []string{"acme"}, ids(p2))
	assert.Empty(t, p2.GetNextPageToken())
}

func TestListCatalogIntegrations_RejectsBadRequests(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	cases := map[string]*reliantv1.ListCatalogIntegrationsRequest{
		"bad page token":   {PageToken: "not-a-token"},
		"negative size":    {PageSize: -1},
		"unspecified kind": {Kinds: []reliantv1.CatalogEntryKind{reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_UNSPECIFIED}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.ListCatalogIntegrations(asUser("bob"), connect.NewRequest(req))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
	_, err := svc.ListCatalogIntegrations(context.Background(), connect.NewRequest(&reliantv1.ListCatalogIntegrationsRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = NewCatalogService(nil).ListCatalogIntegrations(asUser("bob"), connect.NewRequest(&reliantv1.ListCatalogIntegrationsRequest{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestSearchCatalog_RequiresAUser(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	_, err := svc.SearchCatalog(context.Background(), connect.NewRequest(&reliantv1.SearchCatalogRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = svc.GetCatalogEntry(context.Background(), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "web/fetch@1"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestSearchCatalog_UnconfiguredIsUnimplemented(t *testing.T) {
	svc := NewCatalogService(nil)
	_, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(&reliantv1.SearchCatalogRequest{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestGetCatalogEntry_ReturnsSchemasAndConnectionRequirement(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	resp, err := svc.GetCatalogEntry(asUser("alice"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "acme/contact.create@1"}))
	require.NoError(t, err)
	e := resp.Msg.GetEntry()

	assert.Equal(t, "acme/contact.create@1", e.GetSummary().GetRef())
	assert.True(t, e.GetSummary().GetConnected(), "alice has an active acme connection")
	assert.Equal(t, "Create a contact in the connected Acme account and return its id.", e.GetDescription())
	assert.Equal(t, "acme__contact_create", e.GetToolName())

	params := e.GetParamsSchema().AsMap()
	assert.Equal(t, []any{"email"}, params["required"])
	assert.Contains(t, params["properties"], "email")
	// The Struct's keys arrive sorted; the declared order travels beside it.
	assert.Equal(t, []string{"email"}, e.GetParamOrder())
	assert.Contains(t, e.GetOutputSchema().AsMap()["properties"], "id")
	assert.Nil(t, e.GetPayloadSchema(), "an action has no trigger payload")

	conn := e.GetConnection()
	assert.True(t, conn.GetRequired())
	require.Len(t, conn.GetMethods(), 1)
	assert.Equal(t, reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_API_KEY, conn.GetMethods()[0].GetKind())
	assert.True(t, conn.GetMethods()[0].GetAvailable())
	assert.Equal(t, map[string]string{"api_key": "Acme key"}, conn.GetMethods()[0].GetFieldLabels())
	require.Len(t, conn.GetConnectionParams(), 1)
	assert.Equal(t, "region", conn.GetConnectionParams()[0].GetName())
	assert.False(t, conn.GetConnectionParams()[0].GetRequired(), "it has a default")

	bob, err := svc.GetCatalogEntry(asUser("bob"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "acme/contact.create@1"}))
	require.NoError(t, err)
	assert.False(t, bob.Msg.GetEntry().GetSummary().GetConnected())
}

func TestGetCatalogEntry_ActionWithoutSchemasGetsEmptyObjects(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	resp, err := svc.GetCatalogEntry(asUser("bob"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "acme/deal.list@1"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"type": "object"}, resp.Msg.GetEntry().GetParamsSchema().AsMap())
	assert.Equal(t, map[string]any{"type": "object"}, resp.Msg.GetEntry().GetOutputSchema().AsMap())
	assert.Empty(t, resp.Msg.GetEntry().GetToolName())
}

func TestGetCatalogEntry_UnknownRefIsNotFound(t *testing.T) {
	svc := newCatalogSearchTestService(t)
	for _, ref := range []string{"acme/contact.create@2", "acme/nope@1", "nope/fetch@1", "garbage"} {
		_, err := svc.GetCatalogEntry(asUser("bob"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: ref}))
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), ref)
	}
	_, err := svc.GetCatalogEntry(asUser("bob"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// The embedded catalog is what production serves; its entries must convert.
func TestGetCatalogEntry_EveryEmbeddedEntryConverts(t *testing.T) {
	idx := catalogindex.MustBuiltin()
	svc := NewCatalogService(nil).WithCatalogSearch(catalogindex.NewService(idx, nil, nil), nil)
	all, err := svc.SearchCatalog(asUser("u"), connect.NewRequest(&reliantv1.SearchCatalogRequest{PageSize: catalogindex.MaxPageSize}))
	require.NoError(t, err)
	require.NotEmpty(t, all.Msg.GetEntries())
	kinds := map[reliantv1.CatalogEntryKind]int{}
	for _, s := range all.Msg.GetEntries() {
		resp, err := svc.GetCatalogEntry(asUser("u"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: s.GetRef()}))
		require.NoError(t, err, s.GetRef())
		e := resp.Msg.GetEntry()
		kinds[s.GetKind()]++
		// An action converts with its params schema, a trigger with its
		// payload schema; each carries only its own.
		switch s.GetKind() {
		case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER:
			assert.NotNil(t, e.GetPayloadSchema(), s.GetRef())
			assert.Nil(t, e.GetParamsSchema(), s.GetRef())
		default:
			assert.NotNil(t, e.GetParamsSchema(), s.GetRef())
			assert.Nil(t, e.GetPayloadSchema(), s.GetRef())
		}
	}
	assert.Positive(t, kinds[reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION])
	assert.Positive(t, kinds[reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER], "the embedded catalog ships triggers (Slack)")
}

const catalogTriggerFixture = `
id: pager
version: 1
display_name: Pager
category: ops
connection:
  base_url: https://api.pager.example
  auth:
    - api_key: { in: header, name: X-Api-Key }
triggers:
  - id: alert.fired
    display_name: Alert fired
    summary: An alert started firing.
    events: [alert.fired]
    attributes:
      - { name: service }
    data:
      type: object
      properties:
        alert: { type: object, properties: { severity: { type: string } } }
`

// A trigger entry carries the payload schema (trigger.payload) and no
// action schemas; the search lists it under the trigger kind.
func TestGetCatalogEntry_TriggerCarriesItsPayloadSchema(t *testing.T) {
	m, err := manifest.Parse([]byte(catalogTriggerFixture), manifest.TrustCurated)
	require.NoError(t, err)
	idx, err := catalogindex.Build([]*reliantv1.IntegrationManifest{m})
	require.NoError(t, err)
	svc := NewCatalogService(nil).WithCatalogSearch(catalogindex.NewService(idx, fakeCatalogConnections{}, nil), nil)

	list, err := svc.SearchCatalog(asUser("bob"), connect.NewRequest(&reliantv1.SearchCatalogRequest{
		Kinds: []reliantv1.CatalogEntryKind{reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"pager/alert.fired@1"}, entryRefs(list.Msg.GetEntries()))
	assert.Equal(t, "Alert fired", list.Msg.GetEntries()[0].GetDisplayName())

	resp, err := svc.GetCatalogEntry(asUser("bob"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "pager/alert.fired@1"}))
	require.NoError(t, err)
	e := resp.Msg.GetEntry()
	assert.Equal(t, reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER, e.GetSummary().GetKind())
	assert.Nil(t, e.GetParamsSchema())
	assert.Nil(t, e.GetOutputSchema())
	require.NotNil(t, e.GetPayloadSchema())
	props := e.GetPayloadSchema().AsMap()["properties"].(map[string]any)
	assert.Contains(t, props["data"].(map[string]any)["properties"], "alert")
	assert.Equal(t, map[string]any{"type": "string", "const": "pager"}, props["integration"])
}
