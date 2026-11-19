package project

import (
	"context"
	"fmt"
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
//
// Retries belong to the service's lifetime, not the request's: they keep the
// request's values (logging, tracing) but not its cancellation, and they end
// when the service closes (Service.Close, a lifecycle Close the server's
// supervisor calls at shutdown). Closing cancels every backoff wait and
// in-flight attempt, starts no new retry, and waits for the running ones to
// exit; what they did not converge is the startup sync's.

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
	// after, when set, replaces the backoff timer (the tests).
	after func(d time.Duration) <-chan time.Time
	// life is the retries' lifetime, ended by stop when the service closes;
	// made on first use, so the zero value works. closed refuses new
	// retries once Close began.
	life   context.Context
	stop   context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

// lifetime is the retries' context, made on first use. Callers hold r.mu.
func (r *scheduleRetries) lifetime() context.Context {
	if r.life == nil {
		r.life, r.stop = context.WithCancel(context.Background())
	}
	return r.life
}

// wait blocks for d, or until ctx ends: false then.
func (r *scheduleRetries) wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	r.mu.Lock()
	after := r.after
	r.mu.Unlock()
	var fired <-chan time.Time
	if after != nil {
		fired = after(d)
	} else {
		timer := time.NewTimer(d)
		defer timer.Stop()
		fired = timer.C
	}
	select {
	case <-fired:
		return true
	case <-ctx.Done():
		return false
	}
}

// close ends the retries' lifetime and waits, until ctx ends, for the
// running ones to exit.
func (r *scheduleRetries) close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	if r.stop != nil {
		r.stop()
	}
	r.mu.Unlock()
	exited := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(exited)
	}()
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("project: trigger schedule retries still running at shutdown: %w", context.Cause(ctx))
	}
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

// claim starts tracking a retry for triggerID and returns the retries'
// lifetime; false when one already runs (now told to converge again) or the
// service has closed.
func (r *scheduleRetries) claim(triggerID string) (context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false
	}
	if _, running := r.again[triggerID]; running {
		r.again[triggerID] = true
		return nil, false
	}
	if r.again == nil {
		r.again = map[string]bool{}
	}
	r.again[triggerID] = false
	r.wg.Add(1)
	return r.lifetime(), true
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

// Close ends the background schedule retries and waits, until ctx ends, for
// them to exit (see the top of this file). The server's lifecycle calls it
// at shutdown.
func (s *Service) Close(ctx context.Context) error {
	return s.scheduleRetries.close(ctx)
}

// retryScheduleSync retries converging triggerID's schedule in the
// background: with the request's values, but ended by the service's
// lifetime rather than the request's.
func (s *Service) retryScheduleSync(ctx context.Context, triggerID string) {
	life, ok := s.scheduleRetries.claim(triggerID)
	if !ok {
		return
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	unhook := context.AfterFunc(life, cancel)
	go func() {
		defer s.scheduleRetries.wg.Done()
		defer cancel()
		defer unhook()
		s.runScheduleRetry(bg, triggerID)
	}()
}

// runScheduleRetry is one trigger's retry loop (see the top of this file).
func (s *Service) runScheduleRetry(ctx context.Context, triggerID string) {
	policy := s.scheduleRetries.currentPolicy()
	wait := policy.firstWait
	var err error
	for attempt := 1; attempt <= policy.attempts; attempt++ {
		if !s.scheduleRetries.wait(ctx, wait) {
			s.scheduleRetries.abandon(triggerID)
			logging.InfoContext(ctx, "[project] stopped retrying a trigger's schedule at shutdown; the next startup sync converges it",
				"trigger_id", triggerID, "attempt", attempt, "error", err)
			return
		}
		s.scheduleRetries.begin(triggerID)
		attemptCtx, cancel := context.WithTimeout(ctx, policy.attemptBudget)
		// Only convergeTriggerSchedules starts a retry, after its own nil-guard.
		err = s.deps.Schedules.Sync(attemptCtx, triggerID) // forge:optional-checked
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
