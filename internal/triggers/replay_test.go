// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

const replayWorkflow = `name: triage
inputs:
  issue_number:
    type: integer
    default: 0
  label:
    type: string
    default: "none"
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.data.issue.number > 1000"
    origin_filter: "trigger.origin.caused_by_reliant"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage #{{ trigger.payload.data.issue.number }} from {{ trigger.sender.display_name }}."
  - name: nightly
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func parseReplayWorkflow(t *testing.T, src string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return wf
}

func issuePayload(number int) map[string]any {
	return map[string]any{"data": map[string]any{"issue": map[string]any{"number": number}}}
}

// A replayed event goes through the declared trigger's inputs and prompt, and
// NOT its filter or origin_filter: this event has number 7, which the filter
// (> 1000) would turn away, and no Reliant origin, which the origin_filter
// would too.
func TestMapReplay_MapsInputsAndPromptWithoutApplyingFilters(t *testing.T) {
	wf := parseReplayWorkflow(t, replayWorkflow)

	got, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, Replay{
		TriggerName: "new-issue",
		EventID:     "ev-1",
		Kind:        core.TriggerEventKindIntegration,
		Payload:     issuePayload(7),
		Sender:      &core.TriggerSender{Kind: "github", ID: "42", DisplayName: "octocat"},
	})
	require.NoError(t, err)

	assert.EqualValues(t, 7, got.Inputs["issue_number"])
	assert.Equal(t, "Triage #7 from octocat.", got.Prompt)
	_, mapped := got.Inputs["label"]
	assert.False(t, mapped, "only the inputs the declaration maps come back; the rest are the person's to set")
}

// The payload is the person's to edit, and the mapping reads the edited one.
func TestMapReplay_ReadsTheEditedPayload(t *testing.T) {
	wf := parseReplayWorkflow(t, replayWorkflow)
	replay := Replay{TriggerName: "new-issue", Kind: core.TriggerEventKindIntegration, Payload: issuePayload(7),
		Sender: &core.TriggerSender{DisplayName: "octocat"}}

	first, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, replay)
	require.NoError(t, err)
	replay.Payload = issuePayload(99)
	second, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, replay)
	require.NoError(t, err)

	assert.EqualValues(t, 7, first.Inputs["issue_number"])
	assert.EqualValues(t, 99, second.Inputs["issue_number"])
}

// A trigger with no inputs or prompt maps to nothing, and a workflow with one
// declared trigger needs none named.
func TestMapReplay_NamesTheTrigger(t *testing.T) {
	wf := parseReplayWorkflow(t, replayWorkflow)

	// Two declared triggers: the replay must say which.
	_, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, Replay{Payload: issuePayload(1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name the one the event arrived through")

	_, err = MapReplay(context.Background(), nil, "u1", "triage", wf, nil, Replay{TriggerName: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no trigger named "nope"`)

	nightly, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, Replay{TriggerName: "nightly", Kind: core.TriggerEventKindSchedule})
	require.NoError(t, err)
	assert.Empty(t, nightly.Inputs)
	assert.Empty(t, nightly.Prompt)

	single := parseReplayWorkflow(t, strings.Replace(replayWorkflow, "  - name: nightly\n    schedule: {cron: \"0 9 * * *\"}\n", "", 1))
	got, err := MapReplay(context.Background(), nil, "u1", "triage", single, nil, Replay{
		Kind: core.TriggerEventKindIntegration, Payload: issuePayload(3), Sender: &core.TriggerSender{DisplayName: "o"},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.Inputs["issue_number"])
}

// A workflow that declares no trigger has nothing to replay through.
func TestMapReplay_NoDeclaredTrigger(t *testing.T) {
	wf := parseReplayWorkflow(t, "name: plain\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n")
	_, err := MapReplay(context.Background(), nil, "u1", "plain", wf, nil, Replay{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no triggers")
}

// A payload the prompt cannot read is a message the person can act on, not a
// started run with a half-rendered prompt.
func TestMapReplay_PayloadTheTemplateCannotReadIsAnError(t *testing.T) {
	wf := parseReplayWorkflow(t, replayWorkflow)
	_, err := MapReplay(context.Background(), nil, "u1", "triage", wf, nil, Replay{
		TriggerName: "new-issue", Kind: core.TriggerEventKindIntegration, Payload: map[string]any{},
	})
	require.Error(t, err)
}

// The replayed event reaches the agent as the same labelled, delimited block
// a real fire uses.
func TestSeedWithReplay_UsesTheTriggerEventBlock(t *testing.T) {
	seed := SeedWithReplay("Triage #7.", Replay{
		Kind: core.TriggerEventKindIntegration, Payload: issuePayload(7),
	})
	assert.True(t, strings.HasPrefix(seed, "Triage #7.\n\n<trigger_event kind=\"integration\""), seed)
	assert.Contains(t, seed, `"number":7`)
	assert.True(t, strings.HasSuffix(seed, "</trigger_event>"))
}
