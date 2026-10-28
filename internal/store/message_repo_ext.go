// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Message: the message reads and writes of
// internal/db's Repository (P5b lane L1a). Every function
// takes and returns internal/db/core types; the generated Message row stays
// inside this package (internal/db/SCHEMA.md, "Porting onto the forge ORM").
//
// The building blocks, in the order SCHEMA.md prefers them: the generated
// delegates (CreateMessage, GetMessageByID, ListMessage, CountMessage, …)
// with orm.QueryOption filters; the bun builder off db.Bun() where a
// predicate or a join is composed; and NewRaw for the statements whose value
// is that they are one hand-tuned statement (the transcript windows,
// SaveMessageAtomic). Every function takes no transaction parameter: called
// with a ctx that carries a transaction (Repo.RunTx or orm RunTx), it joins
// it.
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertMessage writes msg as a new row. The caller allocates ordinal and
// seq; a collision on either is a unique violation the caller's transaction
// retries.
//
// The row carries the ordinal and seq the caller chose, so it first raises
// the thread's ordinal counter and the chat's seq counter to them
// (message_order_counter_repo_ext.go): a counter is never behind a row. Run
// it in the transaction that allocated the numbers, or in one of its own.
func InsertMessage(ctx context.Context, db orm.Context, msg *core.Message) error {
	if err := raiseMessageOrder(ctx, db, msg); err != nil {
		return err
	}
	return CreateMessage(ctx, db, messageRow(msg))
}

// raiseMessageOrder raises msg's thread ordinal and chat seq counters to the
// numbers msg carries.
func raiseMessageOrder(ctx context.Context, db orm.Context, msg *core.Message) error {
	if err := raiseCounter(ctx, db, CounterOrdinal, msg.ThreadID, msg.Ordinal); err != nil {
		return err
	}
	return raiseCounter(ctx, db, CounterSeq, msg.ChatID, msg.Seq)
}

// InsertMessageOnce writes msg unless a row already holds its id, and
// reports which happened. ON CONFLICT (id) DO NOTHING is the arbiter between
// two writers of one message (a client-named send repeated, an activity
// retried): exactly one inserts. Only the id conflict is absorbed; any other
// unique violation (messages_chat_seq_key: two different writes racing for
// one seq) still raises, and the caller's transaction retries. The row is
// written exactly as given — created_at and updated_at included.
//
// Like InsertMessage it raises the counters to the row's numbers (before the
// insert: a raise for a row the conflict then absorbed is harmless, the
// numbers were the caller's to spend).
func InsertMessageOnce(ctx context.Context, db orm.Context, msg *core.Message) (bool, error) {
	if err := raiseMessageOrder(ctx, db, msg); err != nil {
		return false, err
	}
	res, err := db.Bun().NewInsert().Model(messageRow(msg)).On("CONFLICT (id) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindMessage reads one message by id, or nil when no row holds it.
func FindMessage(ctx context.Context, db orm.Context, id string) (*core.Message, error) {
	row, err := GetMessageByID(ctx, db, id)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	return messageFromRow(row), nil
}

// FindMessageByActivity reads the message a Temporal activity wrote into a
// chat — the idempotency key of an activity retry — or nil. workflowID
// narrows it to one workflow's activity ("" means any): child workflows of
// one chat reuse activity ids.
func FindMessageByActivity(ctx context.Context, db orm.Context, chatID, workflowID, activityID string) (*core.Message, error) {
	opts := []orm.QueryOption{
		orm.WhereEq(MessageFieldChatID, chatID),
		orm.WhereEq(MessageFieldActivityID, activityID),
	}
	if workflowID != "" {
		opts = append(opts, orm.WhereEq(MessageFieldWorkflowID, workflowID))
	}
	return firstMessage(ListMessage(ctx, db, append(opts, orm.WithLimit(1))...))
}

// ListMessages reads a chat's messages in seq order, narrowed by opts: one
// context window (ContextWindowID, which wins), one thread at one context
// sequence (Thread + ContextSequence), or every context window of one thread
// (Thread). Offset and Limit page the result; a zero Limit is unbounded.
func ListMessages(ctx context.Context, db orm.Context, chatID string, opts core.MessageListOptions) ([]*core.Message, error) {
	var rows []*Message
	q := db.Bun().NewSelect().Model(&rows)
	switch {
	case opts.ContextWindowID != nil:
		q = q.Where("messages.context_window_id = ?", *opts.ContextWindowID)
	case opts.Thread != nil && opts.ContextSequence != nil:
		q = q.Join("JOIN context_windows AS cw ON cw.id = messages.context_window_id").
			Where("cw.thread_id = ?", *opts.Thread).
			Where("cw.sequence = ?", int64(*opts.ContextSequence))
	default:
		q = q.Where("messages.chat_id = ?", chatID)
		if opts.Thread != nil {
			q = q.Where("messages.context_window_id IN (?)",
				db.Bun().NewSelect().Model((*ContextWindow)(nil)).Column("id").Where("thread_id = ?", *opts.Thread))
		}
	}
	q = q.OrderExpr("messages.seq ASC")
	if opts.Offset > 0 {
		q = q.Offset(int64(opts.Offset))
	}
	if opts.Limit > 0 {
		q = q.Limit(int64(opts.Limit))
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	return messagesFromRows(rows), nil
}

// transcriptSiblingMessages selects the messages of a chat that belong to
// neither the main thread (read separately, through its context-window chain)
// nor any spawn thread, at or above fromSeq.
//
// Spawn threads are excluded outright. A spawn renders as one tool-call card
// in its parent's transcript, and the card reads its child thread when it is
// expanded (ListMessages with thread_id), so spawn rows in a transcript page
// are never rendered from. They were also most of it: spawn threads out-write
// the main thread by an order of magnitude (636KB of a 2.3MB snapshot on a
// real 55k-message chat). What remains are the threads the transcript renders
// inline (workflow-node and fork threads).
func transcriptSiblingMessages(db orm.Context, chatID, mainThreadID string, fromSeq int64, rows *[]*Message) *bun.SelectQuery {
	spawnThreads := db.Bun().NewSelect().Model((*Thread)(nil)).Column("id").
		Where("chat_id = ?", chatID).
		Where("origin = ?", "spawn")
	return db.Bun().NewSelect().Model(rows).
		Where("chat_id = ?", chatID).
		Where("thread_id <> ?", mainThreadID).
		Where("seq >= ?", fromSeq).
		Where("thread_id NOT IN (?)", spawnThreads)
}

// ListRecentTranscriptSiblingMessages is the chat snapshot's sibling-thread
// rows, ascending: the newest limit messages at or above fromSeq that belong
// to neither the main thread nor any spawn thread (see
// transcriptSiblingMessages), bounded so a node-heavy chat cannot balloon the
// payload. Read DESC, against messages_chat_seq_key walked backward, so the
// LIMIT keeps the newest rows.
func ListRecentTranscriptSiblingMessages(ctx context.Context, db orm.Context, chatID, mainThreadID string, fromSeq int64, limit int) ([]*core.Message, error) {
	if limit <= 0 {
		return []*core.Message{}, nil
	}
	var rows []*Message
	err := transcriptSiblingMessages(db, chatID, mainThreadID, fromSeq, &rows).
		OrderExpr("seq DESC").
		Limit(int64(limit)).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list recent transcript sibling messages: %w", err)
	}
	slices.Reverse(rows)
	return messagesFromRows(rows), nil
}

// ListTranscriptSiblingMessagesInRange is the same sibling set as
// ListRecentTranscriptSiblingMessages, for a ListMessages page: every such
// message with seq >= fromSeq and, when toSeq is set, seq < toSeq, ascending.
// A page whose main-thread window bounds the range passes limit 0: capping
// inside that range would lose siblings no later page returns (the next cursor
// is the page's oldest seq). A page with no main-thread window passes a limit,
// and gets the newest limit rows of the range.
func ListTranscriptSiblingMessagesInRange(ctx context.Context, db orm.Context, chatID, mainThreadID string, fromSeq int64, toSeq *int64, limit int) ([]*core.Message, error) {
	var rows []*Message
	q := transcriptSiblingMessages(db, chatID, mainThreadID, fromSeq, &rows)
	if toSeq != nil {
		q = q.Where("seq < ?", *toSeq)
	}
	if limit > 0 {
		q = q.OrderExpr("seq DESC").Limit(int64(limit))
	} else {
		q = q.OrderExpr("seq ASC")
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("failed to list transcript sibling messages in range: %w", err)
	}
	if limit > 0 {
		slices.Reverse(rows)
	}
	return messagesFromRows(rows), nil
}

// LatestMessageInThread is the thread's newest message by seq, or nil.
func LatestMessageInThread(ctx context.Context, db orm.Context, threadID string) (*core.Message, error) {
	return firstMessage(ListMessage(ctx, db,
		orm.WhereEq(MessageFieldThreadID, threadID),
		orm.WithOrderBy(MessageFieldSeq, orm.Desc),
		orm.WithLimit(1)))
}

// LatestTurnInThread is the thread's newest TURN — its newest message that
// is not a system note — or nil. Notes the server posts about a run, and the
// hidden "params changed" note a send can write after the user's message,
// are not turns, so they never make an unanswered message look answered.
func LatestTurnInThread(ctx context.Context, db orm.Context, threadID string) (*core.Message, error) {
	return firstMessage(ListMessage(ctx, db,
		orm.WhereEq(MessageFieldThreadID, threadID),
		orm.WhereNotEq(MessageFieldRole, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		orm.WithOrderBy(MessageFieldSeq, orm.Desc),
		orm.WithLimit(1)))
}

// LatestMessageWithTokens is the newest message carrying token data in the
// thread's context window at contextSequence, at or before maxSeq when it is
// set, or nil. Its token_count is the context size the LLM last reported,
// which is how the thread's token count is read; maxSeq is a fork cursor
// (the fork message's seq), bounding how much of the parent's history a fork
// inherited.
func LatestMessageWithTokens(ctx context.Context, db orm.Context, threadID string, contextSequence int64, maxSeq *int64) (*core.Message, error) {
	row := new(Message)
	q := db.Bun().NewSelect().Model(row).
		Join("JOIN context_windows AS cw ON cw.id = messages.context_window_id").
		Where("cw.thread_id = ?", threadID).
		Where("cw.sequence = ?", contextSequence).
		Where("messages.token_count IS NOT NULL")
	if maxSeq != nil {
		q = q.Where("messages.seq <= ?", *maxSeq)
	}
	err := q.OrderExpr("messages.seq DESC").Limit(1).Scan(ctx)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest message with tokens: %w", err)
	}
	return messageFromRow(row), nil
}

// ListRecentMessagesInContextWindowBeforeSeq is the newest limit messages of
// one context window strictly before beforeSeq (0 means unbounded: the newest
// page), ascending. Bounded in SQL for the common (unforked) case instead of
// fetching the whole context window.
func ListRecentMessagesInContextWindowBeforeSeq(ctx context.Context, db orm.Context, contextWindowID string, beforeSeq int64, limit int) ([]*core.Message, error) {
	if limit <= 0 {
		return []*core.Message{}, nil
	}
	opts := []orm.QueryOption{orm.WhereEq(MessageFieldContextWindowID, contextWindowID)}
	if beforeSeq != 0 {
		opts = append(opts, orm.WhereLt(MessageFieldSeq, beforeSeq))
	}
	rows, err := ListMessage(ctx, db, append(opts, orm.WithOrderBy(MessageFieldSeq, orm.Desc), orm.WithLimit(limit))...)
	if err != nil {
		return nil, fmt.Errorf("failed to list recent messages in context window before seq: %w", err)
	}
	slices.Reverse(rows)
	return messagesFromRows(rows), nil
}

// CountMessagesInChatOutsideThread is how many messages of the chat sit on
// threads other than threadID: with a thread's own resolved count, the chat's
// total, in one COUNT instead of a chat-wide count minus a thread count.
func CountMessagesInChatOutsideThread(ctx context.Context, db orm.Context, chatID, threadID string) (int, error) {
	n, err := db.Bun().NewSelect().Model((*Message)(nil)).
		Where("chat_id = ?", chatID).
		Where("thread_id <> ?", threadID).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages outside thread: %w", err)
	}
	return int(n), nil
}

// UpdateMessageUsage records what a message cost: the context size the LLM
// reported (token_count) and its price. A message that does not exist is
// not an error.
func UpdateMessageUsage(ctx context.Context, db orm.Context, id string, tokenCount *int, cost *float64) error {
	row := &Message{ID: id, TokenCount: intToInt64Ptr(tokenCount), Cost: cost}
	err := UpdateMessageMasked(ctx, db, row, []string{MessageFieldTokenCount, MessageFieldCost}, crud.NoReadBack())
	if errors.Is(err, orm.ErrNoRows) {
		return nil
	}
	return err
}

// RemoveMessage deletes one message; its content blocks go with it (ON
// DELETE CASCADE). A message that does not exist is not an error.
func RemoveMessage(ctx context.Context, db orm.Context, id string) error {
	err := DeleteMessage(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil
	}
	return err
}

// LastThreadActivityByChat maps every thread of a chat to the timestamp of
// its newest message: messages are the only durable per-thread progress
// evidence the schema has (step executions and checkpoints exist for the
// ROOT run only), so this is how a quiet thread is told from a working one.
func LastThreadActivityByChat(ctx context.Context, db orm.Context, chatID string) (map[string]time.Time, error) {
	var rows []struct {
		ThreadID string    `bun:"thread_id"`
		Last     time.Time `bun:"last"`
	}
	err := db.Bun().NewSelect().Model((*Message)(nil)).
		Column("thread_id").
		ColumnExpr("MAX(created_at) AS last").
		Where("chat_id = ?", chatID).
		Group("thread_id").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to read thread activity: %w", err)
	}
	activity := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		activity[r.ThreadID] = r.Last
	}
	return activity, nil
}

// isMissing reports a read or write that found no row: sql.ErrNoRows from a
// scan or a zero-row write, or the generated Get's svcerr.NotFound.
func isMissing(err error) bool {
	return errors.Is(err, orm.ErrNoRows) || svcerr.IsNotFound(err)
}

// firstMessage maps the first row of a ListMessage result, or nil.
func firstMessage(rows []*Message, err error) (*core.Message, error) {
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return messageFromRow(rows[0]), nil
}

// messageRow is msg as a messages row. Free text is made valid UTF-8 on the
// way in (see validUTF8).
func messageRow(msg *core.Message) *Message {
	return &Message{
		ID:                msg.ID,
		ChatID:            msg.ChatID,
		Ordinal:           msg.Ordinal,
		Seq:               msg.Seq,
		ThreadID:          msg.ThreadID,
		ContextWindowID:   msg.ContextWindowID,
		Role:              msg.Role,
		DisplayStyle:      msg.DisplayStyle,
		Model:             validUTF8Ptr(msg.Model),
		Agent:             validUTF8Ptr(msg.Agent),
		TokenCount:        intToInt64Ptr(msg.TokenCount),
		Cost:              msg.Cost,
		WorkflowID:        validUTF8Ptr(msg.WorkflowID),
		RunID:             validUTF8Ptr(msg.RunID),
		NodeID:            validUTF8Ptr(msg.NodeID),
		NodePath:          validUTF8Ptr(msg.NodePath),
		ActivityID:        validUTF8Ptr(msg.ActivityID),
		ProviderAccountID: msg.ProviderAccountID,
		SenderThreadID:    msg.SenderThreadID,
		SenderKind:        msg.SenderKind,
		CreatedAt:         msg.CreatedAt,
		UpdatedAt:         msg.UpdatedAt,
	}
}

func messageFromRow(row *Message) *core.Message {
	return &core.Message{
		ID:                row.ID,
		ChatID:            row.ChatID,
		Ordinal:           row.Ordinal,
		Seq:               row.Seq,
		ThreadID:          row.ThreadID,
		ContextWindowID:   row.ContextWindowID,
		Role:              row.Role,
		DisplayStyle:      row.DisplayStyle,
		Model:             row.Model,
		Agent:             row.Agent,
		TokenCount:        int64ToIntPtr(row.TokenCount),
		Cost:              row.Cost,
		WorkflowID:        row.WorkflowID,
		RunID:             row.RunID,
		NodeID:            row.NodeID,
		NodePath:          row.NodePath,
		ActivityID:        row.ActivityID,
		ProviderAccountID: row.ProviderAccountID,
		SenderThreadID:    row.SenderThreadID,
		SenderKind:        row.SenderKind,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func messagesFromRows(rows []*Message) []*core.Message {
	out := make([]*core.Message, len(rows))
	for i, row := range rows {
		out[i] = messageFromRow(row)
	}
	return out
}

// validUTF8 protects the database boundary from raw subprocess and provider
// bytes that reached a Go string without being valid text. PostgreSQL text
// rejects malformed UTF-8 and the NUL byte (valid UTF-8, but not storable in a
// text column: "invalid byte sequence for encoding \"UTF8\": 0x00"), so one
// bad byte must not make an entire message write fail — in prod a NUL in a
// tool result exhausted a sub-agent's save_message retries and ended the
// agent (2026-10-19). Both become U+FFFD. Storable text returns unchanged.
func validUTF8(text string) string {
	if storableText(text) {
		return text
	}
	return strings.ReplaceAll(strings.ToValidUTF8(text, "\uFFFD"), "\x00", "\uFFFD")
}

// validUTF8Ptr is validUTF8 through a pointer, keeping a storable value's
// pointer as it is.
func validUTF8Ptr(text *string) *string {
	if text == nil || storableText(*text) {
		return text
	}
	v := validUTF8(*text)
	return &v
}

// storableText reports whether PostgreSQL text accepts text as it is.
func storableText(text string) bool {
	return utf8.ValidString(text) && strings.IndexByte(text, 0) < 0
}

func intToInt64Ptr(i *int) *int64 {
	if i == nil {
		return nil
	}
	v := int64(*i)
	return &v
}

func int64ToIntPtr(i *int64) *int {
	if i == nil {
		return nil
	}
	v := int(*i)
	return &v
}

// MessageExtra carries fields on Message that are NOT columns — computed
// values, transient state, anything derived rather than stored. Message is
// embedded on MessageExtra (see message_orm.go), so a field added here is a field on
// every Message value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type MessageExtra struct {
}
