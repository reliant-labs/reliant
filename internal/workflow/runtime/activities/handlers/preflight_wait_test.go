// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFakeRouter is a DaemonRouter whose liveness and wake outcome the test
// controls. Only the methods preflight touches are implemented.
type waitFakeRouter struct {
	toolexec.DaemonRouter
	online    atomic.Bool
	wakeErr   error
	wakeCalls atomic.Int32

	// onlineIDs answers selector-by-ID questions per daemon; a nil selector or
	// one without an ID falls back to online (any daemon).
	onlineIDs sync.Map
	wokenMu   sync.Mutex
	woken     []*toolexec.DaemonSelector
}

func (r *waitFakeRouter) IsDaemonOnline(_ context.Context, _ string, sel *toolexec.DaemonSelector) (bool, error) {
	if sel != nil && sel.ID != "" {
		_, up := r.onlineIDs.Load(sel.ID)
		return up, nil
	}
	return r.online.Load(), nil
}

func (r *waitFakeRouter) EnsureAwake(_ context.Context, _ string, sel *toolexec.DaemonSelector) (string, error) {
	r.wakeCalls.Add(1)
	r.wokenMu.Lock()
	r.woken = append(r.woken, sel)
	r.wokenMu.Unlock()
	return "d1", r.wakeErr
}

// markerRepo records every daemon-blocked write.
type markerRepo struct {
	db.Repository
	mu     sync.Mutex
	writes []bool
}

func (r *markerRepo) SetChatDaemonBlocked(_ context.Context, _ string, blocked bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, blocked)
	return nil
}

func (r *markerRepo) got() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.writes...)
}

func newWaitFixture(t *testing.T, noMachine bool, router *waitFakeRouter) (*PreflightDaemonCheckActivity, *markerRepo, string) {
	t.Helper()
	ctx := context.Background()
	base := db.NewTestRepo(t)
	t.Cleanup(func() { base.Close() })
	projectID, chatID := uuid.NewString(), uuid.NewString()
	require.NoError(t, base.CreateProject(ctx, &db.Project{ID: projectID, UserID: "u1", Name: "p", Path: "/tmp/p",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, base.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "u1", Title: "t", ProjectID: projectID,
		State: db.ChatStateIdle, NoMachine: noMachine, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	repo := &markerRepo{Repository: base}
	a := NewPreflightDaemonCheckActivity(repo, toolexec.NewRemoteExecutor(router))
	a.pollInterval = 10 * time.Millisecond
	return a, repo, chatID
}

func runPreflight(t *testing.T, a *PreflightDaemonCheckActivity, in PreflightDaemonCheckInput) (PreflightDaemonCheckOutput, error) {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, in)
	if err != nil {
		return PreflightDaemonCheckOutput{}, err
	}
	var out PreflightDaemonCheckOutput
	require.NoError(t, val.Get(&out))
	return out, nil
}

func TestPreflightWait_OnlineIsReadyWithoutMarker(t *testing.T) {
	router := &waitFakeRouter{}
	router.online.Store(true)
	a, repo, chatID := newWaitFixture(t, false, router)
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
	assert.False(t, out.Waiting)
	assert.Zero(t, router.wakeCalls.Load())
	assert.Empty(t, repo.got())
}

func TestPreflightWait_BecomesOnlineDuringSlice(t *testing.T) {
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	a, repo, chatID := newWaitFixture(t, false, router)
	go func() {
		time.Sleep(60 * time.Millisecond)
		router.online.Store(true)
	}()
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
	assert.False(t, out.Waiting)
	assert.EqualValues(t, 1, router.wakeCalls.Load(), "wakes once per execution")
	assert.Equal(t, []bool{true, false}, repo.got())
}

func TestPreflightWait_SliceEndReportsWaiting(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: waits a full one-second slice; runs in task test")
	}
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	a, repo, chatID := newWaitFixture(t, false, router)
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 1})
	require.NoError(t, err)
	assert.True(t, out.Waiting)
	assert.False(t, out.DaemonAvailable)
	assert.Equal(t, []bool{true}, repo.got(), "marker stays set between slices")
}

func TestPreflightWait_FinalSliceFailsAndClearsMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: waits a full one-second slice; runs in task test")
	}
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	a, repo, chatID := newWaitFixture(t, false, router)
	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 1, Final: true, WaitBudgetSeconds: 600})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "didn't come online within 10 minutes", "the message names the workflow's budget")
	assert.Equal(t, []bool{true, false}, repo.got())
}

func TestPreflightWait_HardWakeErrorFailsWithCause(t *testing.T) {
	router := &waitFakeRouter{wakeErr: errors.New("no daemon registered for this account")}
	a, repo, chatID := newWaitFixture(t, false, router)
	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this workflow requires a daemon but none is available")
	assert.Contains(t, err.Error(), "no daemon registered for this account")
	assert.Empty(t, repo.got(), "an unavailable daemon never parks the run")
}

func TestPreflightWait_NoMachineNeverWakes(t *testing.T) {
	router := &waitFakeRouter{}
	a, repo, chatID := newWaitFixture(t, true, router)
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.NoError(t, err)
	assert.False(t, out.DaemonAvailable)
	assert.False(t, out.Waiting)
	assert.Zero(t, router.wakeCalls.Load())
	assert.Empty(t, repo.got())
}

func TestPreflightWait_ZeroSecondsIsSingleCheck(t *testing.T) {
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	a, _, chatID := newWaitFixture(t, false, router)
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID})
	require.NoError(t, err)
	assert.True(t, out.Waiting)

	_, err = runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, Final: true})
	require.Error(t, err)
}

// Daemon A being online must not satisfy a run pinned to suspended daemon B:
// B is woken with its own selector and the check reports Waiting, not ready.
func TestPreflightWait_PinnedDaemonOfflineWhileAnotherIsOnline(t *testing.T) {
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	router.online.Store(true)
	router.onlineIDs.Store("daemon-a", true)
	a, repo, chatID := newWaitFixture(t, false, router)

	out, err := runPreflight(t, a, PreflightDaemonCheckInput{
		ChatID: chatID, DaemonSelector: &toolexec.DaemonSelector{ID: "daemon-b"}})
	require.NoError(t, err)
	assert.True(t, out.Waiting)
	assert.False(t, out.DaemonAvailable)
	require.EqualValues(t, 1, router.wakeCalls.Load())
	require.Len(t, router.woken, 1)
	assert.Equal(t, "daemon-b", router.woken[0].ID)
	assert.Equal(t, []bool{true}, repo.got())

	// Once B itself attaches, the wait ends.
	router.onlineIDs.Store("daemon-b", true)
	out, err = runPreflight(t, a, PreflightDaemonCheckInput{
		ChatID: chatID, DaemonSelector: &toolexec.DaemonSelector{ID: "daemon-b"}})
	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
}
