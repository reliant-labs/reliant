// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type inboxFixture struct {
	*runListFixture
	inbox *InboxService
}

func newInboxFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := newRunListFixture(t)
	return &inboxFixture{runListFixture: f, inbox: NewInboxService(f.repo)}
}

// otherUser returns a context and project for a second, unrelated user in the
// same database.
func (f *inboxFixture) otherUser(t *testing.T) (context.Context, string, string) {
	t.Helper()
	userID := uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	now := time.Now().UTC()
	projectID := "p-" + userID
	require.NoError(t, f.repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "other", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return ctx, userID, projectID
}

func (f *inboxFixture) list(t *testing.T, limit *int32) *reliantv1.ListInboxResponse {
	t.Helper()
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{Limit: limit}))
	require.NoError(t, err)
	return resp.Msg
}

func (f *inboxFixture) approval(t *testing.T, id, chatID string, at time.Time) {
	t.Helper()
	meta := `{"tool_name":"bash","tool_call_id":"tc-1","arguments":{"command":"git push origin main"}}`
	require.NoError(t, f.repo.CreateApproval(f.ctx, &db.Approval{
		ID: id, ChatID: chatID, ApprovalType: 1, EntityID: id, Status: 1,
		Title: "Run bash", Metadata: &meta, CreatedAt: at,
	}))
}

func (f *inboxFixture) question(t *testing.T, id, chatID string, at time.Time) {
	t.Helper()
	meta := `{"type":"ask_user","questions":[{"question":"Which branch?"}]}`
	require.NoError(t, f.repo.CreateQuestion(f.ctx, &db.Question{
		ID: id, ChatID: chatID, WorkflowID: chatID, ThreadID: chatID, Status: 1,
		Metadata: &meta, CreatedAt: at,
	}))
}

func (f *inboxFixture) trigger(t *testing.T, id string) {
	t.Helper()
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, f.repo.CreateTrigger(f.ctx, &core.Trigger{
		ID: id, UserID: f.userID, ProjectID: f.project(), Name: "nightly " + id,
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "wf", DaemonID: "d-" + id,
		Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *inboxFixture) firing(t *testing.T, id, triggerID string, outcome core.TriggerEventOutcome, detail string, at time.Time) {
	t.Helper()
	tid := triggerID
	created, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
		ID: id, TriggerID: &tid, UserID: f.userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: "dd-" + id, OccurredAt: at.UTC().Truncate(time.Microsecond),
		Payload: map[string]any{}, Outcome: outcome, OutcomeDetail: detail,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	require.NoError(t, err)
	require.True(t, created)
}

func inboxKinds(items []*reliantv1.InboxItem) []reliantv1.InboxItemKind {
	kinds := make([]reliantv1.InboxItemKind, len(items))
	for i, it := range items {
		kinds[i] = it.Kind
	}
	return kinds
}

func inboxByKind(items []*reliantv1.InboxItem, kind reliantv1.InboxItemKind) []*reliantv1.InboxItem {
	var out []*reliantv1.InboxItem
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func TestListInbox_ApprovalAppearsWithToolAndClearsWhenResolved(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "chat-a", "wf", db.Active(), now) // interactive: no trigger event behind it
	f.approval(t, "appr-1", "chat-a", now)

	resp := f.list(t, nil)
	require.Len(t, resp.Items, 1)
	it := resp.Items[0]
	assert.Equal(t, reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL, it.Kind)
	assert.Equal(t, "approval:appr-1", it.ItemId)
	assert.Equal(t, "chat-a", it.ChatId)
	assert.Equal(t, "chat-a", it.RunId)
	assert.Equal(t, f.project(), it.ProjectId)
	assert.Equal(t, "proj", it.ProjectName)
	assert.Equal(t, "wf", it.WorkflowName)
	assert.Equal(t, "run chat-a", it.ChatTitle)
	assert.NotEmpty(t, it.WaitingSince)
	a := it.GetApproval()
	require.NotNil(t, a)
	assert.Equal(t, "bash", a.GetToolName())
	assert.Contains(t, a.ArgumentSummary, "git push origin main")
	assert.EqualValues(t, 1, resp.BlockingCount)
	assert.False(t, resp.HasInformational)

	require.NoError(t, f.repo.UpdateApprovalStatus(f.ctx, "appr-1", int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), nil, nil, nil))
	resp = f.list(t, nil)
	assert.Empty(t, resp.Items)
	assert.EqualValues(t, 0, resp.BlockingCount)
}

func TestListInbox_QuestionAppearsAndClearsWhenAnswered(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "chat-q", "wf", db.Active(), now)
	f.question(t, "q-1", "chat-q", now)

	resp := f.list(t, nil)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION, resp.Items[0].Kind)
	assert.Equal(t, "q-1", resp.Items[0].GetQuestion().QuestionId)
	assert.Equal(t, "Which branch?", resp.Items[0].GetQuestion().Prompt)

	require.NoError(t, f.repo.ResolveQuestion(f.ctx, "q-1", nil))
	assert.Empty(t, f.list(t, nil).Items)
}

func TestListInbox_WaitingForMachineAppearsAndClearsOnAttach(t *testing.T) {
	f := newInboxFixture(t)
	f.seed(t, "chat-w", "wf", db.Active(), time.Now().UTC().Add(-time.Minute))
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "chat-w", true))

	resp := f.list(t, nil)
	require.Len(t, resp.Items, 1)
	it := resp.Items[0]
	assert.Equal(t, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE, it.Kind)
	require.NotNil(t, it.GetWaitingForMachine())
	assert.EqualValues(t, 1, resp.BlockingCount)

	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "chat-w", false))
	assert.Empty(t, f.list(t, nil).Items)
}

func TestListInbox_AutomationFailingReusesHealthAndClearsOnSuccess(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-f")
	base := time.Now().UTC().Add(-time.Hour)
	f.firing(t, "ev1", "trg-f", core.TriggerEventFailed, "boom one", base)
	// One failure is DEGRADED, not FAILING: only the launch-failed item shows.
	resp := f.list(t, nil)
	assert.Empty(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING))

	f.firing(t, "ev2", "trg-f", core.TriggerEventFailed, "boom two", base.Add(time.Minute))
	resp = f.list(t, nil)
	failing := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, failing, 1)
	assert.Equal(t, "trg-f", failing[0].TriggerId)
	assert.Equal(t, "automation_failing:ev1", failing[0].ItemId, "keyed by the first failure of the streak")
	h := failing[0].GetAutomationFailing().Health
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, h.Status)
	assert.EqualValues(t, 2, h.ConsecutiveFailures)
	assert.True(t, resp.HasInformational)
	assert.EqualValues(t, 0, resp.BlockingCount, "failures never count toward the badge")

	// A launched, completed run ends the failure streak.
	f.seed(t, "run-ok", "wf", db.Completed(), base.Add(2*time.Minute))
	runID := "run-ok"
	_, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
		ID: "ev3", TriggerID: strPtr("trg-f"), UserID: f.userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: "dd-ev3", OccurredAt: base.Add(3 * time.Minute).Truncate(time.Microsecond),
		Payload: map[string]any{}, Outcome: core.TriggerEventLaunched, ChatID: &runID,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	require.NoError(t, err)
	assert.Empty(t, f.list(t, nil).Items, "a later launched+completed run clears both failure kinds")
}

func TestListInbox_LaunchFailedDismissalAndNewerFailureReappears(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-l")
	base := time.Now().UTC().Add(-time.Hour)
	f.firing(t, "evL1", "trg-l", core.TriggerEventFailed, "daemon gone", base)

	resp := f.list(t, nil)
	require.Len(t, resp.Items, 1)
	it := resp.Items[0]
	assert.Equal(t, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED, it.Kind)
	assert.Equal(t, "automation_launch_failed:evL1", it.ItemId)
	assert.Equal(t, "daemon gone", it.GetAutomationLaunchFailed().Reason)
	assert.Equal(t, "trg-l", it.TriggerId)

	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{it.ItemId}}))
	require.NoError(t, err)
	assert.Empty(t, f.list(t, nil).Items, "dismissed")

	f.firing(t, "evL2", "trg-l", core.TriggerEventFailed, "daemon still gone", base.Add(time.Minute))
	resp = f.list(t, nil)
	assert.Empty(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED),
		"a further failure in the same episode stays dismissed")

	// A launch ends the episode; the next failure is a new one and reappears.
	f.launched(t, "evL3", "chat-l3", "trg-l", base.Add(2*time.Minute), db.Completed())
	f.firing(t, "evL4", "trg-l", core.TriggerEventFailed, "gone again", base.Add(3*time.Minute))
	resp = f.list(t, nil)
	launchFailed := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, launchFailed, 1, "a new episode reappears")
	assert.Equal(t, "automation_launch_failed:evL4", launchFailed[0].ItemId)
	assert.EqualValues(t, 1, launchFailed[0].GetAutomationLaunchFailed().ConsecutiveFailures)
}

// launched records a launched firing of the trigger, backed by a run in the
// given status, so the trigger has a success (or a failed run) to read.
func (f *inboxFixture) launched(t *testing.T, eventID, chatID, triggerID string, at time.Time, status db.WorkflowStatus) {
	t.Helper()
	f.seed(t, chatID, "wf", status, at)
	tid := triggerID
	created, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
		ID: eventID, TriggerID: &tid, UserID: f.userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: "dd-" + eventID, OccurredAt: at.UTC().Truncate(time.Microsecond),
		Payload: map[string]any{}, Outcome: core.TriggerEventLaunched, ChatID: &chatID,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	require.NoError(t, err)
	require.True(t, created)
}

func TestListInbox_FailingItemIDIsStableAcrossAStreakAndNewForANewOne(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-s")
	base := time.Now().UTC().Add(-6 * time.Hour)
	failing := func() []*reliantv1.InboxItem {
		return inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	}

	f.launched(t, "s1", "chat-s1", "trg-s", base, db.Failed())
	f.launched(t, "s2", "chat-s2", "trg-s", base.Add(time.Hour), db.Failed())
	first := failing()
	require.Len(t, first, 1)
	assert.Equal(t, "automation_failing:s1", first[0].ItemId)
	assert.EqualValues(t, 2, first[0].GetAutomationFailing().Health.ConsecutiveFailures)

	f.launched(t, "s3", "chat-s3", "trg-s", base.Add(2*time.Hour), db.Failed())
	f.launched(t, "s4", "chat-s4", "trg-s", base.Add(3*time.Hour), db.Failed())
	later := failing()
	require.Len(t, later, 1)
	assert.Equal(t, first[0].ItemId, later[0].ItemId, "more failures do not change the item id")
	assert.EqualValues(t, 4, later[0].GetAutomationFailing().Health.ConsecutiveFailures, "the count is the whole streak")

	// Dismissing the episode hides the rest of it.
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{later[0].ItemId}}))
	require.NoError(t, err)
	f.launched(t, "s5", "chat-s5", "trg-s", base.Add(4*time.Hour), db.Failed())
	assert.Empty(t, failing(), "a further failure of a dismissed episode stays hidden")

	// A success ends the episode; the next streak is a new item and shows again.
	f.launched(t, "s6", "chat-s6", "trg-s", base.Add(5*time.Hour), db.Completed())
	assert.Empty(t, failing())
	f.launched(t, "s7", "chat-s7", "trg-s", base.Add(6*time.Hour), db.Failed())
	f.launched(t, "s8", "chat-s8", "trg-s", base.Add(7*time.Hour), db.Failed())
	again := failing()
	require.Len(t, again, 1, "a new episode reappears after a dismissal of the old one")
	assert.Equal(t, "automation_failing:s7", again[0].ItemId)
	assert.EqualValues(t, 2, again[0].GetAutomationFailing().Health.ConsecutiveFailures)
}

func TestListInbox_LaunchFailedItemCountsTheEpisode(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-c")
	base := time.Now().UTC().Add(-time.Hour)
	f.firing(t, "c1", "trg-c", core.TriggerEventFailed, "one", base)
	f.firing(t, "c2", "trg-c", core.TriggerEventFailed, "two", base.Add(time.Minute))
	f.firing(t, "c3", "trg-c", core.TriggerEventFailed, "three", base.Add(2*time.Minute))
	items := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, items, 1)
	assert.Equal(t, "automation_launch_failed:c1", items[0].ItemId)
	assert.EqualValues(t, 3, items[0].GetAutomationLaunchFailed().ConsecutiveFailures)
	assert.Equal(t, "three", items[0].GetAutomationLaunchFailed().Reason, "the reason is the newest failure's")
}

func (f *inboxFixture) notifyingTrigger(t *testing.T, id string, notify bool) {
	t.Helper()
	f.trigger(t, id)
	tr, err := f.repo.GetTrigger(f.ctx, id)
	require.NoError(t, err)
	tr.NotifyOnComplete = notify
	require.NoError(t, f.repo.UpdateTrigger(f.ctx, tr))
}

func TestListInbox_RunFinishedOnlyForOptedInAutomationAndClearsWhenOpened(t *testing.T) {
	f := newInboxFixture(t)
	f.notifyingTrigger(t, "trg-on", true)
	f.notifyingTrigger(t, "trg-off", false)
	at := time.Now().UTC().Add(-time.Hour)
	f.launched(t, "rf-on", "chat-on", "trg-on", at, db.Completed())
	f.launched(t, "rf-off", "chat-off", "trg-off", at, db.Completed())
	require.NoError(t, f.repo.UpdateChatUnread(f.ctx, "chat-on", true, "workflow_completed"))
	require.NoError(t, f.repo.UpdateChatUnread(f.ctx, "chat-off", true, "workflow_completed"))

	finished := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED)
	require.Len(t, finished, 1, "only the opted-in automation produces a Run finished item")
	assert.Equal(t, "run_finished:chat-on", finished[0].ItemId)
	assert.Equal(t, "chat-on", finished[0].ChatId)
	assert.Equal(t, "trg-on", finished[0].TriggerId)
	assert.NotNil(t, finished[0].GetRunFinished())
	resp := f.list(t, nil)
	assert.True(t, resp.HasInformational)
	assert.EqualValues(t, 0, resp.BlockingCount)

	require.NoError(t, f.repo.UpdateChatUnread(f.ctx, "chat-on", false, "opened"))
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED), "opening the chat clears it")
}

func TestListInbox_RunFinishedCanBeDismissedAndExcludesFailedAndRunning(t *testing.T) {
	f := newInboxFixture(t)
	f.notifyingTrigger(t, "trg-d", true)
	at := time.Now().UTC().Add(-time.Hour)
	f.launched(t, "d1", "chat-d1", "trg-d", at, db.Completed())
	f.launched(t, "d2", "chat-d2", "trg-d", at.Add(time.Minute), db.Failed())
	f.launched(t, "d3", "chat-d3", "trg-d", at.Add(2*time.Minute), db.Active())
	for _, c := range []string{"chat-d1", "chat-d2", "chat-d3"} {
		require.NoError(t, f.repo.UpdateChatUnread(f.ctx, c, true, "x"))
	}
	finished := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED)
	require.Len(t, finished, 1, "a failed or still-running run is not a finish")
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{finished[0].ItemId}}))
	require.NoError(t, err)
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED))
}

func TestDismissInboxItem_RejectsIDsThatNameNoItem(t *testing.T) {
	f := newInboxFixture(t)
	for _, id := range []string{"appr-1", "q-1", "", "automation_failing:", "approval:"} {
		_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{id}}))
		require.Error(t, err, id)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), id)
	}
}

func TestListInbox_PriorityOrderThenWaitingSince(t *testing.T) {
	f := newInboxFixture(t)
	base := time.Now().UTC().Add(-time.Hour)
	// Seeded in reverse of priority order so insertion order cannot pass this.
	f.trigger(t, "trg-p")
	f.firing(t, "evP", "trg-p", core.TriggerEventFailed, "x", base)
	f.seed(t, "c-wait", "wf", db.Active(), base)
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "c-wait", true))
	f.seed(t, "c-q", "wf", db.Active(), base)
	f.question(t, "q-p", "c-q", base)
	f.seed(t, "c-a1", "wf", db.Active(), base)
	f.approval(t, "appr-new", "c-a1", base.Add(10*time.Minute))
	f.seed(t, "c-a2", "wf", db.Active(), base)
	f.approval(t, "appr-old", "c-a2", base.Add(time.Minute))

	resp := f.list(t, nil)
	assert.Equal(t, []reliantv1.InboxItemKind{
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED,
	}, inboxKinds(resp.Items))
	assert.Equal(t, "approval:appr-old", resp.Items[0].ItemId, "longest-waiting first within a kind")
	assert.Equal(t, "approval:appr-new", resp.Items[1].ItemId)
	assert.EqualValues(t, 4, resp.BlockingCount)
	assert.True(t, resp.HasInformational)
}

func TestListInbox_ScopedToCallerAcrossEveryKind(t *testing.T) {
	f := newInboxFixture(t)
	otherCtx, otherUser, otherProject := f.otherUser(t)
	now := time.Now().UTC().Add(-time.Minute)

	// The other user has one of everything.
	wf := "wf"
	for _, id := range []string{"o-a", "o-q", "o-w"} {
		wid := id
		require.NoError(t, f.repo.CreateChat(otherCtx, &db.Chat{
			ID: id, Title: id, ProjectID: otherProject, UserID: otherUser, WorkflowName: &wf, WorkflowID: &wid,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		_, err := f.repo.CreateThread(otherCtx, &db.Thread{ID: id, ChatID: id, CreatedAt: now})
		require.NoError(t, err)
		require.NoError(t, f.repo.CreateWorkflow(otherCtx, &db.Workflow{
			ID: id, ChatID: id, WorkflowName: wf, Thread: id, Status: db.Active(), CreatedAt: now,
		}))
	}
	require.NoError(t, f.repo.CreateApproval(otherCtx, &db.Approval{ID: "o-appr", ChatID: "o-a", ApprovalType: 1, EntityID: "o-appr", Status: 1, Title: "t", CreatedAt: now}))
	meta := `{}`
	require.NoError(t, f.repo.CreateQuestion(otherCtx, &db.Question{ID: "o-ques", ChatID: "o-q", WorkflowID: "o-q", Status: 1, Metadata: &meta, CreatedAt: now}))
	require.NoError(t, f.repo.SetChatDaemonBlocked(otherCtx, "o-w", true))
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	require.NoError(t, f.repo.CreateTrigger(otherCtx, &core.Trigger{
		ID: "o-trg", UserID: otherUser, ProjectID: otherProject, Name: "theirs", Kind: core.TriggerKindSchedule,
		Enabled: true, Workflow: "wf", DaemonID: "od", Presets: map[string]string{}, Params: map[string]any{},
		Config: cfg, CreatedAt: now, UpdatedAt: now,
	}))
	for i, id := range []string{"o-e1", "o-e2"} {
		_, err := f.repo.CreateTriggerEvent(otherCtx, &core.TriggerEvent{
			ID: id, TriggerID: strPtr("o-trg"), UserID: otherUser, Kind: core.TriggerEventKindSchedule,
			DedupeKey: id, OccurredAt: now.Add(time.Duration(i) * time.Second).Truncate(time.Microsecond),
			Payload: map[string]any{}, Outcome: core.TriggerEventFailed, OutcomeDetail: "x",
			CreatedAt: now.Truncate(time.Microsecond),
		})
		require.NoError(t, err)
	}

	// The caller sees none of it...
	resp := f.list(t, nil)
	assert.Empty(t, resp.Items)
	assert.EqualValues(t, 0, resp.BlockingCount)
	assert.False(t, resp.HasInformational)

	// ...and the other user does see all of it (so the empty result is scoping,
	// not a broken fixture).
	other, err := f.inbox.ListInbox(otherCtx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	assert.ElementsMatch(t, []reliantv1.InboxItemKind{
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED,
	}, inboxKinds(other.Msg.Items))

	// A user cannot dismiss into another user's inbox: dismissals are keyed by
	// the caller, so the other user's failure stays.
	_, err = f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{"automation_launch_failed:o-e2"}}))
	require.NoError(t, err)
	other, err = f.inbox.ListInbox(otherCtx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	assert.Len(t, inboxByKind(other.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED), 1)
}

func TestListInbox_LimitZeroReturnsCountsOnly(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "c1", "wf", db.Active(), now)
	f.approval(t, "a1", "c1", now)
	f.seed(t, "c2", "wf", db.Active(), now)
	f.question(t, "q1", "c2", now)

	zero := int32(0)
	resp := f.list(t, &zero)
	assert.Empty(t, resp.Items)
	assert.EqualValues(t, 2, resp.BlockingCount)
	assert.True(t, resp.Truncated)

	one := int32(1)
	resp = f.list(t, &one)
	assert.Len(t, resp.Items, 1)
	assert.EqualValues(t, 2, resp.BlockingCount, "count is over the whole inbox, not the page")
}

func TestListInbox_ArchivedChatsAreNotWaitingOnAnyone(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "c-arch", "wf", db.Active(), now)
	f.approval(t, "a-arch", "c-arch", now)
	require.Len(t, f.list(t, nil).Items, 1)

	chat, err := f.repo.GetChat(f.ctx, "c-arch")
	require.NoError(t, err)
	chat.State = db.ChatStateArchived
	require.NoError(t, f.repo.UpdateChat(f.ctx, chat))
	assert.Empty(t, f.list(t, nil).Items)
}

// chats.project_id has no foreign key, so deleting a project leaves its chats
// and their pending questions behind. Opening one lands on "This run doesn't
// exist", because the run page cannot select a project that is gone. The
// inbox must not list an item it cannot open.
func TestListInbox_ExcludesItemsWhoseProjectWasDeleted(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	gone := "p-gone-" + f.userID
	require.NoError(t, f.repo.CreateProject(f.ctx, &db.Project{
		ID: gone, UserID: f.userID, Name: "gone", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	for _, id := range []string{"orphan-q", "orphan-a", "orphan-w"} {
		wid := id
		wf := "wf"
		require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
			ID: id, Title: id, ProjectID: gone, UserID: f.userID, WorkflowName: &wf, WorkflowID: &wid,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		_, err := f.repo.CreateThread(f.ctx, &db.Thread{ID: id, ChatID: id, CreatedAt: now})
		require.NoError(t, err)
		require.NoError(t, f.repo.CreateWorkflow(f.ctx, &db.Workflow{
			ID: id, ChatID: id, WorkflowName: wf, Thread: id, Status: db.Active(), CreatedAt: now,
		}))
	}
	f.question(t, "q-orphan", "orphan-q", now)
	f.approval(t, "a-orphan", "orphan-a", now)
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "orphan-w", true))
	// A live item in a live project, so an empty result is the filter and not
	// a broken fixture.
	f.seed(t, "c-live", "wf", db.Active(), now)
	f.question(t, "q-live", "c-live", now)
	require.Len(t, f.list(t, nil).Items, 4)

	require.NoError(t, f.repo.DeleteProject(f.ctx, gone, f.userID))
	resp := f.list(t, nil)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "c-live", resp.Items[0].ChatId)
	assert.EqualValues(t, 1, resp.BlockingCount, "the badge does not count what the list cannot show")
}

// A run that completed or was cancelled will never read an answer: nothing
// expires the question or approval it left pending, so the inbox must.
// A failed or paused run is resumable, and its pending items stay.
func TestListInbox_ExcludesBlockingItemsOfFinishedRuns(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	for id, status := range map[string]db.WorkflowStatus{
		"c-done": db.Completed(), "c-cancel": db.Cancelled(), "c-failed": db.Failed(), "c-active": db.Active(),
	} {
		f.seed(t, id, "wf", status, now)
		f.question(t, "q-"+id, id, now)
		f.approval(t, "a-"+id, id, now)
	}
	var chats []string
	for _, it := range f.list(t, nil).Items {
		chats = append(chats, it.ChatId)
	}
	assert.ElementsMatch(t, []string{"c-failed", "c-failed", "c-active", "c-active"}, chats)
}

func TestListInbox_ProjectScopeAndOtherProjectsCount(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	second := "p-second-" + f.userID
	require.NoError(t, f.repo.CreateProject(f.ctx, &db.Project{
		ID: second, UserID: f.userID, Name: "second", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	f.seed(t, "c-here", "wf", db.Active(), now)
	f.question(t, "q-here", "c-here", now)
	wf, wid := "wf", "c-there"
	require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: "c-there", Title: "there", ProjectID: second, UserID: f.userID, WorkflowName: &wf, WorkflowID: &wid,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := f.repo.CreateThread(f.ctx, &db.Thread{ID: "c-there", ChatID: "c-there", CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, f.repo.CreateWorkflow(f.ctx, &db.Workflow{
		ID: "c-there", ChatID: "c-there", WorkflowName: wf, Thread: "c-there", Status: db.Active(), CreatedAt: now,
	}))
	f.approval(t, "a-there", "c-there", now)

	all := f.list(t, nil)
	assert.Len(t, all.Items, 2)
	assert.EqualValues(t, 0, all.OtherProjectsCount)

	here := f.project()
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: &here}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Items, 1)
	assert.Equal(t, "c-here", resp.Msg.Items[0].ChatId)
	assert.EqualValues(t, 1, resp.Msg.BlockingCount, "counts follow the scope")
	assert.EqualValues(t, 1, resp.Msg.OtherProjectsCount)
}

// Every kind can be dismissed: hiding an approval or a question takes it out of
// the inbox (and the badge) without resolving it. Restore brings it back, which
// is what Undo calls.
func TestDismissInboxItem_EveryKindAndRestore(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "c-1", "wf", db.Active(), now)
	f.approval(t, "a-1", "c-1", now)
	f.seed(t, "c-2", "wf", db.Active(), now)
	f.question(t, "q-2", "c-2", now)
	f.seed(t, "c-3", "wf", db.Active(), now)
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "c-3", true))

	items := f.list(t, nil).Items
	require.Len(t, items, 3)
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ItemId
	}
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: ids}))
	require.NoError(t, err)
	resp := f.list(t, nil)
	assert.Empty(t, resp.Items)
	assert.EqualValues(t, 0, resp.BlockingCount)

	_, err = f.inbox.RestoreInboxItem(f.ctx, connect.NewRequest(&reliantv1.RestoreInboxItemRequest{ItemIds: ids[:1]}))
	require.NoError(t, err)
	resp = f.list(t, nil)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, ids[0], resp.Items[0].ItemId)
}

// A machine that blocks the run again later is a new item, even after the
// previous block was dismissed.
func TestListInbox_WaitingForMachineIsANewItemPerBlock(t *testing.T) {
	f := newInboxFixture(t)
	f.seed(t, "c-w", "wf", db.Active(), time.Now().UTC().Add(-time.Minute))
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "c-w", true))
	first := f.list(t, nil).Items
	require.Len(t, first, 1)
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{first[0].ItemId}}))
	require.NoError(t, err)
	assert.Empty(t, f.list(t, nil).Items)

	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "c-w", false))
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, "c-w", true))
	again := f.list(t, nil).Items
	require.Len(t, again, 1)
	assert.NotEqual(t, first[0].ItemId, again[0].ItemId)
}
