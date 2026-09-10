// Copyright (c) 2025 Reliant Labs
package accountpurge_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/db"
)

// seedUser builds a realistic account: a project with a repo, a chat with a
// thread, messages with content blocks, tool calls with results, an approval,
// a context window, a worktree and a stored provider credential.
//
// It deliberately wires the two SELF-REFERENTIAL fork pointers
// (threads.fork_at_message_id and context_windows.fork_at_message_id), because
// those are ON DELETE RESTRICT against messages and are the exact reason a
// naive delete order fails. A seed without them would let a wrong
// implementation pass.
func seedUser(t *testing.T, rawDB *sql.DB, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	id := func(kind string) string { return fmt.Sprintf("%s-%s", kind, userID) }

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := rawDB.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	exec(`INSERT INTO projects (id, name, path, user_id, is_git_repo, created_at, updated_at, last_active)
	      VALUES ($1,$2,$3,$4,true,$5,$5,$5)`,
		id("proj"), "Test Project", "/tmp/"+id("proj"), userID, now)

	exec(`INSERT INTO repos (id, project_id, name, relative_path, created_at, updated_at)
	      VALUES ($1,$2,$3,$4,$5,$5)`,
		id("repo"), id("proj"), "repo", ".", now)

	// worktrees carry no user_id — ownership is through project_id.
	exec(`INSERT INTO worktrees (id, project_id, name, path, branch, base_branch, status, created_at, updated_at, last_active)
	      VALUES ($1,$2,$3,$4,$5,$6,1,$7,$7,$7)`,
		id("wt"), id("proj"), "wt", "/tmp/"+id("wt"), "feature", "main", now)

	exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
	      VALUES ($1,$2,$3,$4,$5,$5,$5)`,
		id("chat"), "Test Chat", id("proj"), userID, now)

	exec(`INSERT INTO threads (id, chat_id, origin, created_at)
	      VALUES ($1,$2,'root',$3)`,
		id("thread"), id("chat"), now)

	// context_windows must exist before messages: messages.context_window_id
	// is NOT NULL. The fork pointer is attached after the message exists.
	exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at)
	      VALUES ($1,$2,1,$3)`,
		id("cw"), id("thread"), now)

	exec(`INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
	      VALUES ($1,$2,1,$3,$4,1,$5,$5,1)`,
		id("msg"), id("chat"), id("thread"), id("cw"), now)

	exec(`INSERT INTO message_content_blocks (id, message_id, "position", block_type, content, created_at, updated_at)
	      VALUES ($1,$2,0,1,'hello',$3,$3)`,
		id("block"), id("msg"), now)

	exec(`INSERT INTO tool_calls (id, chat_id, thread_id, message_id, tool_name, status, requested_at, created_at, updated_at)
	      VALUES ($1,$2,$3,$4,'bash',1,$5,$5,$5)`,
		id("tc"), id("chat"), id("thread"), id("msg"), now)

	exec(`INSERT INTO tool_call_results (tool_call_id, message_id, content, created_at, updated_at)
	      VALUES ($1,$2,'ok',$3,$3)`,
		id("tc"), id("msg"), now)

	exec(`INSERT INTO approvals (id, chat_id, thread_id, approval_type, entity_id, status, title, created_at)
	      VALUES ($1,$2,$3,1,$4,1,'approve me',$5)`,
		id("appr"), id("chat"), id("thread"), id("tc"), now)

	// The two RESTRICT fork pointers against messages. This is the trap that
	// makes delete order load-bearing.
	exec(`UPDATE context_windows SET fork_at_message_id = $2 WHERE id = $1`, id("cw"), id("msg"))
	exec(`UPDATE threads SET fork_at_message_id = $2 WHERE id = $1`, id("thread"), id("msg"))

	exec(`INSERT INTO claude_auth_tokens (id, user_id, access_token, refresh_token, expires_at, created_at, updated_at)
	      VALUES ($1,$2,'at','rt',$3,$3,$3)`, id("cat"), userID, now)

	exec(`INSERT INTO settings (id, user_id, key, value, value_type, created_at, updated_at)
	      VALUES ($1,$2,'theme','dark','string',$3,$3)`, id("set"), userID, now)
}

// ownershipCount maps a table to the query that counts one user's rows in it.
// Not every table carries user_id: worktrees are owned through project_id and
// workflows through chat_id, which is exactly the kind of detail a purge gets
// wrong, so the test spells the real path out rather than assuming a column.
var ownershipCount = map[string]string{
	"projects":           `SELECT COUNT(*) FROM projects WHERE user_id = $1`,
	"chats":              `SELECT COUNT(*) FROM chats WHERE user_id = $1`,
	"settings":           `SELECT COUNT(*) FROM settings WHERE user_id = $1`,
	"claude_auth_tokens": `SELECT COUNT(*) FROM claude_auth_tokens WHERE user_id = $1`,
	"worktrees": `SELECT COUNT(*) FROM worktrees w
	                JOIN projects p ON p.id = w.project_id WHERE p.user_id = $1`,
	"messages": `SELECT COUNT(*) FROM messages m
	               JOIN chats c ON c.id = m.chat_id WHERE c.user_id = $1`,
	"threads": `SELECT COUNT(*) FROM threads th
	              JOIN chats c ON c.id = th.chat_id WHERE c.user_id = $1`,
	"tool_calls": `SELECT COUNT(*) FROM tool_calls tc
	                 JOIN chats c ON c.id = tc.chat_id WHERE c.user_id = $1`,
}

func countRows(t *testing.T, rawDB *sql.DB, table, userID string) int {
	t.Helper()
	q, ok := ownershipCount[table]
	if !ok {
		t.Fatalf("countRows: no ownership query registered for %q", table)
	}
	var n int
	if err := rawDB.QueryRow(q, userID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestPurge_RemovesEverythingForUser is the core assertion: a fully-populated
// account, including the RESTRICT fork pointers, purges completely.
//
// Before the ordered sequence in purgeSteps existed, this failed at the
// `chats` delete with a foreign-key violation on messages_thread_id_fkey.
func TestPurge_RemovesEverythingForUser(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	const userID = "user-under-test"
	seedUser(t, rawDB, userID)

	total, err := accountpurge.Purge(context.Background(), rawDB, userID)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if total == 0 {
		t.Fatal("Purge reported 0 rows deleted for a seeded account")
	}

	// Includes the join-reached tables (messages, threads, tool_calls,
	// worktrees) — the ones a user_id-only purge would silently orphan.
	for _, table := range []string{
		"projects", "chats", "worktrees", "settings", "claude_auth_tokens",
		"messages", "threads", "tool_calls",
	} {
		if n := countRows(t, rawDB, table, userID); n != 0 {
			t.Errorf("%s: %d rows survived the purge, want 0", table, n)
		}
	}
}

// TestNaiveDeleteFails pins the reason purgeSteps is ordered rather than a
// loop over 23 tables.
//
// It asserts that the obvious implementation — DELETE FROM chats WHERE
// user_id = $1 — is REJECTED by Postgres on a realistic account, because the
// cascade into threads collides with messages.thread_id ON DELETE RESTRICT.
//
// If a future migration relaxes that constraint this test starts failing, and
// that is the point: it would mean the careful ordering in purgeSteps is no
// longer load-bearing and the comment explaining it has gone stale.
func TestNaiveDeleteFails(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	const userID = "user-naive"
	seedUser(t, rawDB, userID)

	_, err := rawDB.ExecContext(context.Background(),
		`DELETE FROM chats WHERE user_id = $1`, userID)
	if err == nil {
		t.Fatal("DELETE FROM chats succeeded; the RESTRICT constraints that " +
			"purgeSteps orders around appear to be gone — re-check the " +
			"ordering rationale in accountpurge.go")
	}
	t.Logf("naive delete correctly rejected: %v", err)
}

// TestPurge_LeavesOtherUsersIntact guards the blast radius. Every statement is
// user-scoped, so a second account seeded identically must be untouched.
func TestPurge_LeavesOtherUsersIntact(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	const victim = "user-deleted"
	const bystander = "user-retained"
	seedUser(t, rawDB, victim)
	seedUser(t, rawDB, bystander)

	if _, err := accountpurge.Purge(context.Background(), rawDB, victim); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	for _, table := range []string{
		"projects", "chats", "worktrees", "settings", "claude_auth_tokens",
		"messages", "threads", "tool_calls",
	} {
		if n := countRows(t, rawDB, table, victim); n != 0 {
			t.Errorf("%s: victim left %d rows, want 0", table, n)
		}
		if n := countRows(t, rawDB, table, bystander); n == 0 {
			t.Errorf("%s: bystander lost their rows", table)
		}
	}
}

// TestPreview_ReportsCountsWithoutDeleting checks the dialog's numbers are
// real and that asking the question changes nothing.
func TestPreview_ReportsCountsWithoutDeleting(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	const userID = "user-preview"
	seedUser(t, rawDB, userID)

	counts, err := accountpurge.Preview(context.Background(), rawDB, userID)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if counts.Projects != 1 {
		t.Errorf("Projects = %d, want 1", counts.Projects)
	}
	if counts.Chats != 1 {
		t.Errorf("Chats = %d, want 1", counts.Chats)
	}
	if counts.Worktrees != 1 {
		t.Errorf("Worktrees = %d, want 1", counts.Worktrees)
	}
	if counts.Messages != 1 {
		t.Errorf("Messages = %d, want 1", counts.Messages)
	}
	if !counts.HasProviderCredentials {
		t.Error("HasProviderCredentials = false, want true (claude token seeded)")
	}

	if n := countRows(t, rawDB, "projects", userID); n != 1 {
		t.Errorf("Preview deleted data: projects = %d, want 1", n)
	}
}

// TestPurge_EmptyUserIDRejected: a missing auth context must be a loud error,
// never a silent "deleted 0 rows" success.
func TestPurge_EmptyUserIDRejected(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	if _, err := accountpurge.Purge(context.Background(), rawDB, ""); err == nil {
		t.Fatal("Purge(\"\") returned nil error, want a rejection")
	}
}

// TestPurge_IsIdempotent: a retry after a successful delete must succeed and
// report nothing, so a client that resends is not shown an error.
func TestPurge_IsIdempotent(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	const userID = "user-twice"
	seedUser(t, rawDB, userID)

	if _, err := accountpurge.Purge(context.Background(), rawDB, userID); err != nil {
		t.Fatalf("first Purge: %v", err)
	}
	second, err := accountpurge.Purge(context.Background(), rawDB, userID)
	if err != nil {
		t.Fatalf("second Purge: %v", err)
	}
	if second != 0 {
		t.Errorf("second Purge deleted %d rows, want 0", second)
	}
}
