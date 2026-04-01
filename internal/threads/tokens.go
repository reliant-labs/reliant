package threads

import (
	"context"
	"fmt"
)

// DefaultCompactionThreshold is the default token threshold for compaction.
const DefaultCompactionThreshold = 185000

// GetContextUsage returns context usage information for a thread.
// This is used by the UI to show the compaction indicator.
func (s *Service) GetContextUsage(ctx context.Context, threadID string) (*ContextUsage, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	// Verify thread exists
	_, _, err := s.repo.GetThreadWithParent(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("thread not found: %w", err)
	}

	// Get current context sequence
	maxSeq, err := s.repo.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		maxSeq = 0
	}

	// Get token count
	tokenCount, err := s.GetThreadTokenCount(ctx, threadID, nil)
	if err != nil {
		tokenCount = 0
	}

	return &ContextUsage{
		ThreadID:            threadID,
		ContextSequence:     maxSeq,
		ThreadTokenCount:    tokenCount,
		CompactionThreshold: DefaultCompactionThreshold,
	}, nil
}

// GetThreadTokenCount returns the context size (token count) for a thread.
// This includes inherited tokens from parent threads in the fork chain.
//
// Token counting logic:
// 1. Find the latest message with token data in the current context sequence
// 2. Return its token_count (which represents the context size the LLM saw)
// 3. If no local tokens, recursively check parent thread at fork ordinal
func (s *Service) GetThreadTokenCount(ctx context.Context, threadID string, maxOrdinal *int64) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get thread to find conversation ID and fork metadata
	thread, _, err := s.repo.GetThreadWithParent(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("thread not found: %w", err)
	}

	// Get current context sequence
	maxSeq, err := s.repo.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		maxSeq = 0
	}

	// Find latest message with token data
	msg, err := s.repo.GetLatestMessageWithTokensInThread(ctx, threadID, maxSeq)
	if err == nil && msg != nil && msg.TokenCount != nil {
		return int64(*msg.TokenCount), nil
	}

	// No local token data - check for fork inheritance
	if thread.ParentThreadID == nil || thread.ForkAtOrdinal == nil {
		return 0, nil
	}

	parentThreadID := *thread.ParentThreadID
	forkAtOrdinal := *thread.ForkAtOrdinal

	// Guard against self-referential forks
	if parentThreadID == threadID {
		return 0, nil
	}

	// Recursively get parent's token count at the fork point
	return s.GetThreadTokenCount(ctx, parentThreadID, &forkAtOrdinal)
}
