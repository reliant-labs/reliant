// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetChatActivity returns the computed activity the chats_with_activity view
// derives for a chat: 0 (IDLE) if it has none.
func (r *Repo) GetChatActivity(ctx context.Context, chatID string) (int, error) {
	return store.GetChatActivity(ctx, r.ORM(), chatID)
}

// postCommitEmitBudget bounds the chat_activity_changed write that runs after
// a transaction committed. The caller's context may be ending by then (a
// request answered, an activity finishing); the event is still owed, so the
// emit runs on a context without cancellation and with this bound instead.
const postCommitEmitBudget = 10 * time.Second

// emitChatActivityIfChanged announces a chat's current activity with a
// chat_activity_changed event. Dedup is handled on the frontend (activityStore
// skips no-op updates, and an event's sequence number decides precedence over
// list/get snapshots), so it always emits.
//
// Inside a transaction it is deferred until the OUTERMOST commit and written
// in a short transaction of its own (orm.AfterCommit): it never runs inside
// the caller's. The read it needs — the chats_with_activity view, which joins
// workflows, threads, approvals and questions — taken inside a SERIALIZABLE
// writer (StartChat's launch, a workflow status change, an adopt) gave every
// such writer predicate locks across those tables, so concurrent launches for
// DIFFERENT users aborted one another with SQLSTATE 40001 until one exhausted
// its retries and StartChat failed ("internal: failed to create chat", 3 of 14
// e2e stories in the release gate). Nothing about the event needs the writer's
// isolation: it reports state that has already committed.
//
// So in a transaction it returns nil once the emit is queued, and the emit
// cannot fail the write it describes: the data is committed whatever happens
// to its announcement. A failed post-commit emit is logged at ERROR, and
// clients still converge: a list or get reads activity from the view
// directly, and the stream's reconnect sweep re-reads the chat caches. In the
// gap between the two commits a reader sees the committed chat (or status)
// without its activity event yet; the event that follows carries a newer
// sequence number and the same or newer state.
//
// Outside a transaction it emits at once and returns the error.
func (r *Repo) emitChatActivityIfChanged(ctx context.Context, chatID string) error {
	if orm.InTx(ctx) {
		// The callback's ctx is the one the outermost RunTx was called with:
		// it carries no transaction, so the emit opens its own.
		return orm.AfterCommit(ctx, func(committedCtx context.Context) {
			emitCtx, cancel := context.WithTimeout(context.WithoutCancel(committedCtx), postCommitEmitBudget)
			defer cancel()
			if err := r.announceChatActivity(emitCtx, chatID, nil); err != nil {
				logging.ErrorContext(emitCtx, "[ActivityEvent] chat_activity_changed was not written after its change committed",
					"chatID", chatID, "error", err)
			}
		})
	}
	return r.announceChatActivity(ctx, chatID, nil)
}

// emitChatActivityChanged writes a chat_activity_changed event carrying state
// rather than the state the view reports now. Outside a transaction only.
func (r *Repo) emitChatActivityChanged(ctx context.Context, chatID string, state ChatActivityState) error {
	return r.announceChatActivity(ctx, chatID, &state)
}

// announceChatActivity writes one chat_activity_changed event for chatID in a
// transaction of its own, reporting fixed when set and otherwise the state the
// chats_with_activity view reports.
//
// READ COMMITTED, and the order inside it, are what keep the event stream
// truthful without SERIALIZABLE's predicate locks:
//  1. the chat row (its user, project and worktree);
//  2. the user's next sequence number (AppendUserUpdateBuiltUnderLock), whose
//     UPDATE … SET last_assigned_seq = last_assigned_seq + 1 RETURNING holds
//     the user's counter row until commit;
//  3. the view, read only then. Each READ COMMITTED statement sees every
//     transaction committed before it started, including whichever emit held
//     the counter before this one, so a higher sequence number never carries
//     an older reading than a lower one. Read before allocating, two emits
//     could commit a stale state at the newer number.
//
// A waiter on the counter row re-reads it and takes the next number, so this
// cannot raise 40001 (TestConcurrentUserUpdatesDoNotSerializationConflict).
// A chat deleted before its announcement is not an error: there is nothing
// left to announce.
func (r *Repo) announceChatActivity(ctx context.Context, chatID string, fixed *ChatActivityState) error {
	var written *UserUpdate
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		written = nil
		chat, err := r.GetChat(txCtx, chatID)
		if errors.Is(err, sql.ErrNoRows) {
			logging.DebugContext(txCtx, "[ActivityEvent] chat is gone; no activity to announce", "chatID", chatID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get chat for activity event: %w", err)
		}
		update, err := store.AppendUserUpdateBuiltUnderLock(txCtx, r.ORM(), chat.UserID, func(lockedCtx context.Context) (*core.UserUpdate, error) {
			state := ChatActivityState{}
			if fixed != nil {
				state = *fixed
			} else {
				read, err := store.GetChatActivityState(lockedCtx, r.ORM(), chatID)
				if err != nil {
					return nil, err
				}
				state = read
			}
			data, err := json.Marshal(map[string]interface{}{
				"chat_id":         chatID,
				"activity":        state.Activity,
				"working_threads": state.WorkingThreads,
				"timestamp":       time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to marshal activity event data: %w", err)
			}
			return &core.UserUpdate{
				ProjectID:  &chat.ProjectID,
				WorktreeID: chat.WorktreeID,
				ChatID:     &chatID,
				UpdateType: UserUpdateChatActivityChanged,
				EntityType: EntityTypeChat,
				EntityID:   chatID,
				Data:       data,
			}, nil
		})
		if err != nil {
			return fmt.Errorf("failed to create activity event: %w", err)
		}
		written = update
		if r.onUserUpdate != nil {
			committed := *update
			return runAfterCommit(txCtx, func() { r.onUserUpdate(&committed) })
		}
		return nil
	})
	if err != nil {
		return err
	}
	if written != nil {
		logging.DebugContext(ctx, "[ActivityEvent] Emitted chat_activity_changed",
			"chatID", chatID, "seq", written.SequenceNumber)
	}
	return nil
}
