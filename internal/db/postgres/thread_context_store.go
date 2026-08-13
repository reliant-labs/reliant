package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

func (s *threadStore) GetRootThread(ctx context.Context, chatID string) (*core.Thread, error) {
	result, err := s.q.GetRootThread(ctx, chatID)
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
		ID:              result.ID,
		ChatID:          result.ChatID,
		ParentThreadID:  nullStringToPtr(result.ParentThreadID),
		ForkAtMessageID: nullStringToPtr(result.ForkAtMessageID),
		WorkflowID:      nullStringToPtr(result.WorkflowID),
		Title:           nullStringToPtr(result.Title),
		CreatedAt:       result.CreatedAt,
		Origin:          result.Origin,
		OriginNodeID:    nullStringToPtr(result.OriginNodeID),
		Status:          result.Status,
		CompletedAt:     threadNullTimeToPtr(result.CompletedAt),
	}
	return thread, nullStringToPtr(result.ParentChatID), nil
}

func (s *threadStore) UpdateThreadStatus(ctx context.Context, threadID string, status int32, completedAt *time.Time) (*core.Thread, error) {
	result, err := s.q.UpdateThreadStatus(ctx, pgdb.UpdateThreadStatusParams{
		ID:          threadID,
		Status:      status,
		CompletedAt: threadPtrToNullTime(completedAt),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update thread status: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) ReviveThread(ctx context.Context, threadID string) (int64, error) {
	return s.q.ReviveThread(ctx, threadID)
}

func (s *threadStore) CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, status int32) error {
	return s.q.CascadeTerminalStatusToThreadSubtree(ctx, pgdb.CascadeTerminalStatusToThreadSubtreeParams{
		WorkflowID: workflowID,
		Status:     status,
	})
}

func (s *threadStore) ReapOrphanedThreads(ctx context.Context) (int64, error) {
	return s.q.ReapOrphanedThreads(ctx)
}

func (s *threadStore) ListThreadsByOrigin(ctx context.Context, chatID string, origin core.ThreadOrigin) ([]*core.Thread, error) {
	results, err := s.q.ListThreadsByOrigin(ctx, pgdb.ListThreadsByOriginParams{
		ChatID: chatID,
		Origin: origin,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list threads by origin: %w", err)
	}
	return threadsFromPG(results), nil
}

func (s *threadStore) ListThreadsByConversation(ctx context.Context, chatID string) ([]*core.Thread, error) {
	results, err := s.q.ListThreadsByConversation(ctx, chatID)
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

func (s *threadStore) UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtMessageID *string) (*core.Thread, error) {
	result, err := s.q.UpdateThreadForkPoint(ctx, pgdb.UpdateThreadForkPointParams{
		ForkAtMessageID: ptrToNullString(forkAtMessageID),
		ID:              threadID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update thread fork point: %w", err)
	}
	return threadFromPG(result), nil
}

func (s *threadStore) DeleteThread(ctx context.Context, id string) error {
	return s.q.DeleteThread(ctx, id)
}

func (s *threadStore) DeleteThreadsByConversation(ctx context.Context, chatID string) error {
	return s.q.DeleteThreadsByConversation(ctx, chatID)
}

func (s *threadStore) CountThreadsInConversation(ctx context.Context, chatID string) (int64, error) {
	return s.q.CountThreadsInConversation(ctx, chatID)
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

func (s *contextWindowStore) GetContextWindowWithThread(ctx context.Context, id string) (*core.ContextWindow, string, *string, *string, error) {
	result, err := s.q.GetContextWindowWithThread(ctx, id)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("failed to get context window with thread: %w", err)
	}
	cw := &core.ContextWindow{
		ID:                         result.ID,
		ThreadID:                   result.ThreadID,
		Sequence:                   int(result.Sequence),
		ParentContextWindowID:      nullStringToPtr(result.ParentContextWindowID),
		ForkAtMessageID:            nullStringToPtr(result.ForkAtMessageID),
		CompactionSummaryMessageID: nullStringToPtr(result.CompactionSummaryMessageID),
		CreatedAt:                  result.CreatedAt,
	}
	return cw, result.ChatID, nullStringToPtr(result.ParentThreadID), nullStringToPtr(result.ForkAtMessageID_2), nil
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
		ID:              st.ID,
		ChatID:          st.ChatID,
		ParentThreadID:  nullStringToPtr(st.ParentThreadID),
		ForkAtMessageID: nullStringToPtr(st.ForkAtMessageID),
		WorkflowID:      nullStringToPtr(st.WorkflowID),
		Title:           nullStringToPtr(st.Title),
		CreatedAt:       st.CreatedAt,
		Origin:          st.Origin,
		OriginNodeID:    nullStringToPtr(st.OriginNodeID),
		Status:          st.Status,
		CompletedAt:     threadNullTimeToPtr(st.CompletedAt),
	}
}

func threadPtrToNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func threadNullTimeToPtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	return &nt.Time
}

func threadsFromPG(rows []pgdb.Thread) []*core.Thread {
	threads := make([]*core.Thread, len(rows))
	for i, row := range rows {
		threads[i] = threadFromPG(row)
	}
	return threads
}

func threadToCreateParams(t *core.Thread) pgdb.CreateThreadParams {
	origin := t.Origin
	if origin == "" {
		// origin is NOT NULL. A caller that forgot to set it is a bug, but
		// defaulting is better than a constraint violation at the driver: a
		// thread with a parent is far more likely a spawn than a root.
		origin = core.ThreadOriginNode
		if t.ParentThreadID == nil {
			origin = core.ThreadOriginMain
		}
	}
	status := t.Status
	if status == 0 {
		status = core.ThreadStatusRunning
	}
	return pgdb.CreateThreadParams{
		ID:              t.ID,
		ChatID:          t.ChatID,
		ParentThreadID:  ptrToNullString(t.ParentThreadID),
		ForkAtMessageID: ptrToNullString(t.ForkAtMessageID),
		WorkflowID:      ptrToNullString(t.WorkflowID),
		Title:           ptrToNullString(t.Title),
		CreatedAt:       t.CreatedAt,
		Origin:          origin,
		OriginNodeID:    ptrToNullString(t.OriginNodeID),
		Status:          status,
	}
}

func contextWindowFromPG(cw pgdb.ContextWindow) *core.ContextWindow {
	return &core.ContextWindow{
		ID:                         cw.ID,
		ThreadID:                   cw.ThreadID,
		Sequence:                   int(cw.Sequence),
		ParentContextWindowID:      nullStringToPtr(cw.ParentContextWindowID),
		ForkAtMessageID:            nullStringToPtr(cw.ForkAtMessageID),
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
		ForkAtMessageID:            ptrToNullString(cw.ForkAtMessageID),
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
