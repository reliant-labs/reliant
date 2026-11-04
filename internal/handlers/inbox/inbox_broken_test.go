// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The workflow `review` with its `pr` trigger renamed: every activation of
// `pr` is broken.
const renamedTriggerWorkflow = `name: review
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: review
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func brokenItems(items []*reliantv1.InboxItem) []*reliantv1.InboxItem {
	var out []*reliantv1.InboxItem
	for _, it := range inboxByKind(items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING) {
		if it.GetAutomationFailing().GetHealth().GetStatus() == reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN {
			out = append(out, it)
		}
	}
	return out
}

// A workflow change that breaks an enabled activation reaches the owner on
// their next look at the inbox, before the activation ever fires again: a
// weekly schedule or a rare webhook would otherwise say nothing for days.
func TestListInbox_BrokenActivationIsListedBeforeItFires(t *testing.T) {
	f := newInboxFixture(t)
	f.saveReviewWorkflow(t, `name: review
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
	f.activation(t, "act-on", true, map[string]any{})
	f.activation(t, "act-off", false, map[string]any{})
	require.Empty(t, brokenItems(f.list(t, nil).Items), "nothing is broken yet")

	f.saveReviewWorkflow(t, renamedTriggerWorkflow)
	items := brokenItems(f.list(t, nil).Items)
	require.Len(t, items, 1, "the enabled activation only: a paused one is the person's choice")
	item := items[0]
	assert.Equal(t, "act-on", item.TriggerId)
	assert.True(t, strings.HasPrefix(item.ItemId, "automation_failing:broken:act-on:"), item.ItemId)
	assert.Equal(t, f.project(), item.ProjectId)
	assert.Equal(t, "review", item.WorkflowName)
	assert.Contains(t, item.GetAutomationFailing().GetHealth().GetLastFailureDetail(), `no longer declares a trigger named "pr"`)
	assert.Contains(t, item.GetAutomationFailing().GetHealth().GetLastFailureDetail(), "nightly", "it says what the workflow declares now")

	// Deleting the workflow is a different reason, so a different item.
	f.dismiss(t, item.ItemId)
	require.Empty(t, brokenItems(f.list(t, nil).Items), "dismissed")
	require.NoError(t, f.repo.DeleteWorkflowDraftBySlug(f.ctx, f.userID, "review"))
	again := brokenItems(f.list(t, nil).Items)
	require.Len(t, again, 1, "a new reason is news again")
	assert.NotEqual(t, item.ItemId, again[0].ItemId)
	assert.Contains(t, again[0].GetAutomationFailing().GetHealth().GetLastFailureDetail(), "does not resolve")
}

// A broken activation that does fire records a failed launch each time. Those
// would list it again as "could not start" (and, after two, as failing) with
// a less useful reason; the broken item is the one that stays.
func TestListInbox_BrokenSupersedesTheSameAutomationsFailures(t *testing.T) {
	f := newInboxFixture(t)
	f.saveReviewWorkflow(t, renamedTriggerWorkflow)
	f.activation(t, "act-1", true, map[string]any{})
	f.trigger(t, "adhoc")
	base := time.Now().UTC().Add(-time.Hour)
	f.firing(t, "ev1", "act-1", core.TriggerEventFailed, "declared trigger broken", base)
	f.firing(t, "ev2", "act-1", core.TriggerEventFailed, "declared trigger broken", base.Add(time.Minute))
	f.firing(t, "ev3", "adhoc", core.TriggerEventFailed, "daemon gone", base)

	items := f.list(t, nil).Items
	require.Len(t, brokenItems(items), 1)
	for _, it := range items {
		if it.TriggerId != "act-1" {
			continue
		}
		assert.True(t, strings.HasPrefix(it.ItemId, "automation_failing:broken:"), "only the broken item lists act-1, got %s", it.ItemId)
	}
	launchFailed := inboxByKind(items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, launchFailed, 1, "another automation's failure is untouched")
	assert.Equal(t, "adhoc", launchFailed[0].TriggerId)
}
