// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatUpdate: the per-chat update ledger a
// chat's stream replays from (`WHERE sequence_number > cursor`). Every
// function takes and returns internal/db/core types.
package store

import (
	"context"
	"encoding/json"
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
// exactly what runs. It takes one parameter, the chat id, in four places.
//
//	1 = MESSAGE, 4 = TOOL_CALL, 19 = STREAM_FINALIZED  (not snapshot state at all)
//	3 = THREAD, 18 = QUESTION                          (re-keyed, read separately via idx_chat_updates_snapshot_rekeyed)
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
			SELECT DISTINCT ON (dedup_key) id, sequence_number
			FROM (
				SELECT
					id,
					sequence_number,
					CASE
						WHEN update_type = 18 THEN left(entity_id, length(entity_id) - position('-' in reverse(entity_id)))
						ELSE 'thread:' || COALESCE(NULLIF(data::jsonb->>'thread', ''), entity_id)
					END AS dedup_key
				FROM chat_updates
				WHERE chat_id = $1 AND update_type IN (3, 18)
			) k
			ORDER BY dedup_key, sequence_number DESC
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
