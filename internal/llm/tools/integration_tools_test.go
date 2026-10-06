// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
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
	assert.Equal(t, PlacementServer, def.Placement)
	placement, err := PlacementOf("http__request")
	require.NoError(t, err, "PlacementOf must resolve an integration tool")
	assert.Equal(t, PlacementServer, placement)
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

// Every exposed action is in the registry, including those whose integration
// needs a connection: whether one may be OFFERED is a question about the run's
// owner, asked per request, so the registry cannot answer it.
func TestEveryExposedActionIsRegistered(t *testing.T) {
	for _, m := range catalog.MustBuiltin().Manifests() {
		for _, a := range m.GetActions() {
			if !a.GetTool().GetExpose() {
				continue
			}
			name := manifest.ToolName(m, a)
			def, ok := registryEntry(name)
			if assert.True(t, ok, "%s/%s is exposed, so %s must be registered", m.GetId(), a.GetId(), name) {
				assert.Contains(t, def.Tags, TagIntegration)
			}
		}
	}
}

// A tool is gated exactly when its integration needs a connection: http's
// auth is optional, so http__request is never withheld.
func TestConnectionGatedIntegration(t *testing.T) {
	for name, want := range map[string]string{
		"github__issue_get":        "github",
		"github__repo_get_content": "github",
		"slack__message_post":      "slack",
	} {
		id, gated := ConnectionGatedIntegration(name)
		assert.True(t, gated, name)
		assert.Equal(t, want, id, name)
	}
	for _, name := range []string{"http__request", ToolFetch, ToolView, "mcp__x__y", "nope"} {
		_, gated := ConnectionGatedIntegration(name)
		assert.False(t, gated, name)
	}
}

// availabilitySource is a credential source that can also say which
// integrations the run's owner has.
type availabilitySource struct {
	toolCredSource
	usable map[string]bool
	runs   []string
	asked  map[string]*reliantv1.ConnectionSpec
}

func (s *availabilitySource) UsableIntegrations(_ context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error) {
	s.runs = append(s.runs, runID)
	s.asked = integrations
	return s.usable, nil
}

// Availability is asked of the very source the integration tools execute
// through, about the run a call would name: the thread, else the chat.
func TestUsableIntegrationsAsksTheToolsCredentialSource(t *testing.T) {
	src := &availabilitySource{usable: map[string]bool{"github": true, "slack": false}}
	f := NewToolsFactory(&ToolsOptions{IntegrationCredentials: src})

	got, err := f.UsableIntegrations(context.Background(), "chat-1", "thread-1", []string{"github", "slack", "http"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"github": true}, got)
	assert.Equal(t, []string{"thread-1"}, src.runs)
	require.Contains(t, src.asked, "github")
	assert.Equal(t, "https://api.github.com", src.asked["github"].GetBaseUrl(), "the integration's connection spec is passed")
	assert.NotContains(t, src.asked, "http", "an integration that needs no connection is never asked about")

	_, err = f.UsableIntegrations(context.Background(), "chat-1", "", []string{"github"})
	require.NoError(t, err)
	assert.Equal(t, "chat-1", src.runs[1], "a run with no thread is the chat, as at call time")
}

// No source, or one that cannot answer, makes nothing usable.
func TestUsableIntegrationsFailsClosed(t *testing.T) {
	for name, f := range map[string]*ToolsFactory{
		"nil factory":   nil,
		"no source":     NewToolsFactory(nil),
		"cannot answer": NewToolsFactory(&ToolsOptions{IntegrationCredentials: &toolCredSource{}}),
	} {
		got, err := f.UsableIntegrations(context.Background(), "chat", "thread", []string{"github"})
		require.NoError(t, err, name)
		assert.Empty(t, got, name)
	}
}

// The read-only GitHub actions are what lets a run with no machine read a
// project's code, so they must be registered, server-placed, and clear of
// NeedsMachine.
func TestGitHubCodeReadingToolsRunWithoutAMachine(t *testing.T) {
	for _, name := range []string{
		"github__repo_get", "github__repo_get_content", "github__repo_get_tree", "github__code_search",
	} {
		def, ok := registryEntry(name)
		require.True(t, ok, "%s must be registered", name)
		assert.Equal(t, PlacementServer, def.Placement, name)
		assert.False(t, NeedsMachine(name), "%s must be usable in a no-machine run", name)
		tool := NewToolsFactory(nil).GetToolByName(name, nil)
		require.NotNil(t, tool, name)
		needs, err := tool.RequiresPermission(nil, ToolCall{})
		require.NoError(t, err)
		assert.False(t, needs, "%s only reads, so it needs no approval", name)
	}
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
