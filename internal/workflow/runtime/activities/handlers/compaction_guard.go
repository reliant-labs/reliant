// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// errCompactionCannotReclaim is the Sentry signal for a thread whose context is
// dominated by what compaction cannot remove. One error value, so every such
// thread groups under one issue.
var errCompactionCannotReclaim = errors.New("compaction skipped: compacting cannot shrink this thread's context")

// compactionSkippedReason is CompactOutput.skipped_reason for a futile
// compaction.
func compactionSkippedReason(reclaim message.CompactionReclaim) string {
	return fmt.Sprintf("compacting would remove ~%d of %d context tokens (minimum %.0f%%); "+
		"the rest is the fixed base every request carries (system prompt, tools, memory, preloaded skills)",
		reclaim.Reclaimable, reclaim.Current, message.CompactionMinReclaimFraction*100)
}

// compactionSkip identifies one context window the guard declined to compact.
type compactionSkip struct {
	ChatID          string
	Thread          string
	Model           string
	ContextSequence int64
	Reclaim         message.CompactionReclaim
}

// newCompactionSkip describes skipping messages, the current context window.
func newCompactionSkip(chatID, thread string, messages []message.Message, reclaim message.CompactionReclaim) compactionSkip {
	skip := compactionSkip{ChatID: chatID, Thread: thread, Reclaim: reclaim}
	for i := len(messages) - 1; i >= 0; i-- {
		if skip.Model == "" && messages[i].TokenCount > 0 {
			skip.Model = string(messages[i].Model)
		}
		if messages[i].ContextSequence > skip.ContextSequence {
			skip.ContextSequence = messages[i].ContextSequence
		}
	}
	return skip
}

// compactionSkipNotices reports each context window the guard skips ONCE.
//
// The agent loop's compact edge fires on every turn the thread stays above its
// threshold, and the guard skips every one of them, so reporting per call
// would bury the signal in its own repetition. A window that later grows
// enough to compact gets a new sequence, and a fresh notice if the window after
// it is just as stuck.
type compactionSkipNotices struct {
	// lastReported maps thread -> the context sequence last reported for it.
	lastReported sync.Map
	capture      func(err error, tags map[string]string, extra map[string]interface{}) string
}

var compactionSkips = &compactionSkipNotices{capture: telemetry.CaptureExceptionWithContext}

// report logs and captures skip unless its window was already reported, and
// returns whether it did.
func (n *compactionSkipNotices) report(skip compactionSkip) bool {
	previous, seen := n.lastReported.Swap(skip.Thread, skip.ContextSequence)
	if seen && previous.(int64) == skip.ContextSequence {
		logging.Debug("[Compact] Still skipping compaction that cannot shrink the context",
			"chatID", skip.ChatID,
			"thread", skip.Thread,
			"contextSequence", skip.ContextSequence,
			"currentTokens", skip.Reclaim.Current,
			"reclaimableTokens", skip.Reclaim.Reclaimable)
		return false
	}

	logging.Warn("[Compact] Skipping compaction: it cannot shrink this thread's context. "+
		"The fixed base (system prompt, tools, memory, preloaded skills) is most of the window, so a "+
		"compacted window would open above the threshold and compact again on the next turn. "+
		"Continuing uncompacted; the trim backstop still bounds tool results to the model's window",
		"chatID", skip.ChatID,
		"thread", skip.Thread,
		"model", skip.Model,
		"contextSequence", skip.ContextSequence,
		"currentTokens", skip.Reclaim.Current,
		"windowFloorTokens", skip.Reclaim.Floor,
		"reclaimableTokens", skip.Reclaim.Reclaimable,
		"minReclaimFraction", message.CompactionMinReclaimFraction)

	n.capture(errCompactionCannotReclaim,
		map[string]string{
			"component": "compaction",
			"chat_id":   skip.ChatID,
			"thread_id": skip.Thread,
			"model":     skip.Model,
		},
		map[string]interface{}{
			"context_sequence":     strconv.FormatInt(skip.ContextSequence, 10),
			"current_tokens":       skip.Reclaim.Current,
			"window_floor_tokens":  skip.Reclaim.Floor,
			"reclaimable_tokens":   skip.Reclaim.Reclaimable,
			"min_reclaim_fraction": message.CompactionMinReclaimFraction,
		})
	return true
}
