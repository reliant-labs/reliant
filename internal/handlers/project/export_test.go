package project

import (
	"context"
	"testing"
	"time"
)

// SetGitCheckBudget shrinks GetProject's daemon git-check budget for one
// test. The test must not run in parallel: the budget is package state.
func SetGitCheckBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gitCheckBudget
	gitCheckBudget = d
	t.Cleanup(func() { gitCheckBudget = prev })
}

// WaitForPurges blocks until every immediate purge s started after its
// request returned has finished.
func (s *Service) WaitForPurges() { s.purges.Wait() }

// SetScheduleRetry makes s retry a trigger schedule sync that failed after
// wait (doubling up to maxWait), attempts times in all. Per service, so
// parallel-safe.
func (s *Service) SetScheduleRetry(wait, maxWait time.Duration, attempts int) {
	s.scheduleRetries.mu.Lock()
	defer s.scheduleRetries.mu.Unlock()
	s.scheduleRetries.policy = &scheduleRetryPolicy{firstWait: wait, maxWait: maxWait, attempts: attempts, attemptBudget: time.Minute}
}

// WaitForScheduleRetries blocks until every background schedule retry s
// started has converged or given up, or fails the test once ctx ends.
func (s *Service) WaitForScheduleRetries(t *testing.T, ctx context.Context) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.scheduleRetries.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("the background schedule retries did not finish: %v", context.Cause(ctx))
	}
}

// SetMoveWait shrinks how long MoveProjectToOrg waits for a move's saga, and
// how often it looks, for one test. Not parallel-safe: package state.
func SetMoveWait(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	prevWait, prevPoll := moveWait, movePoll
	moveWait, movePoll = wait, poll
	t.Cleanup(func() { moveWait, movePoll = prevWait, prevPoll })
}
