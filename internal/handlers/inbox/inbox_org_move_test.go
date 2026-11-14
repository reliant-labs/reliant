// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// An automation a project move left paused is listed as needing setup until
// someone sets it up in its new org and turns it on: one whose source-org
// connection binding the move cleared says to choose a connection, one that
// did not check out says to check it. A pause the person chose is not.
func TestListInbox_AMovePausedAutomationNeedsSetup(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "t-rebind")
	f.trigger(t, "t-check")
	f.trigger(t, "t-person")
	for id, reason := range map[string]string{
		"t-rebind": core.TriggerPausedOrgMoveSetup,
		"t-check":  core.TriggerPausedOrgMove,
	} {
		paused, err := f.repo.PauseTrigger(f.ctx, id, reason)
		require.NoError(t, err)
		require.True(t, paused)
	}
	require.NoError(t, f.repo.SetTriggerEnabled(f.ctx, "t-person", false))

	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP)
	byTrigger := map[string]*reliantv1.InboxItem{}
	for _, it := range items {
		byTrigger[it.TriggerId] = it
	}
	require.Len(t, byTrigger, 2, "the two the move paused, not the person's")

	rebind := byTrigger["t-rebind"]
	require.NotNil(t, rebind)
	assert.Equal(t, "automation_needs_setup:t-rebind:"+core.TriggerPausedOrgMoveSetup, rebind.ItemId)
	assert.Equal(t, f.project(), rebind.ProjectId)
	issues := rebind.GetAutomationNeedsSetup().GetIssues()
	require.Len(t, issues, 1)
	assert.Equal(t, "connection", issues[0].Input)
	assert.Contains(t, issues[0].Message, "choose a connection")

	check := byTrigger["t-check"]
	require.NotNil(t, check)
	assert.Contains(t, check.GetAutomationNeedsSetup().GetIssues()[0].Message, "check its setup")

	// Turning it on, once it is set up, clears it.
	require.NoError(t, f.repo.SetTriggerEnabled(f.ctx, "t-rebind", true))
	for _, it := range inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP) {
		assert.NotEqual(t, "t-rebind", it.TriggerId)
	}
}
