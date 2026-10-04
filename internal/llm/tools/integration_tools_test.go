// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func registryEntry(name string) (ToolDefinition, bool) {
	for _, def := range GetToolRegistry() {
		if def.Name == name {
			return def, true
		}
	}
	return ToolDefinition{}, false
}

func TestExposedActionIsRegisteredAsATool(t *testing.T) {
	def, ok := registryEntry("http__request")
	require.True(t, ok, "http/request sets tool.expose, so http__request must be in the registry")
	assert.Equal(t, ToolRunsOnServer, def.RunsOn)
	assert.Contains(t, def.Tags, TagIntegration)

	tool := NewToolsFactory(nil).GetToolByName("http__request", nil)
	require.NotNil(t, tool)
	assert.Equal(t, "http__request", tool.Name())
	assert.NotEmpty(t, tool.Description())
	props := tool.ParamSchema().Properties
	require.NotNil(t, props)
	_, hasURL := props.Get("url")
	assert.True(t, hasURL, "the manifest's params are the tool's schema")
}

func TestHTTPToolRequiresPermissionBecauseItMutates(t *testing.T) {
	tool := NewToolsFactory(nil).GetToolByName("http__request", nil)
	require.NotNil(t, tool)
	needs, err := tool.RequiresPermission(nil, ToolCall{})
	require.NoError(t, err)
	assert.True(t, needs, "mutates: true must gate the tool behind approval")
}

func TestToolsWithoutAvailableConnectionAreAbsent(t *testing.T) {
	cat := catalog.MustBuiltin()
	var httpManifest *reliantv1.IntegrationManifest
	for _, m := range cat.Manifests() {
		if m.GetId() == "http" {
			httpManifest = m
		}
	}
	require.NotNil(t, httpManifest)
	assert.True(t, ConnectionAvailable(httpManifest), "connection type none is always available")

	// A manifest that needs a connection the user lacks must not surface a tool.
	original := ConnectionAvailable
	defer func() { ConnectionAvailable = original }()
	ConnectionAvailable = func(*reliantv1.IntegrationManifest) bool { return false }
	_, present := registryEntry("http__request")
	assert.False(t, present, "an integration without a usable connection must not expose tools")
}

func TestNonExposedActionsAreNotTools(t *testing.T) {
	for _, def := range integrationToolDefinitions() {
		assert.Contains(t, def.Tags, TagIntegration)
	}
}

// names.AllToolNames is the validator workflow YAML is checked against and
// cannot import this package, so integration tool names are mirrored there.
func TestIntegrationToolNamesAreKnownToTheValidator(t *testing.T) {
	known := map[string]bool{}
	for _, n := range names.AllToolNames {
		known[n] = true
	}
	for _, def := range integrationToolDefinitions() {
		assert.True(t, known[def.Name], "%s missing from names.AllToolNames", def.Name)
	}
}

func TestBoundParamLeavesTheModelSchemaAndIsMergedAtCall(t *testing.T) {
	tool := NewToolsFactory(nil).GetToolByName("http__request", nil)
	bound, err := BindTool(tool, Bindings{"url": LiteralBinding("https://example.com/pinned")})
	require.NoError(t, err)
	_, visible := bound.ParamSchema().Properties.Get("url")
	assert.False(t, visible, "a bound param must not be shown to the model")
	_, stillThere := tool.ParamSchema().Properties.Get("url")
	assert.True(t, stillThere, "binding must not mutate the shared tool")

	_, err = BindTool(tool, Bindings{"nope": LiteralBinding(1)})
	assert.ErrorIs(t, err, ErrUnknownBoundParam)
}
