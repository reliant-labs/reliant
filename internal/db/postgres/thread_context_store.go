package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type threadStore struct{ q pgdb.Querier }

// NewThreadStore creates the Postgres thread store implementation.
func NewThreadStore(q pgdb.Querier) core.ThreadStore { return &threadStore{q: q} }

func (s *threadStore) CreateThread(ctx context.Context, thread *core.Thread) (*core.Thread, error) {
	result, err := s.q.CreateThread(ctx, threadToCreateParams(thread))
	if err != nil {
		return nil, fmt.Errorf("failed to create thread: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) GetThread(ctx context.Context, id string) (*core.Thread, error) {
	result, err := s.q.GetThread(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) GetThreadByWorkflow(ctx context.Context, workflowID string) (*core.Thread, error) {
	result, err := s.q.GetThreadByWorkflow(ctx, threadStringToNullString(workflowID))
	if err != nil {
		return nil, fmt.Errorf("failed to get thread by workflow: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) GetRootThread(ctx context.Context, conversationID string) (*core.Thread, error) {
	result, err := s.q.GetRootThread(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get root thread: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) GetThreadWithParent(ctx context.Context, id string) (*core.Thread, *string, error) {
	result, err := s.q.GetThreadWithParent(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get thread with parent: %w", err)
	}
	thread := &core.Thread{
		ID:                    result.ID,
		ConversationID:        result.ConversationID,
		ParentThreadID:        nullStringToPtr(result.ParentThreadID),
		ForkAtOrdinal:         threadNullInt64ToPtr(result.ForkAtOrdinal),
		ForkAtContextWindowID: nullStringToPtr(result.ForkAtContextWindowID),
		WorkflowID:            nullStringToPtr(result.WorkflowID),
		Title:                 nullStringToPtr(result.Title),
		CreatedAt:             result.CreatedAt,
	}
	return thread, nullStringToPtr(result.ParentConversationID), nil
}

func (s *threadStore) ListThreadsByConversation(ctx context.Context, conversationID string) ([]*core.Thread, error) {
	results, err := s.q.ListThreadsByConversation(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to list threads: %w", err)
	}
	return threadsFromPG(results), nil
}

func (s *threadStore) ListChildThreads(ctx context.Context, parentThreadID string) ([]*core.Thread, error) {
	results, err := s.q.ListChildThreads(ctx, threadStringToNullString(parentThreadID))
	if err != nil {
		return nil, fmt.Errorf("failed to list child threads: %w", err)
	}
	return threadsFromPG(results), nil
}

func (s *threadStore) UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*core.Thread, error) {
	result, err := s.q.UpdateThreadWorkflow(ctx, pgdb.UpdateThreadWorkflowParams{WorkflowID: threadStringToNullString(workflowID), ID: threadID})
	if err != nil {
		return nil, fmt.Errorf("failed to update thread workflow: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtOrdinal *int64, forkAtContextWindowID *string) (*core.Thread, error) {
	result, err := s.q.UpdateThreadForkPoint(ctx, pgdb.UpdateThreadForkPointParams{
		ForkAtOrdinal:         threadPtrToNullInt64(forkAtOrdinal),
		ForkAtContextWindowID: ptrToNullString(forkAtContextWindowID),
		ID:                    threadID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update thread fork point: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) DeleteThread(ctx context.Context, id string) error {
	return s.q.DeleteThread(ctx, id)
}

func (s *threadStore) DeleteThreadsByConversation(ctx context.Context, conversationID string) error {
	return s.q.DeleteThreadsByConversation(ctx, conversationID)
}

func (s *threadStore) CountThreadsInConversation(ctx context.Context, conversationID string) (int64, error) {
	return s.q.CountThreadsInConversation(ctx, conversationID)
}

type contextWindowStore struct{ q pgdb.Querier }

// NewContextWindowStore creates the Postgres context-window store implementation.
func NewContextWindowStore(q pgdb.Querier) core.ContextWindowStore {
	return &contextWindowStore{q: q}
}

func (s *contextWindowStore) CreateContextWindow(ctx context.Context, cw *core.ContextWindow) (*core.ContextWindow, error) {
	result, err := s.q.CreateContextWindow(ctx, contextWindowToCreateParams(cw))
	if err != nil {
		return nil, fmt.Errorf("failed to create context window: %w", err)
	}
	return contextWindowFromPG(result), nil
}

func (s *contextWindowStore) GetContextWindow(ctx context.Context, id string) (*core.ContextWindow, error) {
	result, err := s.q.GetContextWindow(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get context window: %w", err)
	}
	return contextWindowFromPG(result), nil
}

func (s *contextWindowStore) GetLatestContextWindow(ctx context.Context, threadID string) (*core.ContextWindow, error) {
	result, err := s.q.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest context window: %w", err)
	}
	return contextWindowFromPG(result), nil
}

func (s *contextWindowStore) GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*core.ContextWindow, error) {
	result, err := s.q.GetContextWindowBySequence(ctx, pgdb.GetContextWindowBySequenceParams{ThreadID: threadID, Sequence: int64(sequence)})
	if err != nil {
		return nil, fmt.Errorf("failed to get context window by sequence: %w", err)
	}
	return contextWindowFromPG(result), nil
}

func (s *contextWindowStore) GetContextWindowWithThread(ctx context.Context, id string) (*core.ContextWindow, string, *string, *int64, error) {
	result, err := s.q.GetContextWindowWithThread(ctx, id)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("failed to get context window with thread: %w", err)
	}
	cw := &core.ContextWindow{
		ID:                         result.ID,
		ThreadID:                   result.ThreadID,
		Sequence:                   int(result.Sequence),
		ParentContextWindowID:      nullStringToPtr(result.ParentContextWindowID),
		ForkAtOrdinal:              threadNullInt64ToPtr(result.ForkAtOrdinal),
		CompactionSummaryMessageID: nullStringToPtr(result.CompactionSummaryMessageID),
		CreatedAt:                  result.CreatedAt,
	}
	return cw, result.ConversationID, nullStringToPtr(result.ParentThreadID), threadNullInt64ToPtr(result.ForkAtOrdinal_2), nil
}

func (s *contextWindowStore) ListContextWindowsByThread(ctx context.Context, threadID string) ([]*core.ContextWindow, error) {
	results, err := s.q.ListContextWindowsByThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list context windows: %w", err)
	}
	return contextWindowsFromPG(results), nil
}

func (s *contextWindowStore) GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error) {
	result, err := s.q.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("failed to get max sequence: %w", err)
	}
	switch v := result.(type) {
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	default:
		return 0, nil
	}
}

func (s *contextWindowStore) SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*core.ContextWindow, error) {
	result, err := s.q.SetCompactionSummaryMessage(ctx, pgdb.SetCompactionSummaryMessageParams{
		CompactionSummaryMessageID: threadStringToNullString(messageID),
		ID:                         cwID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set compaction summary message: %w", err)
	}
	return contextWindowFromPG(result), nil
}

func (s *contextWindowStore) DeleteContextWindow(ctx context.Context, id string) error {
	return s.q.DeleteContextWindow(ctx, id)
}

func (s *contextWindowStore) DeleteContextWindowsByThread(ctx context.Context, threadID string) error {
	return s.q.DeleteContextWindowsByThread(ctx, threadID)
}

func threadFromPG(st pgdb.Thread) *core.Thread {
	return &core.Thread{
		ID:                    st.ID,
		ConversationID:        st.ConversationID,
		ParentThreadID:        nullStringToPtr(st.ParentThreadID),
		ForkAtOrdinal:         threadNullInt64ToPtr(st.ForkAtOrdinal),
		ForkAtContextWindowID: nullStringToPtr(st.ForkAtContextWindowID),
		WorkflowID:            nullStringToPtr(st.WorkflowID),
		Title:                 nullStringToPtr(st.Title),
		CreatedAt:             st.CreatedAt,
	}
}

func threadsFromPG(rows []pgdb.Thread) []*core.Thread {
	threads := make([]*core.Thread, len(rows))
	for i, row := range rows {
		threads[i] = threadFromPG(row)
	}
	return threads
}

func threadToCreateParams(t *core.Thread) pgdb.CreateThreadParams {
	return pgdb.CreateThreadParams{
		ID:                    t.ID,
		ConversationID:        t.ConversationID,
		ParentThreadID:        ptrToNullString(t.ParentThreadID),
		ForkAtOrdinal:         threadPtrToNullInt64(t.ForkAtOrdinal),
		ForkAtContextWindowID: ptrToNullString(t.ForkAtContextWindowID),
		WorkflowID:            ptrToNullString(t.WorkflowID),
		Title:                 ptrToNullString(t.Title),
		CreatedAt:             t.CreatedAt,
	}
}

func contextWindowFromPG(cw pgdb.ContextWindow) *core.ContextWindow {
	return &core.ContextWindow{
		ID:                         cw.ID,
		ThreadID:                   cw.ThreadID,
		Sequence:                   int(cw.Sequence),
		ParentContextWindowID:      nullStringToPtr(cw.ParentContextWindowID),
		ForkAtOrdinal:              threadNullInt64ToPtr(cw.ForkAtOrdinal),
		CompactionSummaryMessageID: nullStringToPtr(cw.CompactionSummaryMessageID),
		CreatedAt:                  cw.CreatedAt,
	}
}

func contextWindowsFromPG(rows []pgdb.ContextWindow) []*core.ContextWindow {
	items := make([]*core.ContextWindow, len(rows))
	for i, row := range rows {
		items[i] = contextWindowFromPG(row)
	}
	return items
}

func contextWindowToCreateParams(cw *core.ContextWindow) pgdb.CreateContextWindowParams {
	return pgdb.CreateContextWindowParams{
		ID:                         cw.ID,
		ThreadID:                   cw.ThreadID,
		Sequence:                   int64(cw.Sequence),
		ParentContextWindowID:      ptrToNullString(cw.ParentContextWindowID),
		ForkAtOrdinal:              threadPtrToNullInt64(cw.ForkAtOrdinal),
		CompactionSummaryMessageID: ptrToNullString(cw.CompactionSummaryMessageID),
		CreatedAt:                  cw.CreatedAt,
	}
}

func threadStringToNullString(s string) sql.NullString {
	if s != "" {
		return sql.NullString{String: s, Valid: true}
	}
	return sql.NullString{}
}

func threadPtrToNullInt64(v *int64) sql.NullInt64 {
	if v != nil {
		return sql.NullInt64{Int64: *v, Valid: true}
	}
	return sql.NullInt64{}
}

func threadNullInt64ToPtr(v sql.NullInt64) *int64 {
	if v.Valid {
		return &v.Int64
	}
	return nil
}
