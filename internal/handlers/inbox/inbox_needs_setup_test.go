// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const needsSetupWorkflow = `name: review
inputs:
  repos:
    type: array
    required: true
triggers:
  - name: pr
    schedule: {cron: "0 9 * * *"}
    prompt: review
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func (f *inboxFixture) saveReviewWorkflow(t *testing.T, yaml string) {
	t.Helper()
	now := time.Now().UTC()
	existing, err := f.repo.GetWorkflowDraftBySlug(f.ctx, f.userID, "review")
	require.NoError(t, err)
	if existing != nil {
		require.NoError(t, f.repo.UpdateWorkflowDraftDefinition(f.ctx, existing.ID, "review", "review", yaml, db.WorkflowDraftStatusComplete))
		return
	}
	require.NoError(t, f.repo.CreateWorkflowDraft(f.ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: f.userID, Name: "review", Slug: "review",
		Definition: yaml, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *inboxFixture) activation(t *testing.T, id string, enabled bool, params map[string]any) {
	t.Helper()
	name := "pr"
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, f.repo.CreateTrigger(f.ctx, &core.Trigger{
		ID: id, UserID: f.userID, ProjectID: f.project(), Name: "Review " + id,
		Kind: core.TriggerKindSchedule, Enabled: enabled, Workflow: "review", WorkflowTrigger: &name,
		DaemonID: "d-" + id, Presets: map[string]string{}, Params: params, Config: cfg,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestListInbox_NeedsSetupOneItemPerAutomationAndClearsWhenSetupIsFinished(t *testing.T) {
	f := newInboxFixture(t)
	f.saveReviewWorkflow(t, needsSetupWorkflow)
	f.activation(t, "act-1", true, map[string]any{})
	f.activation(t, "act-2", true, map[string]any{"repos": []any{"acme/web"}})

	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP)
	require.Len(t, items, 1, "only the unfinished automation")
	item := items[0]
	assert.Equal(t, "act-1", item.TriggerId)
	assert.Equal(t, "automation_needs_setup:act-1:repos", item.ItemId)
	assert.Equal(t, f.project(), item.ProjectId, "the project and workflow locate the page's automation")
	assert.Equal(t, "review", item.WorkflowName)
	issues := item.GetAutomationNeedsSetup().GetIssues()
	require.Len(t, issues, 1)
	assert.Equal(t, "repos", issues[0].Input)
	assert.Equal(t, "repos is required", issues[0].Message)
	resp := f.list(t, nil)
	assert.True(t, resp.HasInformational)
	assert.EqualValues(t, 0, resp.BlockingCount, "it never counts toward the badge")

	// Finishing setup (the params now hold repos) clears it with no other action.
	row, err := f.repo.GetTrigger(f.ctx, "act-1")
	require.NoError(t, err)
	row.Params = map[string]any{"repos": []any{"acme/api"}}
	require.NoError(t, f.repo.UpdateTrigger(f.ctx, row))
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP))
}

func TestListInbox_NeedsSetupIgnoresPausedAndAdHocAutomations(t *testing.T) {
	f := newInboxFixture(t)
	f.saveReviewWorkflow(t, needsSetupWorkflow)
	f.activation(t, "paused", false, map[string]any{})
	f.trigger(t, "adhoc") // no workflow_trigger: nothing to set up

	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP))
}

// Dismissal holds for the inputs it was dismissed with; a workflow edit that
// makes the automation miss something new is news again.
func TestListInbox_NeedsSetupDismissalHoldsUntilItMissesSomethingNew(t *testing.T) {
	f := newInboxFixture(t)
	f.saveReviewWorkflow(t, needsSetupWorkflow)
	f.activation(t, "act-1", true, map[string]any{})
	first := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP)
	require.Len(t, first, 1)
	f.dismiss(t, first[0].ItemId)
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP))

	f.saveReviewWorkflow(t, `name: review
inputs:
  repos:
    type: array
    required: true
  team:
    type: string
    required: true
triggers:
  - name: pr
    schedule: {cron: "0 9 * * *"}
    prompt: review
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	again := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP)
	require.Len(t, again, 1)
	assert.Equal(t, "automation_needs_setup:act-1:repos,team", again[0].ItemId)
}
