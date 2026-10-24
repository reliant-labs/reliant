// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A pending question or approval makes a chat AWAITING_INPUT only while the
// run that asked can still take the answer, and the chat says separately how
// many threads are still working (prod chat adcee226: two sub-agents running,
// a third parked on ask_user, and a question left behind by a sub-agent that
// had finished pinned the chat to "needs input" with no sign of the work).

// activityRunning and activityAwaitingInput: chat_daemon_blocked_test.go.
const activityPaused = int(reliantv1.ChatActivity_CHAT_ACTIVITY_PAUSED)

// liveGateChat is a chat whose root run issued sub-agents, each on its own
// thread and workflows row (row id == thread id, as spawnRun names them).
type liveGateChat struct {
	t      *testing.T
	repo   *Repo
	chatID string
}

func newLiveGateChat(t *testing.T, chatID string) *liveGateChat {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	// The root run's row id is the chat's workflow_id, as in production; the
	// inbox reads the root run's state through it.
	now := time.Now()
	if err := repo.CreateChat(context.Background(), &Chat{
		ID: chatID, Title: "gate chat", ProjectID: "test-project", UserID: "test-user",
		State: 2, WorkflowID: &chatID, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	createTestRootThread(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, chatID, chatID, nil, Active())
	return &liveGateChat{t: t, repo: repo, chatID: chatID}
}

// spawn creates a sub-agent thread issued by the root, with one run.
func (c *liveGateChat) spawn(id string, status WorkflowStatus) {
	c.t.Helper()
	parent := c.chatID
	if _, err := c.repo.CreateThread(context.Background(), &Thread{
		ID: id, ChatID: c.chatID, ParentThreadID: &parent, Origin: ThreadOriginSpawn,
		Status: ThreadStatusRunning, WorkflowID: &id, CreatedAt: time.Now(),
	}); err != nil {
		c.t.Fatalf("CreateThread(%s): %v", id, err)
	}
	c.run(id, id, status, time.Now())
}

// run adds a workflows row for a thread (a resumed sub-agent gets a new one).
func (c *liveGateChat) run(rowID, thread string, status WorkflowStatus, createdAt time.Time) {
	c.t.Helper()
	parent := c.chatID
	if err := c.repo.CreateWorkflow(context.Background(), &Workflow{
		ID: rowID, ParentID: &parent, ChatID: c.chatID, WorkflowName: "builtin://agent",
		Thread: thread, Status: status, CreatedAt: createdAt,
	}); err != nil {
		c.t.Fatalf("CreateWorkflow(%s): %v", rowID, err)
	}
}

func (c *liveGateChat) setStatus(rowID string, status WorkflowStatus) {
	c.t.Helper()
	if err := c.repo.UpdateWorkflowStatus(context.Background(), rowID, status); err != nil {
		c.t.Fatalf("UpdateWorkflowStatus(%s): %v", rowID, err)
	}
}

// ask records a pending ask_user question from the run rowID on thread.
func (c *liveGateChat) ask(id, rowID, thread string) {
	c.t.Helper()
	metadata := `{"questions":[{"question":"` + id + `?"}]}`
	if err := c.repo.CreateQuestion(context.Background(), &Question{
		ID: id, ChatID: c.chatID, WorkflowID: rowID, TemporalWorkflowID: c.chatID,
		ThreadID: thread, StepID: "agent_loop", Status: QuestionStatusPending,
		Metadata: &metadata, CreatedAt: time.Now(),
	}); err != nil {
		c.t.Fatalf("CreateQuestion(%s): %v", id, err)
	}
}

// approve records a pending approval raised on thread ("" for none).
func (c *liveGateChat) approve(id, thread string) {
	c.t.Helper()
	var threadID *string
	if thread != "" {
		threadID = &thread
	}
	if err := c.repo.CreateApproval(context.Background(), &Approval{
		ID: id, ChatID: c.chatID, ApprovalType: 1, EntityID: id,
		Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), Title: "approve " + id,
		CreatedAt: time.Now(), ThreadID: threadID,
	}); err != nil {
		c.t.Fatalf("CreateApproval(%s): %v", id, err)
	}
}

// expect checks the view through every read the web gets it from: the
// activity-only read, the chat read, and the activity stream's latest event.
func (c *liveGateChat) expect(wantActivity, wantWorking int) {
	c.t.Helper()
	ctx := context.Background()
	activity, err := c.repo.GetChatActivity(ctx, c.chatID)
	if err != nil {
		c.t.Fatalf("GetChatActivity: %v", err)
	}
	chat, err := c.repo.GetChat(ctx, c.chatID)
	if err != nil {
		c.t.Fatalf("GetChat: %v", err)
	}
	if activity != wantActivity || chat.Activity == nil || *chat.Activity != wantActivity {
		c.t.Fatalf("activity = %d (chat read %v), want %d", activity, chat.Activity, wantActivity)
	}
	if chat.WorkingThreads != wantWorking {
		c.t.Fatalf("working_threads = %d, want %d", chat.WorkingThreads, wantWorking)
	}

	// Every write above emitted an activity event; the newest must carry the
	// same pair, or the sidebar would drift from a reload.
	if err := c.repo.emitChatActivityIfChanged(ctx, c.chatID); err != nil {
		c.t.Fatalf("emitChatActivityIfChanged: %v", err)
	}
	updates := activityUpdatesForChat(c.t, c.repo, c.chatID)
	if len(updates) == 0 {
		c.t.Fatalf("no chat_activity_changed event")
	}
	var data struct {
		Activity       int `json:"activity"`
		WorkingThreads int `json:"working_threads"`
	}
	if err := json.Unmarshal(updates[len(updates)-1].Data, &data); err != nil {
		c.t.Fatalf("decode activity event: %v", err)
	}
	if data.Activity != wantActivity || data.WorkingThreads != wantWorking {
		c.t.Fatalf("activity event = %+v, want activity %d working_threads %d", data, wantActivity, wantWorking)
	}
}

func TestChatActivity_QuestionOnRunningThreadWhileOthersRun(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-running")
	c.spawn("sa-1", Active())
	c.spawn("sa-2", Active())
	c.spawn("sa-3", Active())
	c.expect(activityRunning, 3) // the root only waits on its sub-agents

	c.ask("q-3", "sa-3", "sa-3")
	c.expect(activityAwaitingInput, 2)

	// Answered: the asking thread works again.
	if err := c.repo.ResolveQuestion(context.Background(), "q-3", nil); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	c.expect(activityRunning, 3)
}

// A question left pending by a run that ended cannot be answered, whichever
// way the run ended, and must not hold the chat.
func TestChatActivity_QuestionOnTerminalThreadDoesNotCount(t *testing.T) {
	testkit.Slow(t, "a DB-backed test with many round trips to pgtest's Postgres")
	t.Parallel()

	for name, status := range map[string]WorkflowStatus{
		"completed": Completed(),
		"failed":    Failed(),
		"cancelled": Cancelled(),
	} {
		t.Run(name, func(t *testing.T) {
			c := newLiveGateChat(t, "chat-gate-"+name)
			c.spawn("sa-1", Active())
			c.spawn("sa-2", Active())
			c.spawn("sa-3", Active())
			c.ask("q-3", "sa-3", "sa-3")
			c.setStatus("sa-3", status)
			c.expect(activityRunning, 2)
		})
	}
}

// A paused run resumes into the same wait, so its question still counts.
func TestChatActivity_QuestionOnPausedThreadStillCounts(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-paused")
	c.spawn("sa-1", Active())
	c.spawn("sa-3", Active())
	c.ask("q-3", "sa-3", "sa-3")
	for _, row := range []string{c.chatID, "sa-1", "sa-3"} {
		c.setStatus(row, Paused())
	}
	c.expect(activityAwaitingInput, 0)

	// The question gone, the chat reads as the paused run it is.
	if err := c.repo.ResolveQuestion(context.Background(), "q-3", nil); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	c.expect(activityPaused, 0)
}

// The parent of a sub-agent blocked on the user is waiting, not working: with
// nothing else running, "needs you" must not also claim work is going on.
func TestChatActivity_ParentWaitingOnBlockedSubAgentIsNotWorking(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-only-blocked")
	c.spawn("sa-1", Active())
	c.ask("q-1", "sa-1", "sa-1")
	c.expect(activityAwaitingInput, 0)

	// A root that asks with no sub-agent is the same.
	r := newLiveGateChat(t, "chat-gate-root-asks")
	r.ask("q-root", r.chatID, r.chatID)
	r.expect(activityAwaitingInput, 0)
}

// The question belongs to the run that asked it: once that run was cancelled
// and the agent resumed on a new row, the old question is dead even though
// its thread is live again.
func TestChatActivity_QuestionOfSupersededRunDoesNotCount(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-resumed")
	c.spawn("sa-1", Active())
	c.ask("q-1", "sa-1", "sa-1")
	c.setStatus("sa-1", Cancelled())
	c.run("sa-1#resume-1", "sa-1", Active(), time.Now().Add(time.Second))
	c.expect(activityRunning, 1)
}

// Approvals carry only a thread: the thread's newest run decides. One with no
// thread cannot be placed and stays live.
func TestChatActivity_ApprovalFollowsItsThreadsRun(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-approval")
	c.spawn("sa-1", Active())
	c.spawn("sa-2", Active())
	c.approve("ap-2", "sa-2")
	c.expect(activityAwaitingInput, 1)

	c.setStatus("sa-2", Completed())
	c.expect(activityRunning, 1)

	c.approve("ap-none", "")
	c.expect(activityAwaitingInput, 1)
}

// The inbox drops an approval or question whose ASKING run completed or was
// cancelled, not only one whose root did: nobody can answer it. A failed run's
// items stay listed (resumable, the inbox's own rule).
func TestInboxPending_DropsGatesOfFinishedAskingRuns(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-gate-inbox")
	c.spawn("sa-live", Active())
	c.spawn("sa-done", Active())
	c.spawn("sa-failed", Active())
	c.ask("q-live", "sa-live", "sa-live")
	c.ask("q-done", "sa-done", "sa-done")
	c.approve("ap-done", "sa-done")
	c.ask("q-failed", "sa-failed", "sa-failed")
	c.setStatus("sa-done", Completed())
	c.setStatus("sa-failed", Failed())

	items, err := c.repo.ListInboxPending(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("ListInboxPending: %v", err)
	}
	var keys []string
	for _, it := range items {
		if it.ChatID == c.chatID {
			keys = append(keys, it.ItemKey)
		}
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "q-failed" || keys[1] != "q-live" {
		t.Fatalf("inbox items for chat = %v, want [q-failed q-live]", keys)
	}
}

// The pending questions the UI lists (QuestionService.ListPendingQuestions)
// follow the inbox's rule: a question whose asking run completed or was
// cancelled can never be answered and is not listed; a failed or paused run
// resumes into the same wait and keeps its question. Every live thread's
// question is listed, oldest first — not only the chat's newest.
func TestListAnswerablePendingQuestionsByChat(t *testing.T) {
	t.Parallel()

	c := newLiveGateChat(t, "chat-answerable-questions")
	c.spawn("spawn-live", Active())
	c.spawn("spawn-done", Active())
	c.spawn("spawn-cancelled", Active())
	c.spawn("spawn-failed", Active())
	c.spawn("spawn-paused", Active())
	for _, id := range []string{"spawn-live", "spawn-done", "spawn-cancelled", "spawn-failed", "spawn-paused"} {
		c.ask("q-"+id, id, id)
		time.Sleep(2 * time.Millisecond) // distinct created_at, so the order is the asking order
	}
	c.setStatus("spawn-done", Completed())
	c.setStatus("spawn-cancelled", Cancelled())
	c.setStatus("spawn-failed", Failed())
	c.setStatus("spawn-paused", Paused())

	got, err := c.repo.ListAnswerablePendingQuestionsByChat(context.Background(), c.chatID)
	if err != nil {
		t.Fatalf("ListAnswerablePendingQuestionsByChat: %v", err)
	}
	var ids []string
	for _, q := range got {
		ids = append(ids, q.ID)
	}
	want := []string{"q-spawn-live", "q-spawn-failed", "q-spawn-paused"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}

	// Every pending row is still there for the callers that must see them
	// all (terminating a run voids each one).
	all, err := c.repo.ListPendingQuestionsByChat(context.Background(), c.chatID)
	if err != nil {
		t.Fatalf("ListPendingQuestionsByChat: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("ListPendingQuestionsByChat returned %d, want all 5 pending", len(all))
	}
}
