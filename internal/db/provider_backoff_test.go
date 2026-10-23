// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// The provider-backoff marker is the only durable evidence a rate-limited thread
// produces: the retry ladder runs inside one Temporal activity attempt, writing
// no message, no step execution and no status change. These pin the two
// properties every reader depends on — that the marker says "parked RIGHT NOW"
// while the wait is open, and that the time lost survives the wait so post-hoc
// forensics can still answer where a finished run's time went.
func TestProviderBackoff_RecordClearAndAccumulate(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	// Rung 1: 2.4s, announced before the sleep.
	if err := repo.RecordProviderBackoff(ctx, "chat-1", "thread-a", 1, 8, 429, "http_429", 2400*time.Millisecond, base); err != nil {
		t.Fatalf("RecordProviderBackoff: %v", err)
	}

	got, err := repo.ProviderBackoffByChat(ctx, "chat-1")
	if err != nil {
		t.Fatalf("ProviderBackoffByChat: %v", err)
	}
	b := got["thread-a"]
	if !b.Waiting() {
		t.Fatal("thread is not marked as waiting: ps cannot tell backoff from work")
	}
	if b.Attempt != 1 || b.MaxAttempts != 8 || b.StatusCode != 429 || b.Reason != "http_429" {
		t.Errorf("marker = attempt %d/%d status %d reason %q, want 1/8 429 http_429",
			b.Attempt, b.MaxAttempts, b.StatusCode, b.Reason)
	}
	if want := base.Add(2400 * time.Millisecond); !b.ResumeAt.Equal(want) {
		t.Errorf("resume_at = %s, want %s — without it a killed activity leaves a marker that never expires", b.ResumeAt, want)
	}
	if b.Retries != 1 || b.WaitedMs != 0 {
		t.Errorf("retries=%d waited=%dms, want 1 retry and 0ms (the first wait has not been taken yet)", b.Retries, b.WaitedMs)
	}

	// Rung 2, recorded 2.4s later: the wait that just completed is charged in
	// full and the ladder advances.
	second := base.Add(2400 * time.Millisecond)
	if err := repo.RecordProviderBackoff(ctx, "chat-1", "thread-a", 2, 8, 429, "http_429", 4800*time.Millisecond, second); err != nil {
		t.Fatalf("RecordProviderBackoff rung 2: %v", err)
	}
	got, _ = repo.ProviderBackoffByChat(ctx, "chat-1")
	b = got["thread-a"]
	if b.Attempt != 2 || b.Retries != 2 {
		t.Errorf("attempt=%d retries=%d, want 2 and 2", b.Attempt, b.Retries)
	}
	if b.WaitedMs != 2400 {
		t.Errorf("waited = %dms, want 2400 (the completed rung)", b.WaitedMs)
	}

	// The provider answers 1.2s into a 4.8s rung. Only the slept portion is
	// charged — a wait cut short must not be billed as if it ran to completion.
	if err := repo.ClearProviderBackoff(ctx, "thread-a", second.Add(1200*time.Millisecond)); err != nil {
		t.Fatalf("ClearProviderBackoff: %v", err)
	}
	got, _ = repo.ProviderBackoffByChat(ctx, "chat-1")
	b = got["thread-a"]
	if b.Waiting() {
		t.Error("thread still reads as waiting after the provider answered")
	}
	if b.WaitedMs != 3600 {
		t.Errorf("waited = %dms, want 3600 (2400 + the 1200 actually slept)", b.WaitedMs)
	}
	if b.Retries != 2 {
		t.Errorf("retries = %d, want 2 — the cumulative cost must survive the wait", b.Retries)
	}

	// Clearing twice is a no-op: the caller clears unconditionally on every
	// non-retry event, so a second clear must not keep charging time.
	if err := repo.ClearProviderBackoff(ctx, "thread-a", second.Add(time.Hour)); err != nil {
		t.Fatalf("ClearProviderBackoff (idempotent): %v", err)
	}
	got, _ = repo.ProviderBackoffByChat(ctx, "chat-1")
	if got["thread-a"].WaitedMs != 3600 {
		t.Errorf("waited = %dms after a redundant clear, want 3600", got["thread-a"].WaitedMs)
	}

	// Markers are per thread and scoped to their chat: a fan-out has ten threads
	// open at once and only some of them are rate limited.
	if err := repo.RecordProviderBackoff(ctx, "chat-2", "thread-b", 1, 8, 503, "transient_gateway_error", time.Second, base); err != nil {
		t.Fatalf("RecordProviderBackoff other chat: %v", err)
	}
	got, _ = repo.ProviderBackoffByChat(ctx, "chat-1")
	if _, leaked := got["thread-b"]; leaked {
		t.Error("another chat's marker leaked into this chat's view")
	}
	if err := repo.ClearProviderBackoff(ctx, "thread-unknown", base); err != nil {
		t.Errorf("clearing an unmarked thread must be a no-op, got %v", err)
	}
}

// A run held until a provider's usage limit resets writes the SAME durable
// marker table as a driver's retry ladder, so `workflow ps` and the web can tell
// waiting from wedged. What is new is that it is the CHAT's activity: a live run
// whose ROOT thread is parked reads WAITING_FOR_USAGE_LIMIT (7), carrying the
// reset time and provider the footer shows.
func TestUsageLimitWait_MarkerDrivesChatActivity(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-limit-" + time.Now().Format("150405.000000")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.CreateChat(ctx, &Chat{ID: chatID, ProjectID: "test-project", UserID: "test-user", WorkflowID: &chatID}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if err := repo.CreateWorkflow(ctx, &Workflow{ID: chatID, ChatID: chatID, WorkflowName: "wf", Thread: chatID, Status: Active()}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	activity := func() int {
		t.Helper()
		a, err := repo.GetChatActivity(ctx, chatID)
		if err != nil {
			t.Fatalf("GetChatActivity: %v", err)
		}
		return a
	}
	if got := activity(); got != 1 {
		t.Fatalf("a live run with no wait reads RUNNING (1), got %d", got)
	}

	resumeAt := now.Add(72 * time.Minute)
	if err := repo.RecordUsageLimitWait(ctx, chatID, chatID, "Anthropic", false, resumeAt, now); err != nil {
		t.Fatalf("RecordUsageLimitWait: %v", err)
	}
	if got := activity(); got != 7 {
		t.Fatalf("a live run parked on a usage limit reads WAITING_FOR_USAGE_LIMIT (7), got %d", got)
	}

	chat, err := repo.GetChat(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if chat.UsageLimitResumesAt == nil || !chat.UsageLimitResumesAt.Equal(resumeAt) {
		t.Errorf("chat carries resume time %v, want %v", chat.UsageLimitResumesAt, resumeAt)
	}
	if chat.UsageLimitProvider != "Anthropic" || chat.UsageLimitProbing {
		t.Errorf("chat carries provider %q probing=%v, want Anthropic / false", chat.UsageLimitProvider, chat.UsageLimitProbing)
	}

	// The same marker is what `workflow ps` reads, and it tells a usage-limit
	// wait from a driver's retry ladder.
	got, err := repo.ProviderBackoffByChat(ctx, chatID)
	if err != nil {
		t.Fatalf("ProviderBackoffByChat: %v", err)
	}
	b := got[chatID]
	if !b.Waiting() || !b.IsUsageLimit() || b.Provider != "Anthropic" || b.Reason != ReasonUsageLimit {
		t.Errorf("marker = waiting=%v usageLimit=%v provider=%q reason=%q", b.Waiting(), b.IsUsageLimit(), b.Provider, b.Reason)
	}

	// An unknown reset re-parks on the next probe and says so.
	if err := repo.RecordUsageLimitWait(ctx, chatID, chatID, "", true, now.Add(5*time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("RecordUsageLimitWait probe: %v", err)
	}
	chat, _ = repo.GetChat(ctx, chatID)
	if !chat.UsageLimitProbing {
		t.Error("an unknown reset must read as a probe, not a promised time")
	}

	// Cleared on resume: the chat reads RUNNING again and the cost survives.
	if err := repo.ClearUsageLimitWait(ctx, chatID, chatID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ClearUsageLimitWait: %v", err)
	}
	if got := activity(); got != 1 {
		t.Fatalf("after the wait the run reads RUNNING (1), got %d", got)
	}
	chat, _ = repo.GetChat(ctx, chatID)
	if chat.UsageLimitResumesAt != nil {
		t.Errorf("a stale resume time outlived the wait: %v", chat.UsageLimitResumesAt)
	}
	got, _ = repo.ProviderBackoffByChat(ctx, chatID)
	if got[chatID].Waiting() || got[chatID].WaitedMs <= 0 || got[chatID].Retries != 2 {
		t.Errorf("row after clear: waiting=%v waited=%dms retries=%d, want released with its cost kept",
			got[chatID].Waiting(), got[chatID].WaitedMs, got[chatID].Retries)
	}
	if err := repo.ClearUsageLimitWait(ctx, chatID, chatID, now.Add(time.Hour)); err != nil {
		t.Errorf("clearing a released wait must be a no-op, got %v", err)
	}
}

// A sub-agent's wait is a marker on the CHILD's thread: the parent is free to
// work, so the chat must not read as waiting. And a stop releases every thread's
// marker at once (a terminated run runs no code to release its own).
func TestUsageLimitWait_SubAgentDoesNotParkTheChatAndStopReleasesAll(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-limit-sub-" + time.Now().Format("150405.000000")
	childThread := chatID + "-child"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.CreateChat(ctx, &Chat{ID: chatID, ProjectID: "test-project", UserID: "test-user", WorkflowID: &chatID}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if err := repo.CreateWorkflow(ctx, &Workflow{ID: chatID, ChatID: chatID, WorkflowName: "wf", Thread: chatID, Status: Active()}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	if err := repo.RecordUsageLimitWait(ctx, chatID, childThread, "OpenAI", false, now.Add(time.Hour), now); err != nil {
		t.Fatalf("RecordUsageLimitWait: %v", err)
	}
	if a, _ := repo.GetChatActivity(ctx, chatID); a != 1 {
		t.Errorf("a sub-agent's wait must not park the chat: activity = %d, want RUNNING (1)", a)
	}

	if err := repo.RecordUsageLimitWait(ctx, chatID, chatID, "OpenAI", false, now.Add(time.Hour), now); err != nil {
		t.Fatalf("RecordUsageLimitWait root: %v", err)
	}
	if a, _ := repo.GetChatActivity(ctx, chatID); a != 7 {
		t.Fatalf("activity = %d, want 7", a)
	}
	// A driver's own retry ladder on the same chat is not a usage-limit wait and
	// must survive the release.
	if err := repo.RecordProviderBackoff(ctx, chatID, "other-thread", 1, 8, 503, "http_503", time.Minute, now); err != nil {
		t.Fatalf("RecordProviderBackoff: %v", err)
	}

	if err := repo.ClearUsageLimitWait(ctx, chatID, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("ClearUsageLimitWait (all threads): %v", err)
	}
	got, _ := repo.ProviderBackoffByChat(ctx, chatID)
	if got[chatID].Waiting() || got[childThread].Waiting() {
		t.Error("a stop must release every thread's usage-limit marker")
	}
	if !got["other-thread"].Waiting() {
		t.Error("releasing usage-limit waits must not touch a driver's retry-ladder marker")
	}
	if a, _ := repo.GetChatActivity(ctx, chatID); a != 1 {
		t.Errorf("activity after release = %d, want RUNNING (1)", a)
	}
}

// A run that died mid-wait (its workflow is no longer running) must not leave
// the chat stuck on WAITING_FOR_USAGE_LIMIT: the activity only counts a marker
// while the root workflow is live.
func TestUsageLimitWait_DeadRunDoesNotLeaveChatWaiting(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-limit-dead-" + time.Now().Format("150405.000000")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.CreateChat(ctx, &Chat{ID: chatID, ProjectID: "test-project", UserID: "test-user", WorkflowID: &chatID}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if err := repo.CreateWorkflow(ctx, &Workflow{ID: chatID, ChatID: chatID, WorkflowName: "wf", Thread: chatID, Status: Cancelled()}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := repo.RecordUsageLimitWait(ctx, chatID, chatID, "Anthropic", false, now.Add(time.Hour), now); err != nil {
		t.Fatalf("RecordUsageLimitWait: %v", err)
	}
	if a, _ := repo.GetChatActivity(ctx, chatID); a == 7 {
		t.Error("a stopped run must not read as waiting on a usage limit")
	}
}
