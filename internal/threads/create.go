package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// CreateThread creates a new root thread (no fork) with an initial context window.
// Returns the created Thread and its initial ContextWindow.
func (s *Service) CreateThread(ctx context.Context, opts CreateThreadOpts) (*db.Thread, *db.ContextWindow, error) {
	origin := opts.Origin
	if origin == "" {
		origin = db.ThreadOriginMain
	}
	return s.createThreadInternal(ctx, opts.ChatID, opts.ID, opts.Title, nil, nil, origin, opts.OriginNodeID)
}

// createThreadInternal creates a thread with an initial context window.
// parentThreadID and workflowID are optional internal parameters used by
// CreateWorkflowWithThread to set lineage and workflow atomically.
// origin records HOW the thread was created and is required — it is the field
// readers use to tell a spawn from a graph-node thread, and guessing it after
// the fact is exactly the ambiguity this column exists to remove.
func (s *Service) createThreadInternal(ctx context.Context, chatID, id string, title *string, parentThreadID *string, workflowID *string, origin db.ThreadOrigin, originNodeID *string) (*db.Thread, *db.ContextWindow, error) {
	if chatID == "" {
		return nil, nil, fmt.Errorf("chat ID is required")
	}
	if origin == "" {
		return nil, nil, fmt.Errorf("thread origin is required")
	}

	threadID := generateID(id)

	thread := &db.Thread{
		ID:             threadID,
		ChatID:         chatID,
		ParentThreadID: parentThreadID,
		WorkflowID:     workflowID,
		Title:          title,
		CreatedAt:      now(),
		Origin:         origin,
		OriginNodeID:   originNodeID,
		Status:         db.ThreadStatusRunning,
	}

	createdThread, err := s.repo.CreateThread(ctx, thread)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create thread: %w", err)
	}

	// Create initial context window (sequence 0)
	cw := &db.ContextWindow{
		ID:        contextWindowID(chatID, threadID, 0),
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: now(),
	}

	createdCW, err := s.repo.CreateContextWindow(ctx, cw)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create context window: %w", err)
	}

	return createdThread, createdCW, nil
}

// GetThread retrieves a thread by ID.
func (s *Service) GetThread(ctx context.Context, threadID string) (*db.Thread, error) {
	return s.repo.GetThread(ctx, threadID)
}

// GetContextWindow retrieves a context window by ID.
func (s *Service) GetContextWindow(ctx context.Context, contextWindowID string) (*db.ContextWindow, error) {
	return s.repo.GetContextWindow(ctx, contextWindowID)
}

// GetLatestContextWindow retrieves the latest context window for a thread.
func (s *Service) GetLatestContextWindow(ctx context.Context, threadID string) (*db.ContextWindow, error) {
	return s.repo.GetLatestContextWindow(ctx, threadID)
}

// GetContextWindowBySequence retrieves a context window by thread and sequence.
func (s *Service) GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*db.ContextWindow, error) {
	return s.repo.GetContextWindowBySequence(ctx, threadID, sequence)
}
