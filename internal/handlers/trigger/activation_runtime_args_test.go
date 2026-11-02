package trigger

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/stretchr/testify/require"
)

// A value only a run has — an earlier node's output, the event, the run's own
// workflow context — is tainted like an unbounded event input
// (activation_event_inputs.go): it may feed a child's inputs, never choose a
// child, preset or tool, and is never approved as whatever it would resolve
// to before the run.

// reviewShaped is control-plane's pr-review shape: a GitHub-triggered review
// whose agents are a static builtin child, handed their model, system prompt
// and response schema by earlier nodes, with tools and their targets fixed.
const reviewShaped = `name: review-shaped
inputs:
  repos: {type: github.repository, multi: true, required: true}
  repo: {type: string, default: ''}
  pr_number: {type: integer, default: 0}
triggers:
  - name: opened
    integration:
      integration: github
      events: [pull_request.opened]
      match: {repository: {input: repos}}
    inputs:
      repo: '{{ trigger.payload.attributes.repository }}'
      pr_number: '{{ trigger.payload.data.pull_request.number }}'
    prompt: review it
entry: [lane]
nodes:
  - id: lane
    type: compute
    outputs:
      model: "{{ 'flagship' }}"
      tools: "{{ ['github__pr_get'] }}"
  - id: brief
    type: compute
    outputs:
      text: "{{ 'review ' + inputs.repo }}"
      system: "{{ 'you review' }}"
      schema: "{{ {'type': 'object'} }}"
  - id: review
    type: workflow
    ref: builtin://autopilot-agent
    condition: "nodes.lane.model != ''"
    thread:
      mode: new
      inject: {role: user, content: '{{ nodes.brief.text }}'}
    args:
      model: '{{ nodes.lane.model }}'
      max_turns: 100
      tools: [github__pr_get, github__pr_list_files]
      tool_params:
        github__pr_get:
          owner: "{{ inputs.repo.split('/')[0] }}"
          repo: "{{ inputs.repo.split('/')[1] }}"
          pull_number: '{{ inputs.pr_number }}'
      system_prompt: '{{ nodes.brief.system }}'
      response_tool_name: submit_review
      response_schema: '{{ nodes.brief.schema }}'
edges:
  - {from: lane, default: brief}
  - {from: brief, default: review}
`

func TestActivationSnapshotApprovesAChildWhoseArgsReadNodeOutputs(t *testing.T) {
	snap, err := snapshotFor(t, reviewShaped, "opened", map[string]any{"repos": []any{"acme/api"}})
	require.NoError(t, err, "a static child handed node outputs as inputs is approvable")
	summary := strings.Join(snap.Summary, "\n")
	// The child's fixed privileges are still resolved and disclosed.
	require.Contains(t, summary, "Agent tool privileges: github__pr_get, github__pr_list_files;")
	require.Contains(t, snap.Workflows, "builtin://autopilot-agent")
}

func TestActivationSnapshotRefusesAChildOrPresetChosenAtRunTime(t *testing.T) {
	child := func(field string) string {
		return strings.Replace(`name: picks-at-run-time
entry: [lane]
nodes:
  - id: lane
    type: compute
    outputs: {ref: "{{ 'builtin://autopilot-agent' }}"}
  - id: child
    type: workflow
    FIELD
edges:
  - {from: lane, default: child}
`, "FIELD", field, 1)
	}
	for name, tc := range map[string]struct{ yaml, trigger, want string }{
		"ref from a node output": {
			yaml: child("ref: '{{ nodes.lane.ref }}'"),
			want: `Workflow node "child": ref depends on a node output`,
		},
		// The approval's empty nodes namespace would resolve the fallback; the
		// run resolves the node's output.
		"ref from a node output with a benign fallback": {
			yaml: child(`ref: "{{ nodes.?lane.?ref.orValue('builtin://autopilot-agent') }}"`),
			want: `Workflow node "child": ref depends on a node output`,
		},
		"preset from a node output": {
			yaml: child("ref: builtin://autopilot-agent\n    presets: {default: '{{ nodes.lane.ref }}'}"),
			want: `Workflow node "child": presets.default depends on a node output`,
		},
		"ref from the run's workflow context": {
			yaml: child(`ref: "{{ workflow.branch == 'main' ? 'builtin://autopilot-agent' : 'builtin://autopilot-agent' }}"`),
			want: `Workflow node "child": ref depends on`,
		},
		"ref from the event directly": {
			yaml: `name: event-ref
triggers:
  - name: hook
    integration: {integration: github, events: [issues.opened]}
    prompt: go
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: '{{ trigger.payload.data.issue.title }}'
`,
			trigger: "hook",
			want:    `Workflow node "child": ref depends on`,
		},
		"loop ref from a node output": {
			yaml: strings.Replace(child("ref: '{{ nodes.lane.ref }}'"), "type: workflow", "type: loop\n    while: 'false'", 1),
			want: `Loop node "child": ref depends on a node output`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := snapshotFor(t, tc.yaml, tc.trigger, nil)
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "must resolve before activation approval")
		})
	}
}

func TestActivationSnapshotRefusesToolPrivilegesChosenAtRunTime(t *testing.T) {
	// A fallback the approval would resolve ("view") is not what the run
	// grants: the node's output picks the tools.
	_, err := snapshotFor(t, `name: run-time-tools
entry: [plan]
nodes:
  - id: plan
    type: compute
    outputs: {tools: "{{ ['shell'] }}"}
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: "{{ nodes.?plan.?tools.orValue(['view']) }}"
        loadable_tools: []
edges:
  - {from: plan, default: agent}
`, "", nil)
	require.ErrorContains(t, err, `Agent node "agent": tools_config.preloaded_tools depends on a node output`)
	require.ErrorContains(t, err, "fixed privilege choices")
}

// A node output that feeds a child input which chooses a privilege is
// refused like an event value would be; one that feeds a target is disclosed
// as dynamic. Neither is approved as the narrower value the approval could
// compute.
func TestActivationSnapshotNodeOutputArgsFeedingChildPrivilegesAreNeverNarrowed(t *testing.T) {
	withArg := func(arg string) string {
		return strings.Replace(reviewShaped, "      tools: [github__pr_get, github__pr_list_files]\n", arg, 1)
	}
	t.Run("tools", func(t *testing.T) {
		_, err := snapshotFor(t, withArg("      tools: '{{ nodes.lane.tools }}'\n"), "opened", map[string]any{"repos": []any{"acme/api"}})
		require.ErrorContains(t, err, "fixed privilege choices")
	})
	t.Run("tools with a benign fallback", func(t *testing.T) {
		_, err := snapshotFor(t, withArg("      tools: \"{{ nodes.?lane.?tools.orValue(['github__pr_get']) }}\"\n"), "opened", map[string]any{"repos": []any{"acme/api"}})
		require.ErrorContains(t, err, "fixed privilege choices")
	})
}

func TestActivationSnapshotDisclosesNodeOutputTargetsAsDynamic(t *testing.T) {
	// Resolved before the run, the fallback names "sandbox"; the run acts on
	// whatever the node returned. The card must show neither.
	snap, err := snapshotFor(t, `name: run-time-target
entry: [pick]
nodes:
  - id: pick
    type: compute
    outputs: {repo: "{{ 'production' }}"}
  - id: act
    type: action
    uses: github/issue.create@1
    with:
      owner: acme
      repo: "{{ nodes.?pick.?repo.orValue('sandbox') }}"
      title: hello
edges:
  - {from: pick, default: act}
`, "", nil)
	require.NoError(t, err)
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "owner=acme; repo="+unknownScope)
	require.NotContains(t, summary, "sandbox")
}

// Builtins that pass node outputs, loop items and the run's id to their
// children, or name their own definition in a spawn, stay approvable.
func TestActivationSnapshotApprovesBuiltinsThatReadRunTimeValues(t *testing.T) {
	for _, name := range []string{"agent", "autopilot-agent", "autopilot-issue-worker", "autopilot-oncall", "autopilot-review-responder", "autopilot-security-review"} {
		t.Run(name, func(t *testing.T) {
			raw, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
			require.NoError(t, err)
			snap, err := snapshotFor(t, string(raw), "", nil)
			require.NoError(t, err)
			if name == "autopilot-issue-worker" {
				// Its agent runs in a checkout an earlier step made: disclosed, not resolved.
				require.Contains(t, snap.Summary, "Child workflow working directory: chosen at run time on the activation's machine (dynamic/unbounded)")
			}
		})
	}
}

func TestReferencesRuntime(t *testing.T) {
	for text, want := range map[string]bool{
		"builtin://nodes-runner":                  false, // a literal, not a template
		"{{ inputs.repo }}":                       false,
		"{{ workflow.inputs.repo }}":              false,
		"{{ [spawn(workflow.name, inputs.p)] }}":  false,
		"{{ output.text }}":                       true,
		"{{ 'nodes.lane is only text' }}":         false,
		"{{ nodes.lane.model }}":                  true,
		"{{ has(nodes.lane) ? 'a' : 'b' }}":       true,
		"{{ trigger.payload.data.number }}":       true,
		"{{ iter.item }}":                         true,
		"{{ outputs.completed }}":                 true,
		"{{ failure.message }}":                   true,
		"{{ workflow.path }}":                     true,
		"{{ workflow }}":                          true,
		"prefix {{ inputs.a }} {{ nodes.b.c }}":   true,
		"{{ inputs.a + }}":                        true, // does not parse: could read anything
		"{{ items.exists(x, x == inputs.repo) }}": false,
		"{{ inputs.list.map(n, n.name).join() }}": false,
		"{{ {'a': {'b': nodes.x.y}}['a']['b'] }}": true,
	} {
		require.Equal(t, want, referencesRuntime(text), text)
	}
}

// DB-backed: a project child whose integration action authenticates with a
// connection its parent hands it from a node output holds the widest
// connection capability, never the one the approval could compute.
func TestAgentActivationNodeOutputConnectionWidensTheChildsCapability(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval with a project child workflow")
	e := setupTriggerTest(t)
	e.saveWorkflow(t, "commenter", `name: commenter
inputs:
  conn: {type: string, default: ''}
entry: [comment]
nodes:
  - id: comment
    type: action
    uses: github/issue.comment@1
    connection: '{{ inputs.conn }}'
    with: {owner: acme, repo: api, issue_number: 1, body: hi}
`)
	e.saveWorkflow(t, "hands-connection", `name: hands-connection
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: go
entry: [pick]
nodes:
  - id: pick
    type: compute
    outputs: {conn: "{{ 'work' }}"}
  - id: child
    type: workflow
    ref: project://commenter
    args:
      conn: "{{ nodes.?pick.?conn.orValue('personal') }}"
edges:
  - {from: pick, default: child}
`)
	p := tools.ActivationParams{Name: "hands connection", Workflow: "hands-connection", WorkflowTrigger: "nightly", ProjectID: e.projectID, MachineID: &e.daemonID}
	preview, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.NotContains(t, preview.Title, "personal")
	created, err := e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p, preview.Hash)
	require.NoError(t, err)
	history, err := e.repo.ListTriggerChanges(e.ctx, e.userID, created.ID)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	require.Contains(t, history[0].CapabilitiesAdded, "connection:"+dynamicCapability)
	require.NotContains(t, history[0].CapabilitiesAdded, "connection:personal")
}
