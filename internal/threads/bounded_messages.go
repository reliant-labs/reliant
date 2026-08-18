package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// isUnforkedLeaf reports whether cw is a context window whose CW-chain
// resolution is trivial: no parent to inherit from, and (implied by having
// no parent) no fork filter to apply. This is the common case -- the real
// corpus this package was measured against had 30 threads in a chat with
// zero forks, and even a branched chat's own thread only has ONE CW with a
// parent (the fork point itself; every CW created after that has no
// parent). resolveMessagesFromCW's cost is proportional to the CW chain
// length, so this is exactly the condition under which a direct, bounded SQL
// read produces an identical result to the full resolve-then-slice path at a
// fraction of the cost.
func isUnforkedLeaf(cw *db.ContextWindow) bool {
	return cw.ParentContextWindowID == nil
}

// LoadRecentMessagesBefore returns the newest `limit` messages of threadID's
// visual thread that are strictly before beforeSeq (0 means unbounded --
// the newest page), plus whether any message precedes what's returned.
//
// It is LoadRecentMessages generalized with a cursor, and carries the same
// guarantee: the result is a subsequence of LoadCurrentMessages' full
// resolution, so fork, compaction, and visual-thread normalization semantics
// are identical by construction.
//
// Fast path (thread's latest CW has no parent -- see isUnforkedLeaf): bounds
// the read in SQL instead of resolving and slicing the whole history.
// Falls back to the full LoadCurrentMessages resolution, unmodified, for a
// forked or compacted thread, so nothing about the rare, correctness-fragile
// path changes.
func (s *Service) LoadRecentMessagesBefore(ctx context.Context, threadID string, beforeSeq int64, limit int) ([]*db.Message, bool, error) {
	if threadID == "" {
		return nil, false, fmt.Errorf("thread ID cannot be empty")
	}
	if limit <= 0 {
		return []*db.Message{}, false, nil
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		// No context window means empty thread.
		return []*db.Message{}, false, nil
	}

	if !isUnforkedLeaf(latestCW) {
		return s.loadRecentMessagesBeforeSlow(ctx, threadID, beforeSeq, limit)
	}

	messages, err := s.repo.ListRecentMessagesInContextWindowBeforeSeq(ctx, latestCW.ID, beforeSeq, limit)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load recent messages before seq: %w", err)
	}

	hasMoreOlder := false
	if len(messages) > 0 {
		hasMoreOlder, err = s.repo.HasMessagesBeforeInContextWindow(ctx, latestCW.ID, messages[0].Seq)
		if err != nil {
			return nil, false, fmt.Errorf("failed to check for older messages: %w", err)
		}
	} else if beforeSeq > 0 {
		// Nothing returned below the cursor -- check whether the CW has any
		// messages at all older than the cursor (covers beforeSeq sitting
		// above every message the CW holds, vs. genuinely at the start).
		hasMoreOlder, err = s.repo.HasMessagesBeforeInContextWindow(ctx, latestCW.ID, beforeSeq)
		if err != nil {
			return nil, false, fmt.Errorf("failed to check for older messages: %w", err)
		}
	}

	return s.normalizeVisualThread(ctx, threadID, messages), hasMoreOlder, nil
}

// loadRecentMessagesBeforeSlow is the fork/compaction-safe fallback: resolve
// the thread's full history exactly as LoadCurrentMessages does, then filter
// and slice in Go. Cost is identical to today's ListMessages path for the
// threads that take it; only the fast path above changes cost.
func (s *Service) loadRecentMessagesBeforeSlow(ctx context.Context, threadID string, beforeSeq int64, limit int) ([]*db.Message, bool, error) {
	// Display path: cross compaction boundaries so the transcript keeps its
	// summarized history (and, for a compacted branch, its inherited parent
	// history). See LoadDisplayMessages.
	all, err := s.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		return nil, false, err
	}

	chatOldestSeq := int64(0)
	if len(all) > 0 {
		chatOldestSeq = all[0].Seq
	}

	filtered := all
	if beforeSeq > 0 {
		filtered = make([]*db.Message, 0, len(all))
		for _, msg := range all {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
	}

	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	hasMoreOlder := len(filtered) > 0 && filtered[0].Seq > chatOldestSeq
	return filtered, hasMoreOlder, nil
}

// LoadMessagesInSeqRange returns threadID's visual-thread messages with
// seq >= fromSeq and, if toSeq is non-nil, seq < *toSeq. Used to bound a
// sibling (child/spawn) thread's read to the seq span a page's main-thread
// window actually covers, instead of that thread's entire history.
//
// Fast path / slow path split mirrors LoadRecentMessagesBefore.
func (s *Service) LoadMessagesInSeqRange(ctx context.Context, threadID string, fromSeq int64, toSeq *int64) ([]*db.Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
	if err != nil {
		return []*db.Message{}, nil
	}

	var messages []*db.Message
	if isUnforkedLeaf(latestCW) {
		messages, err = s.repo.ListMessagesInContextWindowRange(ctx, latestCW.ID, fromSeq, toSeq)
		if err != nil {
			return nil, fmt.Errorf("failed to load messages in seq range: %w", err)
		}
	} else {
		// Display path: cross compaction boundaries. See LoadDisplayMessages.
		all, err := s.LoadDisplayMessages(ctx, threadID)
		if err != nil {
			return nil, err
		}
		messages = make([]*db.Message, 0, len(all))
		for _, msg := range all {
			if msg.Seq < fromSeq {
				continue
			}
			if toSeq != nil && msg.Seq >= *toSeq {
				continue
			}
			messages = append(messages, msg)
		}
		// LoadCurrentMessages already normalized these; return directly to
		// avoid a redundant GetThread round trip.
		return messages, nil
	}

	return s.normalizeVisualThread(ctx, threadID, messages), nil
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
		return 0, nil
	}

	visited := make(map[string]bool)
	return s.countMessagesFromCWOpts(ctx, latestCW, visited, resolveOpts{crossCompaction: true})
}

func (s *Service) countMessagesFromCW(ctx context.Context, cw *db.ContextWindow, visited map[string]bool) (int, error) {
	return s.countMessagesFromCWOpts(ctx, cw, visited, resolveOpts{})
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
