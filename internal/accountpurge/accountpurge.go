// Copyright (c) 2025 Reliant Labs
//
// Package accountpurge removes every row reliant stores for one user.
//
// forge:exclude-contract
//
// Leaf package: a concrete Purger over *sql.DB with no collaborator to fake
// and no second implementation. An interface here would have one implementor
// and one caller.
//
// # Why this is a hand-written ordered SQL sequence
//
// Reliant has no `users` table. The Supabase `sub` claim is carried as a bare
// `user_id text` column on 23 tables with no foreign key to anything, so there
// is no single row whose deletion cascades the account away. Deleting an
// account means deleting from all 23, and the ORDER MATTERS — see below.
//
// # The RESTRICT hazard, which is the whole reason this file is careful
//
// Most of reliant's foreign keys are ON DELETE CASCADE from `chats` and
// `projects`. Five are ON DELETE RESTRICT:
//
//	approvals.thread_id                -> threads   RESTRICT
//	messages.thread_id                 -> threads   RESTRICT
//	tool_calls.thread_id               -> threads   RESTRICT
//	context_windows.fork_at_message_id -> messages  RESTRICT
//	threads.fork_at_message_id         -> messages  RESTRICT
//
// So the obvious implementation — `DELETE FROM chats WHERE user_id = $1` —
// FAILS. The cascade reaches `threads`, and `messages.thread_id RESTRICT`
// refuses to let those threads go. Postgres aborts the whole statement.
//
// The fix is to delete the RESTRICT-ing children explicitly, innermost first,
// before touching the cascade roots: content blocks and tool-call results,
// then tool calls and approvals and context windows, then messages, then
// threads, and only then chats. By the time `chats` is deleted, every
// RESTRICT-ing referent is already gone and the remaining cascades are free to
// fire.
//
// The self-referential FKs (threads.fork_at_message_id -> messages,
// context_windows.fork_at_message_id -> messages) are why messages cannot go
// before context_windows and threads even though messages look "deeper".
//
// # Why one transaction
//
// A partial purge is the worst outcome available: it leaves orphaned content
// under a user id that no longer has projects, still visible to nothing and
// billable to nobody, with no record of how far the delete got. The whole
// sequence runs in one transaction so it either completes or changes nothing
// and can be retried.
package accountpurge

import (
	"context"
	"database/sql"
	"fmt"
)

// Counts is a preview of what Purge would delete. Headline aggregates only:
// removing a chat also removes its messages, threads and tool calls by
// cascade, so these numbers describe scale rather than the exact row total.
type Counts struct {
	Projects  int64
	Chats     int64
	Worktrees int64
	Messages  int64
	// HasProviderCredentials reports stored third-party credentials (Claude /
	// Codex / Copilot OAuth tokens, or provider API keys). Surfaced separately
	// because losing a credential differs in kind from losing content.
	HasProviderCredentials bool
}

// Preview counts the caller's data without modifying anything.
func Preview(ctx context.Context, db *sql.DB, userID string) (Counts, error) {
	var c Counts
	// One round trip. The message count joins through chats because messages
	// carry no user_id of their own.
	row := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM projects  WHERE user_id = $1),
			(SELECT COUNT(*) FROM chats     WHERE user_id = $1),
			(SELECT COUNT(*) FROM worktrees w
			   JOIN projects p ON p.id = w.project_id
			  WHERE p.user_id = $1),
			(SELECT COUNT(*) FROM messages m
			   JOIN chats c ON c.id = m.chat_id
			  WHERE c.user_id = $1),
			(EXISTS (SELECT 1 FROM claude_auth_tokens  WHERE user_id = $1)
			 OR EXISTS (SELECT 1 FROM codex_auth_tokens   WHERE user_id = $1)
			 OR EXISTS (SELECT 1 FROM copilot_auth_tokens WHERE user_id = $1)
			 OR EXISTS (SELECT 1 FROM api_keys            WHERE user_id = $1))`,
		userID)
	if err := row.Scan(&c.Projects, &c.Chats, &c.Worktrees, &c.Messages, &c.HasProviderCredentials); err != nil {
		return Counts{}, fmt.Errorf("accountpurge: preview: %w", err)
	}
	return c, nil
}

// step is one DELETE in the ordered sequence. The comment on each explains
// why it sits where it does; reordering this slice is how the RESTRICT
// constraints above get violated.
type step struct {
	name string
	sql  string
}

// purgeSteps is the ordered delete sequence. Read it top to bottom as
// "innermost RESTRICT-ing children first, cascade roots last".
//
// Every statement is scoped by user_id, directly or through a join, so a bug
// here can only ever under-delete (leaving the user's own rows), never reach
// another user's data.
var purgeSteps = []step{
	// --- Depth 1: rows that RESTRICT the deletion of messages/threads. ---

	// Content blocks and tool-call results hang off messages by CASCADE, but
	// deleting them explicitly keeps the later message delete cheap and makes
	// the sequence readable as a whole.
	{"message_content_blocks", `
		DELETE FROM message_content_blocks WHERE message_id IN (
			SELECT m.id FROM messages m JOIN chats c ON c.id = m.chat_id WHERE c.user_id = $1)`},
	{"tool_call_results", `
		DELETE FROM tool_call_results WHERE message_id IN (
			SELECT m.id FROM messages m JOIN chats c ON c.id = m.chat_id WHERE c.user_id = $1)`},

	// tool_calls.thread_id and approvals.thread_id are RESTRICT: these must go
	// before threads.
	{"tool_calls", `
		DELETE FROM tool_calls WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},
	{"approvals", `
		DELETE FROM approvals WHERE thread_id IN (
			SELECT t.id FROM threads t JOIN chats c ON c.id = t.chat_id WHERE c.user_id = $1)`},

	// context_windows.fork_at_message_id is RESTRICT against messages, and
	// context_windows.thread_id CASCADEs from threads. Delete before both.
	{"context_windows", `
		DELETE FROM context_windows WHERE thread_id IN (
			SELECT t.id FROM threads t JOIN chats c ON c.id = t.chat_id WHERE c.user_id = $1)`},

	// agent_messages reference threads and messages; clear before either.
	{"agent_messages", `
		DELETE FROM agent_messages WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},

	// --- Depth 2: messages, then threads. ---

	// messages.thread_id RESTRICTs threads, so messages must precede threads.
	// threads.fork_at_message_id RESTRICTs messages, so any thread still
	// pointing at a message would block this — but fork pointers only ever
	// reference messages within the same chat, and the thread rows themselves
	// are removed in the next step, so clear the pointers first.
	{"threads_fork_pointers", `
		UPDATE threads SET fork_at_message_id = NULL WHERE chat_id IN (
			SELECT id FROM chats WHERE user_id = $1) AND fork_at_message_id IS NOT NULL`},
	{"messages", `
		DELETE FROM messages WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},
	{"threads", `
		DELETE FROM threads WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},

	// --- Depth 3: workflow and chat-adjacent rows. ---

	// workflows carry no user_id: they are owned through chat_id. Same for
	// their step_executions and checkpoints, reached one hop further.
	{"step_executions", `
		DELETE FROM step_executions WHERE workflow_id IN (
			SELECT w.id FROM workflows w JOIN chats c ON c.id = w.chat_id WHERE c.user_id = $1)`},
	{"workflow_checkpoints", `
		DELETE FROM workflow_checkpoints WHERE chat_id IN (
			SELECT id FROM chats WHERE user_id = $1)`},
	{"questions", `
		DELETE FROM questions WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},
	{"chat_updates", `
		DELETE FROM chat_updates WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},
	{"background_process_output", `
		DELETE FROM background_process_output WHERE process_id IN (
			SELECT id FROM background_processes WHERE user_id = $1)`},
	{"background_processes", `DELETE FROM background_processes WHERE user_id = $1`},
	{"workflows", `
		DELETE FROM workflows WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)`},

	// user_updates CASCADEs from chats/projects/worktrees, but also carries
	// its own user_id for rows tied to none of them.
	{"user_updates", `DELETE FROM user_updates WHERE user_id = $1`},

	// update_stream_counters is keyed by (stream_kind, stream_id) and has no
	// user_id column. The user's own row is the stream_kind='user' one keyed
	// by their id; per-chat counters are removed alongside their chats.
	{"update_stream_counters_user", `
		DELETE FROM update_stream_counters WHERE stream_kind = 'user' AND stream_id = $1`},
	{"update_stream_counters_chats", `
		DELETE FROM update_stream_counters WHERE stream_kind = 'chat' AND stream_id IN (
			SELECT id FROM chats WHERE user_id = $1)`},

	// --- Depth 4: the cascade roots. ---

	// Safe now: every RESTRICT-ing referent above is gone, so the remaining
	// CASCADEs (threads, messages, tool_calls, user_updates) have nothing left
	// to trip over.
	{"chats", `DELETE FROM chats WHERE user_id = $1`},

	// Project children that CASCADE, deleted explicitly so a future FK change
	// cannot silently orphan them.
	{"plans", `
		DELETE FROM plans WHERE project_id IN (SELECT id FROM projects WHERE user_id = $1)`},
	{"project_configs", `
		DELETE FROM project_configs WHERE project_id IN (SELECT id FROM projects WHERE user_id = $1)`},
	{"project_daemons", `
		DELETE FROM project_daemons WHERE project_id IN (SELECT id FROM projects WHERE user_id = $1)`},
	{"repos", `
		DELETE FROM repos WHERE project_id IN (SELECT id FROM projects WHERE user_id = $1)`},
	// worktrees carry no user_id either — ownership is via project_id. They
	// must go before projects (no FK cascade covers them).
	{"worktrees", `
		DELETE FROM worktrees WHERE project_id IN (SELECT id FROM projects WHERE user_id = $1)`},
	{"projects", `DELETE FROM projects WHERE user_id = $1`},

	// --- Depth 5: standalone user-owned rows, no ordering constraints. ---

	{"connector_client_bindings", `
		DELETE FROM connector_client_bindings WHERE user_id = $1`},
	{"connector_grants", `DELETE FROM connector_grants WHERE user_id = $1`},
	{"connector_audit_log", `DELETE FROM connector_audit_log WHERE user_id = $1`},
	{"daemon_attachment", `DELETE FROM daemon_attachment WHERE user_id = $1`},
	{"daemon_pats", `DELETE FROM daemon_pats WHERE user_id = $1`},
	{"daemons", `DELETE FROM daemons WHERE user_id = $1`},
	{"workflow_scenarios", `DELETE FROM workflow_scenarios WHERE user_id = $1`},
	{"workflow_drafts", `DELETE FROM workflow_drafts WHERE user_id = $1`},
	{"presets", `DELETE FROM presets WHERE user_id = $1`},
	// NOTE: default_preset_assignments and item_defaults are deliberately
	// absent — both are global catalog tables keyed by workflow/slug with no
	// user_id column, not per-user state.
	{"visibility_overrides", `DELETE FROM visibility_overrides WHERE user_id = $1`},
	{"command_favorites", `DELETE FROM command_favorites WHERE user_id = $1`},
	{"settings", `DELETE FROM settings WHERE user_id = $1`},
	{"attachments", `DELETE FROM attachments WHERE user_id = $1`},

	// --- Depth 6: credentials, deleted last. ---
	//
	// Deliberately the final step. These are live third-party OAuth refresh
	// tokens and provider API keys; if any earlier step fails and the
	// transaction rolls back, the user keeps working with their credentials
	// intact rather than being left authenticated to nothing.
	{"claude_auth_tokens", `DELETE FROM claude_auth_tokens WHERE user_id = $1`},
	{"codex_auth_tokens", `DELETE FROM codex_auth_tokens WHERE user_id = $1`},
	{"copilot_auth_tokens", `DELETE FROM copilot_auth_tokens WHERE user_id = $1`},
	{"api_keys", `DELETE FROM api_keys WHERE user_id = $1`},
}

// Purge deletes every row owned by userID, in one transaction, and returns the
// total number of rows removed. Either everything goes or nothing does.
func Purge(ctx context.Context, db *sql.DB, userID string) (int64, error) {
	if userID == "" {
		// A blank user id would make every `WHERE user_id = ''` match nothing,
		// so this is not destructive — but it always indicates a caller bug
		// (missing auth context), and silently reporting "deleted 0 rows"
		// would look like success.
		return 0, fmt.Errorf("accountpurge: empty user id")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("accountpurge: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var total int64
	for _, s := range purgeSteps {
		res, err := tx.ExecContext(ctx, s.sql, userID)
		if err != nil {
			return 0, fmt.Errorf("accountpurge: %s: %w", s.name, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("accountpurge: commit: %w", err)
	}
	return total, nil
}
