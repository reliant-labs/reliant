// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const runNowAutomationOnlyYAML = `name: nightly-digest
apiVersion: v2
automation_only: true
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`

const runNowDeclaringYAML = `name: triage
apiVersion: v2
inputs:
  issue_number:
    type: integer
    default: 0
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.data.issue.number > 1000"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage #{{ trigger.payload.data.issue.number }}."
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`

func (f *builderTestFixture) saveWorkflow(t *testing.T, owner, slug, definition string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateWorkflowDraft(f.ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: owner, Name: slug, Slug: slug,
		Definition: definition, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))
}

// seedText is the text of the chat's first user message, which is what the run
// starts from.
func (f *builderTestFixture) seedText(t *testing.T, chatID string) string {
	t.Helper()
	msgs, err := f.repo.ListMessages(f.ctx, chatID, db.MessageListOptions{Limit: 100})
	require.NoError(t, err)
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_USER {
			continue
		}
		blocks, err := f.repo.ListContentBlocks(f.ctx, m.ID)
		require.NoError(t, err)
		var text string
		for _, b := range blocks {
			if b.Content != nil {
				text += *b.Content
			}
		}
		return text
	}
	t.Fatalf("chat %s has no user message", chatID)
	return ""
}

func (f *builderTestFixture) runNow(req *reliantv1.RunNowRequest) (*connect.Response[reliantv1.RunNowResponse], error) {
	if req.ProjectId == "" {
		req.ProjectId = f.projectID
	}
	return f.service.RunNow(f.ctx, connect.NewRequest(req))
}

// Run now records a manual.run launch, attended and deduped on the chat id, and
// lists in the sidebar like the chat the person is about to open.
func TestRunNow_RecordsManualRunKind(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "triage", runNowDeclaringYAML)

	resp, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "triage", Message: "look at the backlog"})
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	assert.Equal(t, "manual.run", f.launchKind(t, chatID))
	event, err := f.repo.GetTriggerEventByChatID(f.ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventKindManualRun, event.Kind)
	assert.False(t, event.Kind.Unattended(), "a person pressed the button")
	assert.Equal(t, chatID, event.DedupeKey)
	assert.Equal(t, "triage", event.Payload["workflow"])
	assert.Nil(t, event.Payload["replay"], "a plain run replays nothing")

	chat, err := f.repo.GetChat(f.ctx, chatID)
	require.NoError(t, err)
	assert.NotNil(t, chat.AdoptedAt, "the person who pressed the button owns the run")
	assert.Equal(t, core.ChatOriginManualRun, chat.Origin)
	assert.True(t, chat.ListInSidebar, "so it lists in the sidebar like the chat they are about to open")
	assert.NotNil(t, resp.Msg.GetChat().AdoptedAt)
}

// Pressing Run now is how an automation-only workflow is run by hand; the same
// workflow is still refused as a chat start.
func TestRunNow_RunsAnAutomationOnlyWorkflow(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "nightly-digest", runNowAutomationOnlyYAML)

	_, err := f.start("nightly-digest", nil, nil)
	require.Error(t, err, "a chat start of an automation-only workflow stays refused")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))

	resp, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "nightly-digest"})
	require.NoError(t, err)
	assert.Equal(t, "manual.run", f.launchKind(t, resp.Msg.GetChat().GetId()))
}

// A run with no message starts from a stated default rather than failing the
// launcher's "a user message is required".
func TestRunNow_NoMessageStartsFromTheDefault(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "nightly-digest", runNowAutomationOnlyYAML)

	resp, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "nightly-digest"})
	require.NoError(t, err)
	assert.Equal(t, runNowDefaultMessage, f.seedText(t, resp.Msg.GetChat().GetId()))
}

// A replay seeds the run with the event as the labelled block a real fire uses,
// and the launch event names the event it replayed.
func TestRunNow_ReplayRecordsAndSeedsTheEvent(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "triage", runNowDeclaringYAML)
	payload, err := structpb.NewStruct(map[string]any{"data": map[string]any{"issue": map[string]any{"number": 7}}})
	require.NoError(t, err)

	resp, err := f.runNow(&reliantv1.RunNowRequest{
		Workflow: "triage",
		Message:  "Triage #7.",
		Replay: &reliantv1.ReplayEvent{
			TriggerName: "new-issue", EventId: "ev-1", Kind: "integration",
			OccurredAt: "2026-10-20T09:00:00Z", Payload: payload,
		},
	})
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	event, err := f.repo.GetTriggerEventByChatID(f.ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventKindManualRun, event.Kind)
	replay, ok := event.Payload["replay"].(map[string]any)
	require.True(t, ok, "payload: %v", event.Payload)
	assert.Equal(t, "ev-1", replay["event_id"])
	assert.Equal(t, "new-issue", replay["trigger_name"])
	assert.Equal(t, "integration", replay["kind"])

	seeded := f.seedText(t, chatID)
	assert.Contains(t, seeded, "Triage #7.")
	assert.Contains(t, seeded, `<trigger_event kind="integration"`)
	assert.Contains(t, seeded, `"number":7`)
}

// A replay of a kind a trigger never receives is refused, not recorded.
func TestRunNow_ReplayOfANonTriggerKindIsRefused(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "triage", runNowDeclaringYAML)

	for _, kind := range []string{"chat.start", "manual.run", "agent.start_run", "bogus"} {
		_, err := f.runNow(&reliantv1.RunNowRequest{
			Workflow: "triage",
			Replay:   &reliantv1.ReplayEvent{TriggerName: "new-issue", Kind: kind},
		})
		require.Error(t, err, kind)
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), kind)
	}
}

// Another user's workflow does not resolve for the caller: owner-only.
func TestRunNow_IsOwnerOnly(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "someone-else", "theirs", runNowAutomationOnlyYAML)

	_, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "theirs"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "the same refusal StartChat gives an unresolvable workflow: %v", err)
	assert.Contains(t, err.Error(), "theirs")

	chats, err := f.repo.ListChats(f.ctx, db.ChatFilters{ProjectID: &f.projectID})
	require.NoError(t, err)
	assert.Empty(t, chats, "a refused run writes no chat")
}

func TestRunNow_RequiresProjectAndWorkflow(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	_, err := f.service.RunNow(f.ctx, connect.NewRequest(&reliantv1.RunNowRequest{Workflow: "x"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = f.service.RunNow(f.ctx, connect.NewRequest(&reliantv1.RunNowRequest{ProjectId: f.projectID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// manual.run is not a launch kind StartChat can be asked for: the request has no
// field for it, and no flag a client can set produces it.
func TestStartChat_CannotClaimManualRun(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "triage", runNowDeclaringYAML)

	resp, err := f.start("triage", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "chat.start", f.launchKind(t, resp.Msg.GetChat().GetId()))

	fields := (&reliantv1.StartChatRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"launch_kind", "kind", "manual_run", "replay"} {
		assert.Nil(t, fields.ByName(protoreflect.Name(name)), "StartChatRequest must not carry %q", name)
	}
}

// RunNow reads the caller from auth, not from the request.
func TestRunNow_UsesTheAuthenticatedUser(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "nightly-digest", runNowAutomationOnlyYAML)

	other := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	_, err := f.service.RunNow(other, connect.NewRequest(&reliantv1.RunNowRequest{ProjectId: f.projectID, Workflow: "nightly-digest"}))
	require.Error(t, err, "another user cannot run test-user's workflow, or in test-user's project")
}

const runNowRequiredYAML = `name: review
apiVersion: v2
inputs:
  repos:
    type: array
    required: true
    min_items: 1
    max_items: 2
  guidance:
    type: string
    default: ""
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`

// Run now launches with the form's inputs, so a required input the person left
// empty, or a list outside its bounds, is refused before a run exists.
func TestRunNow_EnforcesRequiredAndItemBounds(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveWorkflow(t, "test-user", "review", runNowRequiredYAML)
	repos := func(items ...any) map[string]*structpb.Value {
		list, err := structpb.NewList(items)
		require.NoError(t, err)
		return map[string]*structpb.Value{"repos": structpb.NewListValue(list)}
	}

	for name, tc := range map[string]struct {
		inputs map[string]*structpb.Value
		want   string
	}{
		"a required input left out": {nil, "required input 'repos' is not provided"},
		"fewer than min_items":      {repos(), "at least 1"},
		"more than max_items":       {repos("a/one", "a/two", "a/three"), "at most 2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "review", Inputs: tc.inputs})
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	resp, err := f.runNow(&reliantv1.RunNowRequest{Workflow: "review", Inputs: repos("a/one")})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.GetChat().GetId())
}
