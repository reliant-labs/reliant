package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// Compact creates a new context window with an incremented sequence number.
// This is called after generating a compaction summary to mark the boundary
// between old (summarized) context and new context.
//
// The new context window:
// - Has sequence = current sequence + 1
// - Links to the summary message via CompactionSummaryMessageID
// - Links to the previous CW via ParentContextWindowID
// - Has ForkAtMessageID = nil (compaction is not a branch)
//
// After compaction, message resolution will stop at this CW because
// CompactionSummaryMessageID is set - the summary already contains all prior context.
func (s *Service) Compact(ctx context.Context, threadID string, summaryMessageID string) (*db.ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID is required")
	}
	if summaryMessageID == "" {
		return nil, fmt.Errorf("summary message ID is required")
	}

	// Get thread to find conversation ID
	thread, _, err := s.repo.GetThreadWithParent(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("thread not found: %w", err)
	}

	// Get current context window to link as parent
	currentCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get latest context window: %w", err)
	}

	// A retry of the compaction that opened the latest window IS that window.
	// The new window's id derives from the next sequence, so without this a
	// retry after the first attempt committed opened a second window over the
	// same summary, and whatever had been saved into the first one dropped out
	// of the conversation's context.
	if currentCW != nil && currentCW.CompactionSummaryMessageID != nil && *currentCW.CompactionSummaryMessageID == summaryMessageID {
		return currentCW, nil
	}

	// Get current max sequence
	currentSeq, err := s.repo.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get max context window sequence: %w", err)
	}

	newSeq := currentSeq + 1

	// Create new context window with CW chain linking:
	// - ParentContextWindowID: Links to the previous CW (for chain traversal, though compaction stops here)
	// - ForkAtMessageID: nil (compaction is not a branch)
	// - CompactionSummaryMessageID: Set to mark this as a compaction boundary
	cw := &db.ContextWindow{
		ID:                         contextWindowID(thread.ChatID, threadID, newSeq),
		ThreadID:                   threadID,
		Sequence:                   newSeq,
		CompactionSummaryMessageID: &summaryMessageID,
		ForkAtMessageID:            nil, // Not a branch
		CreatedAt:                  now(),
	}

	// Set parent CW if we have one
	if currentCW != nil {
		cw.ParentContextWindowID = &currentCW.ID
	}

	// Check if it already exists (idempotency for retries).
	existingCW, err := s.repo.GetContextWindow(ctx, cw.ID)
	if err == nil && existingCW != nil {
		// Already exists - update the summary message link if needed.
		if existingCW.CompactionSummaryMessageID == nil || *existingCW.CompactionSummaryMessageID != summaryMessageID {
			return s.repo.SetCompactionSummaryMessage(ctx, cw.ID, summaryMessageID)
		}
		return existingCW, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check existing context window: %w", err)
	}

	// Create the new context window
	createdCW, err := s.repo.CreateContextWindow(ctx, cw)
	if err != nil {
		return nil, fmt.Errorf("failed to create context window: %w", err)
	}

	return createdCW, nil
}

// GetCurrentSequence returns the current context sequence for a thread.
func (s *Service) GetCurrentSequence(ctx context.Context, threadID string) (int, error) {
	return s.repo.GetMaxSequenceForThread(ctx, threadID)
}
