package project

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// A trigger schedule sync that fails after a project delete or restore is
// retried in the background by the same process (trigger_schedule_retry.go),
// so the schedule converges with no restart.

var errTemporalUnavailable = errors.New("test: temporal is unavailable")

// temporalSchedules stands in for the triggers' Temporal Schedules: each
// Sync reads the trigger's row, as triggers.Syncer does, and leaves the
// schedule paused exactly when the trigger is off. fail decides which calls
// (numbered from 1) fail before reaching it; hold, when set, runs between
// the read and the write.
type temporalSchedules struct {
	repo *db.Repo

	mu     sync.Mutex
	calls  int
	paused map[string]bool
	fail   func(call int) bool
	hold   func(call int)
}

func (s *temporalSchedules) Sync(ctx context.Context, id string) error {
	s.mu.Lock()
	s.calls++
	call, fail, hold := s.calls, s.fail, s.hold
	s.mu.Unlock()
	if fail != nil && fail(call) {
		return errTemporalUnavailable
	}
	tr, err := s.repo.GetTrigger(ctx, id)
	if err != nil {
		return err
	}
	if hold != nil {
		hold(call)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused == nil {
		s.paused = map[string]bool{}
	}
	s.paused[id] = !tr.Enabled
	return nil
}

// schedule reports whether id's schedule exists, and whether it is paused.
func (s *temporalSchedules) schedule(id string) (paused, exists bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paused, exists = s.paused[id]
	return paused, exists
}

func (s *temporalSchedules) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *temporalSchedules) failing(fail func(call int) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
}

// retryingService is the project service over f's database and temporal,
// retrying a failed sync at once, up to attempts times.
func retryingService(t *testing.T, f *deleteAuthFixture, temporal *temporalSchedules, attempts int) (*Service, context.Context) {
	t.Helper()
	m := orgs.New(f.members)
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: f.repo, Orgs: m, Schedules: temporal, Now: func() time.Time { return deleteNow }}))
	svc.SetScheduleRetry(0, 0, attempts)
	return svc, as(m, f.owner, auth.CredentialSession, "")
}

func TestProjectDeleteAndRestoreRetryAFailedTriggerScheduleSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	f := newDeleteAuthFixture(t)
	temporal := &temporalSchedules{repo: f.repo}
	svc, owner := retryingService(t, f, temporal, 5)

	// The delete's own sync fails; the retry pauses the schedule.
	temporal.failing(func(call int) bool { return call == 1 })
	_, err := f.delete(svc, owner, false)
	require.NoError(t, err, "a failed sync never fails the delete")
	svc.WaitForScheduleRetries(t, ctx)
	paused, exists := temporal.schedule(f.trigger)
	require.True(t, exists, "the retry converged the schedule")
	assert.True(t, paused, "the deleted project's trigger is paused in Temporal")
	assert.Equal(t, 2, temporal.callCount(), "the failed sync, then one retry")

	// The restore's own sync fails; the retry resumes the schedule.
	temporal.failing(func(call int) bool { return call == 3 })
	restored, err := svc.RestoreProject(owner, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: f.project}))
	require.NoError(t, err, "a failed sync never fails the restore")
	assert.EqualValues(t, 1, restored.Msg.GetTriggersResumed())
	svc.WaitForScheduleRetries(t, ctx)
	paused, _ = temporal.schedule(f.trigger)
	assert.False(t, paused, "the restored trigger fires again in Temporal, with no restart")
	assert.Equal(t, 4, temporal.callCount())
}

func TestTriggerScheduleRetryGivesUpAfterItsAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	f := newDeleteAuthFixture(t)
	temporal := &temporalSchedules{repo: f.repo}
	temporal.failing(func(int) bool { return true })
	svc, owner := retryingService(t, f, temporal, 3)

	_, err := f.delete(svc, owner, false)
	require.NoError(t, err)
	svc.WaitForScheduleRetries(t, ctx)
	assert.Equal(t, 1+3, temporal.callCount(), "the request's sync, then the bounded retries")
	_, exists := temporal.schedule(f.trigger)
	assert.False(t, exists, "left for the startup sync")

	// A later converge starts a fresh retry: the abandoned one left no claim.
	temporal.failing(func(call int) bool { return call == 5 })
	svc.convergeTriggerSchedules(owner, []string{f.trigger})
	svc.WaitForScheduleRetries(t, ctx)
	paused, exists := temporal.schedule(f.trigger)
	require.True(t, exists)
	assert.True(t, paused)
}

// A retry's attempt that read the row before a later write must not have the
// last word: the later converge makes the running retry converge once more.
func TestTriggerScheduleRetryConvergesAgainAfterALaterWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	f := newDeleteAuthFixture(t)
	temporal := &temporalSchedules{repo: f.repo}
	svc, owner := retryingService(t, f, temporal, 5)
	require.NoError(t, f.repo.SetTriggerEnabled(ctx, f.trigger, false))

	// Call 1 (the request's) fails; call 2 (the retry's) reads the row while
	// the trigger is off and is held there until the test lets it write.
	read, write := make(chan struct{}), make(chan struct{})
	temporal.mu.Lock()
	temporal.fail = func(call int) bool { return call == 1 }
	temporal.hold = func(call int) {
		if call == 2 {
			close(read)
			<-write
		}
	}
	temporal.mu.Unlock()
	svc.convergeTriggerSchedules(owner, []string{f.trigger})
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("the retry never read the row")
	}

	// A write turns the trigger on, and its own sync (call 3) converges.
	require.NoError(t, f.repo.SetTriggerEnabled(ctx, f.trigger, true))
	svc.convergeTriggerSchedules(owner, []string{f.trigger})
	paused, _ := temporal.schedule(f.trigger)
	require.False(t, paused)

	// The held attempt now writes what it read (paused); the retry converges
	// once more (call 4) from the row as it is.
	close(write)
	svc.WaitForScheduleRetries(t, ctx)
	paused, _ = temporal.schedule(f.trigger)
	assert.False(t, paused, "the stale attempt is not the last word")
	assert.Equal(t, 4, temporal.callCount())
}
