package project

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// A delete, restore or purge commits its trigger rows first and converges
// their Temporal Schedules after (convergeTriggerSchedules). When that sync
// fails, Temporal unreachable or slow, the trigger's schedule is retried here,
// in the background, until it converges or the retries run out: a restored
// project's triggers fire again without waiting for a restart's startup sync
// (triggers.SyncAllOnStartup), which stays the net for a process that stops
// mid-retry and for a Temporal outage that outlasts the retries.
//
// A retry is safe to repeat and to race: TriggerSchedules.Sync converges from
// a fresh read of the row, so every attempt converges onto the latest write,
// whichever request made it. One retry runs per trigger at a time; a converge
// asked for while it runs makes it converge once more after its current
// attempt, so an attempt that read the row before a later write never has the
// last word.

// scheduleRetryPolicy bounds a trigger's background retries: the wait before
// the first, which doubles after each failure up to maxWait, and how many
// attempts are made in all. The defaults retry for about a quarter hour.
type scheduleRetryPolicy struct {
	firstWait, maxWait time.Duration
	attempts           int
	// attemptBudget bounds one attempt's Temporal calls.
	attemptBudget time.Duration
}

var defaultScheduleRetryPolicy = scheduleRetryPolicy{
	firstWait:     2 * time.Second,
	maxWait:       time.Minute,
	attempts:      20,
	attemptBudget: 30 * time.Second,
}

// scheduleRetries tracks the triggers whose schedules are being retried. The
// zero value is ready to use.
type scheduleRetries struct {
	mu sync.Mutex
	// again maps a trigger with a retry running to whether a converge was
	// asked for since its current attempt began.
	again map[string]bool
	// policy overrides defaultScheduleRetryPolicy when set (the tests).
	policy *scheduleRetryPolicy
	wg     sync.WaitGroup
}

// note records that a converge of triggerID was asked for: true when a retry
// is running for it, which then converges once more.
func (r *scheduleRetries) note(triggerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, running := r.again[triggerID]; !running {
		return false
	}
	r.again[triggerID] = true
	return true
}

// claim starts tracking a retry for triggerID, false when one already runs
// (which note has told to converge again).
func (r *scheduleRetries) claim(triggerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, running := r.again[triggerID]; running {
		r.again[triggerID] = true
		return false
	}
	if r.again == nil {
		r.again = map[string]bool{}
	}
	r.again[triggerID] = false
	r.wg.Add(1)
	return true
}

// begin starts an attempt: what was asked for until now, it converges.
func (r *scheduleRetries) begin(triggerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.again[triggerID] = false
}

// finish ends the retry of triggerID after a successful attempt, unless a
// converge was asked for during it: then it reports false, and the retry
// goes on.
func (r *scheduleRetries) finish(triggerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.again[triggerID] {
		return false
	}
	delete(r.again, triggerID)
	return true
}

// abandon ends the retry of triggerID once its attempts ran out.
func (r *scheduleRetries) abandon(triggerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.again, triggerID)
}

func (r *scheduleRetries) currentPolicy() scheduleRetryPolicy {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policy != nil {
		return *r.policy
	}
	return defaultScheduleRetryPolicy
}

// retryScheduleSync retries converging triggerID's schedule in the
// background, detached from the request that asked for it.
func (s *Service) retryScheduleSync(ctx context.Context, triggerID string) {
	if !s.scheduleRetries.claim(triggerID) {
		return
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		defer s.scheduleRetries.wg.Done()
		s.runScheduleRetry(bg, triggerID)
	}()
}

// runScheduleRetry is one trigger's retry loop (see the top of this file).
func (s *Service) runScheduleRetry(ctx context.Context, triggerID string) {
	policy := s.scheduleRetries.currentPolicy()
	wait := policy.firstWait
	var err error
	for attempt := 1; attempt <= policy.attempts; attempt++ {
		timer := time.NewTimer(wait)
		<-timer.C
		s.scheduleRetries.begin(triggerID)
		attemptCtx, cancel := context.WithTimeout(ctx, policy.attemptBudget)
		err = s.deps.Schedules.Sync(attemptCtx, triggerID)
		cancel()
		if err == nil {
			if s.scheduleRetries.finish(triggerID) {
				logging.InfoContext(ctx, "[project] converged a trigger's schedule on a retry", "trigger_id", triggerID, "attempt", attempt)
				return
			}
			// Asked again while this attempt ran: converge once more, now.
			wait = 0
			continue
		}
		wait = min(max(wait*2, policy.firstWait), policy.maxWait)
	}
	s.scheduleRetries.abandon(triggerID)
	logging.ErrorContext(ctx, "[project] a trigger's schedule did not converge after a project delete or restore; the next startup sync repairs it",
		"trigger_id", triggerID, "attempts", policy.attempts, "error", err)
}
