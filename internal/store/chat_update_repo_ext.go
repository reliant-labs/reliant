// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatUpdate: the per-chat update ledger a
// chat's stream replays from (`WHERE sequence_number > cursor`). Every
// function takes and returns internal/db/core types.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AppendChatUpdate allocates the chat's next update sequence and writes the
// ledger row under it, in the transaction ctx carries (it refuses without
// one: see AllocateUpdateSequence). The row id is "<chat>-<seq>".
func AppendChatUpdate(ctx context.Context, db orm.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID, data string) (core.ChatUpdate, error) {
	seq, err := AllocateUpdateSequence(ctx, db, UpdateStreamChat, chatID)
	if err != nil {
		return core.ChatUpdate{}, fmt.Errorf("failed to get next sequence number: %w", err)
	}
	row := &ChatUpdate{
		ID:             fmt.Sprintf("%s-%d", chatID, seq),
		ChatID:         chatID,
		SequenceNumber: seq,
		UpdateType:     updateType,
		EntityID:       entityID,
		Data:           data,
		CreatedAt:      time.Now().UTC(),
	}
	if err := CreateChatUpdate(ctx, db, row); err != nil {
		return core.ChatUpdate{}, err
	}
	if err := ApplyChatUpdateHead(ctx, db, row); err != nil {
		return core.ChatUpdate{}, err
	}
	return chatUpdateFromRow(row), nil
}

// ApplyChatUpdateHead keeps chat_update_heads equal to what SnapshotHeadsSQL
// would compute from the log, for one just-inserted log row: if its type is a
// snapshot head, it becomes its key's head unless that key already holds a
// newer one. It MUST run in the transaction that inserted the row, the one
// that allocated its sequence (AllocateUpdateSequence advanced the chat's
// heads_through_seq there, vouching that this write maintains the heads).
// Every chat_updates insert goes through here: AppendChatUpdate and
// SaveMessageAtomic. dev-docs/design/chat-update-heads.md has the design.
func ApplyChatUpdateHead(ctx context.Context, db orm.Context, row *ChatUpdate) error {
	if !isSnapshotHeadType(row.UpdateType) {
		return nil
	}
	if _, err := db.Exec(ctx, applyChatUpdateHeadSQL,
		row.ChatID, row.ID, row.SequenceNumber, int32(row.UpdateType), row.EntityID, row.Data, row.CreatedAt,
	); err != nil {
		return fmt.Errorf("apply chat update head: %w", err)
	}
	return nil
}

// isSnapshotHeadType is whether an update type can be a snapshot head: every
// type outside SnapshotHeadsSQL's exclusion list (1, 4, 5, 9, 19, 20) once the
// re-keyed THREAD and QUESTION are counted in. It only saves the statement for
// the types that are never heads; chatUpdateHeadKeySQL's filter is the
// authority, so a type wrongly listed here as a head is harmless, and the
// differential test (TestChatUpdateHeads_MatchTheLog) catches one wrongly
// left out.
func isSnapshotHeadType(t reliantv1.ChatUpdateType) bool {
	switch t {
	case reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED:
		return false
	}
	return true
}

// chatUpdateHeadKeySQL is a chat_updates row's key in chat_update_heads, over
// the columns update_type, entity_id and data: 'e:' || entity_id for the types
// SnapshotHeadsSQL's skip scan keys by entity, 'r:' || SnapshotRekeyKeySQL for
// THREAD and QUESTION, NULL for every type that is no head. The prefixes keep
// the two halves of SnapshotHeadsSQL apart, as its UNION does: an entity id
// that equals a re-key key is two heads there, and two here. The type lists
// are SnapshotHeadsSQL's literals (TestSnapshotHeadsQueryMatchesPartialIndex
// pins those to the enum).
const chatUpdateHeadKeySQL = `CASE
	WHEN update_type IN (3, 18) THEN 'r:' || (` + SnapshotRekeyKeySQL + `)
	WHEN update_type NOT IN (1, 4, 5, 9, 19, 20) THEN 'e:' || entity_id
	END`

// chatUpdateHeadUpsertSQL is the tail every write to chat_update_heads ends
// with: a key's head only ever moves to a higher sequence, so writers and the
// backfill may interleave in any order and still converge on the newest row.
const chatUpdateHeadUpsertSQL = `
	ON CONFLICT (chat_id, head_key) DO UPDATE SET
		update_id = excluded.update_id,
		sequence_number = excluded.sequence_number,
		update_type = excluded.update_type,
		entity_id = excluded.entity_id,
		data = excluded.data,
		created_at = excluded.created_at
	WHERE excluded.sequence_number > chat_update_heads.sequence_number`

// applyChatUpdateHeadSQL makes one log row its key's head. The row's values
// are passed rather than read back from chat_updates, which saves an index
// probe on every head write; the key is still computed by Postgres, from the
// same values the log row holds, through the same expression. At READ
// COMMITTED (the writers' level) a concurrent upsert of the same key waits
// for the row and then judges the WHERE against its committed version, so the
// higher sequence wins either way.
const applyChatUpdateHeadSQL = `
	INSERT INTO chat_update_heads (chat_id, head_key, update_id, sequence_number, update_type, entity_id, data, created_at)
	SELECT chat_id, head_key, id, sequence_number, update_type, entity_id, data, created_at
	  FROM (SELECT v.chat_id, v.id, v.sequence_number, v.update_type, v.entity_id, v.data, v.created_at,
	               (` + chatUpdateHeadKeySQL + `) AS head_key
	          FROM (VALUES ($1::text, $2::text, $3::bigint, $4::integer, $5::text, $6::text, $7::timestamptz))
	               AS v(chat_id, id, sequence_number, update_type, entity_id, data, created_at)) r
	 WHERE head_key IS NOT NULL` + chatUpdateHeadUpsertSQL

// backfillChatUpdateHeadsSQL upserts the heads SnapshotHeadsSQL computes from
// the log: the backfill's one slow statement, the skip scan through
// idx_chat_updates_snapshot_entity_heads and idx_chat_updates_snapshot_rekey_heads.
// Each key appears once in its result (one head per entity, one per re-key
// key, in separate namespaces), as ON CONFLICT DO UPDATE requires.
const backfillChatUpdateHeadsSQL = `
	INSERT INTO chat_update_heads (chat_id, head_key, update_id, sequence_number, update_type, entity_id, data, created_at)
	SELECT chat_id, head_key, id, sequence_number, update_type, entity_id, data, created_at
	  FROM (SELECT s.chat_id, s.id, s.sequence_number, s.update_type, s.entity_id, s.data, s.created_at,
	               (` + chatUpdateHeadKeySQL + `) AS head_key
	          FROM (` + SnapshotHeadsSQL + `) s) r
	 WHERE head_key IS NOT NULL` + chatUpdateHeadUpsertSQL

// applyChatUpdateHeadsRangeSQL applies the log rows of ($2, $3] to the heads,
// the newest per key: what the backfill's last step reads under the counter
// lock, a range of chat_updates_chat_sequence_key.
const applyChatUpdateHeadsRangeSQL = `
	INSERT INTO chat_update_heads (chat_id, head_key, update_id, sequence_number, update_type, entity_id, data, created_at)
	SELECT DISTINCT ON (head_key) chat_id, head_key, id, sequence_number, update_type, entity_id, data, created_at
	  FROM (SELECT cu.id, cu.chat_id, cu.sequence_number, cu.update_type, cu.entity_id, cu.data, cu.created_at,
	               (` + chatUpdateHeadKeySQL + `) AS head_key
	          FROM chat_updates cu
	         WHERE cu.chat_id = $1 AND cu.sequence_number > $2 AND cu.sequence_number <= $3) r
	 WHERE head_key IS NOT NULL
	 ORDER BY head_key, sequence_number DESC` + chatUpdateHeadUpsertSQL

// lockChatStreamCounterSQL locks a chat's counter row and reads it. Taking the
// lock waits out every writer that allocated a sequence and has not yet
// committed (each holds the row from its allocation to its commit), so once it
// returns, every sequence up to last_assigned_seq is committed or gone.
const lockChatStreamCounterSQL = `
	SELECT last_assigned_seq, heads_through_seq FROM update_stream_counters
	 WHERE stream_kind = 'chat' AND stream_id = $1
	   FOR UPDATE`

// BackfillChatUpdateHeads makes one chat's heads complete: afterwards
// chat_update_heads holds what SnapshotHeadsSQL computes from its log, and the
// counter vouches for it (heads_through_seq = last_assigned_seq). It reports
// whether it had anything to do; a chat already complete, or with no counter,
// costs one locked read.
//
// Three steps, so the slow one holds no lock a writer needs
// (dev-docs/design/chat-update-heads.md, "Backfill"):
//
//  1. Lock the counter and read L0, then commit: every sequence <= L0 is
//     committed.
//  2. With no lock, upsert the heads the log has (backfillChatUpdateHeadsSQL).
//     It sees every row <= L0. Writers carry on beside it; a writer of this
//     release upserts its own head, and the upsert is monotone, so their
//     order does not matter.
//  3. Lock the counter again and read L1, apply the rows of (L0, L1] (written
//     meanwhile, by either release), and set heads_through_seq = L1. Holding
//     the lock, no row above L1 exists.
//
// Every log row <= L1 is then reflected. Both steps that lock take the counter
// row first and head rows after, the writers' order, so they cannot deadlock
// with a writer. Concurrent backfills of one chat converge the same way.
// ctx must not carry a transaction: each step is its own.
func BackfillChatUpdateHeads(ctx context.Context, db orm.Context, chatID string) (bool, error) {
	if orm.InTx(ctx) {
		return false, fmt.Errorf("backfill chat update heads: must not run inside a transaction")
	}
	var l0, h0 int64
	found := true
	if err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		err := db.QueryRow(ctx, lockChatStreamCounterSQL, chatID).Scan(&l0, &h0)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		return false, fmt.Errorf("backfill chat update heads: read counter: %w", err)
	}
	if !found || h0 == l0 {
		return false, nil
	}

	if _, err := db.Exec(ctx, backfillChatUpdateHeadsSQL, chatID); err != nil {
		return false, fmt.Errorf("backfill chat update heads: from the log: %w", err)
	}

	if err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var l1, h1 int64
		if err := db.QueryRow(ctx, lockChatStreamCounterSQL, chatID).Scan(&l1, &h1); err != nil {
			return err
		}
		if h1 == l1 {
			return nil // another backfill finished first
		}
		if _, err := db.Exec(ctx, applyChatUpdateHeadsRangeSQL, chatID, l0, l1); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `
			UPDATE update_stream_counters SET heads_through_seq = last_assigned_seq
			 WHERE stream_kind = 'chat' AND stream_id = $1`, chatID)
		return err
	}); err != nil {
		return false, fmt.Errorf("backfill chat update heads: catch up: %w", err)
	}
	return true, nil
}

// ChatsWithIncompleteHeads lists, in id order, up to limit chats after
// afterChatID whose heads the counter does not vouch for: the backfill's work
// list. It is the state itself, so a backfill that stops resumes where the
// state says, and a chat the previous release writes to again (a rollback)
// reappears on it.
func ChatsWithIncompleteHeads(ctx context.Context, db orm.Context, afterChatID string, limit int) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT stream_id FROM update_stream_counters
		 WHERE stream_kind = 'chat' AND stream_id > $1 AND heads_through_seq <> last_assigned_seq
		 ORDER BY stream_id
		 LIMIT $2`, afterChatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chats []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		chats = append(chats, id)
	}
	return chats, rows.Err()
}

// SnapshotHeadsTableSQL is SnapshotUpdateHeads' statement: the chat's heads
// from chat_update_heads, but only when its stream counter vouches for them
// (heads_through_seq = last_assigned_seq: every update the chat has was
// written by a release that maintains the heads, or applied by the backfill).
// The first column is that verdict, on every row; an incomplete chat returns
// one row with NULL heads, and a chat with no counter row returns none. One
// statement is one snapshot, so the verdict and the heads it vouches for can
// never straddle a write.
const SnapshotHeadsTableSQL = `
	SELECT c.heads_through_seq = c.last_assigned_seq,
	       h.update_id, h.chat_id, h.sequence_number, h.update_type, h.entity_id, h.data, h.created_at
	  FROM update_stream_counters c
	  LEFT JOIN chat_update_heads h
	         ON h.chat_id = $1 AND c.heads_through_seq = c.last_assigned_seq
	 WHERE c.stream_kind = 'chat' AND c.stream_id = $1
	 ORDER BY h.sequence_number`

// Where SnapshotUpdateHeads read a chat's heads from.
const (
	HeadsSourceTable       = "heads_table"
	HeadsSourceLogFallback = "log_fallback"
)

// SnapshotUpdateHeads is the chat snapshot's update heads, in sequence order:
// LatestNonMessageUpdatesPerEntity's answer, read from chat_update_heads when
// the chat's heads are complete and from the log (LatestNonMessageUpdatesPerEntity
// itself) when they are not — a chat written by the previous release, or not
// yet backfilled. The source says which.
func SnapshotUpdateHeads(ctx context.Context, db orm.Context, chatID string) ([]core.ChatUpdate, string, error) {
	updates, complete, err := chatUpdateHeadsFromTable(ctx, db, chatID)
	if err != nil {
		return nil, "", err
	}
	if complete {
		return updates, HeadsSourceTable, nil
	}
	updates, err = LatestNonMessageUpdatesPerEntity(ctx, db, chatID)
	if err != nil {
		return nil, "", err
	}
	return updates, HeadsSourceLogFallback, nil
}

// chatUpdateHeadsFromTable reads SnapshotHeadsTableSQL: the heads, and whether
// the counter vouches for them.
func chatUpdateHeadsFromTable(ctx context.Context, db orm.Context, chatID string) ([]core.ChatUpdate, bool, error) {
	rows, err := db.Query(ctx, SnapshotHeadsTableSQL, chatID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	updates := []core.ChatUpdate{}
	complete := false
	for rows.Next() {
		var (
			id, rowChat, entity, data sql.NullString
			seq                       sql.NullInt64
			typ                       sql.NullInt32
			createdAt                 sql.NullTime
		)
		if err := rows.Scan(&complete, &id, &rowChat, &seq, &typ, &entity, &data, &createdAt); err != nil {
			return nil, false, err
		}
		if !complete || !id.Valid {
			continue
		}
		updates = append(updates, core.ChatUpdate{
			ID:             id.String,
			ChatID:         rowChat.String,
			SequenceNumber: seq.Int64,
			UpdateType:     reliantv1.ChatUpdateType(typ.Int32),
			EntityID:       entity.String,
			Data:           json.RawMessage(data.String),
			CreatedAt:      createdAt.Time,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return updates, complete, nil
}

// LatestChatUpdateSequence is the highest sequence number a chat's ledger
// holds, 0 for a chat with none: what a polling client compares its cursor
// against.
func LatestChatUpdateSequence(ctx context.Context, db orm.Context, chatID string) (int64, error) {
	var seq int64
	err := db.Bun().NewSelect().Model((*ChatUpdate)(nil)).
		ColumnExpr("COALESCE(MAX(sequence_number), 0)").
		Where("chat_id = ?", chatID).
		Scan(ctx, &seq)
	return seq, err
}

// ChatUpdatesSince is a chat's updates after sinceSeq, ascending, at most
// limit of them.
func ChatUpdatesSince(ctx context.Context, db orm.Context, chatID string, sinceSeq int64, limit int) ([]core.ChatUpdate, error) {
	rows, err := ListChatUpdate(ctx, db,
		orm.WhereEq(ChatUpdateFieldChatID, chatID),
		orm.WhereGt(ChatUpdateFieldSequenceNumber, sinceSeq),
		orm.WithOrderBy(ChatUpdateFieldSequenceNumber, orm.Asc),
		orm.WithLimit(limit))
	if err != nil {
		return nil, err
	}
	updates := make([]core.ChatUpdate, len(rows))
	for i, row := range rows {
		updates[i] = chatUpdateFromRow(row)
	}
	return updates, nil
}

// SnapshotHeadsSQL is LatestNonMessageUpdatesPerEntity's statement, exported
// so the partial-index tests (internal/db/snapshot_heads_index_test.go) read
// exactly what runs. It takes one parameter, the chat id, in five places.
//
//	1 = MESSAGE, 4 = TOOL_CALL, 19 = STREAM_FINALIZED  (not snapshot state at all)
//	3 = THREAD, 18 = QUESTION                          (re-keyed, read separately via idx_chat_updates_snapshot_rekey_heads)
//	5 = WORKFLOW_STATUS, 9 = NODE_EXECUTION            (workflow state: not in a snapshot; WorkflowNodeHeadsSQL)
//	20 = AGENT_MESSAGES_DRAINED                        (live-only)
//
// The skip scan probes once per distinct entity it walks, so it must not walk
// workflow state: a node's entity is "<workflow>:<node>", every run a chat ever
// made adds its own, and a chat with a long history had tens of thousands of
// them (prod: this read at p50 0.2s, p95 2.5s, max 11s). The skip scan's list
// is EXACTLY idx_chat_updates_snapshot_entity_heads' predicate, so every probe
// is answered from that index alone and never walks a row it would discard.
//
// The update types are LITERALS, not bind parameters, and that is
// load-bearing. The planner can use a partial index only when it can PROVE the
// query's predicate implies the index's, and it can prove that only from
// values it sees at plan time. pgx prepares statements, and Postgres switches
// a prepared statement to a generic plan after five executions — one that
// cannot see a bound parameter. With parameters, that generic plan lost
// idx_chat_updates_snapshot_heads and fell back to a per-entity scan of the
// chat's whole history: measured >20s (cancelled) on the dev copy. The chat id
// IS bound (raw passthrough, $1): a statement text per chat would re-plan the
// recursive skip scan on every chat open.
//
// The skip scan's list must stay EQUAL to the heads index's predicate: a
// superset still lets the planner use it, but every probe then rechecks each
// index entry on the heap and walks past the excluded ones.
//
// The re-keyed half (THREAD, QUESTION) is a second skip scan, over
// SnapshotRekeyKeySQL through idx_chat_updates_snapshot_rekey_heads: one
// probe per key, landing on that key's newest row. It used to read every
// thread and question update the chat ever wrote and DISTINCT ON them; those
// rows are scattered over the table, so on a cold cache each was a random heap
// read (prod: 2,142 blocks to keep 457 rows, 1.86s of the chat open).
const SnapshotHeadsSQL = `
		WITH RECURSIVE heads AS (
			(SELECT entity_id, sequence_number
			   FROM chat_updates
			  WHERE chat_id = $1 AND update_type NOT IN (1, 3, 4, 5, 9, 18, 19, 20)
			  ORDER BY entity_id, sequence_number DESC
			  LIMIT 1)
			UNION ALL
			SELECT nxt.entity_id, nxt.sequence_number
			  FROM heads h
			  CROSS JOIN LATERAL (
				SELECT entity_id, sequence_number
				  FROM chat_updates
				 WHERE chat_id = $1 AND update_type NOT IN (1, 3, 4, 5, 9, 18, 19, 20)
				   AND entity_id > h.entity_id
				 ORDER BY entity_id, sequence_number DESC
				 LIMIT 1) nxt
		),
		rekeyed AS (
			(SELECT (` + SnapshotRekeyKeySQL + `) AS dedup_key, sequence_number
			   FROM chat_updates
			  WHERE chat_id = $1 AND update_type IN (3, 18)
			  ORDER BY (` + SnapshotRekeyKeySQL + `), sequence_number DESC
			  LIMIT 1)
			UNION ALL
			SELECT nxt.dedup_key, nxt.sequence_number
			  FROM rekeyed r
			  CROSS JOIN LATERAL (
				SELECT (` + SnapshotRekeyKeySQL + `) AS dedup_key, sequence_number
				  FROM chat_updates
				 WHERE chat_id = $1 AND update_type IN (3, 18)
				   AND (` + SnapshotRekeyKeySQL + `) > r.dedup_key
				 ORDER BY (` + SnapshotRekeyKeySQL + `), sequence_number DESC
				 LIMIT 1) nxt
		)
		SELECT
			cu.id,
			cu.chat_id,
			cu.sequence_number,
			cu.update_type,
			cu.entity_id,
			cu.data,
			cu.created_at
		FROM (
			SELECT sequence_number FROM heads
			UNION
			SELECT sequence_number FROM rekeyed
		) picked
		JOIN chat_updates cu ON cu.chat_id = $1 AND cu.sequence_number = picked.sequence_number
		ORDER BY cu.sequence_number
	`

// SnapshotRekeyKeySQL is the key a THREAD or QUESTION update is deduplicated
// by in the snapshot: a thread by its payload's `thread` (else its entity), a
// question by its id without the trailing "-<n>". It is also the key column of
// idx_chat_updates_snapshot_rekey_heads, written there character for
// character: the planner matches an expression index only to the same
// expression, so the two must not drift (TestSnapshotHeadsQueryMatchesPartialIndex).
const SnapshotRekeyKeySQL = `CASE WHEN update_type = 18 THEN left(entity_id, length(entity_id) - position('-' in reverse(entity_id))) ELSE 'thread:' || COALESCE(NULLIF(data::jsonb->>'thread', ''), entity_id) END`

// LatestNonMessageUpdatesPerEntity returns, for each entity of a chat, its
// newest non-message update, in sequence order — the chat snapshot's update
// heads. db.Repo.GetLatestNonMessageUpdatesPerEntity explains the shape: why
// one row per entity, which types re-key, and which are excluded.
func LatestNonMessageUpdatesPerEntity(ctx context.Context, db orm.Context, chatID string) ([]core.ChatUpdate, error) {
	updates, err := scanChatUpdates(ctx, db, SnapshotHeadsSQL, chatID)
	if err != nil {
		return nil, err
	}
	// DISTINCT ON forces ORDER BY entity_id inside; restore sequence order so
	// consumers see updates in the order they were emitted.
	sort.Slice(updates, func(i, j int) bool { return updates[i].SequenceNumber < updates[j].SequenceNumber })
	return updates, nil
}

// WorkflowNodeHeadsSQL is LatestWorkflowNodeUpdates' statement, exported so
// the partial-index tests read exactly what runs. $1 is the chat, $2 the
// workflow id.
//
// A node's entity_id is "<workflow id>:<node id>", so one workflow's nodes are
// the range [id || ':', id || ';'), walked by a skip scan: one probe per node,
// so the cost is the size of the workflow's graph, never the length of its
// history (an agent loop's call_llm runs once per turn, all under one entity).
// The range is taken under COLLATE "C", the collation
// idx_chat_updates_workflow_heads indexes entity_id with, so it is the byte
// range whatever the database's default collation is: a linguistic one may
// ignore ':' at the first level and interleave another workflow's nodes. The
// update type is a literal for SnapshotHeadsSQL's reason: a bound parameter
// hides it from a generic plan, which then cannot prove the partial index
// applies.
const WorkflowNodeHeadsSQL = `
		WITH RECURSIVE nodes AS (
			(SELECT entity_id COLLATE "C" AS k, sequence_number
			   FROM chat_updates
			  WHERE chat_id = $1 AND update_type = 9
			    AND entity_id COLLATE "C" > ($2 || ':') COLLATE "C"
			    AND entity_id COLLATE "C" < ($2 || ';') COLLATE "C"
			  ORDER BY entity_id COLLATE "C", sequence_number DESC
			  LIMIT 1)
			UNION ALL
			SELECT nxt.k, nxt.sequence_number
			  FROM nodes n
			  CROSS JOIN LATERAL (
				SELECT entity_id COLLATE "C" AS k, sequence_number
				  FROM chat_updates
				 WHERE chat_id = $1 AND update_type = 9
				   AND entity_id COLLATE "C" > n.k
				   AND entity_id COLLATE "C" < ($2 || ';') COLLATE "C"
				 ORDER BY entity_id COLLATE "C", sequence_number DESC
				 LIMIT 1) nxt
		)
		SELECT
			cu.id,
			cu.chat_id,
			cu.sequence_number,
			cu.update_type,
			cu.entity_id,
			cu.data,
			cu.created_at
		FROM nodes
		JOIN chat_updates cu ON cu.chat_id = $1 AND cu.sequence_number = nodes.sequence_number
		ORDER BY cu.sequence_number
	`

// LatestWorkflowNodeUpdates returns the newest NODE_EXECUTION update of each
// node of one workflow of a chat, in sequence order. A workflow with none, or
// of another chat, has none.
func LatestWorkflowNodeUpdates(ctx context.Context, db orm.Context, chatID, workflowID string) ([]core.ChatUpdate, error) {
	if workflowID == "" {
		return []core.ChatUpdate{}, nil
	}
	return scanChatUpdates(ctx, db, WorkflowNodeHeadsSQL, chatID, workflowID)
}

// ErrorUpdatesInRangeSQL is ErrorUpdatesInRange's statement: for each error
// with an update in [$2, the creation of thread $3's first message at or after
// seq $4), its newest update INSIDE that range, oldest first. $2 NULL leaves
// the range open below; no such message leaves it open above. The range is
// applied before DISTINCT ON on purpose: it is what keeps the read on
// idx_chat_updates_errors_created. Read through that index.
const ErrorUpdatesInRangeSQL = `
		SELECT id, chat_id, sequence_number, update_type, entity_id, data, created_at FROM (
			SELECT DISTINCT ON (entity_id) id, chat_id, sequence_number, update_type, entity_id, data, created_at
			  FROM chat_updates
			 WHERE chat_id = $1 AND update_type = 6
			   AND ($2::timestamptz IS NULL OR created_at >= $2::timestamptz)
			   AND created_at < COALESCE(
					(SELECT m.created_at FROM messages m
					  WHERE m.thread_id = $3 AND m.seq >= $4
					  ORDER BY m.seq LIMIT 1),
					'infinity'::timestamptz)
			 ORDER BY entity_id, sequence_number DESC
		) e
		ORDER BY sequence_number`

// ErrorUpdatesInRange is the errors a page of history holds: raised at or
// after from (nil: since the chat began) and before the first message of
// thread at or after beforeSeq, the message the client already shows next.
// One update per error (a retry series shares one id): its newest attempt
// inside the range, which is not its newest overall when the series runs on
// past the range's upper edge. That later attempt lies in history the client
// already holds, so a caller merging these must never let one replace a newer
// attempt it has (the web client's applyHistoricalErrors).
func ErrorUpdatesInRange(ctx context.Context, db orm.Context, chatID string, from *time.Time, thread string, beforeSeq int64) ([]core.ChatUpdate, error) {
	return scanChatUpdates(ctx, db, ErrorUpdatesInRangeSQL, chatID, from, thread, beforeSeq)
}

// scanChatUpdates runs a statement selecting chat_updates' columns in table
// order and reads its rows.
func scanChatUpdates(ctx context.Context, db orm.Context, query string, args ...any) ([]core.ChatUpdate, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	updates := []core.ChatUpdate{}
	for rows.Next() {
		var u core.ChatUpdate
		var data string
		if err := rows.Scan(&u.ID, &u.ChatID, &u.SequenceNumber, &u.UpdateType, &u.EntityID, &data, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Data = json.RawMessage(data)
		updates = append(updates, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func chatUpdateFromRow(row *ChatUpdate) core.ChatUpdate {
	return core.ChatUpdate{
		ID:             row.ID,
		ChatID:         row.ChatID,
		SequenceNumber: row.SequenceNumber,
		UpdateType:     row.UpdateType,
		EntityID:       row.EntityID,
		Data:           json.RawMessage(row.Data),
		CreatedAt:      row.CreatedAt,
	}
}

// ChatUpdateExtra carries fields on ChatUpdate that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatUpdate is
// embedded on ChatUpdateExtra (see chat_update_orm.go), so a field added here is a field on
// every ChatUpdate value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatUpdateExtra struct {
}
