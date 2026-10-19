// Copyright (c) 2025 Reliant Labs

package accountrouter

import (
	"regexp"
	"strings"
	"time"
)

// unknownWindowStaleAfter is how long a window with neither a known reset nor
// a known length stays believable.
const unknownWindowStaleAfter = 10 * time.Minute

// scopedFamilies are the model families a usage window can be scoped to
// ("seven_day_opus", "7d_sonnet"). A window naming none of them, or a scope we
// do not recognise (a Codex "codex_other.primary"), is counted for every model:
// a cap we cannot attribute is assumed to bind.
var scopedFamilies = []string{"opus", "sonnet", "haiku"}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Score is how close an account is to exhaustion for modelID, 0..1, and the
// name of the window that sets it. It is the maximum used fraction over the
// windows that apply to the model: the window nearest its cap is the one that
// will stop the account, so a 7-day window at 60% outranks a 5-hour one at 10%.
//
// Unknown counts as 0: an account nothing has been observed on looks unused,
// which it usually is. A window whose reset has passed has started over and
// counts 0; one last observed longer ago than its own length is unknown.
func Score(l LimitState, modelID string, now time.Time) (score float64, binding string) {
	for _, w := range l.Windows {
		if !windowApplies(w.Name, modelID) {
			continue
		}
		used, known := effectiveUsed(w, l.ObservedAt, now)
		if known && used > score {
			score, binding = used, w.Name
		}
	}
	return score, binding
}

// effectiveUsed is w's used fraction as it applies at now. known is false when
// nothing believable can be said, and used is then 0.
func effectiveUsed(w LimitWindow, observedAt, now time.Time) (used float64, known bool) {
	if !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
		return 0, true
	}
	length := time.Duration(w.LengthS) * time.Second
	age := now.Sub(observedAt)
	switch {
	case observedAt.IsZero():
		return 0, false
	case length > 0 && age > length:
		return 0, false
	case length == 0 && w.ResetsAt.IsZero() && age > unknownWindowStaleAfter:
		return 0, false
	}
	switch {
	case w.UsedFraction != nil:
		used = *w.UsedFraction
	case w.Limit != nil && w.Remaining != nil && *w.Limit > 0:
		used = 1 - float64(*w.Remaining)/float64(*w.Limit)
	default:
		return 0, false
	}
	return min(max(used, 0), 1), true
}

// windowApplies reports whether a window counts toward modelID: unscoped and
// unrecognised-scope windows always, a family-scoped window only for its family.
func windowApplies(windowName, modelID string) bool {
	model := strings.ToLower(modelID)
	for _, tok := range nonAlnum.Split(strings.ToLower(windowName), -1) {
		for _, family := range scopedFamilies {
			if tok == family {
				return strings.Contains(model, family)
			}
		}
	}
	return true
}
