// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A run reaching a terminal state is the signal workflow-event triggers fire
// from. The outbox row is written by the WorkflowStatus activity in the SAME
// transaction as the terminal status, exactly once per (run, outcome), and
// only for an owner who has an enabled workflow_event trigger.

type runEventFixture struct {
	h       *IdempotencyTestHelper
	userID  string
	project string
	chatID  string
}

func newRunEventFixture(t *testing.T, withTrigger bool) *runEventFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	f := &runEventFixture{h: h, userID: uuid.NewString(), project: uuid.NewString(), chatID: uuid.NewString()}
	h.CreateTestProject(ctx, f.project, f.userID)
	h.CreateTestChat(ctx, f.chatID, f.project, f.userID)
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: f.chatID, ChatID: f.chatID, WorkflowName: "code-review", Thread: f.chatID, Status: db.Active(),
	}))
	if withTrigger {
		allowWorkflowEventKind(t, h)
		now := time.Now().UTC()
		require.NoError(t, h.Repo().CreateTrigger(ctx, &core.Trigger{
			ID: uuid.NewString(), UserID: f.userID, ProjectID: f.project, Name: "on-review",
			Kind: runevents.TriggerKind, Enabled: true, Workflow: "builtin://agent",
			Message: "react", DaemonID: "d1", Config: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
		}))
	}
	return f
}

// allowWorkflowEventKind widens this test database's triggers.kind CHECK to
// accept workflow_event. The production migration that adds the kind belongs
// to the trigger-receivers stream (one migration for all new kinds, so their
// CHECK constraints cannot conflict); once it is on main this is a no-op and
// should be deleted.
func allowWorkflowEventKind(t *testing.T, h *IdempotencyTestHelper) {
	t.Helper()
	_, err := h.DB().Exec(`ALTER TABLE triggers DROP CONSTRAINT IF EXISTS triggers_kind_check;
		ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check CHECK (kind IN ('schedule', 'webhook', 'integration', 'workflow_event'))`)
	require.NoError(t, err)
}

func (f *runEventFixture) finish(t *testing.T, status, outcome, errText string) {
	t.Helper()
	input := WorkflowStatusInput{
		ChatID: f.chatID, WorkflowID: f.chatID, WorkflowName: "code-review",
		Status: status, Thread: f.chatID, Outcome: outcome, Error: errText,
	}
	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, input, &out))
}

func (f *runEventFixture) events(t *testing.T) []*core.RunEvent {
	t.Helper()
	rows, err := f.h.Repo().ClaimRunEvents(context.Background(), time.Now().UTC(), time.Now().UTC().Add(time.Minute), 100)
	require.NoError(t, err)
	var mine []*core.RunEvent
	for _, ev := range rows {
		if ev.ChatID == f.chatID {
			mine = append(mine, ev)
		}
	}
	return mine
}

func TestWorkflowStatus_RootCompletionEmitsOneFinishedRunEvent(t *testing.T) {
	f := newRunEventFixture(t, true)
	f.finish(t, "completed", "", "")
	// The activity is retried by Temporal on any failure after commit; the
	// retry must not record the transition twice.
	f.finish(t, "completed", "", "")

	evs := f.events(t)
	require.Len(t, evs, 1, "one finished event per (run, outcome)")
	ev := evs[0]
	assert.Equal(t, core.RunEventFinished, ev.Outcome)
	assert.Equal(t, f.userID, ev.UserID)
	assert.Equal(t, f.chatID, ev.Payload["run_id"])
	assert.Equal(t, "code-review", ev.Payload["workflow"])
	assert.Equal(t, "finished", ev.Payload["outcome"])
}

func TestWorkflowStatus_DeclaredFailureAndFailedStatusEmitFailed(t *testing.T) {
	f := newRunEventFixture(t, true)
	f.finish(t, "completed", model.OutcomeFailure, "")
	evs := f.events(t)
	require.Len(t, evs, 1)
	assert.Equal(t, core.RunEventFailed, evs[0].Outcome, "a declared failure outcome is a failure")

	g := newRunEventFixture(t, true)
	g.finish(t, "failed", "", "daemon unavailable")
	evs = g.events(t)
	require.Len(t, evs, 1)
	assert.Equal(t, core.RunEventFailed, evs[0].Outcome)
	assert.Equal(t, "daemon unavailable", evs[0].Payload["error"])
}

func TestWorkflowStatus_NoRunEventWithoutAWorkflowEventTrigger(t *testing.T) {
	f := newRunEventFixture(t, false)
	f.finish(t, "completed", "", "")
	assert.Empty(t, f.events(t), "owners with no workflow_event trigger pay nothing")
}

// A run becomes BLOCKED when its first approval or question goes pending. A
// second pending item does not block it again: firing per item would turn one
// stuck run into a burst of triggered runs.
func TestApprovalAndQuestionCreate_EmitBlockedOnceForTheFirstPendingItem(t *testing.T) {
	f := newRunEventFixture(t, true)

	var approvalOut ApprovalCreateOutput
	require.NoError(t, f.h.ExecuteActivity(NewApprovalCreateActivity(f.h.Repo()).Execute, ApprovalCreateInput{
		ChatID: f.chatID, WorkflowID: f.chatID, TemporalWorkflowID: f.chatID, StepID: "gate", Title: "Deploy to production?",
	}, &approvalOut))

	metadata := `{"type":"ask_user","questions":[{"question":"Which region?","options":[]}]}`
	var questionOut QuestionCreateOutput
	require.NoError(t, f.h.ExecuteActivity(NewQuestionCreateActivity(f.h.Repo()).Execute, QuestionCreateInput{
		ChatID: f.chatID, WorkflowID: f.chatID, TemporalWorkflowID: f.chatID, ThreadID: f.chatID, StepID: "ask", Metadata: &metadata,
	}, &questionOut))

	evs := f.events(t)
	require.Len(t, evs, 1, "the run was blocked once, by the approval; the question did not re-block it")
	ev := evs[0]
	assert.Equal(t, core.RunEventBlocked, ev.Outcome)
	assert.Equal(t, "approval", ev.Payload["blocked_on"])
	assert.Equal(t, approvalOut.ApprovalID, ev.Payload["blocker_id"])
	assert.Equal(t, "Deploy to production?", ev.Payload["prompt"])
}

func TestQuestionCreate_EmitsBlockedWithTheQuestionText(t *testing.T) {
	f := newRunEventFixture(t, true)
	metadata := `{"type":"ask_user","questions":[{"question":"Which region?","options":[]}]}`
	var out QuestionCreateOutput
	require.NoError(t, f.h.ExecuteActivity(NewQuestionCreateActivity(f.h.Repo()).Execute, QuestionCreateInput{
		ChatID: f.chatID, WorkflowID: f.chatID, TemporalWorkflowID: f.chatID, ThreadID: f.chatID, StepID: "ask", Metadata: &metadata,
	}, &out))

	evs := f.events(t)
	require.Len(t, evs, 1)
	assert.Equal(t, core.RunEventBlocked, evs[0].Outcome)
	assert.Equal(t, "question", evs[0].Payload["blocked_on"])
	assert.Equal(t, "Which region?", evs[0].Payload["prompt"])
}

func TestWorkflowStatus_ChildAndCancelledEmitNoRunEvent(t *testing.T) {
	f := newRunEventFixture(t, true)
	input := WorkflowStatusInput{
		ChatID: f.chatID, WorkflowID: uuid.NewString(), ParentWorkflowID: f.chatID,
		WorkflowName: "child", Status: "completed", Thread: f.chatID + "/child",
	}
	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, input, &out))
	f.finish(t, "cancelled", "", "")
	assert.Empty(t, f.events(t), "only a ROOT run finishing or failing is a workflow event")
}
