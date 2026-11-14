// Copyright (c) 2026 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestDaemonAttachmentHealth: a heartbeat's machine health is stored on the
// lease and read back by the fresh-attachment list (what the registry and
// wait_for_machine read); a new session starts unreported; and with no lease
// the write is a no-op, not an error.
func TestDaemonAttachmentHealth(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "health-test-user"
	const daemonID = "health-test-daemon"
	clean := func() {
		_, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID)
	}
	clean()
	t.Cleanup(clean)

	degraded := MachineHealth{
		State: MachineHealthDegraded, Code: "process_table_full",
		Reason: "This machine cannot start new processes",
		Since:  time.Date(2026, 10, 30, 17, 57, 0, 0, time.UTC),
	}
	if err := repo.UpdateDaemonAttachmentHealth(ctx, daemonID, degraded); err != nil {
		t.Fatalf("UpdateDaemonAttachmentHealth without a lease: %v", err)
	}

	attach := func() {
		t.Helper()
		if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound}); err != nil {
			t.Fatalf("UpsertDaemonAttachment: %v", err)
		}
	}
	read := func() MachineHealth {
		t.Helper()
		fresh, err := repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
		if err != nil {
			t.Fatalf("ListFreshDaemonAttachmentsForUser: %v", err)
		}
		if len(fresh) != 1 {
			t.Fatalf("fresh attachments = %d, want 1", len(fresh))
		}
		return fresh[0].Health
	}

	attach()
	if got := read(); got != (MachineHealth{}) {
		t.Fatalf("health of a new lease = %+v, want unreported", got)
	}

	if err := repo.UpdateDaemonAttachmentHealth(ctx, daemonID, degraded); err != nil {
		t.Fatalf("UpdateDaemonAttachmentHealth: %v", err)
	}
	got := read()
	if got.State != degraded.State || got.Code != degraded.Code || got.Reason != degraded.Reason || !got.Since.Equal(degraded.Since) {
		t.Fatalf("health = %+v, want %+v", got, degraded)
	}

	// A new stream is a new session: what the last one reported is stale.
	attach()
	if got := read(); got != (MachineHealth{}) {
		t.Fatalf("health after re-attach = %+v, want unreported", got)
	}
}
