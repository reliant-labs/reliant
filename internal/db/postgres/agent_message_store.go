// forge:exclude-contract
//
// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	"github.com/sqlc-dev/pqtype"
)

type agentMessageStore struct {
	q  pgdb.Querier
	db pgdb.DBTX
}

// NewAgentMessageStore creates the Postgres agent message store implementation.
func NewAgentMessageStore(q pgdb.Querier, db pgdb.DBTX) core.AgentMessageStore {
	return &agentMessageStore{q: q, db: db}
}

func (s *agentMessageStore) EnqueueAgentMessage(ctx context.Context, msg *core.AgentMessage) error {
	attachments, err := agentMessageAttachmentsToNullRawMessage(msg.Attachments)
	if err != nil {
		return err
	}
	return s.q.EnqueueAgentMessage(ctx, pgdb.EnqueueAgentMessageParams{
		ID:           msg.ID,
		ChatID:       msg.ChatID,
		FromThreadID: msg.FromThreadID,
		ToThreadID:   msg.ToThreadID,
		Kind:         int32(msg.Kind),
		Body:         msg.Body,
		ToolCallID:   agentMessagePtrToNullString(msg.ToolCallID),
		Status:       int32(msg.Status),
		CreatedAt:    msg.CreatedAt,
		Attachments:  attachments,
	})
}

// EnqueueAgentMessageIfAbsent writes msg unless a terminal report for its
// ToolCallID already exists. sql.ErrNoRows is the constraint's ordinary
// "already reported" outcome (RETURNING id produces no row on a DO NOTHING
// conflict), not a failure -- CreateWorkflow's ON CONFLICT DO NOTHING treats
// the same driver behavior the same way.
func (s *agentMessageStore) EnqueueAgentMessageIfAbsent(ctx context.Context, msg *core.AgentMessage) (bool, error) {
	attachments, err := agentMessageAttachmentsToNullRawMessage(msg.Attachments)
	if err != nil {
		return false, err
	}
	_, err = s.q.EnqueueAgentMessageIfAbsent(ctx, pgdb.EnqueueAgentMessageIfAbsentParams{
		ID:           msg.ID,
		ChatID:       msg.ChatID,
		FromThreadID: msg.FromThreadID,
		ToThreadID:   msg.ToThreadID,
		Kind:         int32(msg.Kind),
		Body:         msg.Body,
		ToolCallID:   agentMessagePtrToNullString(msg.ToolCallID),
		Status:       int32(msg.Status),
		CreatedAt:    msg.CreatedAt,
		Attachments:  attachments,
	})
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *agentMessageStore) ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*core.AgentMessage, error) {
	rows, err := s.q.ListQueuedAgentMessagesForThread(ctx, toThreadID)
	if err != nil {
		return nil, err
	}
	result := make([]*core.AgentMessage, len(rows))
	for i, row := range rows {
		result[i] = agentMessageFromPG(row)
	}
	return result, nil
}

// MarkAgentMessagesDelivered builds its own IN clause rather than calling the
// sqlc-generated MarkAgentMessagesDelivered. The generated code for
// sqlc.slice() under database/sql emits `IN ($3)` and then rewrites a
// `/*SLICE:...*/?` marker that is not present in the Postgres query, so it
// silently matches only the first id. ListToolCallsByMessageIDs in
// tool_call_store.go works around the same defect the same way.
// MarkAgentMessagesDelivered moves queued rows to delivered and reports which
// ones it actually moved.
//
// The status = 1 guard and the RETURNING are what make a drain idempotent. The
// drain lists its rows before writing them, so two drains racing on one thread
// can select the same queued rows; without the guard the loser's UPDATE would
// silently re-deliver rows that are already delivered and repoint
// delivered_message_id at its own duplicate envelope. Returning the ids that
// moved lets the caller see it lost and abandon its inserts instead of writing
// the same messages into the transcript twice.
func (s *agentMessageStore) MarkAgentMessagesDelivered(ctx context.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// An empty id claims the rows with delivered_message_id left NULL: the
	// column is a FK into messages, and a claim happens BEFORE the envelope it
	// will point at exists. The caller backfills it in the same transaction.
	var delivered interface{}
	if deliveredMessageID != "" {
		delivered = deliveredMessageID
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+2)
	args = append(args, deliveredAt, delivered)
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, id)
	}

	query := fmt.Sprintf(
		`UPDATE agent_messages SET status = 2, delivered_at = $1, delivered_message_id = $2 `+
			`WHERE id IN (%s) AND status = 1 RETURNING id`,
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var claimed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		claimed = append(claimed, id)
	}
	return claimed, rows.Err()
}

// SetAgentMessagesDeliveredMessageID backfills the envelope pointer on rows the
// caller already claimed. Builds its own IN clause for the same sqlc.slice()
// defect described above.
func (s *agentMessageStore) SetAgentMessagesDeliveredMessageID(ctx context.Context, ids []string, deliveredMessageID string) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, deliveredMessageID)
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, id)
	}

	query := fmt.Sprintf(
		`UPDATE agent_messages SET delivered_message_id = $1 WHERE id IN (%s)`,
		strings.Join(placeholders, ", "),
	)

	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *agentMessageStore) CountQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) (int64, error) {
	return s.q.CountQueuedAgentMessagesForThread(ctx, toThreadID)
}

// CancelQueuedAgentMessage deletes a mailbox row only if it is still
// queued -- see the CancelQueuedAgentMessage SQL query for why this is a
// conditional DELETE rather than a SELECT-then-DELETE.
func (s *agentMessageStore) CancelQueuedAgentMessage(ctx context.Context, id, chatID string) (bool, error) {
	rowsAffected, err := s.q.CancelQueuedAgentMessage(ctx, pgdb.CancelQueuedAgentMessageParams{
		ID:     id,
		ChatID: chatID,
	})
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (s *agentMessageStore) ClaimQueuedAgentMessagesForThread(ctx context.Context, toThreadID, chatID, messageID string) ([]*core.AgentMessage, error) {
	id := sql.NullString{}
	if messageID != "" {
		id = sql.NullString{String: messageID, Valid: true}
	}

	rows, err := s.q.ClaimQueuedAgentMessagesForThread(ctx, pgdb.ClaimQueuedAgentMessagesForThreadParams{
		ToThreadID: toThreadID,
		ChatID:     chatID,
		ID:         id,
	})
	if err != nil {
		return nil, err
	}

	claimed := make([]*core.AgentMessage, 0, len(rows))
	for _, row := range rows {
		claimed = append(claimed, agentMessageFromPG(row))
	}
	// DELETE ... RETURNING makes no ordering promise — it returns rows in
	// whatever order it deleted them. The caller resends these as
	// conversation turns, where order is the meaning, so it is imposed here
	// rather than left to the plan.
	sort.SliceStable(claimed, func(i, j int) bool {
		return claimed[i].CreatedAt.Before(claimed[j].CreatedAt)
	})
	return claimed, nil
}

// MarkQueuedAgentMessagesUndeliveredForThread resolves a dead thread's
// mailbox -- see the query for why this is conditional on status = 1.
func (s *agentMessageStore) MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error) {
	return s.q.MarkQueuedAgentMessagesUndeliveredForThread(ctx, toThreadID)
}

// ListThreadsWithOrphanedAgentMessages returns threads that are already
// terminal but still carry queued rows -- the reconciler sweep's read half.
func (s *agentMessageStore) ListThreadsWithOrphanedAgentMessages(ctx context.Context) ([]string, error) {
	return s.q.ListThreadsWithOrphanedAgentMessages(ctx)
}

func agentMessageFromPG(row pgdb.AgentMessage) *core.AgentMessage {
	return &core.AgentMessage{
		ID:                 row.ID,
		ChatID:             row.ChatID,
		FromThreadID:       row.FromThreadID,
		ToThreadID:         row.ToThreadID,
		Kind:               core.AgentMessageKind(row.Kind),
		Body:               row.Body,
		Attachments:        agentMessageAttachmentsFromNullRawMessage(row.Attachments),
		ToolCallID:         agentMessageNullStringToPtr(row.ToolCallID),
		Status:             core.AgentMessageStatus(row.Status),
		CreatedAt:          row.CreatedAt,
		DeliveredAt:        agentMessageNullTimeToPtr(row.DeliveredAt),
		DeliveredMessageID: agentMessageNullStringToPtr(row.DeliveredMessageID),
	}
}

// agentMessageAttachmentsToNullRawMessage encodes attachment IDs the same
// way connector_grants.allowed_tools does (json.Marshal into jsonb) -- this
// column is read/written wholesale, never queried by individual element, so
// jsonb needs no array-typed driver support. A nil/empty slice stores as
// SQL NULL rather than "[]": ListQueuedAgentMessagesForThread already reads
// the zero value as "no attachments" everywhere else.
func agentMessageAttachmentsToNullRawMessage(ids []string) (pqtype.NullRawMessage, error) {
	if len(ids) == 0 {
		return pqtype.NullRawMessage{}, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return pqtype.NullRawMessage{}, fmt.Errorf("encode agent message attachments: %w", err)
	}
	return pqtype.NullRawMessage{RawMessage: encoded, Valid: true}, nil
}

func agentMessageAttachmentsFromNullRawMessage(raw pqtype.NullRawMessage) []string {
	if !raw.Valid || len(raw.RawMessage) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw.RawMessage, &ids); err != nil {
		return nil
	}
	return ids
}

func agentMessagePtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func agentMessageNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func agentMessageNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}
