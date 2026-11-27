package audit

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestWithSchema_TableName(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []StoreOption
		want string
	}{
		{"no option", nil, `audit_log`},
		{"empty schema stays unqualified", []StoreOption{WithSchema("")}, `audit_log`},
		{"plain schema", []StoreOption{WithSchema("controlplane")}, `"controlplane".audit_log`},
		{"case is kept", []StoreOption{WithSchema("ControlPlane")}, `"ControlPlane".audit_log`},
		{"embedded quote is doubled", []StoreOption{WithSchema(`we"ird`)}, `"we""ird".audit_log`},
		{"last option wins", []StoreOption{WithSchema("a"), WithSchema("b")}, `"b".audit_log`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewDBAuditStore(nil, tc.opts...).table; got != tc.want {
				t.Errorf("table = %s, want %s", got, tc.want)
			}
		})
	}
}

// pgAuditSchema is the audit_log table as the audit-log recipe's migration
// creates it.
const pgAuditSchema = `
CREATE TABLE public.audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id VARCHAR(255),
    email VARCHAR(255),
    procedure VARCHAR(512) NOT NULL,
    peer_address VARCHAR(255),
    duration_ms INTEGER,
    status VARCHAR(50) NOT NULL DEFAULT 'success',
    error_code VARCHAR(50),
    error_message TEXT,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

// TestDBAuditStore_WithSchemaSurvivesALeakedSearchPath reproduces the failure
// a transaction-pooling PgBouncer produces: a server backend whose
// search_path an earlier session changed with a session-level SET is handed
// to the next client, and every bare relation name stops resolving. The pool
// is pinned to one connection so the poisoned session IS the store's
// connection, as the pooled backend is.
func TestDBAuditStore_WithSchemaSurvivesALeakedSearchPath(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, pgAuditSchema); err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	entry := Entry{Timestamp: time.Now().UTC(), UserID: "u_1", Procedure: "/svc.v1.Users/Get", Status: "ok"}
	bare := NewDBAuditStore(db)
	if err := bare.Log(ctx, entry); err != nil {
		t.Fatalf("bare store on a clean session: %v", err)
	}

	// What pg_dump runs first, and what a psql `SET search_path = ...` does:
	// a session-level change that outlives the statement.
	if _, err := db.ExecContext(ctx, `SELECT pg_catalog.set_config('search_path', '', false)`); err != nil {
		t.Fatalf("poison search_path: %v", err)
	}

	var pgErr *pgconn.PgError
	if err := bare.Log(ctx, entry); !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		t.Fatalf("bare store on the poisoned session: err = %v, want SQLSTATE 42P01 (the production failure)", err)
	}

	qualified := NewDBAuditStore(db, WithSchema("public"))
	if err := qualified.Log(ctx, entry); err != nil {
		t.Fatalf("qualified store on the poisoned session: %v", err)
	}
	got, err := qualified.Query(ctx, Filter{UserID: "u_1"})
	if err != nil {
		t.Fatalf("qualified Query on the poisoned session: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Query returned %d entries, want 2 (the clean-session write and the qualified one)", len(got))
	}
	for _, e := range got {
		if e.ID == "" || e.CreatedAt.IsZero() {
			t.Errorf("entry %+v: id/created_at not filled by the table defaults", e)
		}
	}
}
