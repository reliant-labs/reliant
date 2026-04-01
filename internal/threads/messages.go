package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ResolveMessages resolves all messages visible from a context window, including
// inherited messages from parent context windows in the CW chain.
//
// The resolution walks the context window chain via ParentContextWindowID,
// collecting messages from each CW. When a context window has a compaction
// summary, that summary message acts as the boundary - it already contains
// all prior context, so we don't need to traverse further.
//
// Algorithm:
//  1. Get messages from the current context window
//  2. If CW has CompactionSummaryMessageID, return just its messages (compaction boundary)
//  3. If CW has ParentContextWindowID, recursively resolve parent CW
//  4. If CW has ForkAtOrdinal (it's a branch), filter parent messages by that ordinal
//  5. Return parent messages + current messages
func (s *Service) ResolveMessages(ctx context.Context, opts ResolveMessagesOpts) ([]*db.Message, error) {
	if opts.ThreadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	// Determine the context window we're resolving from
	var startingCW *db.ContextWindow
	var err error
	if opts.ContextWindowID != nil {
		startingCW, err = s.repo.GetContextWindow(ctx, *opts.ContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("context window not found: %w", err)
		}
	} else {
		startingCW, err = s.repo.GetLatestContextWindow(ctx, opts.ThreadID)
		if err != nil {
			// No context window yet - thread has no messages
			return []*db.Message{}, nil
		}
	}

	// Use the recursive CW chain resolution with cycle detection
	visited := make(map[string]bool)
	return s.resolveMessagesFromCW(ctx, startingCW, opts.MaxOrdinal, visited)
}

// resolveMessagesFromCW recursively resolves messages by following the context window chain.
// This is the core resolution algorithm that uses ParentContextWindowID to traverse.
func (s *Service) resolveMessagesFromCW(ctx context.Context, cw *db.ContextWindow, maxOrdinal *int64, visited map[string]bool) ([]*db.Message, error) {
	if cw == nil {
		return []*db.Message{}, nil
	}

	// Guard against circular references in CW chain
	if visited[cw.ID] {
		return nil, fmt.Errorf("circular reference detected in CW chain at %s", cw.ID)
	}
	visited[cw.ID] = true

	// FORK-DEBUG: Log CW chain traversal
	var parentCWID string
	var forkOrdinal int64
	if cw.ParentContextWindowID != nil {
		parentCWID = *cw.ParentContextWindowID
	}
	if cw.ForkAtOrdinal != nil {
		forkOrdinal = *cw.ForkAtOrdinal
	}
	logging.Info("[FORK-DEBUG] resolveMessagesFromCW visiting CW",
		"cwID", cw.ID,
		"threadID", cw.ThreadID,
		"parentContextWindowID", parentCWID,
		"forkAtOrdinal", forkOrdinal,
		"sequence", cw.Sequence)

	// Get messages in this context window
	messages, err := s.repo.GetMessagesByContextWindow(ctx, cw.ID, maxOrdinal)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages for CW %s: %w", cw.ID, err)
	}

	// If this CW has a compaction summary, it contains all prior context - stop here
	if cw.CompactionSummaryMessageID != nil {
		return messages, nil
	}

	// If this CW has a parent, inherit from it
	if cw.ParentContextWindowID != nil {
		parentCW, err := s.repo.GetContextWindow(ctx, *cw.ParentContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("failed to get parent CW %s: %w", *cw.ParentContextWindowID, err)
		}

		// Recursively resolve parent (no maxOrdinal filter for parent - we'll filter below if branching)
		parentMsgs, err := s.resolveMessagesFromCW(ctx, parentCW, nil, visited)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve parent CW: %w", err)
		}

		// If we branched (not compacted), filter parent messages by fork ordinal
		// The ForkAtOrdinal only applies to messages from the DIRECT parent CW.
		// Messages inherited by the parent from its ancestors are already filtered.
		if cw.ForkAtOrdinal != nil {
			forkOrdinal := *cw.ForkAtOrdinal
			parentCWID := *cw.ParentContextWindowID
			var filtered []*db.Message
			for _, msg := range parentMsgs {
				if msg.ContextWindowID == parentCWID {
					// Message is from direct parent's context window - apply fork ordinal filter
					if msg.Ordinal <= forkOrdinal {
						filtered = append(filtered, msg)
					}
				} else {
					// Message is inherited from grandparent+ - already filtered, include as-is
					filtered = append(filtered, msg)
				}
			}
			// FORK-DEBUG: Log filtered parent messages
			logging.Info("[FORK-DEBUG] resolveMessagesFromCW filtered parent messages",
				"cwID", cw.ID,
				"forkOrdinal", forkOrdinal,
				"parentMsgsBefore", len(parentMsgs),
				"parentMsgsAfter", len(filtered),
				"localMessages", len(messages))
			parentMsgs = filtered
		}

		return append(parentMsgs, messages...), nil
	}

	return messages, nil
}

// ResolveMessagesFromCW resolves all messages from a specific context window by ID,
// following the context window chain via ParentContextWindowID.
//
// This is a public wrapper around resolveMessagesFromCW that:
//   - Takes a context window ID string instead of a *db.ContextWindow
//   - Is the primary method for testing the CW chain resolution algorithm
//
// See resolveMessagesFromCW for the resolution algorithm details.
func (s *Service) ResolveMessagesFromCW(ctx context.Context, contextWindowID string) ([]*db.Message, error) {
	if contextWindowID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}

	// Get the context window
	cw, err := s.repo.GetContextWindow(ctx, contextWindowID)
	if err != nil {
		return nil, fmt.Errorf("context window not found: %w", err)
	}

	// Use the recursive CW chain resolution with cycle detection
	visited := make(map[string]bool)
	return s.resolveMessagesFromCW(ctx, cw, nil, visited)
}

// LoadCurrentMessages loads all messages that should be visible in the current
// context for the given thread. This is the primary API for loading conversation
// history for LLM context.
//
// This method handles all complexity internally:
//   - Automatically finds the latest context window for the thread
//   - Walks the CW chain via ParentContextWindowID to collect inherited messages
//   - Respects compaction boundaries (stops at context windows with a compaction summary)
//   - Returns messages in chronological order (inherited first, then local)
//
// Compaction boundary detection:
// Uses CompactionSummaryMessageID != nil to detect compaction boundaries, NOT Sequence > 0.
// This is critical because forked threads inherit the parent's sequence number but don't
// have their own compaction summary. Using Sequence > 0 would incorrectly skip parent
// traversal for forked threads.
//
// CW chain resolution:
// For a CW chain A → B → C, calling LoadCurrentMessages(ctx, "thread-with-CW-C") returns:
//   - Messages from A up to B's fork point
//   - Messages from B up to C's fork point
//   - All messages from C's current context window
//
// Empty threads return an empty slice (not an error).
//
// Visual Thread Normalization:
// All returned messages have their ThreadID and WorkflowID set to the requesting thread's values.
// This implements the "visual thread" concept - inherited messages from parent
// context windows appear as part of the current thread for display purposes.
// The underlying CW chain (logical thread) handles resolution, but callers
// see a unified visual thread without false "agent handoff" indicators.
func (s *Service) LoadCurrentMessages(ctx context.Context, threadID string) ([]*db.Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	// Get the latest context window for this thread
	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		// No context window means empty thread - return empty slice
		return []*db.Message{}, nil
	}

	// FORK-DEBUG: Log LoadCurrentMessages start
	var parentCWID string
	var forkAtOrdinal int64
	if latestCW.ParentContextWindowID != nil {
		parentCWID = *latestCW.ParentContextWindowID
	}
	if latestCW.ForkAtOrdinal != nil {
		forkAtOrdinal = *latestCW.ForkAtOrdinal
	}
	logging.Info("[FORK-DEBUG] LoadCurrentMessages called",
		"threadID", threadID,
		"latestCWID", latestCW.ID,
		"parentContextWindowID", parentCWID,
		"forkAtOrdinal", forkAtOrdinal,
		"sequence", latestCW.Sequence)

	// Use the recursive CW chain resolution with cycle detection
	visited := make(map[string]bool)
	messages, err := s.resolveMessagesFromCW(ctx, latestCW, nil, visited)
	if err != nil {
		return nil, err
	}

	// FORK-DEBUG: Log LoadCurrentMessages result
	logging.Info("[FORK-DEBUG] LoadCurrentMessages resolved messages",
		"threadID", threadID,
		"totalMessages", len(messages),
		"cwsVisited", len(visited))
	// Get the thread to retrieve its workflow ID for normalization
	thread, err := s.repo.GetThread(ctx, threadID)
	if err != nil {
		// Thread might not exist yet - just normalize ThreadID without WorkflowID
		for _, msg := range messages {
			msg.ThreadID = threadID
		}
		return messages, nil
	}

	// Normalize to visual thread: all messages appear as part of the requesting thread/workflow
	// This is a display concern - inherited messages should show as part of this thread/workflow
	// to avoid false "agent handoff" indicators in the UI when viewing branched chats.
	// ChatID must also be normalized so inherited messages from parent chats don't appear
	// with the wrong chat ID in the frontend.
	for _, msg := range messages {
		msg.ChatID = thread.ConversationID
		msg.ThreadID = threadID
		msg.WorkflowID = thread.WorkflowID
	}

	return messages, nil
}
