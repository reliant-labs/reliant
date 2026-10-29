package trigger

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestActivationSnapshotRejectsCycleWithoutUnboundedExpansion(t *testing.T) {
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	root, err := wfyaml.ParseWorkflow([]byte("name: cyclic\nentry: [again]\nnodes:\n - {id: again, type: workflow, ref: project://cyclic}\n"))
	require.NoError(t, err)
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}))
	_, err = svc.activationSnapshot(context.Background(), "owner", &pb.TriggerDefinition{Workflow: "project://cyclic"}, root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validation")
}

func TestActivationSnapshotEffectiveInputsWithholdSecretButShowPrivileges(t *testing.T) {
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	repo.GetPresetBySlugFunc = func(context.Context, string, string) (*db.Preset, error) {
		return &db.Preset{Name: "operator", Params: map[string]interface{}{"tools": []any{"shell"}, "message": "preset-secret"}}, nil
	}
	root, err := wfyaml.ParseWorkflow([]byte(`name: privilege-test
inputs:
  tools: {type: tools, default: [view]}
  message: {type: string, default: ""}
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: "{{inputs.tools}}"
        loadable_tools: []
`))
	require.NoError(t, err)
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}))
	snap, err := svc.activationSnapshot(context.Background(), "owner", &pb.TriggerDefinition{Workflow: "project://privilege-test", Presets: map[string]string{"default": "operator"}}, root)
	require.NoError(t, err)
	require.Equal(t, []any{"shell"}, snap.EffectiveInputs["tools"])
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "shell")
	require.NotContains(t, summary, "preset-secret")
}

// eventPinned is the pr-review shape: a GitHub trigger whose repository is
// bounded by the activation's repos input, feeding an input the agent's tools
// are pinned to, and a thread message that reads node outputs.
const eventPinned = `name: event-pinned
inputs:
  repos: {type: github.repository, multi: true, required: true}
  repo: {type: string, default: ''}
  pr_number: {type: integer, default: 0}
triggers:
  - name: queued
    integration:
      integration: github
      events: [pull_request.labeled]
      match: {repository: {input: repos}}
    inputs:
      repo: '{{ trigger.payload.attributes.repository }}'
      pr_number: '{{ trigger.payload.data.pull_request.number }}'
    prompt: review it
entry: [pr]
nodes:
  - id: pr
    type: action
    uses: github/pr.get@1
    with:
      owner: "{{ inputs.repo.split('/')[0] }}"
      repo: "{{ inputs.repo.split('/')[1] }}"
      pull_number: '{{ inputs.pr_number }}'
  - id: review
    type: workflow
    ref: builtin://autopilot-agent
    thread:
      mode: new
      inject: {role: user, content: 'Review head {{ nodes.pr.data.head.sha }}'}
    args:
      tools: [github__pr_get]
      tool_params:
        github__pr_get:
          owner: "{{ inputs.repo.split('/')[0] }}"
          repo: "{{ inputs.repo.split('/')[1] }}"
edges:
  - {from: pr, default: review}
`

func snapshotFor(t *testing.T, yaml, trigger string, params map[string]any) (*activationSnapshot, error) {
	t.Helper()
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	root, err := wfyaml.ParseWorkflow([]byte(yaml))
	require.NoError(t, err)
	def := &pb.TriggerDefinition{Workflow: "project://" + root.GetName(), Params: map[string]*structpb.Value{}}
	if trigger != "" {
		def.Source = &pb.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: trigger}
	}
	for k, v := range params {
		value, err := structpb.NewValue(v)
		require.NoError(t, err)
		def.Params[k] = value
	}
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}))
	return svc.activationSnapshot(context.Background(), "owner", def, root)
}

func TestActivationSnapshotBoundsEventPinnedTargetsByTheMatchInput(t *testing.T) {
	snap, err := snapshotFor(t, eventPinned, "queued", map[string]any{"repos": []any{"acme/api", "acme/web"}})
	require.NoError(t, err)
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "Event-supplied input repo: set by each event; bounded: the trigger matches only events whose repository is one of input repos = [acme/api, acme/web]")
	require.Contains(t, summary, "Event-supplied input pr_number: set by each event; unbounded")
	// Every value the event can bring is resolved and disclosed, not just one.
	require.Contains(t, summary, "targets: owner=acme; repo=api")
	require.Contains(t, summary, "targets: owner=acme; repo=web")
	require.NotContains(t, summary, "repo=other")
}

func TestActivationSnapshotEventValueWinsOverAnActivationParam(t *testing.T) {
	// The trigger's mapping replaces a param of the same name at run time, so
	// the card must not disclose the param's value as the target.
	snap, err := snapshotFor(t, eventPinned, "queued", map[string]any{"repos": []any{"acme/api"}, "repo": "evil/elsewhere"})
	require.NoError(t, err)
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "repo=api")
	require.NotContains(t, summary, "elsewhere")
}

func TestActivationSnapshotWithoutTheDeclaredTriggerStillRefusesUnresolvableTargets(t *testing.T) {
	// No declared trigger, no bound: an input the graph cannot resolve still
	// fails closed rather than being approved blind.
	_, err := snapshotFor(t, eventPinned, "", map[string]any{"repos": []any{"acme/api"}})
	require.ErrorContains(t, err, "must resolve before activation approval")
}

func TestActivationSnapshotDisclosesUnrestrictedToolsInsteadOfRefusing(t *testing.T) {
	snap, err := snapshotFor(t, `name: wide-agent
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: [view, mcp__github__create_issue]
`, "", nil)
	require.NoError(t, err)
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "Agent tool privileges: UNRESTRICTED — this workflow's agent has unrestricted tools")
	require.Contains(t, summary, "* (every tool)")
	require.Contains(t, summary, "mcp__github__create_issue")
}

func TestActivationSnapshotRefusesAPrivilegeAnUnboundedEventChooses(t *testing.T) {
	// The event names the child workflow. No default may stand in for it: a
	// benign default would be approved and a later event could pick another.
	yaml := `name: event-chooses
inputs:
  agent: {type: string, default: project://harmless}
triggers:
  - name: hook
    integration: {integration: github, events: [issues.opened]}
    inputs: {agent: '{{ trigger.payload.data.issue.title }}'}
    prompt: go
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: '{{ inputs.agent }}'
`
	_, err := snapshotFor(t, yaml, "hook", nil)
	require.ErrorContains(t, err, "must resolve before activation approval")

	tools := `name: event-tools
inputs:
  tools: {type: string, default: view}
triggers:
  - name: hook
    integration: {integration: github, events: [issues.opened]}
    inputs: {tools: '{{ trigger.payload.data.issue.title }}'}
    prompt: go
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: ["{{ inputs.tools }}"]
        loadable_tools: []
`
	_, err = snapshotFor(t, tools, "hook", nil)
	require.ErrorContains(t, err, "fixed privilege choices")
}

func TestActivationSnapshotRefusesAPrivilegeAnEventInputSelectsByComparison(t *testing.T) {
	// The resolved value carries no trace of the event ("view"), so only the
	// reference to the event input can show that an event decides the tools.
	conditional := `name: event-mode
inputs:
  mode: {type: string, default: safe}
triggers:
  - name: hook
    integration: {integration: github, events: [issues.opened]}
    inputs: {mode: '{{ trigger.payload.data.issue.title }}'}
    prompt: go
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: "{{ inputs.mode == 'admin' ? ['shell'] : ['view'] }}"
        loadable_tools: []
        permission: "{{ inputs.mode == 'admin' ? 'mutating' : 'readonly' }}"
`
	_, err := snapshotFor(t, conditional, "hook", nil)
	require.ErrorContains(t, err, "fixed privilege choices")

}

func TestActivationSnapshotDisclosesEventDerivedTargetsAsDynamic(t *testing.T) {
	// Evaluated against the sentinel, the conditional picks "sandbox"; an event
	// with mode=admin targets production. The card must show neither.
	yaml := `name: event-target
inputs:
  mode: {type: string, default: safe}
triggers:
  - name: hook
    integration: {integration: github, events: [issues.opened]}
    inputs: {mode: '{{ trigger.payload.data.issue.title }}'}
    prompt: go
entry: [act]
nodes:
  - id: act
    type: action
    uses: github/issue.create@1
    with:
      owner: acme
      repo: "{{ inputs.mode == 'admin' ? 'production' : 'sandbox' }}"
      title: hello
    edges: []
  - id: agent
    type: workflow
    ref: builtin://autopilot-agent
    args:
      tools: [github__pr_get]
      tool_params:
        github__pr_get:
          owner: acme
          repo: "{{ inputs.mode == 'admin' ? 'production' : 'sandbox' }}"
`
	yaml = strings.Replace(yaml, "    edges: []\n", "", 1) + "edges:\n  - {from: act, default: agent}\n"
	snap, err := snapshotFor(t, yaml, "hook", nil)
	require.NoError(t, err)
	summary := strings.Join(snap.Summary, "\n")
	require.NotContains(t, summary, "sandbox")
	require.NotContains(t, summary, "production")
	require.Contains(t, summary, "owner=acme; repo="+unknownScope)
}

func TestEventInputContextsRefuseAnOversizedProductBeforeBuildingIt(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`name: wide
inputs:
  repos: {type: github.repository, multi: true}
  orgs: {type: string, multi: true}
  repo: {type: string, default: ''}
  org: {type: string, default: ''}
triggers:
  - name: hook
    integration:
      integration: github
      events: [issues.opened]
      match: {repository: {input: repos}, organization: {input: orgs}}
    inputs:
      repo: '{{ trigger.payload.attributes.repository }}'
      org: '{{ trigger.payload.attributes.organization }}'
    prompt: go
entry: [done]
nodes:
  - {id: done, type: save_message, args: {role: assistant, content: ok}}
`))
	require.NoError(t, err)
	many := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = fmt.Sprintf("acme/r%d", i)
		}
		return out
	}
	wt := wf.GetTriggers()[0]
	_, _, err = eventInputContexts(wt, wf, map[string]any{"repos": many(8), "orgs": many(8)})
	require.NoError(t, err, "8 x 8 is exactly the limit")
	_, _, err = eventInputContexts(wt, wf, map[string]any{"repos": many(100000), "orgs": many(100000)})
	require.ErrorContains(t, err, "exceed the activation approval limit")
}
