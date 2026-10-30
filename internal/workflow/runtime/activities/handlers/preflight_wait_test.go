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
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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
	// onWake, when set, runs on every EnsureAwake: a daemon that comes up
	// because it was woken, whenever the activity gets round to waking it.
	onWake func()
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
	if r.onWake != nil {
		r.onWake()
	}
	return "d1", r.wakeErr
}

// markerRepo records every daemon-blocked write over a real repository.
type markerRepo struct {
	*db.Repo
	mu     sync.Mutex
	writes []bool
}

func (r *markerRepo) SetChatDaemonBlocked(_ context.Context, _ string, blocked bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, blocked)
	return nil
}

var (
	_ PreflightDaemonCheckStore = (*markerRepo)(nil)
	_ MachineResumeWakeStore    = (*markerRepo)(nil)
)

func (r *markerRepo) got() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.writes...)
}

// machineStateRouter is a waitFakeRouter that also answers the registry's
// view of the machine (MachineState), which the test sets.
type machineStateRouter struct {
	*waitFakeRouter
	mu    sync.Mutex
	state toolexec.MachineState
}

func (r *machineStateRouter) set(state toolexec.MachineState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
}

func (r *machineStateRouter) MachineState(context.Context, string, *toolexec.DaemonSelector) (toolexec.MachineState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, nil
}

// chatActivity is the chat's activity as the chat list reads it
// (chats_with_activity).
func chatActivity(t *testing.T, repo RunOwnerReader, chatID string) reliantv1.ChatActivity {
	t.Helper()
	chat, err := repo.GetChat(context.Background(), chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.Activity)
	return reliantv1.ChatActivity(*chat.Activity)
}

func newWaitFixture(t *testing.T, noMachine bool, router toolexec.DaemonRouter) (*PreflightDaemonCheckActivity, *markerRepo, string) {
	t.Helper()
	ctx := context.Background()
	base := db.NewTestRepo(t)
	t.Cleanup(func() { base.Close() })
	projectID, chatID := uuid.NewString(), uuid.NewString()
	require.NoError(t, base.CreateProject(ctx, &db.Project{ID: projectID, UserID: "u1", Name: "p", Path: "/tmp/p",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, base.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "u1", Title: "t", ProjectID: projectID,
		State: db.ChatStateIdle, NoMachine: noMachine, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	repo := &markerRepo{Repo: base}
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
	t.Parallel()

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
	t.Parallel()

	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	// Online a little after the wake, so mid-slice: timed from the wake
	// rather than from the test's start, which a loaded machine can let pass
	// before the activity has even checked once.
	router.onWake = func() {
		go func() {
			time.Sleep(60 * time.Millisecond)
			router.online.Store(true)
		}()
	}
	a, repo, chatID := newWaitFixture(t, false, router)
	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
	assert.False(t, out.Waiting)
	assert.EqualValues(t, 1, router.wakeCalls.Load(), "wakes once per execution")
	assert.Equal(t, []bool{true, false}, repo.got())
}

func TestPreflightWait_SliceEndReportsWaiting(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	if testing.Short() {
		t.Skip("-short: waits a full one-second slice; runs in task test")
	}
	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	a, repo, chatID := newWaitFixture(t, false, router)
	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 1, Final: true, WaitBudgetSeconds: 600})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "didn't come online within 10 minutes", "the message names the workflow's budget")
	assert.Contains(t, err.Error(), queuedForMachineNote, "the message is not dropped with the run")
	assert.Equal(t, []bool{true, false}, repo.got())
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID))
}

// The 6-hour cap reads as hours, not as 360 minutes.
func TestPreflightWait_FinalSliceNamesAnHoursBudgetInHours(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "within 6 hours", waitBudgetPhrase(6*60*60))
	assert.Equal(t, "within 10 minutes", waitBudgetPhrase(600))
}

func TestPreflightWait_HardWakeErrorFailsWithCause(t *testing.T) {
	t.Parallel()

	router := &waitFakeRouter{wakeErr: errors.New("no daemon registered for this account")}
	a, repo, chatID := newWaitFixture(t, false, router)
	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this workflow requires a daemon but none is available")
	assert.Contains(t, err.Error(), "no daemon registered for this account")
	assert.NotContains(t, repo.got(), true, "an unavailable daemon never parks the run")
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID),
		"the message waits for a machine to connect rather than being dropped")
}

// The queue records the machine the run waited for, so delivery waits for
// that machine — not the user's default one, which may well be up (prod,
// 2026-10-26: a workflow naming a machine that never connected was delivered,
// failed here and re-queued ~10k times).
func TestPreflightWait_QueueRecordsTheMachineTheRunWaitedFor(t *testing.T) {
	t.Parallel()

	router := &waitFakeRouter{wakeErr: errors.New("no daemon available: the machine this request asked for is not connected")}
	a, repo, chatID := newWaitFixture(t, false, router)

	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5,
		DaemonSelector: &activitytypes.DaemonSelector{Name: "cicd"}})
	require.Error(t, err)

	queued, err := repo.ListChatsQueuedForMachine(context.Background(), "u1", 10)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, chatID, queued[0].ChatID)
	require.NotNil(t, queued[0].Machine)
	assert.Equal(t, db.QueuedMachine{Name: "cicd"}, *queued[0].Machine)

	// A deliverer's claim and requeue keeps the machine.
	_, err = repo.SetChatQueuedForMachine(context.Background(), chatID, false)
	require.NoError(t, err)
	_, err = repo.SetChatQueuedForMachine(context.Background(), chatID, true)
	require.NoError(t, err)
	queued, err = repo.ListChatsQueuedForMachine(context.Background(), "u1", 10)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.NotNil(t, queued[0].Machine)
	assert.Equal(t, "cicd", queued[0].Machine.Name)
}

// A machine that failed to start is not coming up however long the run
// waits — the prod workspace crash-looped for hours. The wait ends at once,
// naming the control plane's reason, and the message stays queued for the
// machine instead of being dropped with the run.
func TestPreflightWait_FailedMachineEndsTheWaitAndQueuesTheMessage(t *testing.T) {
	t.Parallel()

	router := &machineStateRouter{waitFakeRouter: &waitFakeRouter{}}
	router.set(toolexec.MachineState{DaemonID: "d1", Exists: true, Failed: true, StatusMessage: "tools-daemon exited with code 1."})
	a, repo, chatID := newWaitFixture(t, false, router)

	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Your machine failed to start: tools-daemon exited with code 1.")
	assert.Contains(t, err.Error(), queuedForMachineNote)
	assert.NotContains(t, repo.got(), true, "a failed machine never parks the run")
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID))
}

// A machine that fails (or is removed) while the run waits ends the wait at
// the slice's end rather than after the whole budget.
func TestPreflightWait_MachineRemovedDuringTheSliceEndsTheWait(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("-short: waits a full one-second slice; runs in task test")
	}
	router := &machineStateRouter{waitFakeRouter: &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}}
	router.set(toolexec.MachineState{DaemonID: "d1", Exists: true})
	// Removed a little after the wake, so mid-slice (timed from the wake, not
	// from the test's start; see TestPreflightWait_BecomesOnlineDuringSlice).
	router.onWake = func() {
		go func() {
			time.Sleep(100 * time.Millisecond)
			router.set(toolexec.MachineState{})
		}()
	}
	a, repo, chatID := newWaitFixture(t, false, router)

	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 1})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "This chat's machine was removed")
	assert.Equal(t, []bool{true, false}, repo.got())
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID))
}

// The run that finds the machine up is the one that reads the queued message:
// it clears the marker an earlier run left.
func TestPreflightWait_ReadyMachineClearsTheQueuedMarker(t *testing.T) {
	t.Parallel()

	router := &waitFakeRouter{}
	router.online.Store(true)
	a, repo, chatID := newWaitFixture(t, false, router)
	changed, err := repo.SetChatQueuedForMachine(context.Background(), chatID, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID))

	out, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})

	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
	assert.NotEqual(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, chatActivity(t, repo, chatID))
}

func TestPreflightWait_NoMachineNeverWakes(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	router := &waitFakeRouter{wakeErr: toolexec.ErrDaemonPending}
	router.online.Store(true)
	router.onlineIDs.Store("daemon-a", true)
	a, repo, chatID := newWaitFixture(t, false, router)

	out, err := runPreflight(t, a, PreflightDaemonCheckInput{
		ChatID: chatID, DaemonSelector: &activitytypes.DaemonSelector{ID: "daemon-b"}})
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
		ChatID: chatID, DaemonSelector: &activitytypes.DaemonSelector{ID: "daemon-b"}})
	require.NoError(t, err)
	assert.True(t, out.DaemonAvailable)
}

// A run whose machine is offline or gone fails as a user error, so it
// crosses the activity boundary benign and its failure is logged at INFO, not
// raised as an issue (prod: ~9.5k ERROR records from one offline machine). A
// wake that failed for a reason of ours stays a server fault.
func TestPreflightTerminalErrors_OfflineIsAUserErrorOursIsNot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, svcerr.ClassUser, errclass.Classify(errPreflightNoDaemon(nil)))
	assert.Equal(t, svcerr.ClassUser, errclass.Classify(errPreflightNoDaemon(
		svcerr.WithMessage(toolexec.ErrNoDaemon, "no daemon available: the machine this request asked for is not connected"))))
	assert.Equal(t, svcerr.ClassServer, errclass.Classify(errPreflightNoDaemon(errors.New("control plane ResumeDaemon(d1): internal"))))

	removed := svcerr.WithClass(errors.New("This chat's machine was removed"), svcerr.ClassUser)
	assert.Equal(t, svcerr.ClassUser, errclass.Classify(keepClass(removed, errors.New("removed. queued"))))
	assert.Equal(t, svcerr.ClassServer, errclass.Classify(keepClass(errors.New("Your machine failed to start"), errors.New("failed. queued"))))
}
