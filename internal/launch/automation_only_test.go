// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const automationWorkflowYAML = `
name: nightly-digest
apiVersion: v2
automation_only: true
inputs:
  model:
    type: model
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

func seedCompleteWorkflow(t *testing.T, ctx context.Context, repo db.Repository, slug, definition string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     launchTestUserID,
		Name:       slug,
		Slug:       slug,
		Definition: definition,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
		Version:    1,
	}))
}

// A workflow whose Chat trigger is off (automation_only) cannot be started
// from a chat: the composer would not offer it, and a client that asks anyway
// is refused before anything is written.
func TestLaunchChatStartRefusesAnAutomationOnlyWorkflow(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "nightly-digest", automationWorkflowYAML)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "nightly-digest",
		Params: mockModelParams(t), Messages: userSeed("go"),
	})
	require.Error(t, err)
	var validation *ValidationError
	require.True(t, errors.As(err, &validation), "got %T: %v", err, err)
	assert.Equal(t, ValidationFailedPrecondition, validation.Kind)
	assert.Contains(t, validation.Error(), "cannot be started from a chat")
	assert.Empty(t, starter.calls, "a refused launch starts nothing")

	chats, err := repo.ListChats(ctx, db.ChatFilters{UserID: launchTestUserID, ProjectID: &projectID})
	require.NoError(t, err)
	assert.Empty(t, chats, "a refused launch writes no chat")
}

// The Chat trigger gates only a chat start. A builder test run of the same
// workflow still starts: the author has to be able to try it.
func TestLaunchBuilderTestStartsAnAutomationOnlyWorkflow(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "nightly-digest", automationWorkflowYAML)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindBuilderTest, OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "nightly-digest",
		Params: mockModelParams(t), Messages: userSeed("go"),
	})
	require.NoError(t, err)
	assert.Len(t, starter.calls, 1)
}
