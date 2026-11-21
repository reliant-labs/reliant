// Copyright (c) 2026 Reliant Labs
package workspacecreate

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// recoveryRefusalText is the daemon's refusal (toolexec.ErrRecoveryFinalizing).
const recoveryRefusalText = "workspace recovery finalization in progress; retry this operation"

// fakeRefusal stands in for the router's error for a daemon's refusal
// (toolexec.DaemonReportedFailure), which this package cannot import.
type fakeRefusal struct{}

func (fakeRefusal) Error() string          { return recoveryRefusalText }
func (fakeRefusal) RetryableRefusal() bool { return true }

// refusingMachine refuses each command in refuse that many times (-1:
// always), as a daemon finalizing another workspace's recovery does, then
// answers it as fakeMachine does.
type refusingMachine struct {
	fakeMachine
	refuseMu sync.Mutex
	refuse   map[string]int
}

func (m *refusingMachine) Send(ctx context.Context, cmd string, payload, resp any, timeoutMs int32) error {
	m.refuseMu.Lock()
	n := m.refuse[cmd]
	if n > 0 {
		m.refuse[cmd] = n - 1
	}
	m.refuseMu.Unlock()
	if n != 0 {
		m.mu.Lock()
		m.sent = append(m.sent, sent{cmd: cmd})
		m.mu.Unlock()
		// Wrapped as the agent tool's workspaceMachine wraps it.
		return fmt.Errorf("%s: daemon command %q failed: %w", cmd, cmd, fakeRefusal{})
	}
	return m.fakeMachine.Send(ctx, cmd, payload, resp, timeoutMs)
}

// waitsNoTime replaces the wait between sends with one that records it and
// returns at once (or ctx's error), so no test sleeps.
func waitsNoTime(t *testing.T) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	prev := recoveryWaitSleep
	recoveryWaitSleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { recoveryWaitSleep = prev })
	return &waits
}

// The prod event (obs bug reports b793ea9b, cf91a1ef): another workspace on
// the machine was finalizing its removal, so the daemon refused
// worktree.create. The create waits it out and succeeds.
func TestFinishWaitsOutAnotherWorkspacesRecoveryFinalization(t *testing.T) {
	waits := waitsNoTime(t)
	st := &fakeStore{}
	m := &refusingMachine{fakeMachine: fakeMachine{root: "/w/demo/feat-1"}, refuse: map[string]int{"worktree.create": 3}}
	req := oneRepoReq()
	wt, err := Insert(context.Background(), st, nil, req)
	require.NoError(t, err)

	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), st.rows[wt.ID].Status)
	assert.Equal(t, "/w/demo/feat-1", st.rows[wt.ID].Path)
	assert.Len(t, m.cmds("worktree.create"), 4, "three refusals, then the create that ran")
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second}, *waits)
}

// The copy is refused the same way, and is sent again rather than silently
// leaving the requested files behind.
func TestFinishWaitsOutRecoveryFinalizationForTheCopy(t *testing.T) {
	waitsNoTime(t)
	st := &fakeStore{}
	m := &refusingMachine{fakeMachine: fakeMachine{root: "/w/r"}, refuse: map[string]int{"worktree.copy_paths": 1}}
	req := oneRepoReq()
	req.CopyPaths = []string{".env"}
	wt, _ := Insert(context.Background(), st, nil, req)

	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	assert.Len(t, m.cmds("worktree.copy_paths"), 2)
}

// A machine that refuses for longer than any finalization holds its lease is
// not one waiting clears: the create fails as before, saying why, as a
// machine-state failure (WARN, never an issue) after a bounded wait.
func TestFinishFailsWhenRecoveryFinalizationOutlastsTheWait(t *testing.T) {
	waits := waitsNoTime(t)
	st := &fakeStore{}
	m := &refusingMachine{fakeMachine: fakeMachine{root: "/w/r"}, refuse: map[string]int{"worktree.create": -1}}
	req := oneRepoReq()
	wt, _ := Insert(context.Background(), st, nil, req)

	err := Finish(context.Background(), st, m, req, wt)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	assert.True(t, isMachineFailure(err))
	assert.True(t, isRecoveryRefusal(err), "the daemon's refusal stays on the chain")

	var total time.Duration
	for _, w := range *waits {
		total += w
	}
	assert.LessOrEqual(t, total, recoveryWaitBudget)
	assert.Greater(t, total, 2*time.Minute, "longer than a finalization holds its lease")

	row := st.rows[wt.ID]
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), row.Status)
	assert.Contains(t, row.FailureReason, "finalizing another workspace's removal")
	assert.Empty(t, row.Path)
}

// A caller that gives up stops the wait at once.
func TestFinishStopsWaitingWhenCancelled(t *testing.T) {
	waits := waitsNoTime(t)
	st := &fakeStore{}
	m := &refusingMachine{fakeMachine: fakeMachine{root: "/w/r"}, refuse: map[string]int{"worktree.create": -1}}
	req := oneRepoReq()
	wt, _ := Insert(context.Background(), st, nil, req)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Error(t, Finish(ctx, st, m, req, wt))
	assert.Empty(t, *waits)
	assert.Len(t, m.cmds("worktree.create"), 1)
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), st.rows[wt.ID].Status)
}

// Any other failure is not waited on.
func TestFinishDoesNotWaitOnOtherFailures(t *testing.T) {
	waits := waitsNoTime(t)
	st := &fakeStore{}
	m := &fakeMachine{root: "/w/r", failRepo: "app", sendErr: fmt.Errorf("boom")}
	req := oneRepoReq()
	wt, _ := Insert(context.Background(), st, nil, req)

	require.Error(t, Finish(context.Background(), st, m, req, wt))
	assert.Empty(t, *waits)
	assert.Len(t, m.cmds("worktree.create"), 1)
}
