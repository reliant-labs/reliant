// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestDaemonAttachmentDetectedPorts verifies the heartbeat-driven detected
// listener port lifecycle on the attachment record: update, fresh-list
// exposure, clearing, reset on re-attachment, and graceful no-op when the
// row doesn't exist. Mirrors TestDaemonAttachmentMemoryTelemetry.
func TestDaemonAttachmentDetectedPorts(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "ports-test-user"
	const daemonID = "ports-test-daemon"

	clean := func() {
		_, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID)
	}
	clean()
	t.Cleanup(clean)

	// No row yet: updating ports must be a silent no-op, not an error.
	if err := repo.UpdateDaemonAttachmentPorts(ctx, daemonID, []uint32{5174}); err != nil {
		t.Fatalf("UpdateDaemonAttachmentPorts without row: %v", err)
	}

	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID,
		UserID:   userID,
		Source:   DaemonAttachmentSourceInbound,
	}); err != nil {
		t.Fatalf("UpsertDaemonAttachment: %v", err)
	}

	freshPorts := func(context string) []uint32 {
		t.Helper()
		fresh, err := repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
		if err != nil {
			t.Fatalf("ListFreshDaemonAttachmentsForUser (%s): %v", context, err)
		}
		if len(fresh) != 1 {
			t.Fatalf("fresh attachments (%s) = %d, want 1", context, len(fresh))
		}
		return fresh[0].DetectedPorts
	}

	// Fresh attachment starts with no ports.
	if got := freshPorts("initial"); len(got) != 0 {
		t.Errorf("initial detected ports = %v, want none", got)
	}

	// Record ports as the heartbeat handler would.
	want := []uint32{3000, 5174}
	if err := repo.UpdateDaemonAttachmentPorts(ctx, daemonID, want); err != nil {
		t.Fatalf("UpdateDaemonAttachmentPorts: %v", err)
	}
	if got := freshPorts("after update"); !slices.Equal(got, want) {
		t.Errorf("detected ports = %v, want %v", got, want)
	}

	// A nil update clears the set (dev server stopped).
	if err := repo.UpdateDaemonAttachmentPorts(ctx, daemonID, nil); err != nil {
		t.Fatalf("UpdateDaemonAttachmentPorts(nil): %v", err)
	}
	if got := freshPorts("after clear"); len(got) != 0 {
		t.Errorf("detected ports after clear = %v, want none", got)
	}

	// Re-attachment (new stream) resets ports — a fresh session's listener
	// set is unknown until its first heartbeat.
	if err := repo.UpdateDaemonAttachmentPorts(ctx, daemonID, []uint32{8080}); err != nil {
		t.Fatalf("UpdateDaemonAttachmentPorts pre-re-attach: %v", err)
	}
	if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID,
		UserID:   userID,
		Source:   DaemonAttachmentSourceInbound,
	}); err != nil {
		t.Fatalf("re-UpsertDaemonAttachment: %v", err)
	}
	if got := freshPorts("after re-attach"); len(got) != 0 {
		t.Errorf("detected ports after re-attach = %v, want none", got)
	}
}
