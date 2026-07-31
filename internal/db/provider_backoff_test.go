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
