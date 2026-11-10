// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatUpdate: the per-chat update ledger a
// chat's stream replays from (`WHERE sequence_number > cursor`). Every
// function takes and returns internal/db/core types.
package store

import (
	"context"
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
	return chatUpdateFromRow(row), nil
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

// ─── Compaction ──────────────────────────────────────────────────────────────
//
// A chat's ledger is append-only, and it was never pruned: on prod
// (2026-11-10) 7.7M rows / 10 GB, more than half of them NODE_EXECUTION churn
// and MESSAGE / TOOL_CALL / STREAM_FINALIZED rows nothing reads after the live
// stream has delivered them. Compaction deletes an aged row only when no
// reader can need it, which is a statement about each reader:
//
//   - the snapshot heads (SnapshotHeadsSQL): the newest update per entity of
//     the types it walks, plus the newest THREAD / QUESTION per re-key;
//   - a run-status view's node heads (WorkflowNodeHeadsSQL): the newest
//     NODE_EXECUTION per node;
//   - an approval's announced state (listUnannouncedResolutionsSQL): the newest
//     APPROVAL update per approval;
//   - scroll-back's errors (ErrorUpdatesInRangeSQL): every ERROR attempt,
//     because it picks the newest one INSIDE a page's range;
//   - a polling follower (GetChatUpdates, sdk/go/reliant/follow): the run's
//     WORKFLOW_STATUS rows, its verdict;
//   - the chat's high-water mark (LatestChatUpdateSequence): its newest row.
//
// So THREAD, QUESTION, ERROR and WORKFLOW_STATUS are kept whole (together
// about 2% of rows); MESSAGE, TOOL_CALL, STREAM_FINALIZED and
// AGENT_MESSAGES_DRAINED, which no reader takes from the log, are deleted; and
// every other type keeps its newest row per (type, entity) — a superset of
// each head reader above, so each reads exactly what it read before (a type
// added later lands in this class, which is the safe default). Nothing is
// deleted that is the chat's newest row.
//
// Rows at or below the chat's compaction watermark
// (update_stream_counters.compacted_through_seq) are what is left; a replay
// from a cursor below it would skip rows, so a reconnect there gets a snapshot.
// CompactChatUpdates raises the watermark BEFORE it deletes, so it never
// under-states what is gone.
//
// The watermark is a replay floor, never progress. Progress is
// compaction_done_seq, recorded only AFTER a batch's delete has committed, and
// a pass resumes from it: a batch whose delete failed, or whose worker stopped
// between the two statements, is simply done again by the next pass (the
// delete is idempotent). Done never passes the watermark.

// ChatCompactionDue is a chat whose ledger has rows past where compaction
// finished that have aged past the cutoff.
type ChatCompactionDue struct {
	ChatID string `bun:"stream_id"`
	// Done is where compaction finished: the next batch starts after it.
	Done int64 `bun:"compaction_done_seq"`
}

// chatsDueForCompactionSQL finds them, after ?2 in chat id order, at most ?1.
// Every chat's counter row is read (the table holds one row per chat and per
// user), but only one with rows past its done mark costs a probe: the first
// row past it, on chat_updates_chat_sequence_key. A chat idle since its last
// compaction is done through its newest row and is skipped by the first
// predicate alone.
const chatsDueForCompactionSQL = `
SELECT c.stream_id, c.compaction_done_seq
  FROM update_stream_counters c
 WHERE c.stream_kind = 'chat'
   AND c.last_assigned_seq > c.compaction_done_seq
   AND c.stream_id > ?2
   AND (SELECT u.created_at FROM chat_updates u
         WHERE u.chat_id = c.stream_id AND u.sequence_number > c.compaction_done_seq
         ORDER BY u.sequence_number
         LIMIT 1) < ?0
 ORDER BY c.stream_id
 LIMIT ?1`

// ChatsDueForCompaction returns up to limit chats, after afterChatID in id
// order, whose first update past where compaction finished was written before
// cutoff.
func ChatsDueForCompaction(ctx context.Context, db orm.Context, cutoff time.Time, afterChatID string, limit int) ([]ChatCompactionDue, error) {
	var out []ChatCompactionDue
	if err := db.Bun().NewRaw(chatsDueForCompactionSQL, cutoff.UTC(), limit, afterChatID).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("chats due for compaction: %w", err)
	}
	return out, nil
}

// ChatCompactedThrough is a chat's compaction watermark: a replay must start
// at or above it. 0 for a chat never compacted, or with no counter row.
func ChatCompactedThrough(ctx context.Context, db orm.Context, chatID string) (int64, error) {
	var through int64
	err := db.QueryRow(ctx, `SELECT COALESCE((SELECT compacted_through_seq FROM update_stream_counters WHERE stream_kind = 'chat' AND stream_id = $1), 0)`, chatID).Scan(&through)
	if err != nil {
		return 0, fmt.Errorf("chat compaction watermark: %w", err)
	}
	return through, nil
}

// raiseChatWatermarkSQL raises a chat's compaction watermark, never lowers it.
// It is its own statement, committed before the delete it covers: the counter
// row is the chat's sequence allocator, and holding its lock across a delete
// would stall every writer of the chat for as long.
const raiseChatWatermarkSQL = `
UPDATE update_stream_counters SET compacted_through_seq = GREATEST(compacted_through_seq, $2)
 WHERE stream_kind = 'chat' AND stream_id = $1`

// recordChatCompactionDoneSQL records that compaction finished deleting
// through $2, once that delete has committed. Its own short statement for the
// watermark's reason; LEAST keeps it from ever passing the watermark.
const recordChatCompactionDoneSQL = `
UPDATE update_stream_counters
   SET compaction_done_seq = GREATEST(compaction_done_seq, LEAST($2, compacted_through_seq))
 WHERE stream_kind = 'chat' AND stream_id = $1`

// CompactChatUpdatesSQL deletes the rows of chat $1 in ($2, $3] that no reader
// needs (the section comment above). Each kept class is probed through the
// partial index its own reader uses, and the update types are LITERALS for
// SnapshotHeadsSQL's reason: a generic plan cannot prove a partial index
// applies through a bound parameter. TestChatCompactionClassesMatchTheReaders
// pins the lists to the enum and to SnapshotHeadsSQL's exclusion list.
//
//	3 THREAD, 5 WORKFLOW_STATUS, 6 ERROR, 18 QUESTION    kept whole
//	1 MESSAGE, 4 TOOL_CALL, 19 STREAM_FINALIZED,
//	20 AGENT_MESSAGES_DRAINED                            deleted
//	9 NODE_EXECUTION                                     newest per node (idx_chat_updates_workflow_heads)
//	everything else                                      newest per (type, entity) (idx_chat_updates_snapshot_entity_heads)
const CompactChatUpdatesSQL = `
DELETE FROM chat_updates c
 WHERE c.chat_id = $1 AND c.sequence_number > $2 AND c.sequence_number <= $3
   AND c.update_type NOT IN (3, 5, 6, 18)
   AND EXISTS (SELECT 1 FROM chat_updates n
                WHERE n.chat_id = c.chat_id AND n.sequence_number > c.sequence_number)
   AND (
        c.update_type IN (1, 4, 19, 20)
     OR (c.update_type = 9 AND EXISTS (
            SELECT 1 FROM chat_updates n
             WHERE n.chat_id = c.chat_id AND n.update_type = 9
               AND n.entity_id COLLATE "C" = c.entity_id COLLATE "C"
               AND n.sequence_number > c.sequence_number))
     OR (c.update_type NOT IN (1, 3, 4, 5, 9, 18, 19, 20) AND EXISTS (
            SELECT 1 FROM chat_updates n
             WHERE n.chat_id = c.chat_id AND n.update_type NOT IN (1, 3, 4, 5, 9, 18, 19, 20)
               AND n.entity_id = c.entity_id AND n.update_type = c.update_type
               AND n.sequence_number > c.sequence_number))
   )`

// ChatCompactionBatch is what one CompactChatUpdates call did.
type ChatCompactionBatch struct {
	// Examined is how many aged rows the batch covered.
	Examined int
	// Deleted is how many of them it deleted.
	Deleted int64
	// Through is how far the batch compacted: the watermark it raised, and
	// where compaction is now finished.
	Through int64
	// Done is true when the chat has no aged row past Through.
	Done bool
}

// CompactChatUpdates compacts the next run of at most limit aged rows of a
// chat after `after`, where compaction last finished: rows are aged when
// written before cutoff, and the run stops at the first row that is not, so
// the watermark never passes a row younger than the cutoff. It runs outside
// any transaction, as three statements in this order: raise the watermark,
// delete, record the delete done. Stopped anywhere in between, the next call
// from the done mark repeats the batch, and nothing is skipped.
func CompactChatUpdates(ctx context.Context, db orm.Context, chatID string, after int64, cutoff time.Time, limit int) (ChatCompactionBatch, error) {
	batch := ChatCompactionBatch{Through: after}
	if limit <= 0 {
		return batch, errors.New("compact chat updates: limit must be positive")
	}
	rows, err := db.Query(ctx, `SELECT sequence_number, created_at FROM chat_updates
		WHERE chat_id = $1 AND sequence_number > $2 ORDER BY sequence_number LIMIT $3`, chatID, after, limit)
	if err != nil {
		return batch, fmt.Errorf("compact chat updates: read: %w", err)
	}
	read, young := 0, false
	for rows.Next() {
		var seq int64
		var createdAt time.Time
		if err := rows.Scan(&seq, &createdAt); err != nil {
			rows.Close()
			return batch, fmt.Errorf("compact chat updates: scan: %w", err)
		}
		read++
		if young || !createdAt.Before(cutoff) {
			young = true
			continue
		}
		batch.Through = seq
		batch.Examined++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return batch, fmt.Errorf("compact chat updates: read: %w", err)
	}
	batch.Done = young || read < limit
	if batch.Examined == 0 {
		return batch, nil
	}
	if _, err := db.Exec(ctx, raiseChatWatermarkSQL, chatID, batch.Through); err != nil {
		return batch, fmt.Errorf("compact chat updates: raise watermark: %w", err)
	}
	res, err := db.Exec(ctx, CompactChatUpdatesSQL, chatID, after, batch.Through)
	if err != nil {
		return batch, fmt.Errorf("compact chat updates: delete: %w", err)
	}
	if batch.Deleted, err = res.RowsAffected(); err != nil {
		return batch, fmt.Errorf("compact chat updates: delete: %w", err)
	}
	if _, err := db.Exec(ctx, recordChatCompactionDoneSQL, chatID, batch.Through); err != nil {
		return batch, fmt.Errorf("compact chat updates: record done: %w", err)
	}
	return batch, nil
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
