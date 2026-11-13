package inbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attentionFiring(t *testing.T, f *inboxFixture, id, trigger string, status db.WorkflowStatus, at time.Time) {
	t.Helper()
	f.launched(t, "event-"+id, id, trigger, at, status)
}

func applyInboxAttention(t *testing.T, f *inboxFixture, chat, run, key, phase, state string, at time.Time, timeout time.Duration) {
	t.Helper()
	result, err := f.repo.ApplyAttentionUpdate(f.ctx, core.AttentionUpdate{
		ChatID: chat, RunID: run, Key: key, Phase: phase, State: state, StartedAt: at, RecoveryTimeout: timeout,
		FailureClass: "unsuccessful", Detail: "Needs a person",
	})
	require.NoError(t, err)
	require.True(t, result.Managed)
}

func TestInboxAttention_IncidentRecoveryDismissalAndExpiry(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "attention-trigger")
	at := time.Now().UTC().Add(-time.Minute)
	attentionFiring(t, f, "failed-job", "attention-trigger", db.Failed(), at)
	applyInboxAttention(t, f, "failed-job", "actor-1", "pr:42", "finish", "action_required", at, 0)
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1)
	id := items[0].ItemId
	require.Contains(t, id, "automation_failing:incident:")
	assert.Equal(t, "pr:42", items[0].GetAutomationFailing().IncidentKey)
	assert.Equal(t, "unsuccessful", items[0].GetAutomationFailing().FailureClass)
	assert.Equal(t, f.project(), items[0].ProjectId)
	assert.Equal(t, "failed-job", items[0].GetAutomationFailing().GetLastRunChatId())
	// Trigger health remains failed even though human attention has a separate state.
	assert.EqualValues(t, 1, items[0].GetAutomationFailing().Health.ConsecutiveFailures)
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{id}}))
	require.NoError(t, err)
	assert.Empty(t, f.list(t, nil).Items)
	attentionFiring(t, f, "repair-job", "attention-trigger", db.Active(), at.Add(time.Second))
	applyInboxAttention(t, f, "repair-job", "actor-2", "pr:42", "start", "recovering", at.Add(time.Second), time.Hour)
	assert.Empty(t, f.list(t, nil).Items, "a live recovery is not human attention")
	applyInboxAttention(t, f, "repair-job", "actor-2", "pr:42", "finish", "resolved", at.Add(time.Second), 0)
	assert.Empty(t, f.list(t, nil).Items)
	// A fresh action_required episode is not hidden by the previous dismissal.
	attentionFiring(t, f, "new-job", "attention-trigger", db.Failed(), at.Add(2*time.Second))
	applyInboxAttention(t, f, "new-job", "actor-3", "pr:42", "finish", "action_required", at.Add(2*time.Second), 0)
	items = inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1)
	assert.NotEqual(t, id, items[0].ItemId)
	// A different key stays independent; expired recovering is projected by the store.
	attentionFiring(t, f, "dead-repair", "attention-trigger", db.Active(), at.Add(3*time.Second))
	applyInboxAttention(t, f, "dead-repair", "actor-4", "pr:43", "start", "recovering", at.Add(3*time.Second), time.Second)
	items = inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 2)
	classes := []string{items[0].GetAutomationFailing().FailureClass, items[1].GetAutomationFailing().FailureClass}
	assert.Contains(t, classes, "recovery_expired")
}

func TestInboxAttention_MixedTriggerKeepsUnmanagedFailureBeforeManagedSuccess(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "mixed")
	at := time.Now().UTC().Add(-time.Hour)
	attentionFiring(t, f, "unmanaged-failed", "mixed", db.Failed(), at)
	attentionFiring(t, f, "managed-success", "mixed", db.Completed(), at.Add(time.Minute))
	applyInboxAttention(t, f, "managed-success", "managed-run", "pr:1", "finish", "resolved", at.Add(time.Minute), 0)
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1, "managed success must not cut off an unmanaged episode")
	assert.Equal(t, "automation_failing:event-unmanaged-failed", items[0].ItemId)
	assert.Empty(t, items[0].GetAutomationFailing().IncidentKey)
	// The diagnostic health still sees the later success.
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED, items[0].GetAutomationFailing().Health.Status)
	assert.Zero(t, items[0].GetAutomationFailing().Health.ConsecutiveFailures)
	// Filtering happens before cap, not only after it.
	episode, err := f.repo.FiringsSinceLastUnmanagedSuccess(f.ctx, f.userID, []string{"mixed"}, 1)
	require.NoError(t, err)
	require.Len(t, episode["mixed"], 1)
	assert.Equal(t, "event-unmanaged-failed", episode["mixed"][0].Event.ID)
	other, err := f.repo.FiringsSinceLastUnmanagedSuccess(f.ctx, "someone-else", []string{"mixed"}, 10)
	require.NoError(t, err)
	assert.Empty(t, other)
	attentionFiring(t, f, "unmanaged-success", "mixed", db.Completed(), at.Add(2*time.Minute))
	assert.Empty(t, f.list(t, nil).Items, "unmanaged success still ends the legacy episode")
}

func TestInboxAttention_DiagnosticCountUsesUnfilteredSuccessBeyondHealthWindow(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "diagnostics")
	at := time.Now().UTC().Add(-time.Hour)
	attentionFiring(t, f, "old-unmanaged", "diagnostics", db.Failed(), at)
	attentionFiring(t, f, "older-managed-success", "diagnostics", db.Completed(), at.Add(time.Minute))
	applyInboxAttention(t, f, "older-managed-success", "diagnostic-actor", "pr:42", "finish", "resolved", at.Add(time.Minute), 0)
	for i := range triggers.HealthWindow + 1 {
		f.firing(t, fmt.Sprintf("diagnostic-launch-%d", i), "diagnostics", core.TriggerEventFailed, "offline", at.Add(time.Duration(i+2)*time.Minute))
	}
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1)
	assert.EqualValues(t, triggers.HealthWindow+1, items[0].GetAutomationFailing().Health.ConsecutiveFailures, "diagnostic count stops at managed success even beyond recent window")
	assert.Equal(t, "automation_failing:event-old-unmanaged", items[0].ItemId, "legacy actionable episode independently spans managed success")
}

func TestInboxAttention_ManagedRunsDoNotHideLaunchErrors(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "launch-errors")
	at := time.Now().UTC().Add(-time.Minute)
	attentionFiring(t, f, "managed-failed", "launch-errors", db.Failed(), at)
	applyInboxAttention(t, f, "managed-failed", "managed-failed-run", "pr:42", "finish", "action_required", at, 0)
	f.firing(t, "launch-error-1", "launch-errors", core.TriggerEventFailed, "machine offline", at.Add(time.Second))
	f.firing(t, "launch-error-2", "launch-errors", core.TriggerEventFailed, "machine offline", at.Add(2*time.Second))
	resp := f.list(t, nil)
	assert.Len(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING), 2, "incident and legacy launch-failure streak both survive")
	assert.Len(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED), 1)
}

func TestInboxAttention_ObserverWithMissingFinishRetainsLegacyFailure(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "observer")
	at := time.Now().UTC().Add(-time.Minute)
	attentionFiring(t, f, "observer-job", "observer", db.Failed(), at)
	applyInboxAttention(t, f, "observer-job", "observer-run", "pr:42", "start", "", at, 0)
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1, "observer with no durable terminal cannot own away the failure")
	assert.Equal(t, "automation_failing:event-observer-job", items[0].ItemId)
	// A later actor resolves/fences the incident; the old terminal still safely
	// marks its own failed chat managed without reopening the resolved episode.
	attentionFiring(t, f, "newer-observer", "observer", db.Completed(), at.Add(time.Second))
	applyInboxAttention(t, f, "newer-observer", "newer-run", "pr:42", "finish", "resolved", at.Add(time.Second), 0)
	applyInboxAttention(t, f, "observer-job", "observer-run", "pr:42", "finish", "action_required", at, 0)
	assert.Empty(t, f.list(t, nil).Items, "finished stale actor is safely excluded, not reopened")
}

func TestInboxAttention_StaleStartWithoutOpenBackstopRetainsLegacyFailure(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "stale-start")
	at := time.Now().UTC().Add(-time.Minute)
	attentionFiring(t, f, "old-repair", "stale-start", db.Failed(), at)
	attentionFiring(t, f, "resolved-repair", "stale-start", db.Completed(), at.Add(time.Second))
	applyInboxAttention(t, f, "resolved-repair", "resolved-actor", "pr:42", "finish", "resolved", at.Add(time.Second), 0)
	applyInboxAttention(t, f, "old-repair", "old-actor", "pr:42", "start", "recovering", at, time.Hour)
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1, "fenced start created no open incident, so cannot hide its failed chat")
	assert.Equal(t, "automation_failing:event-old-repair", items[0].ItemId)
}

func TestInboxAttention_ProjectionStatesAndOwnership(t *testing.T) {
	at := time.Now().UTC()
	incidents := []*core.AttentionIncident{
		{UserID: "u", ProjectID: "p", EpisodeID: "live", State: "recovering"},
		{UserID: "u", ProjectID: "p", EpisodeID: "done", State: "resolved"},
		{UserID: "other", ProjectID: "p", EpisodeID: "foreign", State: "action_required"},
		{UserID: "u", ProjectID: "p", EpisodeID: "episode", State: "action_required", Key: "pr:1", TriggerID: "deleted", OpenedAt: at},
	}
	items := attentionInboxItems("u", incidents, nil, nil)
	require.Len(t, items, 1, "open attention survives a missing or paused trigger")
	assert.Equal(t, "automation_failing:incident:episode", items[0].ItemId)
	assert.Equal(t, "p", items[0].ProjectId)
	assert.NoError(t, validateInboxItemIDs([]string{items[0].ItemId}))
	// A new broken definition cannot supersede an earlier incident.
	broken := &reliantv1.InboxItem{ItemId: inboxFailingPrefix + brokenItemInfix + "deleted", TriggerId: "deleted"}
	items[0].TriggerId = "deleted"
	assert.Len(t, dropSupersededByBroken(append(items, broken)), 2)
}

func TestInboxAttention_RepositoryTenantAndProjectScope(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "tenant-trigger")
	at := time.Now().UTC().Add(-time.Minute)
	attentionFiring(t, f, "tenant-job", "tenant-trigger", db.Failed(), at)
	applyInboxAttention(t, f, "tenant-job", "tenant-run", "shared-key", "finish", "action_required", at, 0)
	otherCtx, _, _ := f.otherUser(t)
	other, err := f.inbox.ListInbox(otherCtx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	assert.Empty(t, other.Msg.Items, "incident identity never crosses repository user ownership")
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: ptr.Of("other-project")}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Items)
	assert.EqualValues(t, 1, resp.Msg.OtherProjectsCount)
	resp, err = f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: ptr.Of(f.project())}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Items, 1)
	assert.Equal(t, f.project(), resp.Msg.Items[0].ProjectId)
}

func TestInboxAttention_FilterOnlyRegisteredLaunchedChats(t *testing.T) {
	firings := []*core.TriggerEventWithRun{
		{Event: &core.TriggerEvent{ID: "managed", Outcome: core.TriggerEventLaunched, ChatID: ptr.Of("registered")}},
		{Event: &core.TriggerEvent{ID: "invalid-policy", Outcome: core.TriggerEventLaunched, ChatID: ptr.Of("not-registered")}},
		{Event: &core.TriggerEvent{ID: "launch-error", Outcome: core.TriggerEventFailed, ChatID: ptr.Of("registered")}},
	}
	filtered := unmanagedFirings(firings, map[string]bool{"registered": true})
	require.Len(t, filtered, 2)
	assert.Equal(t, "invalid-policy", filtered[0].Event.ID)
	assert.Equal(t, "launch-error", filtered[1].Event.ID)
	assert.Len(t, firings, 3, "never mutate diagnostic health input")
}

func TestInboxAttention_RequiredReadErrorsFailClosed(t *testing.T) {
	repo := newInflightRepo(0)
	repo.ListAttentionIncidentsFunc = func(context.Context, string) ([]*core.AttentionIncident, error) { return nil, assert.AnError }
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.Error(t, err)
}
