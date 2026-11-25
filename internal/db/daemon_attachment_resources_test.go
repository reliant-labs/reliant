// Copyright (c) 2026 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// TestDaemonAttachmentResourceStats: a heartbeat's resource reading is stored
// on the lease (resource_stats) and read back by both attachment readers, the
// user's fresh list and the all-leases list; another user's list never shows
// it; a new session starts unreported; and with no lease the write is a no-op,
// not an error.
func TestDaemonAttachmentResourceStats(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID = "resources-test-user"
	const otherUserID = "resources-test-other-user"
	const daemonID = "resources-test-daemon"
	const otherDaemonID = "resources-test-other-daemon"
	clean := func() {
		_, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id IN ($1, $2)`, userID, otherUserID)
	}
	clean()
	t.Cleanup(clean)

	// No lease yet: the write matches nothing and is not an error.
	if err := store.SetDaemonAttachmentResources(ctx, repo.ORM(), daemonID, `{"machine":{"cpus":4}}`); err != nil {
		t.Fatalf("SetDaemonAttachmentResources without a lease: %v", err)
	}
	var leases int
	if err := rawDB.QueryRow(`SELECT count(*) FROM daemon_attachment WHERE daemon_id = $1`, daemonID).Scan(&leases); err != nil {
		t.Fatalf("counting leases: %v", err)
	}
	if leases != 0 {
		t.Fatalf("a resource write without a lease created %d lease row(s)", leases)
	}

	attach := func(daemon, user string) {
		t.Helper()
		if err := repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
			DaemonID: daemon, UserID: user, Source: DaemonAttachmentSourceInbound,
		}); err != nil {
			t.Fatalf("UpsertDaemonAttachment(%s): %v", daemon, err)
		}
	}
	attach(daemonID, userID)
	attach(otherDaemonID, otherUserID)

	fresh := func(user string) map[string]*DaemonAttachment {
		t.Helper()
		atts, err := repo.ListFreshDaemonAttachmentsForUser(ctx, user, time.Minute)
		if err != nil {
			t.Fatalf("ListFreshDaemonAttachmentsForUser(%s): %v", user, err)
		}
		out := map[string]*DaemonAttachment{}
		for _, a := range atts {
			out[a.DaemonID] = a
		}
		return out
	}
	all := func() map[string]*DaemonAttachment {
		t.Helper()
		atts, err := repo.ListAllDaemonAttachments(ctx)
		if err != nil {
			t.Fatalf("ListAllDaemonAttachments: %v", err)
		}
		out := map[string]*DaemonAttachment{}
		for _, a := range atts {
			out[a.DaemonID] = a
		}
		return out
	}

	// A new lease reads as "not reported" through both readers.
	if got := fresh(userID)[daemonID]; got == nil || got.ResourceStats != "" {
		t.Fatalf("new lease's resource stats = %+v, want an attached lease with none", got)
	}
	if got := all()[daemonID]; got == nil || got.ResourceStats != "" {
		t.Fatalf("new lease in the all-leases list = %+v, want one with no resource stats", got)
	}

	const reading = `{"machine":{"cpus":4,"memTotalBytes":"1000"},"process":{"goroutines":50}}`
	if err := store.SetDaemonAttachmentResources(ctx, repo.ORM(), daemonID, reading); err != nil {
		t.Fatalf("SetDaemonAttachmentResources: %v", err)
	}
	if got := fresh(userID)[daemonID]; got == nil || got.ResourceStats != reading {
		t.Fatalf("fresh list resource stats = %+v, want %q", got, reading)
	}
	if got := all()[daemonID]; got == nil || got.ResourceStats != reading {
		t.Fatalf("all-leases list resource stats = %+v, want %q", got, reading)
	}

	// Another user's list never carries this user's lease, and the write
	// touched only the named daemon's lease.
	other := fresh(otherUserID)
	if _, leaked := other[daemonID]; leaked {
		t.Fatalf("another user's fresh list includes %s", daemonID)
	}
	if got := other[otherDaemonID]; got == nil || got.ResourceStats != "" {
		t.Fatalf("other user's lease = %+v, want it attached with no resource stats", got)
	}

	// A re-attachment is a new session: the previous session's reading is
	// stale and is reset.
	attach(daemonID, userID)
	if got := fresh(userID)[daemonID]; got == nil || got.ResourceStats != "" {
		t.Fatalf("resource stats after re-attachment = %+v, want reset to none", got)
	}
}
