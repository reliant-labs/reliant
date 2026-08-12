package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// DefaultCompactionThreshold is the default token threshold for compaction.
const DefaultCompactionThreshold = models.GlobalDefaultCompactionThreshold

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
		CompactionThreshold: int64(s.resolveCompactionThreshold(ctx, threadID, nil)),
	}, nil
}

// resolveCompactionThreshold returns the compaction threshold for the model that
// produced the thread's current token count, mirroring the fork inheritance walk
// in GetThreadTokenCount so the indicator's denominator tracks the same model
// the trigger evaluates. Falls back to the global default when no model-bearing
// message is found. maxSeq bounds inheritance to a fork point, the same way
// maxOrdinal used to -- see GetThreadTokenCount.
func (s *Service) resolveCompactionThreshold(ctx context.Context, threadID string, maxSeq *int64) int {
	if threadID == "" {
		return models.GlobalDefaultCompactionThreshold
	}

	currentSeq, err := s.repo.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		currentSeq = 0
	}

	msg, err := s.repo.GetLatestMessageWithTokensInThread(ctx, threadID, currentSeq)
	if err == nil && msg != nil && msg.TokenCount != nil && (maxSeq == nil || msg.Seq <= *maxSeq) {
		if msg.Model != nil {
			return models.CompactionThresholdForModel(*msg.Model)
		}
		return models.GlobalDefaultCompactionThreshold
	}

	// No local token-bearing message — follow fork inheritance like GetThreadTokenCount.
	thread, _, err := s.repo.GetThreadWithParent(ctx, threadID)
	if err != nil || thread.ParentThreadID == nil || thread.ForkAtMessageID == nil {
		return models.GlobalDefaultCompactionThreshold
	}
	parentThreadID := *thread.ParentThreadID
	if parentThreadID == threadID {
		return models.GlobalDefaultCompactionThreshold
	}
	forkMsg, err := s.repo.GetMessage(ctx, *thread.ForkAtMessageID)
	if err != nil {
		return models.GlobalDefaultCompactionThreshold
	}
	return s.resolveCompactionThreshold(ctx, parentThreadID, &forkMsg.Seq)
}

// GetThreadTokenCount returns the context size (token count) for a thread.
// This includes inherited tokens from parent threads in the fork chain.
//
// Token counting logic:
// 1. Find the latest message with token data in the current context sequence
// 2. Return its token_count (which represents the context size the LLM saw)
// 3. If no local tokens, recursively check parent thread at the fork message's seq
//
// maxSeq bounds a recursive call to "no later than the fork point" -- the top-level
// call always passes nil (the thread's own current state is unbounded).
func (s *Service) GetThreadTokenCount(ctx context.Context, threadID string, maxSeq *int64) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get thread to find chat ID and fork metadata
	thread, _, err := s.repo.GetThreadWithParent(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("thread not found: %w", err)
	}

	// Get current context sequence
	currentSeq, err := s.repo.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		currentSeq = 0
	}

	// Find latest message with token data
	msg, err := s.repo.GetLatestMessageWithTokensInThread(ctx, threadID, currentSeq)
	if err == nil && msg != nil && msg.TokenCount != nil {
		return int64(*msg.TokenCount), nil
	}

	// No local token data - check for fork inheritance
	if thread.ParentThreadID == nil || thread.ForkAtMessageID == nil {
		return 0, nil
	}

	parentThreadID := *thread.ParentThreadID

	// Guard against self-referential forks
	if parentThreadID == threadID {
		return 0, nil
	}

	forkMsg, err := s.repo.GetMessage(ctx, *thread.ForkAtMessageID)
	if err != nil {
		return 0, fmt.Errorf("failed to get fork message %s: %w", *thread.ForkAtMessageID, err)
	}

	// Recursively get parent's token count at the fork point
	return s.GetThreadTokenCount(ctx, parentThreadID, &forkMsg.Seq)
}
