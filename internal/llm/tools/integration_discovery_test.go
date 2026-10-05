// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/rctx"
)

const discoveryFixture = `
id: acme
version: 1
display_name: Acme CRM
category: crm
keywords: [customers]
connection:
  base_url: https://api.acme.example
  auth:
    - api_key: { in: header, name: X-Api-Key }
actions:
  - id: contact.create
    display_name: Create contact
    summary: Add a contact to Acme.
    description: Create a contact in the connected Acme account and return its id.
    placement: server
    mutates: true
    params:
      type: object
      required: [email]
      properties:
        email: { type: string, description: The contact's email. }
        name: { type: string }
    request: { method: POST, path: /contacts }
    output:
      schema: { type: object, properties: { id: { type: string } } }
  - id: contact.get
    display_name: Get contact
    summary: Read one contact | by id.
    placement: server
    request: { method: GET, path: /contacts/1 }
`

type discoveryConnections map[string][]*core.Connection

func (d discoveryConnections) ListConnections(_ context.Context, userID, _ string) ([]*core.Connection, error) {
	return d[userID], nil
}

func discoverySearcher(t *testing.T) CatalogSearcher {
	t.Helper()
	m, err := manifest.Parse([]byte(discoveryFixture), manifest.TrustCurated)
	require.NoError(t, err)
	idx, err := catalogindex.Build([]*reliantv1.IntegrationManifest{m})
	require.NoError(t, err)
	return catalogindex.NewService(idx, discoveryConnections{
		"alice": {{IntegrationID: "acme", AuthKind: core.ConnectionAuthAPIKey, Status: core.ConnectionStatusActive}},
	}, nil)
}

func discoveryCtx(user string) *rctx.ToolContext {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	return rctx.NewToolContext(ctx, "chat", "chat", nil, nil)
}

func runDiscoveryTool(t *testing.T, tool Tool, user string, input any) ToolResponse {
	t.Helper()
	b, err := json.Marshal(input)
	require.NoError(t, err)
	resp, err := tool.Run(discoveryCtx(user), ToolCall{ID: "c", Name: tool.Name(), Input: string(b)})
	require.NoError(t, err)
	return resp
}

func TestSearchIntegrationsTool_ListsRefsWithSummariesAndConnected(t *testing.T) {
	tool := NewSearchIntegrationsTool(discoverySearcher(t))

	resp := runDiscoveryTool(t, tool, "bob", SearchIntegrationsParams{Query: "contact"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Showing 2 of 2.")
	assert.Contains(t, resp.Content, "| `acme/contact.create@1` | action | Add a contact to Acme. | no — the user must connect Acme CRM |")
	assert.Contains(t, resp.Content, `Read one contact \| by id.`, "a pipe in a summary must not break the table")
	assert.Less(t, strings.Index(resp.Content, "acme/contact.create@1"), strings.Index(resp.Content, "acme/contact.get@1"))
	assert.Contains(t, resp.Content, "get_integration_schema")

	alice := runDiscoveryTool(t, tool, "alice", SearchIntegrationsParams{Query: "contact"})
	assert.Contains(t, alice.Content, "| `acme/contact.create@1` | action | Add a contact to Acme. | yes |")
}

func TestSearchIntegrationsTool_ConnectedOnlyAndLimit(t *testing.T) {
	tool := NewSearchIntegrationsTool(discoverySearcher(t))

	none := runDiscoveryTool(t, tool, "bob", SearchIntegrationsParams{Query: "contact", ConnectedOnly: true})
	assert.Contains(t, none.Content, "No integration actions or triggers match")
	assert.Contains(t, none.Content, "http/request@1", "a miss points at the generic HTTP action")

	one := runDiscoveryTool(t, tool, "alice", SearchIntegrationsParams{Query: "contact", Limit: 1})
	assert.Contains(t, one.Content, "Showing 1 of 2.")
}

func TestSearchIntegrationsTool_KindFilter(t *testing.T) {
	tool := NewSearchIntegrationsTool(discoverySearcher(t))
	resp := runDiscoveryTool(t, tool, "bob", SearchIntegrationsParams{Query: "contact", Kind: "trigger"})
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "No integration actions or triggers match")

	// The params schema's enum refuses anything else before the tool runs.
	bad := runDiscoveryTool(t, tool, "bob", SearchIntegrationsParams{Query: "contact", Kind: "node"})
	assert.True(t, bad.IsError)
	assert.Contains(t, bad.Content, "action trigger")
}

func TestGetIntegrationSchemaTool_RendersParamsOutputAndUsage(t *testing.T) {
	tool := NewGetIntegrationSchemaTool(discoverySearcher(t))
	resp := runDiscoveryTool(t, tool, "bob", GetIntegrationSchemaParams{Ref: "acme/contact.create@1"})
	require.False(t, resp.IsError, resp.Content)
	c := resp.Content
	assert.Contains(t, c, "# Create contact (`acme/contact.create@1`)")
	assert.Contains(t, c, "changes external state")
	assert.Contains(t, c, "Create a contact in the connected Acme account and return its id.")
	assert.Contains(t, c, "has NOT connected Acme CRM")
	assert.Contains(t, c, `"email": {`)
	assert.Contains(t, c, `"description": "The contact's email."`)
	assert.Contains(t, c, "## Output (`nodes.<id>.data`)")
	assert.Contains(t, c, `"id": {`)
	assert.Contains(t, c, "- id: contact_create\n  type: action\n  uses: acme/contact.create@1\n  with:\n    email: ...\n")

	alice := runDiscoveryTool(t, tool, "alice", GetIntegrationSchemaParams{Ref: "acme/contact.create@1"})
	assert.Contains(t, alice.Content, "Required, and the user can use it now")
}

func TestGetIntegrationSchemaTool_UnknownRef(t *testing.T) {
	tool := NewGetIntegrationSchemaTool(discoverySearcher(t))
	for _, ref := range []string{"acme/contact.create@2", "acme/nope@1", "nonsense"} {
		resp := runDiscoveryTool(t, tool, "bob", GetIntegrationSchemaParams{Ref: ref})
		assert.True(t, resp.IsError, ref)
		assert.Contains(t, resp.Content, "no integration action or trigger has ref", ref)
		assert.Contains(t, resp.Content, "search_integrations", ref)
	}
	empty := runDiscoveryTool(t, tool, "bob", GetIntegrationSchemaParams{})
	assert.True(t, empty.IsError)
}

// With no injected searcher (the daemon runtime, doc generators) the tools
// still search the embedded catalog: refs and schemas are found, and only
// what needs no connection reads as connected.
func TestIntegrationDiscoveryTools_FallBackToTheEmbeddedCatalog(t *testing.T) {
	f := NewToolsFactory(nil)
	search := runDiscoveryTool(t, f.SearchIntegrations(), "u", SearchIntegrationsParams{Query: "http request"})
	require.False(t, search.IsError, search.Content)
	assert.Contains(t, search.Content, "| `http/request@1` | action |")
	assert.Contains(t, search.Content, "yes (no connection needed)")

	schema := runDiscoveryTool(t, f.GetIntegrationSchema(), "u", GetIntegrationSchemaParams{Ref: "http/request@1"})
	require.False(t, schema.IsError, schema.Content)
	assert.Contains(t, schema.Content, "Optional. Without one the call is unauthenticated")
	assert.Contains(t, schema.Content, `"url": {`)
	assert.Contains(t, schema.Content, "Also available to agents as the `http__request` tool.")
}

// The tools are granted wherever create_workflow / edit_workflow are: the
// workflow tag, which is how the workflow_builder preset (and anything that
// names tag:workflow) reaches them.
func TestIntegrationDiscoveryTools_AreRegisteredWithTheWorkflowTools(t *testing.T) {
	byName := map[string]ToolDefinition{}
	for _, d := range GetToolRegistry() {
		byName[d.Name] = d
	}
	create, ok := byName[ToolCreateWorkflow]
	require.True(t, ok)
	for _, name := range []string{ToolSearchIntegrations, ToolGetIntegrationSchema} {
		def, ok := byName[name]
		require.True(t, ok, name)
		assert.Contains(t, def.Tags, TagWorkflow, name)
		assert.Contains(t, create.Tags, TagWorkflow)
		assert.Contains(t, def.Tags, TagReadOnly, name)
		assert.Equal(t, PlacementServer, def.Placement, name)
		assert.True(t, names.IsValidToolName(name), "%s must be a valid tool name in workflow YAML", name)
	}
}
