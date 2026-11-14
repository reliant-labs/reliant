// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// Deleting a project, a bounded piece at a time.
//
// # Why this is not `DELETE FROM projects WHERE id = $1`
//
// That one statement cascades through everything the project owns — chats,
// their threads, messages, content blocks, tool calls, updates, worktrees —
// as a single transaction that holds the project's row from its first moment.
// Every write that names the project (each user_updates insert takes a key
// share on it) waits for that transaction, holding whatever it had already
// locked, and the writes behind those wait in turn. On a project with 35k
// chats and 1.4M messages the statement ran for 17 minutes and took prod's
// connection pool with it (2026-11-13).
//
// So the project's contents go first, in batches that are each their own short
// transaction and never touch the project's row, and the project row goes
// last, when the cascade left to it is small.
//
// # What a failure leaves
//
// The delete is not atomic. A batch that fails, or a caller that goes away,
// leaves the project in place with fewer chats; nothing is half-deleted inside
// a chat, because one chat's rows go in one transaction. Deleting again
// resumes from what is left. That is the trade for never holding a lock longer
// than a batch.
//
// # The order inside a batch
//
// The same hazard internal/accountpurge documents: five foreign keys are ON
// DELETE RESTRICT (messages, tool_calls and approvals against threads;
// threads.fork_at_message_id and context_windows.fork_at_message_id against
// messages), so the rows that restrict go before what they point at.
//
// One thing differs from the account purge. A fork can cross chats: a thread
// or context window in a chat that is NOT being deleted may be forked at a
// message that is. Those pointers are cleared first, wherever they are — the
// alternative is a project that can never be deleted because another chat was
// once forked from one of its messages.
const (
	// projectDeleteChatBatch is how many chats one transaction removes. A chat
	// averages tens of messages and a few hundred chat_updates, so a batch is
	// thousands of rows, not millions.
	projectDeleteChatBatch = 25
	// projectDeleteWorktreeBatch is how many worktrees one transaction removes.
	projectDeleteWorktreeBatch = 50
	// projectDeleteUpdateBatch is how many of the project's remaining
	// user_updates one transaction removes.
	projectDeleteUpdateBatch = 5000
	// projectDeleteStatementTimeout bounds each statement of a batch. A batch
	// that cannot finish in it fails, loudly, instead of holding its locks
	// while writes queue behind them.
	projectDeleteStatementTimeout = "60s"
	// projectDeleteLockTimeout bounds how long a batch waits for a row another
	// transaction holds.
	projectDeleteLockTimeout = "10s"
)

// chatPurgeSteps removes everything stored for a set of chats, ?0 being their
// ids. Read it top to bottom as "what restricts goes before what it points
// at; the chats themselves go last".
var chatPurgeSteps = []struct{ name, sql string }{
	// Fork pointers at these chats' messages, from any chat (see above).
	{"thread fork pointers", `
		UPDATE threads SET fork_at_message_id = NULL
		WHERE fork_at_message_id IN (SELECT id FROM messages WHERE chat_id = ANY(?0))`},
	{"context window fork pointers", `
		UPDATE context_windows SET fork_at_message_id = NULL
		WHERE fork_at_message_id IN (SELECT id FROM messages WHERE chat_id = ANY(?0))`},

	// Children of messages. They cascade, but going first keeps the message
	// delete cheap.
	{"message_content_blocks", `
		DELETE FROM message_content_blocks
		WHERE message_id IN (SELECT id FROM messages WHERE chat_id = ANY(?0))`},
	{"tool_call_results", `
		DELETE FROM tool_call_results
		WHERE message_id IN (SELECT id FROM messages WHERE chat_id = ANY(?0))`},

	// tool_calls.thread_id and approvals.thread_id RESTRICT threads.
	{"tool_calls", `DELETE FROM tool_calls WHERE chat_id = ANY(?0)`},
	{"approvals", `
		DELETE FROM approvals
		WHERE thread_id IN (SELECT id FROM threads WHERE chat_id = ANY(?0))`},
	{"context_windows", `
		DELETE FROM context_windows
		WHERE thread_id IN (SELECT id FROM threads WHERE chat_id = ANY(?0))`},
	{"agent_messages", `DELETE FROM agent_messages WHERE chat_id = ANY(?0)`},

	// messages.thread_id RESTRICTs threads, so messages precede threads.
	{"messages", `DELETE FROM messages WHERE chat_id = ANY(?0)`},
	{"threads", `DELETE FROM threads WHERE chat_id = ANY(?0)`},

	// Rows keyed by chat with no foreign key: nothing cascades them, so a
	// project delete that left them to the cascade left them for good.
	{"step_executions", `
		DELETE FROM step_executions
		WHERE workflow_id IN (SELECT id FROM workflows WHERE chat_id = ANY(?0))`},
	{"workflow_checkpoints", `DELETE FROM workflow_checkpoints WHERE chat_id = ANY(?0)`},
	{"questions", `DELETE FROM questions WHERE chat_id = ANY(?0)`},
	{"chat_updates", `DELETE FROM chat_updates WHERE chat_id = ANY(?0)`},
	{"chat_update_heads", `DELETE FROM chat_update_heads WHERE chat_id = ANY(?0)`},
	{"workflows", `DELETE FROM workflows WHERE chat_id = ANY(?0)`},
	{"update_stream_counters", `
		DELETE FROM update_stream_counters WHERE stream_kind = 'chat' AND stream_id = ANY(?0)`},

	// The chats, and what still cascades from them (user_updates, run_events,
	// chat_selections).
	{"chats", `DELETE FROM chats WHERE id = ANY(?0)`},
}

// DeleteUserProject deletes the user's project and everything it owns. A
// project that is missing or another user's is left alone, without an error.
//
// It works in bounded batches, each its own transaction (see the top of this
// file), so it must not be called inside a transaction, which would join every
// batch into it. A failure leaves the project with whatever was not yet
// removed; calling it again resumes.
func DeleteUserProject(ctx context.Context, db orm.Context, id, userID string) error {
	return deleteUserProject(ctx, db, id, userID, projectDeleteChatBatch, projectDeleteWorktreeBatch, projectDeleteUpdateBatch)
}

func deleteUserProject(ctx context.Context, db orm.Context, id, userID string, chatBatch, worktreeBatch, updateBatch int) error {
	var owned bool
	if err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM projects WHERE id = ?0 AND user_id = ?1)`, id, userID).Scan(ctx, &owned); err != nil {
		return fmt.Errorf("delete project: read the project: %w", err)
	}
	if !owned {
		return nil
	}
	for round := 1; ; round++ {
		if err := purgeProjectContents(ctx, db, id, chatBatch, worktreeBatch, updateBatch); err != nil {
			return err
		}
		if hook := projectDeleteBeforeFinal.Load(); hook != nil {
			(*hook)(ctx, id)
		}
		deleted, err := deleteProjectRow(ctx, db, id, userID)
		if err != nil {
			return fmt.Errorf("delete project: %w", err)
		}
		if deleted {
			return nil
		}
		if round == projectDeleteMaxRounds {
			return fmt.Errorf("delete project: chats or worktrees kept arriving through %d rounds; delete it again", round)
		}
	}
}

// projectDeleteMaxRounds bounds how many times a delete goes back to the
// batch loops because chats or worktrees arrived while they ran.
const projectDeleteMaxRounds = 5

// projectDeleteBeforeFinal, when set (tests only), runs after the batch loops
// and before the project row's transaction, with the project's id: where a
// concurrent write that lands after the loops passed lands.
var projectDeleteBeforeFinal atomic.Pointer[func(ctx context.Context, projectID string)]

// purgeProjectContents runs the batch loops: the project's chats with
// everything stored under them, its worktrees, then its remaining updates.
func purgeProjectContents(ctx context.Context, db orm.Context, id string, chatBatch, worktreeBatch, updateBatch int) error {
	// Chats, with everything stored under them.
	for {
		var n int
		err := projectDeleteBatch(ctx, db, func(ctx context.Context) error {
			var ids []string
			if err := db.Bun().NewRaw(`SELECT id FROM chats WHERE project_id = ?0 ORDER BY id LIMIT ?1`, id, orm.Arg(chatBatch)).Scan(ctx, &ids); err != nil {
				return fmt.Errorf("list chats: %w", err)
			}
			n = len(ids)
			if n == 0 {
				return nil
			}
			for _, step := range chatPurgeSteps {
				if _, err := db.Bun().NewRaw(step.sql, pgdialect.Array(ids)).Exec(ctx); err != nil {
					return fmt.Errorf("%s: %w", step.name, err)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("delete project: chats: %w", err)
		}
		if n == 0 {
			break
		}
	}

	// Worktrees. Each takes its user_updates with it and clears the threads and
	// triggers that named it.
	if err := projectDeleteUntilShort(ctx, db, worktreeBatch, `
		DELETE FROM worktrees WHERE id IN (
			SELECT id FROM worktrees WHERE project_id = ?0 ORDER BY id LIMIT ?1)`, id); err != nil {
		return fmt.Errorf("delete project: worktrees: %w", err)
	}

	// The updates that name the project and neither a chat nor a worktree of
	// it. On a long-lived project these alone are hundreds of thousands of
	// rows, which is too many to leave to the project row's cascade.
	if err := projectDeleteUntilShort(ctx, db, updateBatch, `
		DELETE FROM user_updates WHERE id IN (
			SELECT id FROM user_updates WHERE project_id = ?0 LIMIT ?1)`, id); err != nil {
		return fmt.Errorf("delete project: user updates: %w", err)
	}
	return nil
}

// deleteProjectRow deletes the project row, and the little that still
// cascades from it: its repos, plans, triggers, configs, machine catalogs and
// any update written since the loops passed. It reports false, deleting
// nothing, when a chat or worktree arrived after the loops passed it: a chat's
// workflows, chat_updates and other rows with no foreign key would not go with
// the cascade, so the caller runs the loops again instead.
//
// FOR UPDATE on the project row is what makes the recheck the last word: every
// insert of a chat or worktree takes a key share on it, which conflicts, so
// one in flight is waited for (and then seen) and one that starts later waits
// for this transaction and then fails its foreign key.
func deleteProjectRow(ctx context.Context, db orm.Context, id, userID string) (bool, error) {
	deleted := false
	err := projectDeleteBatch(ctx, db, func(ctx context.Context) error {
		var locked []string
		if err := db.Bun().NewRaw(`SELECT id FROM projects WHERE id = ?0 AND user_id = ?1 FOR UPDATE`, id, userID).Scan(ctx, &locked); err != nil {
			return fmt.Errorf("lock the project: %w", err)
		}
		if len(locked) == 0 {
			deleted = true // gone already (a concurrent delete)
			return nil
		}
		var arrived bool
		if err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM chats WHERE project_id = ?0)
			OR EXISTS (SELECT 1 FROM worktrees WHERE project_id = ?0)`, id).Scan(ctx, &arrived); err != nil {
			return fmt.Errorf("recheck the project's chats and worktrees: %w", err)
		}
		if arrived {
			return nil
		}
		if _, err := db.Bun().NewRaw(`DELETE FROM projects WHERE id = ?0 AND user_id = ?1`, id, userID).Exec(ctx); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// projectDeleteUntilShort runs a `DELETE ... LIMIT ?1` statement for the
// project, one transaction a batch, until a batch removes fewer rows than the
// limit. It stops on a short batch rather than an empty one so that rows
// arriving while it runs cannot keep it going forever; the project row's own
// cascade takes the stragglers (deleteProjectRow sends a late chat or worktree
// back through the loops instead).
func projectDeleteUntilShort(ctx context.Context, db orm.Context, batch int, query, projectID string) error {
	for {
		var n int64
		err := projectDeleteBatch(ctx, db, func(ctx context.Context) error {
			res, err := db.Bun().NewRaw(query, projectID, orm.Arg(batch)).Exec(ctx)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return err
		}
		if n < int64(batch) {
			return nil
		}
	}
}

// projectDeleteBatch runs f as one transaction under the delete's statement
// and lock timeouts.
func projectDeleteBatch(ctx context.Context, db orm.Context, f func(ctx context.Context) error) error {
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if _, err := db.Bun().NewRaw("SET LOCAL statement_timeout = '" + projectDeleteStatementTimeout + "'").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Bun().NewRaw("SET LOCAL lock_timeout = '" + projectDeleteLockTimeout + "'").Exec(ctx); err != nil {
			return err
		}
		return f(ctx)
	})
}
