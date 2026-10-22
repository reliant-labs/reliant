// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"slices"
	"testing"
	"time"
)

func insertPendingQuestionTest(t *testing.T, repo *Repo, id, chatID, threadID, stepID string, status int, createdAt time.Time) {
	t.Helper()
	metadata := `{"questions":[{"question":"` + stepID + `?"}]}`
	q := &Question{
		ID:                 id,
		ChatID:             chatID,
		WorkflowID:         chatID,
		TemporalWorkflowID: chatID,
		ThreadID:           threadID,
		StepID:             stepID,
		Status:             status,
		Metadata:           &metadata,
		CreatedAt:          createdAt,
	}
	if err := repo.CreateQuestion(context.Background(), q); err != nil {
		t.Fatalf("CreateQuestion(%s): %v", id, err)
	}
}

// TestPendingQuestionsByThread_MultiThreadChat pins the fix for the smear: the
// removed newest-pending-per-chat accessor (ORDER BY created_at DESC LIMIT 1)
// reported the newest question from ANY thread of a chat with several spawned
// threads. PendingQuestionsByThread keeps the scoping the row already carries.
func TestPendingQuestionsByThread_MultiThreadChat(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-multi-thread"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// Two threads are genuinely gated; a third's question was already answered.
	insertPendingQuestionTest(t, repo, "q-a", chatID, "thread-a", "review_checkpoint", QuestionStatusPending, base)
	insertPendingQuestionTest(t, repo, "q-b", chatID, "thread-b", "stuck_checkpoint", QuestionStatusPending, base.Add(10*time.Minute))
	insertPendingQuestionTest(t, repo, "q-c", chatID, "thread-c", "ask_question", QuestionStatusResolved, base.Add(20*time.Minute))
	// A different chat must not leak in.
	insertPendingQuestionTest(t, repo, "q-other", "chat-other", "thread-z", "execute_tools", QuestionStatusPending, base.Add(30*time.Minute))

	byThread, err := repo.PendingQuestionsByThread(ctx, chatID)
	if err != nil {
		t.Fatalf("PendingQuestionsByThread: %v", err)
	}

	if len(byThread) != 2 {
		t.Fatalf("got %d gated threads, want 2: %v", len(byThread), byThread)
	}
	if q := byThread["thread-a"]; q == nil || q.StepID != "review_checkpoint" {
		t.Errorf("thread-a = %+v, want the review_checkpoint question", q)
	}
	if q := byThread["thread-b"]; q == nil || q.StepID != "stuck_checkpoint" {
		t.Errorf("thread-b = %+v, want the stuck_checkpoint question", q)
	}
	if _, gated := byThread["thread-c"]; gated {
		t.Error("thread-c has only a RESOLVED question and must not be reported as gated")
	}
	if _, leaked := byThread["thread-z"]; leaked {
		t.Error("a question from another chat leaked into the result")
	}
}

// TestPendingQuestionsByThread_NewestPerThread pins that a thread which raised
// several pending questions reports its newest one.
func TestPendingQuestionsByThread_NewestPerThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-repeat"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	insertPendingQuestionTest(t, repo, "q-old", chatID, "thread-a", "first_gate", QuestionStatusPending, base)
	insertPendingQuestionTest(t, repo, "q-new", chatID, "thread-a", "second_gate", QuestionStatusPending, base.Add(5*time.Minute))

	byThread, err := repo.PendingQuestionsByThread(ctx, chatID)
	if err != nil {
		t.Fatalf("PendingQuestionsByThread: %v", err)
	}
	if len(byThread) != 1 {
		t.Fatalf("got %d threads, want 1", len(byThread))
	}
	if q := byThread["thread-a"]; q == nil || q.ID != "q-new" {
		t.Errorf("thread-a = %+v, want the newest pending question q-new", q)
	}
}

// TestListPendingQuestionsByChat_EveryThread pins what the UI reads: every
// pending question in the chat, oldest first, each with its thread — including
// an older question from one thread that a sibling's newer question used to
// hide (a newest-per-chat LIMIT 1), and both of a thread's pending
// rows rather than only its newest.
func TestListPendingQuestionsByChat_EveryThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-list-pending"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	insertPendingQuestionTest(t, repo, "q-a", chatID, "thread-a", "ask_user", QuestionStatusPending, base)
	insertPendingQuestionTest(t, repo, "q-b1", chatID, "thread-b", "ask_user", QuestionStatusResolved, base.Add(5*time.Minute))
	insertPendingQuestionTest(t, repo, "q-b2", chatID, "thread-b", "ask_user", QuestionStatusPending, base.Add(10*time.Minute))
	insertPendingQuestionTest(t, repo, "q-b3", chatID, "thread-b", "ask_user", QuestionStatusPending, base.Add(20*time.Minute))
	insertPendingQuestionTest(t, repo, "q-other", "chat-other-list", "thread-z", "ask_user", QuestionStatusPending, base)

	got, err := repo.ListPendingQuestionsByChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ListPendingQuestionsByChat: %v", err)
	}
	var ids, threads []string
	for _, q := range got {
		ids = append(ids, q.ID)
		threads = append(threads, q.ThreadID)
	}
	if want := []string{"q-a", "q-b2", "q-b3"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (every pending question, oldest first, this chat only)", ids, want)
	}
	if want := []string{"thread-a", "thread-b", "thread-b"}; !slices.Equal(threads, want) {
		t.Errorf("threads = %v, want %v", threads, want)
	}

	empty, err := repo.ListPendingQuestionsByChat(ctx, "chat-nothing-pending")
	if err != nil {
		t.Fatalf("ListPendingQuestionsByChat(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d questions for a quiet chat, want 0", len(empty))
	}
}

// TestPendingQuestionsByThread_Empty covers a chat with nothing pending.
func TestPendingQuestionsByThread_Empty(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	byThread, err := repo.PendingQuestionsByThread(context.Background(), "chat-quiet")
	if err != nil {
		t.Fatalf("PendingQuestionsByThread: %v", err)
	}
	if len(byThread) != 0 {
		t.Errorf("got %d gated threads, want 0", len(byThread))
	}
}

// TestLastThreadActivityByChat covers the per-thread progress marker: messages
// are the only durable evidence a spawned thread is still working (step
// executions and position checkpoints exist for the root run only).
func TestLastThreadActivityByChat(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// Messages reference a chat, a thread and a context window by foreign key,
	// so the fixture has to build the rows it implies rather than inserting a
	// message into empty space.
	for _, chatID := range []string{"chat-a", "chat-b"} {
		createActivityTestChat(t, repo, chatID)
	}
	for _, spec := range []struct{ threadID, chatID string }{
		{"thread-busy", "chat-a"},
		{"thread-quiet", "chat-a"},
		{"thread-other", "chat-b"},
	} {
		if _, err := repo.CreateThread(ctx, &Thread{
			ID:        spec.threadID,
			ChatID:    spec.chatID,
			CreatedAt: base,
		}); err != nil {
			t.Fatalf("create thread %s: %v", spec.threadID, err)
		}
	}
	if _, err := rawDB.ExecContext(ctx,
		`INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES ('cw-1', 'thread-busy', 0, $1)`,
		base); err != nil {
		t.Fatalf("insert context window: %v", err)
	}

	insert := func(id, chatID, threadID string, ordinal int, at time.Time) {
		t.Helper()
		_, err := rawDB.ExecContext(ctx,
			`INSERT INTO messages (id, chat_id, ordinal, seq, thread_id, context_window_id, role, created_at, updated_at)
			 VALUES ($1, $2, $3, $3, $4, 'cw-1', 2, $5, $5)`, id, chatID, ordinal, threadID, at)
		if err != nil {
			t.Fatalf("insert message %s: %v", id, err)
		}
	}

	insert("m1", "chat-a", "thread-busy", 1, base)
	insert("m2", "chat-a", "thread-busy", 2, base.Add(30*time.Minute)) // newest for its thread
	insert("m3", "chat-a", "thread-quiet", 3, base)
	insert("m4", "chat-b", "thread-other", 1, base.Add(59*time.Minute))

	activity, err := repo.LastThreadActivityByChat(ctx, "chat-a")
	if err != nil {
		t.Fatalf("LastThreadActivityByChat: %v", err)
	}
	if len(activity) != 2 {
		t.Fatalf("got %d threads, want 2: %v", len(activity), activity)
	}
	if got := activity["thread-busy"]; !got.Equal(base.Add(30 * time.Minute)) {
		t.Errorf("thread-busy = %v, want the newest message %v", got, base.Add(30*time.Minute))
	}
	if got := activity["thread-quiet"]; !got.Equal(base) {
		t.Errorf("thread-quiet = %v, want %v", got, base)
	}
	if _, leaked := activity["thread-other"]; leaked {
		t.Error("a thread from another chat leaked into the result")
	}
}
