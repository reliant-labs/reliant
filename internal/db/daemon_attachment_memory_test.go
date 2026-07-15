// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestDaemonAttachmentMemoryTelemetry verifies the heartbeat-driven memory
// telemetry lifecycle on the attachment record: update, fresh-list exposure,
// reset on re-attachment, and graceful no-op when the row doesn't exist.
func TestDaemonAttachmentMemoryTelemetry(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "mem-test-user"
	const daemonID = "mem-test-daemon"

	clean := func() {
		_, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID)
	}
	clean()
	t.Cleanup(clean)

	// No row yet: updating memory must be a silent no-op, not an error.
	if err := repo.UpdateDaemonAttachmentMemory(ctx, daemonID, 1, 2, true); err != nil {
		t.Fatalf("UpdateDaemonAttachmentMemory without row: %v", err)
	}

	att := &DaemonAttachment{
		DaemonID: daemonID,
		UserID:   userID,
		Source:   DaemonAttachmentSourceInbound,
	}
	if err := repo.UpsertDaemonAttachment(ctx, att); err != nil {
		t.Fatalf("UpsertDaemonAttachment: %v", err)
	}

	// Record telemetry as the heartbeat handler would.
	const used, limit = int64(3_650_722_201), int64(4_294_967_296)
	if err := repo.UpdateDaemonAttachmentMemory(ctx, daemonID, used, limit, true); err != nil {
		t.Fatalf("UpdateDaemonAttachmentMemory: %v", err)
	}

	fresh, err := repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
	if err != nil {
		t.Fatalf("ListFreshDaemonAttachmentsForUser: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("fresh attachments = %d, want 1", len(fresh))
	}
	got := fresh[0]
	if got.MemoryUsedBytes != used || got.MemoryLimitBytes != limit || !got.MemoryPressure {
		t.Errorf("telemetry = (%d, %d, %v), want (%d, %d, true)",
			got.MemoryUsedBytes, got.MemoryLimitBytes, got.MemoryPressure, used, limit)
	}

	// Re-attachment (new stream) resets telemetry — readings from the
	// previous daemon session are stale.
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID,
		UserID:   userID,
		Source:   DaemonAttachmentSourceInbound,
	}); err != nil {
		t.Fatalf("re-UpsertDaemonAttachment: %v", err)
	}
	fresh, err = repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
	if err != nil {
		t.Fatalf("ListFreshDaemonAttachmentsForUser after re-attach: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("fresh attachments after re-attach = %d, want 1", len(fresh))
	}
	got = fresh[0]
	if got.MemoryUsedBytes != 0 || got.MemoryLimitBytes != 0 || got.MemoryPressure {
		t.Errorf("telemetry after re-attach = (%d, %d, %v), want (0, 0, false)",
			got.MemoryUsedBytes, got.MemoryLimitBytes, got.MemoryPressure)
	}

	// A stale row falls out of the fresh list.
	if _, err := rawDB.Exec(
		`UPDATE daemon_attachment SET last_stream_activity = $1 WHERE daemon_id = $2`,
		time.Now().UTC().Add(-2*time.Minute), daemonID,
	); err != nil {
		t.Fatalf("backdating attachment: %v", err)
	}
	fresh, err = repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
	if err != nil {
		t.Fatalf("ListFreshDaemonAttachmentsForUser stale: %v", err)
	}
	if len(fresh) != 0 {
		t.Errorf("stale attachment leaked into fresh list: %d rows", len(fresh))
	}
}
