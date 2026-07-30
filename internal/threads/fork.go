package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ForkThread creates a forked thread from a parent thread.
// The new thread inherits the sequence from the parent's ForkAtContextWindowID.
// Returns the created Thread and its initial ContextWindow.
func (s *Service) ForkThread(ctx context.Context, opts ForkThreadOpts) (*db.Thread, *db.ContextWindow, error) {
	return s.forkThreadInternal(ctx, opts, nil)
}

// forkThreadInternal is the internal implementation that accepts an optional workflowID.
// This is used by CreateWorkflowWithThread to set the workflow_id atomically.
func (s *Service) forkThreadInternal(ctx context.Context, opts ForkThreadOpts, workflowID *string) (*db.Thread, *db.ContextWindow, error) {
	if opts.ConversationID == "" {
		return nil, nil, fmt.Errorf("conversation ID is required")
	}
	if opts.ParentThreadID == "" {
		return nil, nil, fmt.Errorf("parent thread ID is required")
	}
	if opts.ForkAtContextWindowID == "" {
		return nil, nil, fmt.Errorf("fork at context window ID is required")
	}

	// Verify parent thread exists. "not found" only for a genuine missing
	// row — infrastructure errors (serialization conflict, aborted tx) keep
	// their own message so retry classification upstream sees them for what
	// they are instead of a terminal-looking not-found.
	_, err := s.repo.GetThread(ctx, opts.ParentThreadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("parent thread not found: %w", err)
		}
		return nil, nil, fmt.Errorf("failed to load parent thread: %w", err)
	}

	// Get the parent's context window to inherit the sequence
	parentCW, err := s.repo.GetContextWindow(ctx, opts.ForkAtContextWindowID)
	if err != nil {
		return nil, nil, fmt.Errorf("fork context window not found: %w", err)
	}

	threadID := generateID(opts.ID)

	// Create thread with fork metadata
	thread := &db.Thread{
		ID:                    threadID,
		ConversationID:        opts.ConversationID,
		ParentThreadID:        &opts.ParentThreadID,
		ForkAtOrdinal:         &opts.ForkAtOrdinal,
		ForkAtContextWindowID: &opts.ForkAtContextWindowID,
		WorkflowID:            workflowID,
		Title:                 opts.Title,
		CreatedAt:             now(),
		Origin:                db.ThreadOriginFork,
		Status:                db.ThreadStatusRunning,
	}

	createdThread, err := s.repo.CreateThread(ctx, thread)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create thread: %w", err)
	}

	// Create context window with CW chain linking:
	// - ParentContextWindowID: Links to the source CW we're forking from
	// - ForkAtOrdinal: The max ordinal to inherit from the parent CW
	// - Sequence: Inherited from parent (forked threads share the same context sequence)
	cw := &db.ContextWindow{
		ID:                    contextWindowID(opts.ConversationID, threadID, parentCW.Sequence),
		ThreadID:              threadID,
		Sequence:              parentCW.Sequence,
		ParentContextWindowID: &opts.ForkAtContextWindowID,
		ForkAtOrdinal:         &opts.ForkAtOrdinal,
		CreatedAt:             now(),
	}

	createdCW, err := s.repo.CreateContextWindow(ctx, cw)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create context window: %w", err)
	}

	// FORK-DEBUG: Log forked thread and context window creation
	logging.Info("[FORK-DEBUG] forkThreadInternal created forked thread and CW",
		"threadID", createdThread.ID,
		"parentThreadID", opts.ParentThreadID,
		"forkAtOrdinal", opts.ForkAtOrdinal,
		"contextWindowID", createdCW.ID,
		"parentContextWindowID", *cw.ParentContextWindowID,
		"cwForkAtOrdinal", *cw.ForkAtOrdinal,
		"cwSequence", cw.Sequence)
	return createdThread, createdCW, nil
}

// forkChainVisitor is called for each thread in the fork chain.
// Returns true to continue traversal, false to stop.
type forkChainVisitor func(thread *db.Thread, cw *db.ContextWindow) (bool, error)

// walkForkChain traverses the fork chain from a thread up to its ancestors.
// It calls the visitor function for each thread in the chain.
// Traversal stops at compaction boundaries (when context window sequence changes).
//
// Parameters:
//   - threadID: Starting thread
//   - contextWindowID: Specific context window (nil = latest)
//   - visitor: Function called for each thread (current -> parent -> grandparent, etc.)
//
// The visitor receives:
//   - thread: The current thread in the chain
//   - cw: The context window being resolved for this thread
//
// Traversal stops when:
//   - visitor returns false
//   - thread has no parent
//   - compaction boundary is hit (sequence > 0 and different from starting sequence)
//   - self-referential fork is detected
func (s *Service) walkForkChain(ctx context.Context, threadID string, contextWindowID *string, visitor forkChainVisitor) error {
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}

	// Track visited threads to prevent infinite loops
	visited := make(map[string]bool)
	startingSequence := -1 // Will be set from first context window

	currentThreadID := threadID
	currentCWID := contextWindowID

	for {
		// Guard against infinite loops
		if visited[currentThreadID] {
			return fmt.Errorf("circular fork chain detected at thread %s", currentThreadID)
		}
		visited[currentThreadID] = true

		// Load current thread
		thread, _, err := s.repo.GetThreadWithParent(ctx, currentThreadID)
		if err != nil {
			return fmt.Errorf("failed to get thread %s: %w", currentThreadID, err)
		}

		// Get context window
		var cw *db.ContextWindow
		if currentCWID != nil {
			cw, err = s.repo.GetContextWindow(ctx, *currentCWID)
			if err != nil {
				return fmt.Errorf("failed to get context window %s: %w", *currentCWID, err)
			}
		} else {
			cw, err = s.repo.GetLatestContextWindow(ctx, currentThreadID)
			if err != nil {
				// No context window yet - thread has no messages
				cw = nil
			}
		}

		// Set starting sequence from first context window
		if startingSequence < 0 && cw != nil {
			startingSequence = cw.Sequence
		}

		// Check compaction boundary: if we're past the first thread and
		// the context window sequence differs from starting sequence,
		// stop traversal because the compaction summary already contains
		// all inherited context
		if len(visited) > 1 && cw != nil && startingSequence >= 0 && cw.Sequence != startingSequence {
			// Compaction boundary - stop traversal
			return nil
		}

		// Call visitor
		shouldContinue, err := visitor(thread, cw)
		if err != nil {
			return err
		}
		if !shouldContinue {
			return nil
		}

		// Check if we should continue to parent
		if thread.ParentThreadID == nil {
			// No parent - end of chain
			return nil
		}

		// Guard against self-referential forks
		if *thread.ParentThreadID == currentThreadID {
			return fmt.Errorf("self-referential fork detected at thread %s", currentThreadID)
		}

		// Move to parent
		currentThreadID = *thread.ParentThreadID
		currentCWID = thread.ForkAtContextWindowID
	}
}
