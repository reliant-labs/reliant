package audit

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests drive DBAuditStore through the production driver (pgx) against
// a real postgres, on the audit_log table as the audit-log recipe's migration
// creates it (pgAuditSchema), so the INSERT/SELECT/scan contract is pinned
// against what the database actually does — including the defaults that fill
// id and created_at.

func newTestStore(t *testing.T) *DBAuditStore {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.Exec(pgAuditSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return NewDBAuditStore(db)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDBAuditStore_WithSchema pins that WithSchema makes every statement name
// the table in that schema. A decoy audit_log in `public` is what a bare name
// resolves to on the default search_path — so a store that ignored the option
// would write the decoy and read it back, and only the per-schema row counts
// tell the two apart.
func TestDBAuditStore_WithSchema(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(pgAuditSchema); err != nil {
		t.Fatalf("create decoy public.audit_log: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA controlplane`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	qualified := strings.Replace(pgAuditSchema, "CREATE TABLE public.audit_log", "CREATE TABLE controlplane.audit_log", 1)
	if _, err := db.Exec(qualified); err != nil {
		t.Fatalf("create controlplane.audit_log: %v", err)
	}
	ctx := context.Background()

	store := NewDBAuditStore(db, WithSchema("controlplane"))
	if err := store.Log(ctx, Entry{Timestamp: time.Now().UTC(), Procedure: "/svc.v1.Users/Get", Status: "ok"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	got, err := store.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Procedure != "/svc.v1.Users/Get" {
		t.Fatalf("Query = %+v, want the one entry just logged", got)
	}

	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	if n := count("controlplane.audit_log"); n != 1 {
		t.Errorf("controlplane.audit_log has %d rows, want 1", n)
	}
	if n := count("public.audit_log"); n != 0 {
		t.Errorf("public.audit_log (what a bare audit_log resolves to) has %d rows, want 0", n)
	}
}

func TestDBAuditStore_LogAndQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Second)
	want := Entry{
		Timestamp:   ts,
		UserID:      "user_123",
		Email:       "u@example.com",
		Procedure:   "/svc.v1.Users/Get",
		PeerAddress: "10.0.0.1:5555",
		DurationMs:  1500,
		Status:      "ok",
		Metadata:    map[string]string{"trace_id": "abc123"},
	}
	if err := store.Log(ctx, want); err != nil {
		t.Fatalf("Log: %v", err)
	}

	got, err := store.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ID == "" {
		t.Error("ID not populated by the DB default")
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt not populated by the DB default")
	}
	if e.Timestamp.Unix() != ts.Unix() {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, ts)
	}
	if e.UserID != want.UserID || e.Email != want.Email {
		t.Errorf("identity = (%q,%q), want (%q,%q)", e.UserID, e.Email, want.UserID, want.Email)
	}
	if e.Procedure != want.Procedure || e.PeerAddress != want.PeerAddress {
		t.Errorf("proc/peer = (%q,%q), want (%q,%q)", e.Procedure, e.PeerAddress, want.Procedure, want.PeerAddress)
	}
	if e.DurationMs != want.DurationMs || e.Status != want.Status {
		t.Errorf("duration/status = (%d,%q), want (%d,%q)", e.DurationMs, e.Status, want.DurationMs, want.Status)
	}
	if e.Metadata["trace_id"] != "abc123" {
		t.Errorf("Metadata = %v, want trace_id=abc123", e.Metadata)
	}
	// A successful entry stores NULL error columns; those scan back as "".
	if e.ErrorCode != "" || e.ErrorMessage != "" {
		t.Errorf("error fields = (%q,%q), want empty", e.ErrorCode, e.ErrorMessage)
	}
}

func TestDBAuditStore_ErrorFieldsPersist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Log(ctx, Entry{
		Timestamp:    time.Now().UTC(),
		Procedure:    "/svc.v1.Users/Delete",
		Status:       "error",
		ErrorCode:    "permission_denied",
		ErrorMessage: "not allowed",
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	got, err := store.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(got))
	}
	if got[0].ErrorCode != "permission_denied" || got[0].ErrorMessage != "not allowed" {
		t.Errorf("error fields = (%q,%q), want (permission_denied, not allowed)",
			got[0].ErrorCode, got[0].ErrorMessage)
	}
}

func TestDBAuditStore_QueryFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	entries := []Entry{
		{Timestamp: base.Add(-3 * time.Hour), UserID: "a", Procedure: "/svc/Get", Status: "ok"},
		{Timestamp: base.Add(-2 * time.Hour), UserID: "b", Procedure: "/svc/List", Status: "ok"},
		{Timestamp: base.Add(-1 * time.Hour), UserID: "a", Procedure: "/svc/List", Status: "ok"},
	}
	for _, e := range entries {
		if err := store.Log(ctx, e); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	t.Run("by user", func(t *testing.T) {
		got, err := store.Query(ctx, Filter{UserID: "a"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		// Ordered by timestamp DESC — newest first.
		if got[0].Timestamp.Before(got[1].Timestamp) {
			t.Errorf("results not ordered newest-first: %v then %v", got[0].Timestamp, got[1].Timestamp)
		}
	})

	t.Run("by procedure", func(t *testing.T) {
		got, err := store.Query(ctx, Filter{Procedure: "/svc/List"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
	})

	t.Run("by time window", func(t *testing.T) {
		got, err := store.Query(ctx, Filter{Since: base.Add(-90 * time.Minute)})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 (only the -1h entry)", len(got))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		page1, err := store.Query(ctx, Filter{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("Query page1: %v", err)
		}
		if len(page1) != 2 {
			t.Fatalf("page1 len = %d, want 2", len(page1))
		}
		page2, err := store.Query(ctx, Filter{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("Query page2: %v", err)
		}
		if len(page2) != 1 {
			t.Fatalf("page2 len = %d, want 1", len(page2))
		}
		// Pages must not overlap.
		if page1[0].Timestamp.Equal(page2[0].Timestamp) {
			t.Error("page1 and page2 overlap")
		}
	})
}
