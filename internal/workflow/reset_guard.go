// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"sync"
	"time"
)

const (
	// DefaultMaxResetAttempts bounds how many times a single workflow may be
	// reset-and-replayed WITHOUT forward progress before the reset path gives up
	// and the caller falls back to the coarse fresh-restart-with-checkpoint (or
	// surfaces the error). A deterministic failure — bad CEL, a workflow-code
	// panic, or a non-deterministic replay wedge after a code change — replays to
	// the same failure every time, so resetting forever would spin. Two attempts
	// absorb a genuinely transient re-failure while surfacing a stuck run quickly.
	DefaultMaxResetAttempts = 2

	// resetAttemptTTL ages out a workflow's reset streak. A workflow quiet this
	// long is treated as fresh: a much-later interruption is a new incident, not
	// a continuation of an old streak.
	resetAttemptTTL = 30 * time.Minute
)

// ResetAttemptGuard bounds reset-and-replay attempts per workflow so a
// deterministic failure cannot be reset forever. It is the single shared home
// for the attempt counter used by the two components that reset workflows:
//   - the reconciler's automatic stuck/interrupted recovery, and
//   - the user-driven resume path invoked from SendMessage for a Failed run.
//
// Tracking is in-memory and best-effort, matching the reconciler's existing
// debounce rationale: a process restart merely restarts the streak, and the
// terminal CAS transitions guard against duplicate destructive actions. "Forward
// progress" is measured by the run's Temporal HistoryLength: if a reset run
// progressed past the previous attempt's length before failing again, the reset
// worked and the streak is cleared — only resets that keep re-failing at the
// same point accumulate toward the bound.
//
// A nil *ResetAttemptGuard is valid and always allows (guarding is optional).
type ResetAttemptGuard struct {
	mu       sync.Mutex
	attempts map[string]*resetAttempt
	max      int
}

type resetAttempt struct {
	count          int
	lastHistoryLen int64
	firstAt        time.Time
	lastAt         time.Time
}

// NewResetAttemptGuard creates a guard bounding resets at max attempts without
// progress. A non-positive max falls back to DefaultMaxResetAttempts.
func NewResetAttemptGuard(max int) *ResetAttemptGuard {
	if max <= 0 {
		max = DefaultMaxResetAttempts
	}
	return &ResetAttemptGuard{attempts: map[string]*resetAttempt{}, max: max}
}

// Allow reports whether workflowID may be reset again, given the current Temporal
// HistoryLength of the run about to be reset. Growth beyond the last recorded
// attempt's length is forward progress and clears the streak. Allow does NOT
// record the attempt — call Record after a reset is actually issued.
func (g *ResetAttemptGuard) Allow(workflowID string, historyLen int64) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	a := g.attempts[workflowID]
	if a == nil {
		return true
	}
	if time.Since(a.lastAt) > resetAttemptTTL || historyLen > a.lastHistoryLen {
		delete(g.attempts, workflowID)
		return true
	}
	return a.count < g.max
}

// Record notes that a reset was issued for workflowID at the given pre-reset
// HistoryLength. Forward progress since the last attempt (or an aged-out streak)
// restarts the count at 1.
func (g *ResetAttemptGuard) Record(workflowID string, historyLen int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	a := g.attempts[workflowID]
	if a == nil || time.Since(a.lastAt) > resetAttemptTTL || historyLen > a.lastHistoryLen {
		a = &resetAttempt{firstAt: now}
		g.attempts[workflowID] = a
	}
	a.count++
	a.lastHistoryLen = historyLen
	a.lastAt = now
}

// Attempts returns the current reset-attempt count for a workflow (0 if none).
func (g *ResetAttemptGuard) Attempts(workflowID string) int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if a := g.attempts[workflowID]; a != nil {
		return a.count
	}
	return 0
}

// Clear drops the streak for a workflow (on confirmed forward progress or
// completion).
func (g *ResetAttemptGuard) Clear(workflowID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.attempts, workflowID)
}

// Prune drops streaks for workflows absent from keep, bounding the map.
func (g *ResetAttemptGuard) Prune(keep map[string]bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.attempts {
		if !keep[id] {
			delete(g.attempts, id)
		}
	}
}
