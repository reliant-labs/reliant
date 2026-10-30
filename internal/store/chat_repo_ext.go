// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Chat. The generated chat_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatRepo (a *crud.Repo[Chat]) and the
//     package-level GetChatByID / ListChat / CreateChat / UpdateChat / DeleteChat
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Chat struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindChatBy(ctx context.Context, db orm.Context, /* args */) ([]*Chat, error) {
//		var rows []*Chat
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("title = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChats(ctx context.Context, db orm.Context) ([]*Chat, error) {
//		return ListChat(ctx, db, orm.WhereEq("title", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetLiveChatIDForWorktree returns one non-archived bound chat, or an empty
// string. Query the base table by its indexed binding, never a project-sized list.
func GetLiveChatIDForWorktree(ctx context.Context, db orm.Context, worktreeID string) (string, error) {
	var id string
	err := db.Bun().NewRaw(`SELECT id FROM chats WHERE worktree_id = ? AND state <> ? LIMIT 1`, worktreeID, core.ChatStateArchived).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// The chats writes (P5b L1b). Reads go through the view — see
// chats_with_activity_repo_ext.go.

// InsertChat stores a new chat with the caller's created_at, updated_at and
// last_active (the generated Create keeps a caller-set timestamp). An unset
// State is IDLE (the column DEFAULT).
func InsertChat(ctx context.Context, db orm.Context, chat *core.Chat) error {
	return withChatWorktreeAdmission(ctx, db, chat.ID, chat.WorktreeID, func(ctx context.Context) error {
		if err := CreateChat(ctx, db, chatToRow(chat)); err != nil {
			return fmt.Errorf("failed to create chat: %w", recoveryAdmissionError(err))
		}
		return nil
	})
}

// Parent before child is the common lock order for chat admission and reclaim.
// The SQL trigger is a backstop for other writers, not a handler-only guard.
func withChatWorktreeAdmission(ctx context.Context, db orm.Context, chatID string, worktreeID *string, fn func(context.Context) error) error {
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		// A rebind locks old and new parents in the same stable order as the
		// trigger. The trigger still protects against a concurrently moved row.
		//
		// FOR NO KEY UPDATE, as the trigger takes, not FOR UPDATE: it still
		// excludes another admission and the recovery claim's FOR UPDATE,
		// but not the FOR KEY SHARE of a foreign-key check. FOR UPDATE made
		// a user_updates insert for this worktree wait behind every chat
		// admission in it, which deadlocked concurrent StartChats in one
		// worktree against their own activity announcements (see
		// 20261026040257_recovery_admission_no_key_update.up.sql).
		var ids []string
		if err := db.Bun().NewRaw(`SELECT id FROM worktrees WHERE id=? OR id=(SELECT worktree_id FROM chats WHERE id=?) ORDER BY id FOR NO KEY UPDATE`, worktreeID, chatID).Scan(ctx, &ids); err != nil {
			return err
		}
		return fn(ctx)
	})
}

// chatUpdateColumns are the columns SaveChat writes; updated_at is stamped
// by the masked update itself.
var chatUpdateColumns = []string{
	ChatFieldTitle, ChatFieldProjectID, ChatFieldWorktreeID, ChatFieldWorkflowName,
	ChatFieldState, ChatFieldWorkflowID, ChatFieldRunID, ChatFieldSelectedPresets,
	ChatFieldLastActive,
}

// SaveChat writes a chat's editable columns and stamps updated_at. A chat
// that does not exist is left alone, as the statement it replaced did. The
// caller holds the row it wrote, so nothing is read back.
func SaveChat(ctx context.Context, db orm.Context, chat *core.Chat) error {
	return withChatWorktreeAdmission(ctx, db, chat.ID, chat.WorktreeID, func(ctx context.Context) error {
		err := UpdateChatMasked(ctx, db, chatToRow(chat), chatUpdateColumns, crud.NoReadBack())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to update chat: %w", recoveryAdmissionError(err))
		}
		return nil
	})
}

// UpdateChatState moves a chat to state, capturing archivedWorktreeName onto
// it unless one is already recorded (so an archived chat keeps naming its
// worktree after the worktree row is gone), and stamps updated_at and
// last_active with now.
func UpdateChatState(ctx context.Context, db orm.Context, chatID string, state core.ChatState, archivedWorktreeName *string, now time.Time) error {
	var worktreeID *string
	if err := db.Bun().NewRaw(`SELECT worktree_id FROM chats WHERE id=?`, chatID).Scan(ctx, &worktreeID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return withChatWorktreeAdmission(ctx, db, chatID, worktreeID, func(ctx context.Context) error {
		// Archiving drops what the chat was holding for its machine: a
		// message queued for it, a breaker pause awaiting its reconnect, the
		// waiting-for-machine marker. The delivery sweep skips archived chats
		// already, but a marker left set comes back to life the moment the
		// chat is unarchived, starting a run nobody asked for.
		_, err := db.Bun().NewRaw(`
UPDATE chats
SET state = ?0,
    archived_worktree_name = COALESCE(archived_worktree_name, ?1),
    updated_at = ?2,
    last_active = ?2,
    queued_for_machine_at = CASE WHEN ?0 = ?4 THEN NULL ELSE queued_for_machine_at END,
    paused_for_machine_at = CASE WHEN ?0 = ?4 THEN NULL ELSE paused_for_machine_at END,
    daemon_blocked_at = CASE WHEN ?0 = ?4 THEN NULL ELSE daemon_blocked_at END
WHERE id = ?3`, state, archivedWorktreeName, now, chatID, core.ChatStateArchived).Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to update chat state: %w", recoveryAdmissionError(err))
		}
		return nil
	})
}

// UpdateChatUnread sets the chat's unread flag and stamps updated_at. A chat
// that does not exist is left alone.
func UpdateChatUnread(ctx context.Context, db orm.Context, chatID string, unread bool) error {
	row := &Chat{ID: chatID}
	if unread {
		row.Unread = 1
	}
	err := UpdateChatMasked(ctx, db, row, []string{ChatFieldUnread}, crud.NoReadBack())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to update chat unread: %w", err)
	}
	return nil
}

// updateChatActiveDaemonSQL pins (or clears) the chat's daemon. Pinning a
// daemon puts the chat on a machine, so it ends no-machine in the same write
// (chats_no_machine_has_no_daemon_check). Clearing the daemon never sets
// no_machine: a chat that has had a machine does not become one without.
const updateChatActiveDaemonSQL = `
UPDATE chats SET
    active_daemon_id = ?0,
    no_machine = no_machine AND ?0::text IS NULL,
    updated_at = NOW()
WHERE id = ?1`

// UpdateChatActiveDaemon pins the chat to daemonID, or clears the pin when it
// is nil.
func UpdateChatActiveDaemon(ctx context.Context, db orm.Context, chatID string, daemonID *string) error {
	_, err := db.Bun().NewRaw(updateChatActiveDaemonSQL, daemonID, chatID).Exec(ctx)
	return err
}

// pinChatDaemonIfUnsetSQL pins an unpinned chat that is on a machine. A
// no-machine chat has no daemon by constraint, so it is never matched. Neither
// updated_at nor last_active moves: choosing where a chat runs is not
// something the user did to it, and the sidebar orders by them.
const pinChatDaemonIfUnsetSQL = `
UPDATE chats SET active_daemon_id = ?0
WHERE id = ?1 AND NOT no_machine AND (active_daemon_id IS NULL OR active_daemon_id = '')
RETURNING user_id, project_id, worktree_id`

// PinnedChat is the chat PinChatDaemonIfUnset pinned.
type PinnedChat struct {
	UserID     string  `bun:"user_id"`
	ProjectID  string  `bun:"project_id"`
	WorktreeID *string `bun:"worktree_id"`
}

// PinChatDaemonIfUnset pins the chat to daemonID only when it has no pin and
// is not a no-machine chat, and returns the pin the row holds afterwards ("" for
// none). pinned is non-nil only when THIS call wrote it, so of two racing
// callers exactly one announces the pin and both return the same machine.
func PinChatDaemonIfUnset(ctx context.Context, db orm.Context, chatID, daemonID string) (stored string, pinned *PinnedChat, err error) {
	var rows []PinnedChat
	if err := db.Bun().NewRaw(pinChatDaemonIfUnsetSQL, daemonID, chatID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("pinning chat %s to daemon %s: %w", chatID, daemonID, err)
	}
	if len(rows) > 0 {
		return daemonID, &rows[0], nil
	}
	// Not written: the chat already had a pin (a concurrent send won), has no
	// machine by design, or is gone. A second statement, so under READ
	// COMMITTED it reads what the winner committed.
	var existing *string
	err = db.Bun().NewRaw(`SELECT active_daemon_id FROM chats WHERE id = ?0`, chatID).Scan(ctx, &existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("reading chat %s pin: %w", chatID, err)
	}
	if existing == nil {
		return "", nil, nil
	}
	return *existing, nil, nil
}

// UnpinnedChat is a chat UnpinChatsFromDaemon released.
type UnpinnedChat struct {
	ID         string  `bun:"id"`
	UserID     string  `bun:"user_id"`
	ProjectID  string  `bun:"project_id"`
	WorktreeID *string `bun:"worktree_id"`
}

const unpinChatsFromDaemonSQL = `
UPDATE chats SET active_daemon_id = NULL, updated_at = NOW()
WHERE active_daemon_id = ?0
RETURNING id, user_id, project_id, worktree_id`

// UnpinChatsFromDaemon clears every chat's pin to a removed daemon and
// returns the chats it released, so the caller can announce each one.
func UnpinChatsFromDaemon(ctx context.Context, db orm.Context, daemonID string) ([]UnpinnedChat, error) {
	var rows []UnpinnedChat
	if err := db.Bun().NewRaw(unpinChatsFromDaemonSQL, daemonID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("unpinning chats from removed daemon %s: %w", daemonID, err)
	}
	return rows, nil
}

// RemoveChat deletes the chat (the generated DeleteChat), treating one that
// does not exist as already deleted, as the statement it replaced did.
func RemoveChat(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteChat(ctx, db, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// setChatAdoptedSQL is idempotent: adopting an adopted chat keeps its original
// adopted_at, and clearing an un-adopted one is a no-op. Scoped to the owner;
// zero rows means the chat is not the caller's (or does not exist).
const setChatAdoptedSQL = `
UPDATE chats SET
    adopted_at = CASE WHEN ?0::boolean THEN COALESCE(adopted_at, NOW()) ELSE NULL END,
    updated_at = NOW()
WHERE id = ?1 AND user_id = ?2`

// SetChatAdopted adopts or un-adopts a chat owned by userID, reporting whether
// the chat exists and is the user's.
func SetChatAdopted(ctx context.Context, db orm.Context, chatID, userID string, adopted bool) (bool, error) {
	res, err := db.Bun().NewRaw(setChatAdoptedSQL, adopted, chatID, userID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to set chat adoption: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetChatDaemonBlocked sets or clears the daemon-pending marker and reports
// whether the stored value changed, so callers emit chat_activity_changed on
// transitions and not on every tool call.
//
// It runs on every tool call, so it is the raw passthrough with bound $N
// parameters: bun would format the arguments into the text, a new prepared
// statement per chat under pgx's statement cache (internal/db/SCHEMA.md).
func SetChatDaemonBlocked(ctx context.Context, db orm.Context, chatID string, blocked bool) (bool, error) {
	res, err := db.Exec(ctx, `
UPDATE chats SET
    daemon_blocked_at = CASE WHEN $1::boolean THEN NOW() ELSE NULL END
WHERE id = $2
  AND (daemon_blocked_at IS NOT NULL) IS DISTINCT FROM $1::boolean`, blocked, chatID)
	if err != nil {
		return false, fmt.Errorf("failed to set chat daemon-blocked marker: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// setChatQueuedForMachineSQL sets or clears the queued-for-machine marker,
// matching a row only when the value actually changes: clearing is how a
// deliverer CLAIMS the queued message, so of two that race exactly one sees
// the row change.
const setChatQueuedForMachineSQL = `
UPDATE chats SET
    queued_for_machine_at = CASE WHEN ?0::boolean THEN NOW() ELSE NULL END
WHERE id = ?1
  AND (queued_for_machine_at IS NOT NULL) IS DISTINCT FROM ?0::boolean`

// SetChatQueuedForMachine sets or clears the queued-for-machine marker and
// reports whether the stored value changed.
func SetChatQueuedForMachine(ctx context.Context, db orm.Context, chatID string, queued bool) (bool, error) {
	res, err := db.Bun().NewRaw(setChatQueuedForMachineSQL, queued, chatID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to set chat queued-for-machine marker: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// queueChatForMachineSQL sets the queued-for-machine marker (keeping the
// time of a marker already set) and records the machine the message waits
// for, matching a row only when either changes. ?1 is NULL when the run named
// no machine.
const queueChatForMachineSQL = `
UPDATE chats SET
    queued_for_machine_at = COALESCE(queued_for_machine_at, NOW()),
    queued_for_machine_selector = ?1::jsonb
WHERE id = ?0
  AND (queued_for_machine_at IS NULL OR queued_for_machine_selector IS DISTINCT FROM ?1::jsonb)`

// QueueChatForMachine sets the queued-for-machine marker with the machine
// the message waits for, and reports whether the stored row changed.
// SetChatQueuedForMachine's clear leaves the machine in place: it is read
// only while the marker is set, so a deliverer that claims the message and
// then puts it back (ContinueQueued's requeue) keeps it.
func QueueChatForMachine(ctx context.Context, db orm.Context, chatID string, machine *core.QueuedMachine) (bool, error) {
	var selector *string
	if machine != nil {
		raw, err := json.Marshal(machine)
		if err != nil {
			return false, fmt.Errorf("encode the machine a queued message waits for: %w", err)
		}
		s := string(raw)
		selector = &s
	}
	res, err := db.Bun().NewRaw(queueChatForMachineSQL, chatID, selector).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to queue chat for its machine: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// setChatRunIDSQL records the Temporal run currently executing a chat's root
// workflow. It only touches the chat whose root workflow it is, and only when
// the id changes, so the write a run makes at every start is free when nothing
// moved.
const setChatRunIDSQL = `
UPDATE chats SET run_id = ?0, updated_at = NOW()
WHERE id = ?1
  AND (workflow_id IS NULL OR workflow_id = ?2)
  AND run_id IS DISTINCT FROM ?0`

// SetChatRunID records runID as the chat's current run of workflowID and
// reports whether the stored value changed.
func SetChatRunID(ctx context.Context, db orm.Context, chatID, workflowID, runID string) (bool, error) {
	res, err := db.Bun().NewRaw(setChatRunIDSQL, runID, chatID, workflowID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to record chat run id: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// setChatPausedForMachineSQL sets or clears the paused-for-machine marker: the
// chat's run was paused by the daemon-offline breaker (not by the user). Like
// the queued marker, clearing is a claim: of two resumers that race exactly
// one sees the row change.
const setChatPausedForMachineSQL = `
UPDATE chats SET
    paused_for_machine_at = CASE WHEN ?0::boolean THEN NOW() ELSE NULL END
WHERE id = ?1
  AND (paused_for_machine_at IS NOT NULL) IS DISTINCT FROM ?0::boolean`

// SetChatPausedForMachine sets or clears the paused-for-machine marker and
// reports whether the stored value changed.
func SetChatPausedForMachine(ctx context.Context, db orm.Context, chatID string, paused bool) (bool, error) {
	res, err := db.Bun().NewRaw(setChatPausedForMachineSQL, paused, chatID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to set chat paused-for-machine marker: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// listChatsQueuedForMachineSQL lists chats holding a message queued for their
// machine, oldest first. An empty user id lists every user's (the delivery
// sweep); otherwise one user's (a machine of theirs just connected).
// Archived chats are left alone.
const listChatsQueuedForMachineSQL = `
SELECT id, user_id, active_daemon_id, queued_for_machine_at, queued_for_machine_selector
FROM chats
WHERE queued_for_machine_at IS NOT NULL
  AND state IS DISTINCT FROM 3
  AND (?0::text = '' OR user_id = ?0::text)
ORDER BY queued_for_machine_at ASC, id ASC
LIMIT ?1`

// ListChatsQueuedForMachine lists up to limit chats holding a message queued
// for their machine.
func ListChatsQueuedForMachine(ctx context.Context, db orm.Context, userID string, limit int) ([]core.QueuedForMachineChat, error) {
	var rows []struct {
		ID                 string    `bun:"id"`
		UserID             string    `bun:"user_id"`
		ActiveDaemonID     *string   `bun:"active_daemon_id"`
		QueuedForMachineAt time.Time `bun:"queued_for_machine_at"`
		Selector           *string   `bun:"queued_for_machine_selector"`
	}
	if err := db.Bun().NewRaw(listChatsQueuedForMachineSQL, userID, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list chats queued for their machine: %w", err)
	}
	chats := make([]core.QueuedForMachineChat, len(rows))
	for i, row := range rows {
		chats[i] = core.QueuedForMachineChat{
			ChatID:         row.ID,
			UserID:         row.UserID,
			ActiveDaemonID: row.ActiveDaemonID,
			QueuedAt:       row.QueuedForMachineAt,
		}
		if row.Selector != nil {
			var machine core.QueuedMachine
			// A selector that does not decode leaves Machine nil: delivery
			// falls back to the chat's pinned machine, as before the column.
			if err := json.Unmarshal([]byte(*row.Selector), &machine); err == nil {
				chats[i].Machine = &machine
			}
		}
	}
	return chats, nil
}

// listChatsPausedForMachineSQL lists chats whose root run the daemon-offline
// breaker paused and nothing has resumed, oldest first. The root run must
// still read paused (STOPPED/PAUSED): a marker left behind by a run that
// ended since is not a chat to resume. An empty user id lists every user's
// (the sweep); otherwise one user's (a machine of theirs just connected).
// Archived chats are left alone.
const listChatsPausedForMachineSQL = `
SELECT c.id, c.user_id, c.workflow_id, c.active_daemon_id, c.paused_for_machine_at
FROM chats c
JOIN workflows w ON w.id = c.workflow_id
WHERE c.paused_for_machine_at IS NOT NULL
  AND c.state IS DISTINCT FROM 3
  AND w.state = 3 AND w.stop_reason = 3
  AND (?0::text = '' OR c.user_id = ?0::text)
ORDER BY c.paused_for_machine_at ASC, c.id ASC
LIMIT ?1`

// ListChatsPausedForMachine lists up to limit chats the breaker paused.
func ListChatsPausedForMachine(ctx context.Context, db orm.Context, userID string, limit int) ([]core.PausedForMachineChat, error) {
	var rows []struct {
		ID                 string    `bun:"id"`
		UserID             string    `bun:"user_id"`
		WorkflowID         string    `bun:"workflow_id"`
		ActiveDaemonID     *string   `bun:"active_daemon_id"`
		PausedForMachineAt time.Time `bun:"paused_for_machine_at"`
	}
	if err := db.Bun().NewRaw(listChatsPausedForMachineSQL, userID, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list chats paused for their machine: %w", err)
	}
	chats := make([]core.PausedForMachineChat, len(rows))
	for i, row := range rows {
		chats[i] = core.PausedForMachineChat{
			ChatID:         row.ID,
			UserID:         row.UserID,
			WorkflowID:     row.WorkflowID,
			ActiveDaemonID: row.ActiveDaemonID,
			PausedAt:       row.PausedForMachineAt,
		}
	}
	return chats, nil
}

// listChatsWaitingForMachineSQL lists one user's chats whose root run is live
// (ACTIVE) and parked waiting for a machine (the daemon-pending marker): the
// runs to tell when one of the user's machines connects.
const listChatsWaitingForMachineSQL = `
SELECT c.id, c.workflow_id
FROM chats c
JOIN workflows w ON w.id = c.workflow_id
WHERE c.user_id = ?0::text
  AND c.daemon_blocked_at IS NOT NULL
  AND w.state = 2`

// ListChatsWaitingForMachine lists a user's chats whose live root run is
// parked waiting for a machine.
func ListChatsWaitingForMachine(ctx context.Context, db orm.Context, userID string) ([]core.WaitingForMachineRun, error) {
	var rows []struct {
		ID         string `bun:"id"`
		WorkflowID string `bun:"workflow_id"`
	}
	if err := db.Bun().NewRaw(listChatsWaitingForMachineSQL, userID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list chats waiting for their machine: %w", err)
	}
	runs := make([]core.WaitingForMachineRun, len(rows))
	for i, row := range rows {
		runs[i] = core.WaitingForMachineRun{ChatID: row.ID, WorkflowID: row.WorkflowID}
	}
	return runs, nil
}

// GetChatRow reads a chat's own row (core.ChatRow) from the chats table, by
// primary key: no activity view. A missing chat is core.ErrChatNotFound.
func GetChatRow(ctx context.Context, db orm.Context, id string) (*core.ChatRow, error) {
	row, err := GetChatByID(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}
	out := &core.ChatRow{
		ID:             row.ID,
		Title:          row.Title,
		ProjectID:      row.ProjectID,
		UserID:         row.UserID,
		WorkflowID:     row.WorkflowID,
		WorkflowName:   row.WorkflowName,
		RunID:          row.RunID,
		WorktreeID:     row.WorktreeID,
		ActiveDaemonID: row.ActiveDaemonID,
		Unread:         row.Unread != 0,
		NoMachine:      row.NoMachine,
		AdoptedAt:      row.AdoptedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		LastActive:     row.LastActive,
	}
	if row.State != nil {
		out.State = *row.State
	}
	return out, nil
}

// GetChatRowWithUserCheck is GetChatRow for userID's chat only: any
// other chat is core.ErrChatNotFound, exactly like one that does not exist, so
// a chat id is not an existence oracle across users.
func GetChatRowWithUserCheck(ctx context.Context, db orm.Context, id, userID string) (*core.ChatRow, error) {
	chat, err := GetChatRow(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if chat.UserID != userID {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}
	return chat, nil
}

// chatToRow maps a core.Chat onto the chats row its writes take. An unset
// state is IDLE: nil leaves the column to its DEFAULT on insert, and an update
// writes IDLE explicitly, as the statements these replaced did.
func chatToRow(chat *core.Chat) *Chat {
	state := chat.State
	if state == core.ChatStateUnspecified {
		state = core.ChatStateIdle
	}
	// A chat nobody classified is a person's (the column default): the
	// launcher, the one creator of runs, always names the origin.
	origin := chat.Origin
	if origin == core.ChatOriginUnspecified {
		origin = core.ChatOriginUser
	}
	row := &Chat{
		ID:              chat.ID,
		Title:           chat.Title,
		ProjectID:       chat.ProjectID,
		UserID:          chat.UserID,
		State:           &state,
		WorkflowID:      chat.WorkflowID,
		RunID:           chat.RunID,
		CreatedAt:       chat.CreatedAt,
		UpdatedAt:       chat.UpdatedAt,
		LastActive:      chat.LastActive,
		WorktreeID:      chat.WorktreeID,
		WorkflowName:    chat.WorkflowName,
		SelectedPresets: chatPresetsToColumn(chat.SelectedPresets),
		ActiveDaemonID:  chat.ActiveDaemonID,
		NoMachine:       chat.NoMachine,
		MachinePoolID:   chat.MachinePoolID,
		Origin:          origin,
	}
	return row
}

// chatPresetsToColumn encodes selected presets for chats.selected_presets; no
// presets is NULL.
func chatPresetsToColumn(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	s := string(data)
	return &s
}

// ChatExtra carries fields on Chat that are NOT columns — computed
// values, transient state, anything derived rather than stored. Chat is
// embedded on ChatExtra (see chat_orm.go), so a field added here is a field on
// every Chat value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatExtra struct {
}
