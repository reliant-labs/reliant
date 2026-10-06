// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for the activity integration tests
// ---------------------------------------------------------------------------

func createActivityTestChat(t *testing.T, repo *Repo, chatID string) {
	t.Helper()
	ctx := context.Background()
	chat := &Chat{
		ID:         chatID,
		Title:      "test chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		State:      2, // IDLE
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	if err := repo.CreateChat(ctx, chat); err != nil {
		t.Fatalf("createActivityTestChat: %v", err)
	}
	// Every real chat gets a root thread whose id equals the chat id, and
	// messages/context windows reference it. Fixtures that skipped it were
	// relying on the absence of foreign keys.
	createTestRootThread(t, repo, chatID)
}

// createTestRootThread creates the root thread row for a chat, matching the
// production invariant that a chat's root thread id equals the chat id.
func createTestRootThread(t *testing.T, repo *Repo, chatID string) {
	t.Helper()
	if _, err := repo.CreateThread(context.Background(), &Thread{
		ID:        chatID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("createTestRootThread: %v", err)
	}
}

func insertTestWorkflow(t *testing.T, repo *Repo, id, chatID, workflowName string, status WorkflowStatus) {
	t.Helper()
	ctx := context.Background()
	wf := &Workflow{
		ID:           id,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       id,
		Status:       status,
		CreatedAt:    time.Now(),
	}
	if err := repo.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("insertTestWorkflow: %v", err)
	}
}

func insertTestApproval(t *testing.T, repo *Repo, id, chatID string, status int32) {
	t.Helper()
	ctx := context.Background()
	approval := &Approval{
		ID:           id,
		ChatID:       chatID,
		ApprovalType: 1, // tool
		EntityID:     id,
		Status:       status,
		Title:        "test approval",
		CreatedAt:    time.Now(),
	}
	if err := repo.CreateApproval(ctx, approval); err != nil {
		t.Fatalf("insertTestApproval: %v", err)
	}
}

// activityUpdatesForChat filters user_updates to only chat_activity_changed
// events for the given chat.
func activityUpdatesForChat(t *testing.T, repo *Repo, chatID string) []UserUpdate {
	t.Helper()
	updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, 1000)
	if err != nil {
		t.Fatalf("activityUpdatesForChat: %v", err)
	}
	var result []UserUpdate
	for _, u := range updates {
		if u.UpdateType == UserUpdateChatActivityChanged {
			var data map[string]interface{}
			if err := json.Unmarshal(u.Data, &data); err == nil {
				if data["chat_id"] == chatID {
					result = append(result, u)
				}
			}
		}
	}
	return result
}

// activityValueFromUpdate extracts the "activity" int from a user_update's JSON data.
func activityValueFromUpdate(t *testing.T, u UserUpdate) int {
	t.Helper()
	var data map[string]interface{}
	if err := json.Unmarshal(u.Data, &data); err != nil {
		t.Fatalf("activityValueFromUpdate: unmarshal: %v", err)
	}
	v, ok := data["activity"]
	if !ok {
		t.Fatalf("activityValueFromUpdate: missing 'activity' key")
	}
	return int(v.(float64))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGetChatActivity_Idle(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-idle"
	createActivityTestChat(t, repo, chatID)

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 0 {
		t.Fatalf("expected activity=0 (IDLE), got %d", activity)
	}
}

func TestGetChatActivity_Running(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-running"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-1", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING), got %d", activity)
	}
}

func TestGetChatActivity_RunningWithThreadWorkflow(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-thread"
	createActivityTestChat(t, repo, chatID)

	// Only a thread-prefixed workflow is running. Previously excluded
	// from the activity view, causing the chat to appear IDLE.
	insertTestWorkflow(t, repo, "wf-thread", chatID, "thread:abc123", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING) for thread workflow, got %d", activity)
	}
}

func TestGetChatActivity_AwaitingInput_PendingApproval(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-approval"
	createActivityTestChat(t, repo, chatID)
	insertTestApproval(t, repo, "approval-1", chatID, 1) // status=1 → PENDING

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 2 {
		t.Fatalf("expected activity=2 (AWAITING_INPUT), got %d", activity)
	}
}

func TestGetChatActivity_ApprovalTakesPriorityOverRunning(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-priority"
	createActivityTestChat(t, repo, chatID)

	// Both a running workflow AND a pending approval exist.
	insertTestWorkflow(t, repo, "wf-run", chatID, "builtin://agent", Active())
	insertTestApproval(t, repo, "approval-pri", chatID, 1) // PENDING

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 2 {
		t.Fatalf("expected activity=2 (AWAITING_INPUT takes priority over RUNNING), got %d", activity)
	}
}

func TestGetChatActivity_Paused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-paused"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-paused", chatID, "builtin://agent", Paused())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 4 {
		t.Fatalf("expected activity=4 (PAUSED), got %d", activity)
	}
}

func TestGetChatActivity_RunningTakesPriorityOverPaused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-running-and-paused"
	createActivityTestChat(t, repo, chatID)

	// One workflow running, another paused (e.g. a paused thread alongside a
	// live one). The chat must still read RUNNING.
	insertTestWorkflow(t, repo, "wf-run", chatID, "builtin://agent", Active())
	insertTestWorkflow(t, repo, "wf-paused", chatID, "thread:abc123", Paused())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING takes priority over PAUSED), got %d", activity)
	}
}

func TestEmitChatActivityIfChanged_AlwaysEmits(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-always-emit"
	createActivityTestChat(t, repo, chatID)

	// Call emitChatActivityIfChanged twice with the same underlying state (IDLE).
	// Both calls should produce a user_update row — no dedup.
	if err := repo.emitChatActivityIfChanged(ctx, chatID); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	if err := repo.emitChatActivityIfChanged(ctx, chatID); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	events := activityUpdatesForChat(t, repo, chatID)
	if len(events) != 2 {
		t.Fatalf("expected 2 activity events (no dedup), got %d", len(events))
	}
}

func TestEmitChatActivityIfChanged_TransitionRunningToIdle(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-transition"
	createActivityTestChat(t, repo, chatID)

	// Start a workflow → activity should be RUNNING (1).
	insertTestWorkflow(t, repo, "wf-transition", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatActivity after start: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING) after workflow start, got %d", activity)
	}

	// Complete the workflow → activity should transition to IDLE (0).
	if err := repo.UpdateWorkflowStatus(ctx, "wf-transition", Completed()); err != nil {
		t.Fatalf("UpdateWorkflowStatus: %v", err)
	}

	activity, err = repo.GetChatActivity(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatActivity after complete: %v", err)
	}
	if activity != 0 {
		t.Fatalf("expected activity=0 (IDLE) after workflow completion, got %d", activity)
	}

	// Verify the emitted events reflect the transition.
	// CreateWorkflow emits 1 activity event, UpdateWorkflowStatus emits another.
	events := activityUpdatesForChat(t, repo, chatID)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 activity events (running + idle), got %d", len(events))
	}

	// The last event should reflect IDLE (0).
	lastActivity := activityValueFromUpdate(t, events[len(events)-1])
	if lastActivity != 0 {
		t.Fatalf("expected last activity event to be 0 (IDLE), got %d", lastActivity)
	}

	// Verify at least one RUNNING event was emitted.
	foundRunning := false
	for _, ev := range events {
		if activityValueFromUpdate(t, ev) == 1 {
			foundRunning = true
			break
		}
	}
	if !foundRunning {
		t.Fatal("expected at least one activity event with activity=1 (RUNNING)")
	}
}

// ---------------------------------------------------------------------------
// ERROR recency (recovered chats must not stay red)
// ---------------------------------------------------------------------------

// completeWorkflowNow drives a workflow to a terminal status through the
// production path, which is what stamps completed_at (see the CASE in
// queries/workflows.sql). The ERROR branch compares those timestamps, so a
// fixture that only INSERTed rows would not exercise it.
func completeWorkflowNow(t *testing.T, repo *Repo, id string, status WorkflowStatus) {
	t.Helper()
	if err := repo.UpdateWorkflowStatus(context.Background(), id, status); err != nil {
		t.Fatalf("completeWorkflowNow(%s, %d): %v", id, status, err)
	}
}

func TestGetChatActivity_Error_WhenNothingSucceededAfterFailure(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-failed"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 3 {
		t.Fatalf("a chat whose only terminal workflow failed must read ERROR (3), got %d", activity)
	}
}

// The regression this file exists to pin: a chat that failed once and then
// recovered must stop reading ERROR. The old view asked
// `EXISTS (status = 4)` over the chat's whole history, so a single failure
// pinned the chat red forever and the sidebar sorted it up as "needs
// attention" for the rest of its life.
//
// Observed on chat c0ce9449-…: two spawned workflows failed, twenty later
// workflows completed (the last five hours afterwards), and the view still
// returned 3.
func TestGetChatActivity_ErrorClearedAfterRecovery(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-recovered"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	// The user retries and it works. completed_at is stamped by the same
	// production path, so the retry lands strictly after the failure.
	insertTestWorkflow(t, repo, "wf-retry", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-retry", Completed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 0 {
		t.Fatalf("a chat that recovered after a failure must read IDLE (0), got %d "+
			"(the red dot must clear once work succeeds again)", activity)
	}
}

// A success that happened BEFORE the failure must not clear it — otherwise any
// chat with one early success would be permanently immune to showing an error.
func TestGetChatActivity_ErrorNotClearedByEarlierSuccess(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-success-then-failure"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-ok", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-ok", Completed())

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 3 {
		t.Fatalf("a failure after a success must still read ERROR (3), got %d", activity)
	}
}

// A live run outranks a past failure: the chat is working again, so it reads
// RUNNING rather than advertising an error the user can do nothing about.
func TestGetChatActivity_RunningTakesPriorityOverPastFailure(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-failed-then-running"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())
	insertTestWorkflow(t, repo, "wf-live", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING takes priority over a past failure), got %d", activity)
	}
}

// ---------------------------------------------------------------------------
// Sequence allocation (SERIALIZABLE contention)
// ---------------------------------------------------------------------------

// Sequence numbers are the cursor a reconnecting stream resumes from. The
// scoped counter and ledger insert share a transaction, so committed values
// must be contiguous: another user's traffic cannot create a gap, and a
// rollback rolls the counter increment back with the update.
func TestUserUpdateSequencesStrictlyIncrease(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-seq-mono"
	createActivityTestChat(t, repo, chatID)

	ctx := context.Background()
	var seqs []int64
	for i := 0; i < 8; i++ {
		if err := repo.emitChatActivityChanged(ctx, chatID, i%5); err != nil {
			t.Fatalf("emitChatActivityChanged(%d): %v", i, err)
		}
		updates := activityUpdatesForChat(t, repo, chatID)
		seqs = append(seqs, updates[len(updates)-1].SequenceNumber)
	}

	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("sequence numbers must be contiguous, got %v (index %d did not advance by one)", seqs, i)
		}
	}
}

// Every update sequence for a user is allocated from ONE counter row, so all
// of that user's concurrent chats serialize on it. This test pins the property
// that matters: under realistic fan-out those writes must all SUCCEED, not
// merely mostly succeed, and the cursor they produce must stay contiguous. A
// dropped allocation is a lost update event in the UI.
//
// This used to fail intermittently (4 runs in 20 on main) with "could not
// serialize access due to concurrent update (SQLSTATE 40001)". That was not a
// test bug: under SERIALIZABLE, a writer that WAITS on the counter row is
// aborted the moment the holder commits, so 48 writers on one row kept aborting
// each other until someone exhausted the retry budget. CreateUserUpdate now
// allocates at READ COMMITTED, where a waiter re-reads the committed row and
// takes the next number instead — so this fan-out cannot raise 40001 at all,
// and passes deterministically rather than by winning a race against the
// retry ladder. TestUserUpdateAllocationWaitsOutAConcurrentCommit pins that
// mechanism directly, without relying on a race to reach it.
func TestConcurrentUserUpdatesDoNotSerializationConflict(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	const (
		writers         = 48
		writesPerWriter = 5
	)
	chatIDs := make([]string, writers)
	for i := range chatIDs {
		chatIDs[i] = fmt.Sprintf("chat-seq-conc-%d", i)
		createActivityTestChat(t, repo, chatIDs[i])
	}

	// All chats belong to the SAME user ("test-user"), which is the whole
	// point: user_updates has one counter per user, so every chat in a
	// multi-spawn run contends on it.
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(chatID string) {
			defer wg.Done()
			for n := 0; n < writesPerWriter; n++ {
				if err := repo.emitChatActivityChanged(context.Background(), chatID, n%5); err != nil {
					errCh <- err
					return
				}
			}
		}(chatIDs[i])
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent user-update writes must not fail: %v", err)
	}

	// Weakening isolation must not cost the cursor contract: every write
	// landed, and the user's stream has no gap a reconnecting client would
	// read as lost delivery.
	updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, writers*writesPerWriter+1)
	if err != nil {
		t.Fatalf("GetUserUpdatesSince: %v", err)
	}
	if len(updates) != writers*writesPerWriter {
		t.Fatalf("got %d user updates, want %d", len(updates), writers*writesPerWriter)
	}
	for i := 1; i < len(updates); i++ {
		if updates[i].SequenceNumber != updates[i-1].SequenceNumber+1 {
			t.Fatalf("user update sequence has a gap at index %d: %d then %d",
				i, updates[i-1].SequenceNumber, updates[i].SequenceNumber)
		}
	}
}

// The deterministic form of the race above: a writer that has to WAIT for a
// user's counter row must, once the holder commits, take the next number — not
// be aborted because the holder got there first.
//
// A rival transaction takes the row lock first and commits only once the
// writer is provably blocked behind it (pg_stat_activity shows the lock wait),
// then immediately takes the lock again for the writer's next attempt. Under
// SERIALIZABLE every one of those commits aborts the writer with SQLSTATE
// 40001, so the writer burns its whole retry budget and the update is lost —
// this fails on every run against the old code, not one run in five. At READ
// COMMITTED the writer waits out each commit in its ONE transaction and takes
// the number after the last of them.
func TestUserUpdateAllocationWaitsOutAConcurrentCommit(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const userID = "contended-user"
	newUpdate := func(entityID string) *UserUpdate {
		return &UserUpdate{
			UserID:     userID,
			UpdateType: UserUpdateNotification,
			EntityType: EntityTypeSystem,
			EntityID:   entityID,
			Data:       []byte(`{}`),
		}
	}

	// Seed the counter row so every later allocation contends on an existing
	// row, which is the production steady state.
	seed := newUpdate("seed")
	if err := repo.CreateUserUpdate(ctx, seed); err != nil {
		t.Fatalf("seed CreateUserUpdate: %v", err)
	}

	// holdCounterRow is the rival: another of this user's chats allocating
	// from the same row, left uncommitted until we choose to release it.
	holdCounterRow := func() *sql.Tx {
		t.Helper()
		tx, err := rawDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("rival BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE update_stream_counters
			SET last_assigned_seq = last_assigned_seq + 1
			WHERE stream_kind = 'user' AND stream_id = $1`, userID); err != nil {
			_ = tx.Rollback()
			t.Fatalf("rival allocation: %v", err)
		}
		return tx
	}

	// waitingTxn identifies the transaction seen waiting on the counter row. A
	// backend's xact_start is fixed for the life of one transaction, so a
	// writer that was aborted and retried shows up as a different waitingTxn
	// even when the pool hands the retry the same connection.
	type waitingTxn struct {
		pid       int
		xactStart time.Time
	}

	// awaitWriter blocks until the writer is either waiting on a lock or has
	// returned, and reports which transaction is waiting. pg_stat_activity is
	// read through the pool, outside any transaction, so each poll sees a
	// fresh view. The database belongs to this test alone and the rival only
	// ever HOLDS the lock, so the writer is the only backend that can wait.
	writerDone := make(chan error, 1)
	awaitWriter := func() (finished bool, waiter waitingTxn, writerErr error) {
		t.Helper()
		for {
			select {
			case err := <-writerDone:
				return true, waitingTxn{}, err
			case <-ctx.Done():
				t.Fatalf("writer neither blocked on the counter row nor finished: %v", ctx.Err())
			default:
			}
			rows, err := rawDB.QueryContext(ctx, `
				SELECT pid, xact_start FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
				  AND pid <> pg_backend_pid()`)
			if err != nil {
				t.Fatalf("poll pg_stat_activity: %v", err)
			}
			var waiters []waitingTxn
			for rows.Next() {
				var w waitingTxn
				if err := rows.Scan(&w.pid, &w.xactStart); err != nil {
					t.Fatalf("scan pg_stat_activity: %v", err)
				}
				waiters = append(waiters, w)
			}
			if err := rows.Close(); err != nil {
				t.Fatalf("close pg_stat_activity rows: %v", err)
			}
			switch len(waiters) {
			case 0:
			case 1:
				return false, waiters[0], nil
			default:
				t.Fatalf("expected only the writer to wait on a lock, found %d waiters: %+v", len(waiters), waiters)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	rival := holdCounterRow()
	writer := newUpdate("writer")
	go func() { writerDone <- repo.CreateUserUpdate(ctx, writer) }()

	// Commit under the writer once per writer attempt, plus one, so a writer
	// that retries on every commit exhausts its budget rather than being
	// stopped short of it.
	//
	// The writer may legitimately wait behind more than one of these commits.
	// The rival takes the lock again the instant it commits, and the writer —
	// woken inside Postgres — still has to re-find the row's NEW version and
	// lock that; on a loaded box the rival's next UPDATE regularly gets there
	// first, and the writer waits again. What READ COMMITTED guarantees is
	// that every one of those waits is the SAME transaction, never an abort
	// and a retry. That is what this asserts, not how many waits it took.
	rivalCommits := 0
	var writerErr error
	var writerTxn waitingTxn
	finished := false
	for round := 0; round <= maxRetries+1 && !finished; round++ {
		var waiter waitingTxn
		finished, waiter, writerErr = awaitWriter()
		if finished {
			break
		}
		if round == 0 {
			writerTxn = waiter
		} else if waiter != writerTxn {
			t.Fatalf("after %d rival commit(s) the writer is waiting in a NEW transaction %+v, "+
				"not the one that first waited %+v: it was aborted and retried instead of "+
				"waiting out the commit", rivalCommits, waiter, writerTxn)
		}
		if err := rival.Commit(); err != nil {
			t.Fatalf("rival commit: %v", err)
		}
		rivalCommits++
		rival = holdCounterRow()
	}
	_ = rival.Rollback()
	if !finished {
		// The writer lost every race for the row; with the rival gone it can
		// only proceed now.
		select {
		case writerErr = <-writerDone:
		case <-ctx.Done():
			t.Fatalf("writer did not finish once the rival released the row: %v", ctx.Err())
		}
	}

	if writerErr != nil {
		t.Fatalf("a writer waiting on the counter row was aborted instead of waiting "+
			"(rival commits: %d): %v", rivalCommits, writerErr)
	}
	if rivalCommits == 0 {
		t.Fatal("the writer never waited on the rival's lock, so this run proved nothing")
	}
	if rivalCommits > 1 {
		t.Logf("writer waited out %d rival commits in one transaction", rivalCommits)
	}
	// Seed took 1 and each committed rival one more: the writer must observe
	// the last committed value and take the very next number.
	if want := seed.SequenceNumber + int64(rivalCommits) + 1; writer.SequenceNumber != want {
		t.Fatalf("writer sequence = %d, want %d (the number after the last rival commit's)",
			writer.SequenceNumber, want)
	}
}
