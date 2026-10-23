// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// runWakeRouter is a machine the idle policy put to sleep: offline until
// WakeForRun is called, then online after attachAfter polls. It has no
// EnsureAwake semantics of its own — the resume wake must use WakeForRun.
type runWakeRouter struct {
	*machineStateRouter
	runWakes    atomic.Int32
	runWakeErr  error
	attachAfter int32
	polls       atomic.Int32
}

func (r *runWakeRouter) WakeForRun(_ context.Context, _ string, _ *toolexec.DaemonSelector) (toolexec.WakeResult, error) {
	r.runWakes.Add(1)
	if r.runWakeErr != nil {
		return toolexec.WakeResult{}, r.runWakeErr
	}
	return toolexec.WakeResult{DaemonID: "d1", Resumed: true}, nil
}

func (r *runWakeRouter) IsDaemonOnline(_ context.Context, _ string, _ *toolexec.DaemonSelector) (bool, error) {
	if r.runWakes.Load() == 0 {
		return false, nil
	}
	return r.polls.Add(1) > r.attachAfter, nil
}

func newRunWakeRouter() *runWakeRouter {
	inner := &machineStateRouter{waitFakeRouter: &waitFakeRouter{}}
	inner.set(toolexec.MachineState{DaemonID: "d1", Exists: true, Suspended: true})
	return &runWakeRouter{machineStateRouter: inner, attachAfter: 2}
}

func newResumeWakeFixture(t *testing.T, router toolexec.DaemonRouter) (*MachineResumeWakeActivity, *markerRepo, string) {
	t.Helper()
	ctx := context.Background()
	base := db.NewTestRepo(t)
	t.Cleanup(func() { base.Close() })
	projectID, chatID := uuid.NewString(), uuid.NewString()
	require.NoError(t, base.CreateProject(ctx, &db.Project{ID: projectID, UserID: "u1", Name: "p", Path: "/tmp/p",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, base.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "u1", Title: "t", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	repo := &markerRepo{Repo: base}
	a := NewMachineResumeWakeActivity(repo, toolexec.NewRemoteExecutor(router))
	a.pollInterval = 5 * time.Millisecond
	return a, repo, chatID
}

func runResumeWake(t *testing.T, a *MachineResumeWakeActivity, in MachineResumeWakeInput) MachineResumeWakeOutput {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	res, err := env.ExecuteActivity(a.Execute, in)
	require.NoError(t, err)
	var out MachineResumeWakeOutput
	require.NoError(t, res.Get(&out))
	return out
}

// The machine slept through the hold: the resume wakes it with the run-resume
// door (never the attended EnsureAwake), shows the chat waiting for it, and
// returns once it attaches.
func TestMachineResumeWake_WakesTheSleepingMachineAndWaitsForIt(t *testing.T) {
	t.Parallel()

	router := newRunWakeRouter()
	a, repo, chatID := newResumeWakeFixture(t, router)

	out := runResumeWake(t, a, MachineResumeWakeInput{
		ChatID: chatID, ToolNames: []string{"shell"}, ToolsKnown: true, WaitSeconds: 30,
	})

	assert.Equal(t, MachineResumeWakeOutput{Ready: true, Woke: true}, out)
	assert.EqualValues(t, 1, router.runWakes.Load())
	assert.Zero(t, router.wakeCalls.Load(), "a resume never wakes through the attended EnsureAwake")
	assert.Equal(t, []bool{true, false}, repo.got(), "the chat shows the wait, then clears it")
}

// A batch of tools that never touch the machine (web search, fetch) wakes
// nothing, and does not even read the chat.
func TestMachineResumeWake_ServerOnlyBatchWakesNothing(t *testing.T) {
	t.Parallel()

	router := newRunWakeRouter()
	a := NewMachineResumeWakeActivity(nil, toolexec.NewRemoteExecutor(router))

	out := runResumeWake(t, a, MachineResumeWakeInput{
		ChatID: "unused", ToolNames: []string{"websearch", "fetch"}, ToolsKnown: true, WaitSeconds: 30,
	})

	assert.True(t, out.Ready)
	assert.Zero(t, router.runWakes.Load())
}

// A refused wake (a failed machine, a funding gate) never fails the run: the
// step runs and reports the machine's state, as it always did.
func TestMachineResumeWake_ARefusedWakeLetsTheStepRun(t *testing.T) {
	t.Parallel()

	router := newRunWakeRouter()
	router.runWakeErr = errors.New("compute is not funded")
	a, repo, chatID := newResumeWakeFixture(t, router)

	out := runResumeWake(t, a, MachineResumeWakeInput{ChatID: chatID, WaitSeconds: 30})

	assert.True(t, out.Ready)
	assert.Contains(t, out.NotComing, "compute is not funded")
	assert.Empty(t, repo.got(), "nothing to wait for, so the chat never shows a wait")
}

// A machine still coming up when the slice ends leaves the chat waiting for
// the workflow's next slice — except on the final one, which clears it
// because the step runs now.
func TestMachineResumeWake_SliceEndsWhileTheMachineIsStillComingUp(t *testing.T) {
	t.Parallel()

	for _, final := range []bool{false, true} {
		router := newRunWakeRouter()
		router.attachAfter = 1 << 30
		a, repo, chatID := newResumeWakeFixture(t, router)

		out := runResumeWake(t, a, MachineResumeWakeInput{ChatID: chatID, WaitSeconds: 0, Final: final})

		assert.True(t, out.Waiting)
		if final {
			assert.Equal(t, []bool{true, false}, repo.got())
		} else {
			assert.Equal(t, []bool{true}, repo.got())
		}
	}
}

// A run that continues an earlier one (continue-as-new) wakes its machine the
// way any resuming run does — its session token may be hours old — while a
// freshly started run keeps the attended wake.
func TestPreflight_ARunResumeWakesThroughTheRunDoor(t *testing.T) {
	t.Parallel()

	for _, runResume := range []bool{true, false} {
		router := newRunWakeRouter()
		router.online.Store(false)
		a, _, chatID := newWaitFixture(t, false, router)

		_, _ = runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, RunResume: runResume})

		if runResume {
			assert.EqualValues(t, 1, router.runWakes.Load(), "a continuing run wakes through WakeForRun")
			assert.Zero(t, router.wakeCalls.Load())
		} else {
			assert.Zero(t, router.runWakes.Load())
			assert.EqualValues(t, 1, router.wakeCalls.Load(), "a fresh run keeps the attended EnsureAwake")
		}
	}
}
