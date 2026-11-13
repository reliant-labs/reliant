// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// endRun reports the chat's launch run ended failed with errText.
func (f *streakFixture) endRun(chatID, errText string) {
	f.t.Helper()
	in := WorkflowStatusInput{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: "failed", Thread: chatID,
		Error: errText, LaunchRun: true,
	}
	var out WorkflowStatusOutput
	require.NoError(f.t, f.h.ExecuteActivity(NewWorkflowStatusActivity(f.h.Repo()).Execute, in, &out))
}

func (f *streakFixture) trigger() *core.Trigger {
	f.t.Helper()
	tr, err := f.h.Repo().GetTrigger(context.Background(), f.triggerID)
	require.NoError(f.t, err)
	return tr
}

// A trigger whose unattended run an org policy ended is paused ONCE, with
// paused_reason org_policy: a second refused run (the next tick) finds it
// paused and changes nothing. A run that failed for any other reason pauses
// nothing.
func TestWorkflowStatus_PausesATriggerAnOrgPolicyRefusedOnce(t *testing.T) {
	t.Parallel()
	f := newStreakFixtureOfKind(t, core.TriggerKindSchedule, false)
	refused := "step call_llm failed: " + (&orgpolicy.Refusal{Reason: orgpolicy.ReasonOrgSpendCapReached}).Error()

	other := f.launch(core.TriggerKindSchedule.EventKind(), db.Active())
	f.endRun(other, "step call_llm failed: exit status 1")
	require.True(t, f.trigger().Enabled, "an ordinary failure pauses nothing")

	first := f.launch(core.TriggerKindSchedule.EventKind(), db.Active())
	f.endRun(first, refused)
	tr := f.trigger()
	require.False(t, tr.Enabled)
	assert.Equal(t, core.TriggerPausedOrgPolicy, tr.PausedReason)
	pausedAt := tr.UpdatedAt

	second := f.launch(core.TriggerKindSchedule.EventKind(), db.Active())
	f.endRun(second, refused)
	tr = f.trigger()
	assert.False(t, tr.Enabled)
	assert.Equal(t, core.TriggerPausedOrgPolicy, tr.PausedReason)
	assert.Equal(t, pausedAt, tr.UpdatedAt, "the second refusal changed nothing: one pause, not one per tick")
}
