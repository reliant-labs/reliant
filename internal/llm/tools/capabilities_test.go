// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolCtxWithCaps is a tool context carrying a turn's capability set, the way
// execute_tools hands one to load_tool.
func toolCtxWithCaps(t *testing.T, caps *Capabilities) *rctx.ToolContext {
	t.Helper()
	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	return rctx.NewToolContext(WithCapabilities(context.Background(), caps), "chat-"+t.Name(), "0", nil, worktree)
}

// declaredCaps resolves a node's set the way call_llm does.
func declaredCaps(permission string, preloaded, loadable, mcp []string) *Capabilities {
	return ResolveCapabilities(CapabilityInputs{
		Access:     ResolveToolAccess(preloaded, loadable, mcp),
		Permission: permission,
		MCPTools:   mcp,
	})
}

// grantsOf is what a load_tool response granted, read from its metadata the
// way execute_tools reads it into granted_tools.
func grantsOf(t *testing.T, resp ToolResponse) []string {
	t.Helper()
	if resp.Metadata == "" {
		return nil
	}
	var metadata LoadToolMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &metadata))
	return metadata.LoadedTools
}

func TestResolveCapabilities_OffersPreloadedAndStructuralTools(t *testing.T) {
	t.Parallel()

	caps := ResolveCapabilities(CapabilityInputs{
		Access:           ResolveToolAccess([]string{ToolView, ToolWrite}, []string{LoadableWildcard}, nil),
		Permission:       PermissionMutating,
		MailboxReachable: true,
		CanSpawnChildren: true,
		SpawnPresets:     []string{"researcher", "general", "researcher"},
		ResponseTool:     "submit_result",
	})

	assert.Equal(t,
		[]string{ToolLoadTool, "spawn", ToolSpawnSend, ToolSpawnStop, "submit_result", ToolView, ToolWrite},
		caps.Offered, "preloaded + structural + spawn + response tool, sorted")
	assert.Equal(t, []string{"general", "researcher"}, caps.SpawnPresets)
	assert.True(t, caps.LoadableAll)
	assert.Equal(t, PermissionMutating, caps.Permission)
}

// The tier gates the MENU now, as the ToolsConfig.permission comment always
// said it did. Before, a mutating node naming start_run was handed it and then
// refused at execution.
func TestResolveCapabilities_TierWithholdsToolsAboveIt(t *testing.T) {
	t.Parallel()

	mutating := declaredCaps(PermissionMutating, []string{ToolStartRun, ToolListRuns}, nil, nil)
	assert.False(t, mutating.Offers(ToolStartRun), "start_run is orchestrator-only")
	assert.True(t, mutating.Offers(ToolListRuns))
	assert.Contains(t, mutating.Explain(ToolStartRun), "requires 'orchestrator' permission")

	orchestrator := declaredCaps(PermissionOrchestrator, []string{ToolStartRun, ToolListRuns}, nil, nil)
	assert.True(t, orchestrator.Offers(ToolStartRun))
}

// spawn is granted by the spawn declaration, not the tier: the builtin agent
// spawns at mutating.
func TestResolveCapabilities_SpawnFollowsTheDeclarationNotTheTier(t *testing.T) {
	t.Parallel()

	caps := ResolveCapabilities(CapabilityInputs{
		Permission:   PermissionMutating,
		SpawnPresets: []string{"general"},
	})
	assert.True(t, caps.Offers("spawn"))
	assert.True(t, caps.AllowsPreset("general"))
	assert.False(t, caps.AllowsPreset("planner"))
}

// The spawn management tools follow the thread's sub-agents, not only its
// spawn config: a thread that has spawned manages what it spawned whatever its
// config says now, and a branch that inherited sub-agents may only look.
// spawn_status needs a sub-agent to look at; nothing else grants it.
func TestResolveCapabilities_SpawnManagementToolsFollowTheThreadsSubAgents(t *testing.T) {
	t.Parallel()

	management := []string{ToolSpawnStatus, ToolSpawnSend, ToolSpawnStop}
	tests := []struct {
		name    string
		in      CapabilityInputs
		offered []string
	}{
		{name: "no sub-agents and no counterpart: none"},
		{
			name:    "a sub-agent with a parent may message it, but has nothing to look at or stop",
			in:      CapabilityInputs{MailboxReachable: true},
			offered: []string{ToolSpawnSend},
		},
		{
			name:    "an orchestrator before its first spawn may message and stop, not yet look",
			in:      CapabilityInputs{MailboxReachable: true, CanSpawnChildren: true},
			offered: []string{ToolSpawnSend, ToolSpawnStop},
		},
		{
			name:    "a thread with sub-agents of its own manages them with no spawn config",
			in:      CapabilityInputs{OwnChildren: true},
			offered: management,
		},
		{
			name:    "a branch that inherited sub-agents may only look",
			in:      CapabilityInputs{InheritedChildren: true},
			offered: []string{ToolSpawnStatus},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Permission = PermissionMutating
			caps := ResolveCapabilities(tc.in)
			for _, name := range management {
				assert.Equal(t, containsSorted(sortedUnique(tc.offered), name), caps.Offers(name), name)
			}
		})
	}
}

// A grant is offered on the next turn only within what the node declares.
func TestResolveCapabilities_GrantsAreIntersectedWithTheDeclaration(t *testing.T) {
	t.Parallel()

	in := CapabilityInputs{
		Access:     ResolveToolAccess([]string{ToolView}, []string{ToolGenerateImage}, nil),
		Permission: PermissionMutating,
		Grants:     []string{ToolGenerateImage, ToolWrite, "not_a_tool"},
	}
	caps := ResolveCapabilities(in)
	assert.True(t, caps.Offers(ToolGenerateImage), "a declared-loadable grant is offered")
	assert.False(t, caps.Offers(ToolWrite), "a grant the node does not declare loadable is not")
	assert.False(t, caps.Offers("not_a_tool"))
}

func TestResolveCapabilities_NoMachineOffersNothingThatNeedsOne(t *testing.T) {
	t.Parallel()

	caps := ResolveCapabilities(CapabilityInputs{
		Access:     ResolveToolAccess([]string{ToolView, ShellToolName, ToolFetch}, []string{LoadableWildcard}, []string{"mcp__s__t"}),
		Permission: PermissionMutating,
		NoMachine:  true,
		Grants:     []string{ToolWrite},
		MCPTools:   []string{"mcp__s__t"},
	})
	assert.Equal(t, []string{ToolFetch, ToolLoadTool}, caps.Offered)
	assert.Empty(t, caps.MCPTools, "MCP is daemon-placed")
	assert.False(t, caps.CanLoad(ToolEdit))
	assert.True(t, caps.CanLoad(ToolGenerateImage))
	assert.Equal(t, nomachine.Refusal(ToolView), caps.Explain(ToolView))
}

// Connection-gated integrations FAIL CLOSED: only those the owner can use are
// reachable, and the rest of what the declaration reaches is withheld — by
// integration, with a reason a refusal can repeat.
func TestResolveCapabilities_UnusableIntegrationsAreWithheld(t *testing.T) {
	t.Parallel()

	t.Run("an integration the owner can use is offered and loadable", func(t *testing.T) {
		t.Parallel()
		caps := ResolveCapabilities(CapabilityInputs{
			Access:             ResolveToolAccess([]string{"github__issue_get", ToolFetch}, []string{LoadableWildcard}, nil),
			Permission:         PermissionMutating,
			UsableIntegrations: map[string]bool{"github": true},
		})
		assert.True(t, caps.Offers("github__issue_get"))
		assert.True(t, caps.CanLoad("github__pr_get"))
		assert.NotContains(t, caps.WithheldIntegrations, "github")
		assert.Contains(t, caps.WithheldIntegrations, "slack", `"*" reaches Slack, which the owner has not connected`)
		assert.False(t, caps.CanLoad("slack__message_post"))
		assert.NotContains(t, caps.Deferred(), "slack__message_post")
		assert.Contains(t, caps.Explain("slack__message_post"), "needs a Slack connection")
		assert.True(t, caps.LoadableAll, `withholding needs no explicit list: "*" stays "*"`)
		assert.Empty(t, caps.Loadable)
	})

	t.Run("no answer withholds every gated integration reached", func(t *testing.T) {
		t.Parallel()
		caps := ResolveCapabilities(CapabilityInputs{
			Access:     ResolveToolAccess([]string{"tag:integration"}, nil, nil),
			Permission: PermissionMutating,
		})
		assert.False(t, caps.Offers("github__issue_get"))
		assert.True(t, caps.Offers("http__request"), "an integration that needs no connection is never gated")
		assert.Contains(t, caps.LoadRefusal("github__issue_get"), "needs a GitHub connection")
	})

	t.Run("only what the declaration reaches is recorded", func(t *testing.T) {
		t.Parallel()
		caps := ResolveCapabilities(CapabilityInputs{
			Access:     ResolveToolAccess(nil, []string{"github__issue_get", "slack__message_post", ToolFetch}, nil),
			Permission: PermissionMutating,
		})
		assert.Len(t, caps.WithheldIntegrations, 2)
		assert.Contains(t, caps.WithheldIntegrations, "github")
		assert.Contains(t, caps.WithheldIntegrations, "slack")
		assert.True(t, caps.CanLoad(ToolFetch), "an explicit list just loses the gated names")
		assert.False(t, caps.CanLoad("github__issue_get"))
	})

	t.Run("a declaration that reaches no gated tool withholds nothing", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, ReachedGatedIntegrations(ResolveToolAccess([]string{ToolFetch}, []string{ToolView}, nil)))
		caps := declaredCaps(PermissionMutating, []string{ToolFetch}, []string{ToolView}, nil)
		assert.Nil(t, caps.WithheldIntegrations)
	})
}

// request_machine exists only for a run with no machine: on a run that has
// one it is excluded however it is named — preloaded, matched by a glob or
// "*", loadable — and load_tool never grants, advertises or lists it on either.
func TestResolveCapabilities_RequestMachineOnlyWithoutAMachine(t *testing.T) {
	t.Parallel()

	for _, filter := range [][]string{{ToolRequestMachine}, {"*"}, {"request_*"}} {
		onMachine := ResolveCapabilities(CapabilityInputs{
			Access:     ResolveToolAccess(filter, []string{LoadableWildcard}, nil),
			Permission: PermissionOrchestrator,
		})
		assert.False(t, onMachine.Offers(ToolRequestMachine), "filter %v must not hand it to a run on a machine", filter)
		assert.False(t, onMachine.CanLoad(ToolRequestMachine))
		assert.NotContains(t, onMachine.Deferred(), ToolRequestMachine)
		assert.False(t, onMachine.Searchable(ToolRequestMachine))
		assert.Contains(t, onMachine.Explain(ToolRequestMachine), "only for a chat with no machine")
	}

	noMachine := ResolveCapabilities(CapabilityInputs{
		Access:     ResolveToolAccess([]string{ToolRequestMachine, ToolFetch}, []string{LoadableWildcard}, nil),
		Permission: PermissionMutating,
		NoMachine:  true,
	})
	assert.True(t, noMachine.Offers(ToolRequestMachine), "a no-machine run is handed it")
	assert.False(t, noMachine.Searchable(ToolRequestMachine), "handed over, never searched for")
	assert.NotContains(t, noMachine.Deferred(), ToolRequestMachine)

	withoutIt := &Capabilities{LoadableAll: true, Permission: PermissionMutating, NoMachine: true}
	assert.Contains(t, withoutIt.LoadRefusal(ToolRequestMachine), "is not loadable",
		"a node that was not handed it cannot load it either")
}

func TestCapabilities_ExplainSaysWhyAToolWasNotOffered(t *testing.T) {
	t.Parallel()

	caps := declaredCaps(PermissionMutating, []string{ToolView}, []string{ToolGenerateImage}, nil)
	assert.Empty(t, caps.Explain(ToolView), "offered")
	assert.Contains(t, caps.Explain(ToolGenerateImage), `load_tool(name="generate_image")`, "loadable: load it first")
	assert.Contains(t, caps.Explain(ToolWrite), "does not grant it", "not declared")
	assert.Contains(t, caps.Explain("hallucinated_tool"), "does not exist")
	assert.Contains(t, caps.Explain("mcp__s__t"), "not connected")
	assert.Contains(t, caps.Explain("spawn"), "Spawning sub-agents is not available")
}

func TestCapabilities_ProtoRoundTrip(t *testing.T) {
	t.Parallel()

	caps := ResolveCapabilities(CapabilityInputs{
		Access:             ResolveToolAccess([]string{ToolView, "github__issue_get"}, []string{ToolEdit, "mcp__s__t", "slack__message_post"}, []string{"mcp__s__t"}),
		Permission:         PermissionOrchestrator,
		MCPTools:           []string{"mcp__s__t"},
		SpawnPresets:       []string{"general"},
		UsableIntegrations: map[string]bool{"github": true},
	})
	require.Contains(t, caps.WithheldIntegrations, "slack")
	caps.RecordBoundParams(map[string]map[string]BoundParam{
		ToolView: {
			"limit": {Value: LiteralBinding(float64(5))},
			"repo":  {Value: ExprBinding("inputs.repo")},
			"pages": {Global: true},
		},
		"github__issue_get": {"repo": {Value: LiteralBinding(map[string]any{"owner": "o", "name": "n"})}},
		ToolEdit:            {},
	})
	back := CapabilitiesFromProto(caps.Proto())
	require.NotNil(t, back)
	assert.Equal(t, caps, back)
	assert.NotContains(t, back.BoundParams, ToolEdit, "a tool with nothing bound is not recorded")
}

// What a call runs with: the carried values as recorded, a global
// parameter's value from the setting re-read at execution, and a refusal —
// not an open parameter — when the setting no longer binds it.
func TestCapabilities_ExecutionBindings(t *testing.T) {
	t.Parallel()

	caps := &Capabilities{Permission: PermissionMutating}
	caps.RecordBoundParams(map[string]map[string]BoundParam{
		"http__request": {
			"url":     {Value: LiteralBinding("https://hooks.example.com/x")},
			"headers": {Global: true},
		},
	})
	require.True(t, caps.Binds("http__request"))
	require.True(t, caps.BindsFromGlobalSetting("http__request"))
	assert.False(t, caps.Binds(ToolView))

	headers := LiteralBinding(map[string]any{"Authorization": "Bearer t"})
	got, err := caps.ExecutionBindings("http__request", Bindings{"headers": headers, "method": LiteralBinding("PUT")})
	require.NoError(t, err)
	assert.Equal(t, Bindings{"url": LiteralBinding("https://hooks.example.com/x"), "headers": headers}, got,
		"only what was recorded is bound; a setting that now binds more does not reach this call")

	_, err = caps.ExecutionBindings("http__request", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'headers'")

	none, err := (*Capabilities)(nil).ExecutionBindings("http__request", nil)
	require.NoError(t, err, "a batch with no recorded set binds nothing")
	assert.Nil(t, none)
}

// An output from before the set existed normalizes to a zero-valued message,
// which must read as "no set", not as "a set that offers nothing".
func TestCapabilitiesFromProto_UnresolvedIsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, CapabilitiesFromProto(nil))
	assert.Nil(t, CapabilitiesFromProto((&Capabilities{}).Proto()))
}

func TestCapabilities_DeferredAgreesWithLoadRefusal(t *testing.T) {
	t.Parallel()

	caps := declaredCaps(PermissionMutating, []string{ToolView}, []string{LoadableWildcard}, []string{"mcp__s__t"})
	deferred := caps.Deferred()
	require.NotEmpty(t, deferred)
	assert.Contains(t, deferred, ToolGenerateImage)
	assert.Contains(t, deferred, "mcp__s__t")
	assert.NotContains(t, deferred, ToolView, "already offered")
	assert.NotContains(t, deferred, ToolStartRun, "above the tier")
	for _, name := range deferred {
		assert.Empty(t, caps.LoadRefusal(name), "advertised %q but loading refuses it", name)
	}
}

func TestCapabilitiesFrom_ContextRoundTrip(t *testing.T) {
	t.Parallel()

	assert.Nil(t, CapabilitiesFrom(context.Background()))
	caps := declaredCaps(PermissionMutating, []string{ToolView}, nil, nil)
	assert.Same(t, caps, CapabilitiesFrom(WithCapabilities(context.Background(), caps)))
	assert.Nil(t, CapabilitiesFrom(WithCapabilities(context.Background(), nil)))
}
