// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// scriptedObserver answers MachineStatus from a script, one entry per call; the
// last entry repeats. An entry with err set simulates a failed read.
type scriptedObserver struct {
	mu    sync.Mutex
	steps []observerStep
	calls int
	// onCall runs on every call, with the 1-based call number.
	onCall func(n int)
}

type observerStep struct {
	status MachineStatus
	err    error
}

func (o *scriptedObserver) MachineStatus(_ context.Context, userID string) (MachineStatus, error) {
	o.mu.Lock()
	o.calls++
	n := o.calls
	step := o.steps[min(n-1, len(o.steps)-1)]
	hook := o.onCall
	o.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return step.status, step.err
}

func (o *scriptedObserver) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func waitForMachineCtx(ctx context.Context) *rctx.ToolContext {
	return rctx.NewToolContext(ctx, "chat-wait-for-machine", "0", nil, nil)
}

func withUser(ctx context.Context) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, "user-1")
}

// fastWaitTool polls every millisecond and counts timeout_seconds in
// milliseconds, so a "budget" of 30 is 30ms.
func fastWaitTool(o MachineObserver) *waitForMachineTool {
	tool := newWaitForMachineTool(o)
	tool.pollInterval = time.Millisecond
	tool.timeoutUnit = time.Millisecond
	return tool
}

func waitMetadata(t *testing.T, resp ToolResponse) WaitForMachineResponseMetadata {
	t.Helper()
	var md WaitForMachineResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &md))
	return md
}

func steps(statuses ...MachineStatus) []observerStep {
	out := make([]observerStep, len(statuses))
	for i, s := range statuses {
		out[i] = observerStep{status: s}
	}
	return out
}

func TestWaitForMachine_ConnectedAtOnce(t *testing.T) {
	obs := &scriptedObserver{steps: steps(MachineStatus{Connected: true})}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())), WaitForMachineParams{})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "Retry the call that failed")
	assert.Equal(t, 1, obs.callCount())
	md := waitMetadata(t, resp)
	assert.True(t, md.Connected)
	assert.Equal(t, "connected", md.State)
	assert.False(t, md.TimedOut)
}

func TestWaitForMachine_ConnectsAfterSomePolls(t *testing.T) {
	obs := &scriptedObserver{steps: steps(
		MachineStatus{},
		MachineStatus{Starting: true},
		MachineStatus{},
		MachineStatus{Connected: true},
	)}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
		WaitForMachineParams{TimeoutSeconds: 5000})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Equal(t, 4, obs.callCount())
	md := waitMetadata(t, resp)
	assert.True(t, md.Connected)
	assert.False(t, md.TimedOut)
}

// Removed and Failed are terminal: waiting cannot help, so the call returns at
// once with guidance to tell the user and the control plane's own reason.
func TestWaitForMachine_TerminalStatesReturnAtOnce(t *testing.T) {
	tests := []struct {
		name   string
		status MachineStatus
		state  string
		guide  string
	}{
		{"removed", MachineStatus{Removed: true, StatusMessage: "machine deleted by owner"}, "removed", "no longer exists"},
		{"failed", MachineStatus{Failed: true, StatusMessage: "out of capacity"}, "failed", "failed to start"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &scriptedObserver{steps: steps(tc.status)}
			resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
				WaitForMachineParams{TimeoutSeconds: 60000})
			require.NoError(t, err)
			assert.False(t, resp.IsError)
			assert.Equal(t, 1, obs.callCount())
			assert.Contains(t, resp.Content, tc.guide)
			assert.Contains(t, resp.Content, "Tell the user")
			assert.Contains(t, resp.Content, tc.status.StatusMessage)
			md := waitMetadata(t, resp)
			assert.False(t, md.Connected)
			assert.False(t, md.TimedOut)
			assert.Equal(t, tc.state, md.State)
		})
	}
}

// When the budget elapses the answer names the state last seen, differently for
// each, and is a successful result.
func TestWaitForMachine_BudgetElapsedGuidanceByState(t *testing.T) {
	tests := []struct {
		name   string
		status MachineStatus
		state  string
		guide  string
	}{
		{"suspended", MachineStatus{Suspended: true}, "suspended", "asleep"},
		{"starting", MachineStatus{Starting: true, StatusMessage: "cloning repository"}, "starting", "on its way up"},
		{"disconnected", MachineStatus{}, "disconnected", "restarting or reconnecting"},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &scriptedObserver{steps: steps(tc.status)}
			resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
				WaitForMachineParams{TimeoutSeconds: 20})
			require.NoError(t, err)
			assert.False(t, resp.IsError)
			assert.Contains(t, resp.Content, "still not connected")
			assert.Contains(t, resp.Content, tc.guide)
			assert.Contains(t, resp.Content, "wait_for_machine again")
			if tc.status.StatusMessage != "" {
				assert.Contains(t, resp.Content, tc.status.StatusMessage)
			}
			md := waitMetadata(t, resp)
			assert.True(t, md.TimedOut)
			assert.False(t, md.Connected)
			assert.Equal(t, tc.state, md.State)
			assert.GreaterOrEqual(t, md.WaitedMs, int64(20))
			assert.Greater(t, obs.callCount(), 1, "polled more than once")
			seen[resp.Content] = true
		})
	}
	assert.Len(t, seen, len(tests), "each state gets its own guidance")
}

func TestWaitForMachine_SuspendedGuidanceSaysTheRunCannotWakeIt(t *testing.T) {
	obs := &scriptedObserver{steps: steps(MachineStatus{Suspended: true})}
	resp, _ := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
		WaitForMachineParams{TimeoutSeconds: 5})
	assert.Contains(t, resp.Content, "cannot wake it")
	assert.Contains(t, resp.Content, "machines page")
}

// A failed status read is transient: the wait keeps going and still sees the
// machine come back.
func TestWaitForMachine_StatusReadErrorIsTransient(t *testing.T) {
	obs := &scriptedObserver{steps: []observerStep{
		{err: errors.New("registry unavailable")},
		{status: MachineStatus{Suspended: true}},
		{err: errors.New("registry unavailable")},
		{status: MachineStatus{Connected: true}},
	}}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
		WaitForMachineParams{TimeoutSeconds: 5000})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.True(t, waitMetadata(t, resp).Connected)
	assert.Equal(t, 4, obs.callCount())
}

func TestWaitForMachine_OnlyReadErrorsTimesOutAsUnknown(t *testing.T) {
	obs := &scriptedObserver{steps: []observerStep{{err: errors.New("registry unavailable")}}}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
		WaitForMachineParams{TimeoutSeconds: 10})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	md := waitMetadata(t, resp)
	assert.True(t, md.TimedOut)
	assert.Equal(t, "unknown", md.State)
	assert.Contains(t, resp.Content, "could not be read")
}

// Preempted or interrupted: the call answers with a real, non-error result —
// and the detached last look still catches a machine that came back in time.
func TestWaitForMachine_CtxCancelledStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(withUser(context.Background()))
	defer cancel()
	obs := &scriptedObserver{steps: steps(MachineStatus{Suspended: true, StatusMessage: "asleep since 10:00"})}
	obs.onCall = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(ctx), WaitForMachineParams{TimeoutSeconds: 60000})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	md := waitMetadata(t, resp)
	assert.True(t, md.StoppedEarly)
	assert.False(t, md.TimedOut)
	assert.Equal(t, "suspended", md.State)
	assert.Contains(t, resp.Content, "Stopped waiting")
	assert.Contains(t, resp.Content, "asleep since 10:00")
}

func TestWaitForMachine_StoppedEarlyLastLookSeesConnected(t *testing.T) {
	ctx, cancel := context.WithCancel(withUser(context.Background()))
	defer cancel()
	obs := &scriptedObserver{steps: steps(MachineStatus{}, MachineStatus{Connected: true})}
	obs.onCall = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(ctx), WaitForMachineParams{TimeoutSeconds: 60000})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	md := waitMetadata(t, resp)
	assert.True(t, md.Connected)
	assert.False(t, md.StoppedEarly)
}

func TestWaitForMachine_NilObserverIsUnavailable(t *testing.T) {
	resp, err := fastWaitTool(nil).Execute(waitForMachineCtx(withUser(context.Background())), WaitForMachineParams{})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "unavailable here")
}

func TestWaitForMachine_RefusesInANoMachineRun(t *testing.T) {
	obs := &scriptedObserver{steps: steps(MachineStatus{Connected: true})}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(nomachine.With(withUser(context.Background()))), WaitForMachineParams{})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Equal(t, nomachine.Refusal(ToolWaitForMachine), resp.Content)
	assert.Zero(t, obs.callCount(), "a no-machine run never looks")
}

func TestWaitForMachine_MissingUserIsAnError(t *testing.T) {
	obs := &scriptedObserver{steps: steps(MachineStatus{Connected: true})}
	resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(context.Background()), WaitForMachineParams{})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no user")
	assert.Zero(t, obs.callCount())
}

// The offline breaker and the execute_tools hint both key on this substring in
// a tool result; no answer from this tool may carry it.
func TestWaitForMachine_NoResultSaysNoDaemonConnected(t *testing.T) {
	all := []MachineStatus{
		{Connected: true},
		{Removed: true, StatusMessage: "gone"},
		{Failed: true, StatusMessage: "boom"},
		{Suspended: true},
		{Starting: true},
		{},
	}
	for _, status := range all {
		obs := &scriptedObserver{steps: steps(status)}
		resp, err := fastWaitTool(obs).Execute(waitForMachineCtx(withUser(context.Background())),
			WaitForMachineParams{TimeoutSeconds: 5})
		require.NoError(t, err)
		assert.NotContains(t, resp.Content, "no daemon connected")
	}

	unavailable, _ := fastWaitTool(nil).Execute(waitForMachineCtx(withUser(context.Background())), WaitForMachineParams{})
	assert.NotContains(t, unavailable.Content, "no daemon connected")
	noUser, _ := fastWaitTool(&scriptedObserver{steps: steps(MachineStatus{})}).Execute(waitForMachineCtx(context.Background()), WaitForMachineParams{})
	assert.NotContains(t, noUser.Content, "no daemon connected")
	assert.NotContains(t, waitForMachineDescription, "no daemon connected")
}

func TestWaitForMachine_RegisteredServerSidePreemptable(t *testing.T) {
	placement, err := PlacementOf(ToolWaitForMachine)
	require.NoError(t, err)
	assert.Equal(t, PlacementServer, placement)
	assert.True(t, NeedsMachine(ToolWaitForMachine))

	f := &ToolsFactory{opts: &ToolsOptions{}}
	assert.False(t, f.ObservesMachine())
	tool := f.WaitForMachine()
	assert.Equal(t, ToolWaitForMachine, tool.Name())
}
