// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: thread/context-window management; its collaborators (Repository, TemporalSignaler, ToolCanceler) are consumer-declared interfaces
package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// LoadRecentMessagesBefore returns the newest `limit` messages of threadID's
// visual thread that are strictly before beforeSeq (0 means unbounded --
// the newest page), plus whether any message precedes what's returned.
//
// It is the scroll-back read, and it carries one guarantee: the page is the
// tail of the below-the-cursor prefix of LoadDisplayMessages' full
// resolution, so fork, compaction, and visual-thread normalization semantics
// are identical by construction. It gets there without resolving the
// history: recentDisplayMessagesBeforeFromCW walks the context-window chain
// newest-first, one bounded indexed read per window, and stops once the page
// is full -- the same walk the chat snapshot uses for its initial window. A
// compacted or branched thread (every window after the first has a parent)
// therefore costs the windows the page spans, not the whole transcript.
//
// One row past the page answers "is there older history?" exactly, without a
// second query: the walk only returns fewer than limit+1 rows once it has
// reached the root of the chain.
func (s *Service) LoadRecentMessagesBefore(ctx context.Context, threadID string, beforeSeq int64, limit int) ([]*db.Message, bool, error) {
	if threadID == "" {
		return nil, false, fmt.Errorf("thread ID cannot be empty")
	}
	if limit <= 0 {
		return []*db.Message{}, false, nil
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		if !isEmptyThread(err) {
			return nil, false, fmt.Errorf("failed to load latest context window for thread %s: %w", threadID, err)
		}
		// No context window means empty thread.
		return []*db.Message{}, false, nil
	}

	messages, err := s.recentDisplayMessagesBeforeFromCW(ctx, latestCW, beforeSeq, limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMoreOlder := len(messages) > limit
	if hasMoreOlder {
		messages = messages[len(messages)-limit:]
	}

	return s.normalizeVisualThread(ctx, threadID, messages), hasMoreOlder, nil
}

// CountCurrentMessages returns the true count of messages visible in
// threadID's current context -- the count-only mirror of LoadCurrentMessages,
// computed by walking the CW chain and summing COUNTs instead of fetching and
// concatenating rows. Mirrors resolveMessagesFromCW's local/inherited split
// exactly: a forked CW counts its direct parent's messages only up to the
// fork point (CountMessagesByContextWindowUpToSeq), and messages inherited
// from further back are already correctly bounded by the parent's own count.
func (s *Service) CountCurrentMessages(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		if !isEmptyThread(err) {
			return 0, fmt.Errorf("failed to load latest context window for thread %s: %w", threadID, err)
		}
		return 0, nil
	}

	visited := make(map[string]bool)
	return s.countMessagesFromCWOpts(ctx, latestCW, visited, resolveOpts{})
}

// CountDisplayMessages is the count-only mirror of LoadDisplayMessages, the
// same way CountCurrentMessages mirrors LoadCurrentMessages.
//
// There are two resolutions in this package and therefore two counts: the LLM
// one stops at a compaction summary, the transcript one crosses it. A display
// read that reported the LLM count was wrong by the whole summarized history
// -- on the chat that surfaced this, 13 against a 1,539-message transcript,
// because all three compactions in its ancestry were counted out but shown.
func (s *Service) CountDisplayMessages(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		if !isEmptyThread(err) {
			return 0, fmt.Errorf("failed to load latest context window for thread %s: %w", threadID, err)
		}
		return 0, nil
	}

	visited := make(map[string]bool)
	return s.countMessagesFromCWOpts(ctx, latestCW, visited, resolveOpts{crossCompaction: true})
}

// countMessagesFromCWOpts walks the CW chain summing COUNTs. It must track
// resolveMessagesFromCWOpts branch for branch -- same compaction stop, same
// fork cut, same crossCompaction behavior -- or the total disagrees with the
// list it describes.
func (s *Service) countMessagesFromCWOpts(
	ctx context.Context, cw *db.ContextWindow, visited map[string]bool, opts resolveOpts,
) (int, error) {
	if cw == nil {
		return 0, nil
	}
	if visited[cw.ID] {
		return 0, fmt.Errorf("circular reference detected in CW chain at %s", cw.ID)
	}
	visited[cw.ID] = true

	localCount, err := s.repo.CountMessagesByContextWindow(ctx, cw.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages for CW %s: %w", cw.ID, err)
	}

	// Compaction boundary: nothing upstream contributes once a summary
	// exists -- unless this is the transcript's count, which shows the
	// summarized turns and so must keep counting past them.
	if cw.CompactionSummaryMessageID != nil && !opts.crossCompaction {
		return localCount, nil
	}

	if cw.ParentContextWindowID == nil {
		return localCount, nil
	}

	parentCW, err := s.repo.GetContextWindow(ctx, *cw.ParentContextWindowID)
	if err != nil {
		return 0, fmt.Errorf("failed to get parent CW %s: %w", *cw.ParentContextWindowID, err)
	}

	// The parent CW's own fully-resolved count (its local messages plus
	// whatever IT inherited), mirroring resolveMessagesFromCW's unfiltered
	// recursive call on the parent before this fork's cut is applied.
	parentTotal, err := s.countMessagesFromCWOpts(ctx, parentCW, visited, opts)
	if err != nil {
		return 0, err
	}

	// A compaction CW is not a fork (Compact always sets ForkAtMessageID =
	// nil), so there is no cut to apply and the parent is inherited whole.
	// Reaching here on one means crossCompaction sent the walk past the
	// boundary. This mirrors resolveMessagesFromCWOpts; applying the fork cut
	// anyway would resolve forkSeq to -1 and count out the entire parent
	// window, which is the list-side bug this count has to stay in step with.
	if cw.CompactionSummaryMessageID != nil {
		return localCount + parentTotal, nil
	}

	parentLocalCount, err := s.repo.CountMessagesByContextWindow(ctx, parentCW.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to count parent CW messages: %w", err)
	}
	// What the parent inherited from ITS OWN ancestors -- already correctly
	// bounded by the recursion above, and untouched by this fork's cut
	// (resolveMessagesFromCW only filters messages whose ContextWindowID is
	// the DIRECT parent CW; grandparent+ messages pass through as-is).
	parentAncestorCount := parentTotal - parentLocalCount

	// forkSeq is -1 when ForkAtMessageID is nil (fork of an empty thread):
	// seq is never negative, so the up-to-seq count is naturally 0, exactly
	// matching the row-based `msg.Seq <= forkSeq` filter with forkSeq=-1.
	forkSeq := int64(-1)
	if cw.ForkAtMessageID != nil {
		forkMsg, err := s.repo.GetMessage(ctx, *cw.ForkAtMessageID)
		if err != nil {
			return 0, fmt.Errorf("failed to get fork message %s for CW %s: %w", *cw.ForkAtMessageID, cw.ID, err)
		}
		forkSeq = forkMsg.Seq
	}
	parentLocalUpToFork, err := s.repo.CountMessagesByContextWindowUpToSeq(ctx, parentCW.ID, forkSeq)
	if err != nil {
		return 0, fmt.Errorf("failed to count parent messages up to fork: %w", err)
	}

	return localCount + parentAncestorCount + parentLocalUpToFork, nil
}
