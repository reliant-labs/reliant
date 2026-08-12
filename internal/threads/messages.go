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
//  4. If CW has ForkAtMessageID (it's a branch), filter parent messages at that message
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
// resolveOpts controls how far back the CW chain walk goes.
type resolveOpts struct {
	// crossCompaction continues past a compaction boundary instead of stopping
	// at it.
	//
	// The two consumers of this walk want different things from a compaction.
	// The LLM wants to STOP: the summary message replaces everything before it,
	// and replaying the summarized turns as well would both duplicate the
	// content and blow the context budget the compaction existed to reclaim.
	// The transcript wants to CONTINUE: those turns are the user's history, they
	// are still on disk, and hiding them makes a compacted chat look like it
	// begins mid-sentence.
	//
	// Conflating the two truncated the UI. A branched chat that was later
	// compacted showed 19 messages instead of ~1,755, because the compaction
	// boundary sat between the branch's own context window and the one carrying
	// the fork link to its parent -- so the walk stopped before it ever reached
	// the inherited history.
	crossCompaction bool
}

func (s *Service) resolveMessagesFromCW(ctx context.Context, cw *db.ContextWindow, maxOrdinal *int64, visited map[string]bool) ([]*db.Message, error) {
	return s.resolveMessagesFromCWOpts(ctx, cw, maxOrdinal, visited, resolveOpts{})
}

func (s *Service) resolveMessagesFromCWOpts(ctx context.Context, cw *db.ContextWindow, maxOrdinal *int64, visited map[string]bool, opts resolveOpts) ([]*db.Message, error) {
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
	var forkMessageID string
	if cw.ParentContextWindowID != nil {
		parentCWID = *cw.ParentContextWindowID
	}
	if cw.ForkAtMessageID != nil {
		forkMessageID = *cw.ForkAtMessageID
	}
	logging.Info("[FORK-DEBUG] resolveMessagesFromCW visiting CW",
		"cwID", cw.ID,
		"threadID", cw.ThreadID,
		"parentContextWindowID", parentCWID,
		"forkAtMessageID", forkMessageID,
		"sequence", cw.Sequence)

	// Get messages in this context window
	messages, err := s.repo.GetMessagesByContextWindow(ctx, cw.ID, maxOrdinal)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages for CW %s: %w", cw.ID, err)
	}

	// A compaction summary stands in for everything before it. For LLM context
	// that is the whole point, so stop. For the transcript the summarized turns
	// are still real history the user expects to scroll to, so continue.
	if cw.CompactionSummaryMessageID != nil && !opts.crossCompaction {
		return messages, nil
	}

	// If this CW has a parent, inherit from it
	if cw.ParentContextWindowID != nil {
		parentCW, err := s.repo.GetContextWindow(ctx, *cw.ParentContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("failed to get parent CW %s: %w", *cw.ParentContextWindowID, err)
		}

		// Recursively resolve parent (no maxOrdinal filter for parent - we'll filter below if branching)
		parentMsgs, err := s.resolveMessagesFromCWOpts(ctx, parentCW, nil, visited, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve parent CW: %w", err)
		}

		// A chained CW that isn't a compaction boundary (handled above) is
		// always a fork -- Compact always sets CompactionSummaryMessageID, so
		// reaching here with ParentContextWindowID set means ForkThread built
		// this CW. Cut the parent's history at the fork message. The cut only
		// applies to messages from the DIRECT parent CW; messages the parent
		// inherited from its own ancestors were already filtered when the
		// parent resolved them, so they pass through.
		//
		// ForkAtMessageID is nil when the fork's parent had no messages at
		// fork time (forking an empty thread) -- there is no message to
		// reference, and the correct result is the same as if there were one
		// at negative ordinal: nothing from the direct parent CW is included.
		//
		// The comparison is on Seq (the chat-global order every read path
		// uses) rather than the fork message's position within its thread.
		// Inside a single context window the two orders are identical, and
		// the ContextWindowID gate below is what confines the comparison to
		// that window -- without the gate a chat-global seq would also sweep
		// in messages from sibling threads of the same chat.
		var forkSeq int64 = -1
		if cw.ForkAtMessageID != nil {
			forkMsg, err := s.repo.GetMessage(ctx, *cw.ForkAtMessageID)
			if err != nil {
				return nil, fmt.Errorf("failed to get fork message %s for CW %s: %w", *cw.ForkAtMessageID, cw.ID, err)
			}
			forkSeq = forkMsg.Seq
		}
		parentCWID := *cw.ParentContextWindowID
		var filtered []*db.Message
		for _, msg := range parentMsgs {
			if msg.ContextWindowID == parentCWID {
				// Message is from direct parent's context window - apply fork filter
				if msg.Seq <= forkSeq {
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
			"forkAtMessageID", cw.ForkAtMessageID,
			"forkSeq", forkSeq,
			"parentMsgsBefore", len(parentMsgs),
			"parentMsgsAfter", len(filtered),
			"localMessages", len(messages))
		parentMsgs = filtered

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
// LoadRecentMessages returns the most recent `limit` messages of the thread's
// visual thread, for display paths that only need a window (the initial chat
// snapshot). It is the tail of LoadCurrentMessages' result, so fork, compaction
// and visual-thread normalization semantics are identical by construction — a
// suffix of the correct resolution is still correctly resolved.
//
// Do NOT use this for LLM context assembly: a truncated context window is not
// the same conversation. That path must keep calling LoadCurrentMessages.
func (s *Service) LoadRecentMessages(ctx context.Context, threadID string, limit int) ([]*db.Message, error) {
	messages, err := s.LoadCurrentMessages(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

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
	var forkAtMessageID string
	if latestCW.ParentContextWindowID != nil {
		parentCWID = *latestCW.ParentContextWindowID
	}
	if latestCW.ForkAtMessageID != nil {
		forkAtMessageID = *latestCW.ForkAtMessageID
	}
	logging.Info("[FORK-DEBUG] LoadCurrentMessages called",
		"threadID", threadID,
		"latestCWID", latestCW.ID,
		"parentContextWindowID", parentCWID,
		"forkAtMessageID", forkAtMessageID,
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

	return s.normalizeVisualThread(ctx, threadID, messages), nil
}

// LoadDisplayMessages resolves a thread's full visible history for the
// TRANSCRIPT, continuing past compaction boundaries.
//
// This is deliberately not LoadCurrentMessages. That function stops at a
// compaction summary because the summary replaces the turns before it, which is
// correct for LLM context and wrong for the UI: those turns are still on disk
// and the user expects to scroll to them.
//
// The distinction is load-bearing for branched chats. A branch's fork link to
// its parent lives on its FIRST context window, so once the branch compacts,
// the compaction boundary sits between the newest window and the one carrying
// that link. Stopping at the boundary therefore hides not just the summarized
// turns but the entire inherited parent history: one real chat showed 19
// messages instead of ~1,755.
//
// Do NOT use this to build LLM context — replaying summarized turns alongside
// their summary duplicates content and defeats the compaction.
func (s *Service) LoadDisplayMessages(ctx context.Context, threadID string) ([]*db.Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		// No context window yet - thread has no messages.
		return []*db.Message{}, nil
	}

	visited := make(map[string]bool)
	messages, err := s.resolveMessagesFromCWOpts(ctx, latestCW, nil, visited, resolveOpts{
		crossCompaction: true,
	})
	if err != nil {
		return nil, err
	}

	return s.normalizeVisualThread(ctx, threadID, messages), nil
}

// LoadRecentDisplayMessages is the bounded form of LoadDisplayMessages: the
// newest `limit` messages of the thread's visible history. A suffix of a
// correct resolution is still correctly resolved, so fork, compaction and
// visual-thread semantics are identical by construction.
//
// Use this for the initial snapshot; use LoadDisplayMessages when the whole
// transcript is needed. Neither is safe for LLM context.
func (s *Service) LoadRecentDisplayMessages(ctx context.Context, threadID string, limit int) ([]*db.Message, error) {
	messages, err := s.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

// normalizeVisualThread stamps every message's ChatID/ThreadID/WorkflowID to
// the requesting thread's own values -- the "visual thread" concept.
// Inherited messages from parent context windows appear as part of the
// current thread/chat for display purposes, so the UI doesn't show a false
// "agent handoff" when viewing a branched chat. Shared by every resolution
// path (full and bounded) so the display contract stays identical.
func (s *Service) normalizeVisualThread(ctx context.Context, threadID string, messages []*db.Message) []*db.Message {
	thread, err := s.repo.GetThread(ctx, threadID)
	if err != nil {
		// Thread might not exist yet - just normalize ThreadID without WorkflowID
		for _, msg := range messages {
			msg.ThreadID = threadID
		}
		return messages
	}

	for _, msg := range messages {
		msg.ChatID = thread.ChatID
		msg.ThreadID = threadID
		msg.WorkflowID = thread.WorkflowID
	}
	return messages
}
