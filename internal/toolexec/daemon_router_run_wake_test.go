// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// fakeRunResumer is control-plane's ResumeDaemonForRun: it records each call
// and can block, so a test can line several runs up behind one wake.
type fakeRunResumer struct {
	mu      sync.Mutex
	calls   []string // "<userID>|<daemonID>"
	err     error
	release chan struct{}
	entered chan struct{}
}

func (f *fakeRunResumer) ResumeDaemonForRun(_ context.Context, userID, daemonID string) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, userID+"|"+daemonID)
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return f.err == nil, f.err
}

func (f *fakeRunResumer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func suspendedRecords(userID string) *fakeDaemonRecords {
	return &fakeDaemonRecords{daemons: []*db.Daemon{{
		ID: "daemon-suspended", UserID: userID, LifecyclePhase: strPtr("suspended"),
	}}}
}

// A resuming run wakes its machine through the run-resume door, with no user
// token at all: the session it started with may be long expired.
func TestWakeForRunWakesTheSuspendedMachineWithoutAUserToken(t *testing.T) {
	rr := &fakeRunResumer{}
	tokenResumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDatabase(suspendedRecords("user-held")),
		WithDaemonResumer(tokenResumer), WithRunResumer(rr))

	res, err := router.WakeForRun(context.Background(), "user-held", pinned)
	require.NoError(t, err)
	assert.Equal(t, WakeResult{DaemonID: "daemon-suspended", Resumed: true}, res)
	assert.Equal(t, []string{"user-held|daemon-suspended"}, rr.calls)
	assert.Empty(t, tokenResumer.calls, "the owner's token is not used when the door exists")
}

// Runs that resume together (sub-agents released by the same usage-limit
// reset) cost one control-plane wake: concurrent callers wait for the one in
// flight, and later ones within the cooldown are answered from memory.
func TestWakeForRunCollapsesRunsResumingTogether(t *testing.T) {
	rr := &fakeRunResumer{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	router := NewNATSDaemonRouter(nil, WithDatabase(suspendedRecords("u")), WithRunResumer(rr))

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	start := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := router.WakeForRun(context.Background(), "u", pinned)
			errs <- err
		}()
	}
	start()
	<-rr.entered // the first wake is in flight
	for i := 0; i < 4; i++ {
		start()
	}
	time.Sleep(20 * time.Millisecond) // let the others queue behind it
	close(rr.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, rr.count(), "five runs, one wake")

	_, err := router.WakeForRun(context.Background(), "u", pinned)
	require.NoError(t, err)
	assert.Equal(t, 1, rr.count(), "a run resuming right after is answered from the cooldown")
}

// A refused wake (a failed machine, a funding gate) is reported, and is not
// cached: the next resume asks again.
func TestWakeForRunReportsARefusalAndDoesNotCacheIt(t *testing.T) {
	refusal := errors.New("compute is not funded")
	rr := &fakeRunResumer{err: refusal}
	router := NewNATSDaemonRouter(nil, WithDatabase(suspendedRecords("u")), WithRunResumer(rr))

	_, err := router.WakeForRun(context.Background(), "u", pinned)
	require.ErrorIs(t, err, refusal)
	_, err = router.WakeForRun(context.Background(), "u", pinned)
	require.ErrorIs(t, err, refusal)
	assert.Equal(t, 2, rr.count())
}

// Against a control plane that predates the door, the wake falls back to the
// owner's token, exactly as EnsureAwake wakes.
func TestWakeForRunFallsBackToTheOwnersTokenOnAnOlderControlPlane(t *testing.T) {
	const userID = "user-old-cp"
	auth.SetUserJWT(userID, "jwt-still-valid")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	rr := &fakeRunResumer{err: ErrRunResumeUnsupported}
	tokenResumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDatabase(suspendedRecords(userID)),
		WithDaemonResumer(tokenResumer), WithRunResumer(rr))

	res, err := router.WakeForRun(context.Background(), userID, pinned)
	require.NoError(t, err)
	assert.True(t, res.Resumed)
	assert.Equal(t, []resumeCall{{token: "jwt-still-valid", daemonID: "daemon-suspended"}}, tokenResumer.calls)
}

// A machine that is up, or already starting, is not woken.
func TestWakeForRunLeavesAMachineThatIsNotAsleepAlone(t *testing.T) {
	rr := &fakeRunResumer{}
	router := NewNATSDaemonRouter(nil, WithRunResumer(rr), WithDatabase(&fakeDaemonRecords{
		daemons: []*db.Daemon{
			{ID: "daemon-live", UserID: "u", LifecyclePhase: strPtr("suspended")},
			{ID: "daemon-booting", UserID: "u", LifecyclePhase: strPtr("provisioning")},
		},
		attached: []string{"daemon-live"},
	}))

	res, err := router.WakeForRun(context.Background(), "u", &DaemonSelector{ID: "daemon-live"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-live", res.DaemonID)
	_, err = router.WakeForRun(context.Background(), "u", &DaemonSelector{ID: "daemon-booting"})
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Zero(t, rr.count())
}
