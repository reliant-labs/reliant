// Copyright (c) 2025 Reliant Labs
package tools

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// unattendedCaps resolves a node's set for a run nobody is attending, with
// every integration usable so only the unattended rule is in play.
func unattendedCaps(permission string, preloaded, loadable []string, unattended bool) *Capabilities {
	return ResolveCapabilities(CapabilityInputs{
		Access:             ResolveToolAccess(preloaded, loadable, nil),
		Permission:         permission,
		Unattended:         unattended,
		UsableIntegrations: everyIntegrationUsable(),
	})
}

func everyIntegrationUsable() map[string]bool {
	usable := map[string]bool{}
	for _, m := range connectionGated() {
		usable[m.GetId()] = true
	}
	return usable
}

// mutatingActionTools is every exposed integration action whose manifest says
// it changes external state, read straight from the catalog.
func mutatingActionTools(t *testing.T) (mutating, readOnly []string) {
	t.Helper()
	cat, err := catalog.Builtin()
	require.NoError(t, err)
	for _, m := range cat.Manifests() {
		for _, a := range m.GetActions() {
			if !a.GetTool().GetExpose() {
				continue
			}
			if a.GetMutates() {
				mutating = append(mutating, manifest.ToolName(m, a))
			} else {
				readOnly = append(readOnly, manifest.ToolName(m, a))
			}
		}
	}
	require.NotEmpty(t, mutating, "the catalog ships mutating actions (Slack, GitHub, Gmail, Twilio, HTTP)")
	require.NotEmpty(t, readOnly)
	return mutating, readOnly
}

// What an unattended run is withheld, by category, and what it keeps: the read
// side of every category, and spawn, whose children inherit the restriction.
func TestUnattendedWithholding_Categories(t *testing.T) {
	t.Parallel()

	withheld := map[string]string{
		ToolCreateWorkflow:  "workflows",
		ToolEditWorkflow:    "workflows",
		ToolWriteWorkflow:   "workflows",
		ToolWriteScenario:   "workflows",
		ToolEditScenario:    "workflows",
		ToolDeleteScenario:  "workflows",
		ToolActivateTrigger: "activate triggers",
		ToolStartRun:        "other runs",
		ToolSendToRun:       "other runs",
		ToolControlRun:      "other runs",
	}
	for name, reason := range withheld {
		assert.Contains(t, UnattendedWithholding(name), reason, name)
	}
	for _, name := range []string{
		ToolListWorkflows, ToolGetWorkflow, ToolListScenarios, ToolViewScenario, ToolListTriggers,
		ToolListRuns, ToolGetRun, ToolSearchIntegrations, ToolGetIntegrationSchema, ToolGetSchema,
		// run_scenario runs the workflow against mocked activities only
		// (scenario/runner), so it changes nothing.
		ToolRunScenario,
		ToolSpawnStatus, ToolSpawnSend, ToolSpawnStop, ToolSpawn, ToolLoadTool, ToolView, ShellToolName,
	} {
		assert.Empty(t, UnattendedWithholding(name), "%s stays reachable for an unattended run", name)
	}

	mutating, readOnly := mutatingActionTools(t)
	for _, name := range mutating {
		assert.Contains(t, UnattendedWithholding(name), "integration actions that change something", name)
	}
	for _, name := range readOnly {
		assert.Empty(t, UnattendedWithholding(name), "read-only integration action %s stays reachable", name)
	}
}

// A tool added to the workflow or runs families that is not read-only has to
// be classified on purpose: withheld from unattended runs, or listed here with
// the reason it is safe for one.
func TestUnattendedWithholding_EveryWritingWorkflowOrRunsToolIsClassified(t *testing.T) {
	t.Parallel()

	safeUnattended := map[string]string{
		ToolRunScenario: "runs the workflow against mocked activities; nothing real executes",
	}
	for _, def := range GetToolRegistry() {
		writes := !slices.Contains(def.Tags, TagReadOnly)
		family := slices.Contains(def.Tags, TagWorkflow) || slices.Contains(def.Tags, TagRuns)
		if !writes || !family {
			continue
		}
		if _, safe := safeUnattended[def.Name]; safe {
			continue
		}
		assert.NotEmpty(t, UnattendedWithholding(def.Name),
			"%s changes workflows or runs: withhold it from unattended runs (unattended.go) or list why it is safe", def.Name)
	}
}

// A webhook-fired run whose step reaches the authoring tools only through a
// tag or "*" is not offered them, cannot load them, and is refused if it calls
// one anyway — with the reason. A person's turn, same declaration, is offered
// them.
func TestResolveCapabilities_UnattendedRunIsWithheldAuthoringTools(t *testing.T) {
	t.Parallel()

	preloaded := []string{"tag:workflow", ToolView}
	unattended := unattendedCaps(PermissionMutating, preloaded, []string{LoadableWildcard}, true)
	attended := unattendedCaps(PermissionMutating, preloaded, []string{LoadableWildcard}, false)

	for _, name := range []string{ToolCreateWorkflow, ToolEditWorkflow, ToolWriteWorkflow, ToolWriteScenario, ToolDeleteScenario} {
		assert.True(t, attended.Offers(name), "a person's turn is offered %s", name)

		assert.False(t, unattended.Offers(name), "an unattended run is not offered %s", name)
		assert.False(t, unattended.CanLoad(name), "nor may it load %s", name)
		assert.NotContains(t, unattended.Deferred(), name, "nor is %s advertised", name)
		assert.False(t, unattended.Searchable(name))
		assert.Contains(t, unattended.LoadRefusal(name), "unattended runs can't create or change workflows")
		assert.Contains(t, unattended.Explain(name), "unattended runs can't create or change workflows")
		assert.Contains(t, unattended.Explain(name), "Nobody is attending this run")
	}
	for _, name := range []string{ToolListWorkflows, ToolGetWorkflow, ToolListTriggers, ToolRunScenario, ToolView} {
		assert.True(t, unattended.Offers(name), "%s is still offered", name)
	}
	assert.True(t, unattended.Unattended)
	assert.Empty(t, unattended.UnattendedOptIn, "a tag names nothing")
	assert.False(t, attended.Unattended)

	// A grant made on an attended turn of the same thread cannot carry over.
	granted := ResolveCapabilities(CapabilityInputs{
		Access:     ResolveToolAccess([]string{ToolView}, []string{LoadableWildcard}, nil),
		Permission: PermissionMutating,
		Unattended: true,
		Grants:     []string{ToolEditWorkflow},
	})
	assert.False(t, granted.Offers(ToolEditWorkflow))
}

// The orchestrator-tier tools are withheld for being unattended, not only by
// the tier: an orchestrator reaching them through tags loses them.
func TestResolveCapabilities_UnattendedOrchestratorIsWithheldStandingWork(t *testing.T) {
	t.Parallel()

	caps := unattendedCaps(PermissionOrchestrator, []string{"tag:workflow", "tag:runs"}, nil, true)
	for _, name := range []string{ToolActivateTrigger, ToolStartRun, ToolSendToRun, ToolControlRun} {
		assert.False(t, caps.Offers(name), name)
		assert.Contains(t, caps.Explain(name), "unattended runs can't", name)
	}
	assert.True(t, caps.Offers(ToolListRuns))
	assert.True(t, caps.Offers(ToolListTriggers))

	attended := unattendedCaps(PermissionOrchestrator, []string{"tag:workflow", "tag:runs"}, nil, false)
	assert.True(t, attended.Offers(ToolActivateTrigger))
	assert.True(t, attended.Offers(ToolStartRun))
}

// No mutating integration action is offered to, or loadable by, an unattended
// run that reaches it through tag:integration or "*" — every one the catalog
// marks `mutates`, so a new integration is covered without editing this test.
// Read-only actions are untouched, and a person's turn gets every one.
func TestResolveCapabilities_UnattendedRunIsWithheldMutatingIntegrationActions(t *testing.T) {
	t.Parallel()

	mutating, readOnly := mutatingActionTools(t)
	unattended := unattendedCaps(PermissionMutating, []string{"tag:integration"}, []string{LoadableWildcard}, true)
	attended := unattendedCaps(PermissionMutating, []string{"tag:integration"}, []string{LoadableWildcard}, false)

	for _, name := range mutating {
		assert.True(t, attended.Offers(name), "a person's turn is offered %s", name)
		assert.False(t, unattended.Offers(name), "a webhook-fired run must not be offered %s", name)
		assert.False(t, unattended.CanLoad(name), "nor load %s", name)
		assert.Contains(t, unattended.Explain(name), "integration actions that change something outside Reliant")
	}
	for _, name := range readOnly {
		assert.True(t, unattended.Offers(name), "read-only %s stays offered", name)
	}
}

// Naming a tool exactly in the step's declaration — preloaded or loadable —
// keeps it for an unattended run. A tag, a glob or "*" does not, and a name the
// same list excludes is no name.
func TestResolveCapabilities_ExplicitlyNamedToolsSurviveUnattended(t *testing.T) {
	t.Parallel()

	t.Run("preloaded by name", func(t *testing.T) {
		t.Parallel()
		caps := unattendedCaps(PermissionMutating, []string{ToolEditWorkflow, "tag:workflow", "slack__message_post"}, nil, true)
		assert.True(t, caps.Offers(ToolEditWorkflow))
		assert.True(t, caps.Offers("slack__message_post"), "an integration action is opted in the same way")
		assert.False(t, caps.Offers(ToolWriteWorkflow), "its neighbours from the tag are still withheld")
		assert.Equal(t, []string{ToolEditWorkflow, "slack__message_post"}, caps.UnattendedOptIn)
	})

	t.Run("loadable by name", func(t *testing.T) {
		t.Parallel()
		caps := unattendedCaps(PermissionMutating, []string{ToolView}, []string{ToolEditWorkflow}, true)
		assert.False(t, caps.Offers(ToolEditWorkflow), "loadable, not preloaded")
		assert.True(t, caps.CanLoad(ToolEditWorkflow))
		assert.Contains(t, caps.Explain(ToolEditWorkflow), "Load it first")

		next := ResolveCapabilities(CapabilityInputs{
			Access:     ResolveToolAccess([]string{ToolView}, []string{ToolEditWorkflow}, nil),
			Permission: PermissionMutating,
			Unattended: true,
			Grants:     []string{ToolEditWorkflow},
		})
		assert.True(t, next.Offers(ToolEditWorkflow), "once loaded, offered on the next turn")
	})

	t.Run("a name beside * still counts", func(t *testing.T) {
		t.Parallel()
		caps := unattendedCaps(PermissionMutating, []string{ToolView}, []string{LoadableWildcard, ToolCreateWorkflow}, true)
		assert.True(t, caps.CanLoad(ToolCreateWorkflow))
		assert.False(t, caps.CanLoad(ToolEditWorkflow))
	})

	t.Run("a glob, a tag or * is not a name", func(t *testing.T) {
		t.Parallel()
		for _, filter := range [][]string{{"*_workflow"}, {"tag:workflow"}, {"*"}, {"edit_workflow", "!edit_workflow"}} {
			caps := unattendedCaps(PermissionMutating, filter, nil, true)
			assert.False(t, caps.Offers(ToolEditWorkflow), "filter %v", filter)
			assert.Empty(t, caps.UnattendedOptIn, "filter %v", filter)
		}
	})

	t.Run("a name is still capped by the tier", func(t *testing.T) {
		t.Parallel()
		caps := unattendedCaps(PermissionMutating, []string{ToolActivateTrigger}, nil, true)
		assert.False(t, caps.Offers(ToolActivateTrigger))
		assert.Contains(t, caps.Explain(ToolActivateTrigger), "requires 'orchestrator' permission")
	})
}

func TestResolveToolAccess_NamedIsWhatEitherListSpellsOut(t *testing.T) {
	t.Parallel()

	access := ResolveToolAccess(
		[]string{ToolView, "tag:workflow", "list_*", "spawn:builtin://agent(general)", ToolWrite, "!" + ToolWrite},
		[]string{ToolEditWorkflow, "tag:runs"}, nil)
	assert.Equal(t, []string{ToolEditWorkflow, ToolView}, access.Named)

	assert.Equal(t, []string{ToolStartRun},
		ResolveToolAccess(nil, []string{LoadableWildcard, ToolStartRun}, nil).Named)
	assert.Empty(t, ResolveToolAccess(nil, nil, nil).Named)
}

// The unattended fact and the opt-ins cross history with the set, so
// execute_tools and load_tool refuse what call_llm withheld.
func TestCapabilities_UnattendedProtoRoundTrip(t *testing.T) {
	t.Parallel()

	caps := unattendedCaps(PermissionMutating, []string{ToolEditWorkflow, "tag:workflow"}, []string{LoadableWildcard}, true)
	require.Equal(t, []string{ToolEditWorkflow}, caps.UnattendedOptIn)
	back := CapabilitiesFromProto(caps.Proto())
	require.NotNil(t, back)
	assert.Equal(t, caps, back)
	assert.False(t, back.CanLoad(ToolWriteWorkflow), "the restriction survives the round trip")
	assert.True(t, back.Offers(ToolEditWorkflow))
}
