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
	assert.Equal(t, "appr-1", it.ItemId)
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
	assert.Equal(t, "automation_failing:ev2", failing[0].ItemId)
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

	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemId: it.ItemId}))
	require.NoError(t, err)
	assert.Empty(t, f.list(t, nil).Items, "dismissed")

	f.firing(t, "evL2", "trg-l", core.TriggerEventFailed, "daemon still gone", base.Add(time.Minute))
	resp = f.list(t, nil)
	require.NotEmpty(t, resp.Items, "a newer failure reappears")
	assert.Equal(t, "automation_launch_failed:evL2", inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)[0].ItemId)
}

func TestDismissInboxItem_RejectsApprovalsAndQuestions(t *testing.T) {
	f := newInboxFixture(t)
	for _, id := range []string{"appr-1", "q-1", "", "automation_failing:"} {
		_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemId: id}))
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
	assert.Equal(t, "appr-old", resp.Items[0].ItemId, "longest-waiting first within a kind")
	assert.Equal(t, "appr-new", resp.Items[1].ItemId)
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
	_, err = f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemId: "automation_launch_failed:o-e2"}))
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
