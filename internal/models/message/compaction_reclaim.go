// Copyright (c) 2025 Reliant Labs
package message

// CompactionMinReclaimFraction is the smallest share of the current context a
// compaction must be able to remove before it is worth running.
//
// Compaction replaces a context window's conversation with a summary. It cannot
// touch the fixed base every request carries — system prompt, tool schemas,
// injected memory, preloaded skills — so when that base is most of the context,
// the compacted window starts just as full as the one it replaced, trips the
// threshold on the next turn, and compacts again. Prod incident 2026-10-09: a
// sub-agent whose base was 246.8k tokens against a 231.2k threshold compacted
// 183 times in 44 minutes, a full-context summarization call plus total loss of
// working memory on every single turn.
//
// Requiring each compaction to reclaim a real share of the context bounds the
// compaction rate by context GROWTH rather than by turns or wall-clock time: a
// thread cannot compact again until it has accumulated at least this much new,
// removable history. 10% keeps a compaction reachable before the trim backstop
// in the default layout — the threshold sits at 85% of the window and the
// backstop at 95% (TrimBackstopFraction), ten points apart.
const CompactionMinReclaimFraction = 0.10

// CompactionReclaim is what compacting the current context window could remove,
// measured from the provider's own token counts wherever they exist.
type CompactionReclaim struct {
	// Current is the size of the context the next request would send: the
	// latest provider-reported count plus an estimate for anything saved after
	// it (typically the tool results of the turn that tripped the threshold).
	Current int
	// Floor is the provider-reported context size at the window's first turn:
	// the fixed base plus whatever the window opened with. 0 when no message in
	// the window carries token data.
	Floor int
	// Reclaimable is the part of Current a compaction removes: everything the
	// window accumulated since the floor, plus the conversation the floor
	// already contained (a fresh thread's opening prompt, the first reply).
	// System messages — the previous compaction's summary — are not counted:
	// a new summary takes their place.
	Reclaimable int
}

// Worthwhile reports whether compacting would remove at least
// CompactionMinReclaimFraction of the current context. A window with nothing
// measured is always worth compacting, which is the behaviour before this
// guard existed.
func (r CompactionReclaim) Worthwhile() bool {
	if r.Current <= 0 {
		return true
	}
	return float64(r.Reclaimable) >= float64(r.Current)*CompactionMinReclaimFraction
}

// EstimateCompactionReclaim measures how much of messages — the current context
// window, as the compact activity loads it — a compaction could remove.
//
// A stored TokenCount is the provider's total for that turn (input + output +
// cache), i.e. the whole context including the fixed base. The difference
// between the window's first and latest counts is therefore exact growth, with
// no estimate of the base needed; only content outside those two counts is
// estimated from characters.
func EstimateCompactionReclaim(messages []Message) CompactionReclaim {
	first, last := -1, -1
	for i := range messages {
		if !hasTokenData(&messages[i]) {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}

	if first < 0 {
		var reclaim CompactionReclaim
		for _, msg := range messages {
			tokens := estimateMessageChars(msg) / CharsPerToken
			reclaim.Current += tokens
			if msg.Role != System {
				reclaim.Reclaimable += tokens
			}
		}
		return reclaim
	}

	reclaim := CompactionReclaim{
		Floor:   int(messages[first].TokenCount),
		Current: int(messages[last].TokenCount),
	}
	reclaim.Reclaimable = max(0, reclaim.Current-reclaim.Floor)
	// The floor turn's count includes the conversation up to and including
	// that turn's own reply. All of it is compactable except the summary.
	for _, msg := range messages[:first+1] {
		if msg.Role != System {
			reclaim.Reclaimable += estimateMessageChars(msg) / CharsPerToken
		}
	}
	// Saved after the latest count, so in neither figure yet.
	for _, msg := range messages[last+1:] {
		tokens := estimateMessageChars(msg) / CharsPerToken
		reclaim.Current += tokens
		reclaim.Reclaimable += tokens
	}
	return reclaim
}
