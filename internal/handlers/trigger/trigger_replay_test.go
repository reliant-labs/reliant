// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// issue-opened payloads are the shape the GitHub integration delivers.
func issuePayloadStruct(t *testing.T, number int) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(map[string]any{"data": map[string]any{"issue": map[string]any{"number": number}}})
	require.NoError(t, err)
	return s
}

const replayDeclaringYAML = `name: triage
inputs:
  issue_number:
    type: integer
    default: 0
triggers:
  - name: new-issue
    integration: {integration: test, events: [issues.opened]}
    filter: "trigger.payload.data.issue.number > 1000"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage #{{ trigger.payload.data.issue.number }}."
  - name: nightly
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func (e *triggerTestEnv) seedReplayEvent(t *testing.T, triggerID string, at time.Time, kind core.TriggerEventKind, payload map[string]any) string {
	t.Helper()
	id := uuid.NewString()
	created, err := e.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID: id, TriggerID: &triggerID, UserID: e.userID, Kind: kind,
		DedupeKey: uuid.NewString(), OccurredAt: at, Payload: payload,
		Outcome: core.TriggerEventSkipped, OutcomeDetail: "filtered out", CreatedAt: at,
	})
	require.NoError(t, err)
	require.True(t, created)
	return id
}

// PreviewReplay maps the event through the declared trigger's inputs and prompt
// and does NOT apply its filter: issue 7 is below the filter's threshold of
// 1000, which is exactly the event someone wants to replay.
func TestPreviewReplay_MapsInputsAndPromptWithoutTheFilter(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", replayDeclaringYAML)

	resp, err := env.svc.PreviewReplay(env.ctx, connect.NewRequest(&reliantv1.PreviewReplayRequest{
		ProjectId: env.projectID,
		Workflow:  "triage",
		Replay: &reliantv1.ReplayEvent{
			TriggerName: "new-issue", Kind: "integration", Payload: issuePayloadStruct(t, 7),
		},
	}))
	require.NoError(t, err)

	assert.EqualValues(t, 7, resp.Msg.GetInputs()["issue_number"].GetNumberValue())
	assert.Equal(t, "Triage #7.", resp.Msg.GetPrompt())
}

// The edited payload is what is mapped; an event kind a trigger never receives
// is refused; a trigger the workflow does not declare is refused.
func TestPreviewReplay_RefusesWhatItCannotMap(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", replayDeclaringYAML)

	for name, tc := range map[string]struct {
		replay *reliantv1.ReplayEvent
		code   connect.Code
		reason string
	}{
		"a kind no trigger receives": {&reliantv1.ReplayEvent{TriggerName: "new-issue", Kind: "chat.start"}, connect.CodeInvalidArgument, "not an event a trigger receives"},
		"an undeclared trigger":      {&reliantv1.ReplayEvent{TriggerName: "nope"}, connect.CodeInvalidArgument, `no trigger named "nope"`},
		"no trigger named, several":  {&reliantv1.ReplayEvent{}, connect.CodeInvalidArgument, "name the one the event arrived through"},
		"a bad timestamp":            {&reliantv1.ReplayEvent{TriggerName: "new-issue", OccurredAt: "yesterday"}, connect.CodeInvalidArgument, "RFC3339"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.svc.PreviewReplay(env.ctx, connect.NewRequest(&reliantv1.PreviewReplayRequest{
				ProjectId: env.projectID, Workflow: "triage", Replay: tc.replay,
			}))
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err), err.Error())
			assert.Contains(t, err.Error(), tc.reason)
		})
	}

	_, err := env.svc.PreviewReplay(env.ctx, connect.NewRequest(&reliantv1.PreviewReplayRequest{ProjectId: env.projectID, Workflow: "triage"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "replay is required")
}

// A workflow the caller does not own does not resolve for them.
func TestPreviewReplay_ResolvesTheWorkflowForTheCaller(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", replayDeclaringYAML)

	other := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.PreviewReplay(other, connect.NewRequest(&reliantv1.PreviewReplayRequest{
		ProjectId: env.projectID, Workflow: "triage",
		Replay: &reliantv1.ReplayEvent{TriggerName: "new-issue", Kind: "integration", Payload: issuePayloadStruct(t, 7)},
	}))
	require.Error(t, err)
}

// ListReplayEvents merges the events of the caller's activations of the
// workflow's triggers, newest first, labelled with the trigger they arrived
// through, and reads nobody else's.
func TestListReplayEvents_ListsTheCallersActivationsEventsNewestFirst(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", replayDeclaringYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	issues := env.create(t, env.activation("new-issue", func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &conn.ID }))
	nightly := env.create(t, env.activation("nightly", nil))

	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	oldest := env.seedReplayEvent(t, issues.Id, base, core.TriggerEventKindIntegration, map[string]any{"n": 1})
	middle := env.seedReplayEvent(t, nightly.Id, base.Add(time.Hour), core.TriggerEventKindSchedule, map[string]any{"n": 2})
	newest := env.seedReplayEvent(t, issues.Id, base.Add(2*time.Hour), core.TriggerEventKindIntegration, map[string]any{"n": 3})

	// Another user's trigger on a workflow of the same name must not appear.
	otherUser := uuid.NewString()
	foreign := uuid.NewString()
	require.NoError(t, env.repo.CreateTrigger(context.Background(), &core.Trigger{
		ID: foreign, UserID: otherUser, ProjectID: env.projectID, Name: "theirs", Kind: core.TriggerKindSchedule,
		Workflow: "triage", WorkflowTrigger: strPtr("nightly"), Enabled: true, ActingIdentity: core.ActingIdentityOwner,
		Config: []byte(`{"cron":["0 9 * * *"]}`),
	}))
	_, err := env.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &foreign, UserID: otherUser, Kind: core.TriggerEventKindSchedule,
		DedupeKey: uuid.NewString(), OccurredAt: base.Add(3 * time.Hour), Payload: map[string]any{}, Outcome: core.TriggerEventSkipped,
	})
	require.NoError(t, err)

	resp, err := env.svc.ListReplayEvents(env.ctx, connect.NewRequest(&reliantv1.ListReplayEventsRequest{
		ProjectId: env.projectID, Workflow: "triage",
	}))
	require.NoError(t, err)

	var gotIDs, gotTriggers []string
	for _, c := range resp.Msg.GetEvents() {
		gotIDs = append(gotIDs, c.GetEvent().GetId())
		gotTriggers = append(gotTriggers, c.GetTriggerName())
	}
	assert.Equal(t, []string{newest, middle, oldest}, gotIDs)
	assert.Equal(t, []string{"new-issue", "nightly", "new-issue"}, gotTriggers)
	assert.Equal(t, reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_INTEGRATION, resp.Msg.GetEvents()[0].GetEvent().GetKind())
	assert.EqualValues(t, 3, resp.Msg.GetEvents()[0].GetEvent().GetPayload().AsMap()["n"])
}

func TestListReplayEvents_LimitsAndHandlesNoActivations(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", replayDeclaringYAML)

	empty, err := env.svc.ListReplayEvents(env.ctx, connect.NewRequest(&reliantv1.ListReplayEventsRequest{ProjectId: env.projectID, Workflow: "triage"}))
	require.NoError(t, err)
	assert.Empty(t, empty.Msg.GetEvents(), "a workflow nobody enabled has no events, and that is not an error")

	nightly := env.create(t, env.activation("nightly", nil))
	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		env.seedReplayEvent(t, nightly.Id, base.Add(time.Duration(i)*time.Hour), core.TriggerEventKindSchedule, map[string]any{})
	}
	limited, err := env.svc.ListReplayEvents(env.ctx, connect.NewRequest(&reliantv1.ListReplayEventsRequest{ProjectId: env.projectID, Workflow: "triage", Limit: 2}))
	require.NoError(t, err)
	assert.Len(t, limited.Msg.GetEvents(), 2)

	_, err = env.svc.ListReplayEvents(env.ctx, connect.NewRequest(&reliantv1.ListReplayEventsRequest{ProjectId: env.projectID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "workflow is required")
}

func strPtr(s string) *string { return &s }
