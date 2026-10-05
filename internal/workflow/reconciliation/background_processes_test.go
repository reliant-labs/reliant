// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProcessDaemons stands in for the user's daemons. processes maps
// daemonID -> processID -> status; a daemon absent from processes is
// unreachable, and an id absent from a reachable daemon's map is reported
// Unknown, exactly as handleExecBGStatus reports a forgotten process.
type fakeProcessDaemons struct {
	mu        sync.Mutex
	processes map[string]map[string]daemon.ProcessStatusInfo
	connected map[string][]string // userID -> daemon ids
	asked     []string            // daemon ids, in call order
}

func (f *fakeProcessDaemons) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, daemonID)
	if commandType != "exec.bg_status" {
		return nil, errors.New("unexpected command " + commandType)
	}
	table, reachable := f.processes[daemonID]
	if !reachable {
		return nil, errors.New("daemon unreachable")
	}
	var req daemon.ProcessStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	var resp daemon.ProcessStatusResponse
	for _, id := range req.ProcessIDs {
		if info, ok := table[id]; ok {
			info.ID = id
			resp.Processes = append(resp.Processes, info)
		} else {
			resp.Unknown = append(resp.Unknown, id)
		}
	}
	return json.Marshal(resp)
}

func (f *fakeProcessDaemons) ConnectedDaemonIDs(_ context.Context, userID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected[userID], nil
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// seedBackgroundedCall registers a status-6 call in both the sweep's query
// result and the tool_calls table the close writes to.
func (m *mockRepo) seedBackgroundedCall(id, userID string, processID, daemonID *string) {
	m.toolCalls[id] = &db.ToolCall{ID: id, ChatID: "chat-1", ToolName: "shell", Status: core.ToolCallStatusBackgrounded}
	m.backgroundedProcessCalls = append(m.backgroundedProcessCalls, &db.BackgroundedProcessCall{
		ToolCallID:          id,
		ChatID:              "chat-1",
		ToolName:            "shell",
		UserID:              userID,
		BackgroundProcessID: processID,
		DaemonID:            daemonID,
		RequestedAt:         time.Now(),
	})
}

// A backgrounded call must reach a terminal status when its process ends.
//
// Before this sweep, nothing ever moved a backgrounded shell call off status
// 6: the daemon learned the process exited, but it has no database and no
// code reported the exit anywhere. 4,213 of 4,224 backgrounded rows on the
// dev database were exactly this, and the chat snapshot shipped every one as
// live work. This drives the whole reconcile pass, so it also pins that the
// sweep is wired into it.
func TestReconcile_ClosesBackgroundedCallsFromTheirProcessOutcome(t *testing.T) {
	repo := newMockRepo()
	repo.daemons = map[string]bool{"d-1": true}
	repo.seedBackgroundedCall("tc-exit0", "u-1", strp("p-exit0"), strp("d-1"))
	repo.seedBackgroundedCall("tc-exit2", "u-1", strp("p-exit2"), strp("d-1"))
	repo.seedBackgroundedCall("tc-killed", "u-1", strp("p-killed"), strp("d-1"))
	repo.seedBackgroundedCall("tc-running", "u-1", strp("p-running"), strp("d-1"))
	// The daemon restarted since: its in-memory registry no longer has it.
	repo.seedBackgroundedCall("tc-forgotten", "u-1", strp("p-forgotten"), strp("d-1"))

	daemons := &fakeProcessDaemons{processes: map[string]map[string]daemon.ProcessStatusInfo{
		"d-1": {
			"p-exit0":   {Status: "completed", ExitCode: intp(0)},
			"p-exit2":   {Status: "failed", ExitCode: intp(2)},
			"p-killed":  {Status: "killed"},
			"p-running": {Status: "running"},
		},
	}}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
	reconciler.SetBackgroundProcessDaemons(daemons)

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	want := map[string]core.ToolCallStatus{
		"tc-exit0":     core.ToolCallStatusCompleted,
		"tc-exit2":     core.ToolCallStatusFailed,
		"tc-killed":    core.ToolCallStatusCancelled,
		"tc-forgotten": core.ToolCallStatusCancelled,
		"tc-running":   core.ToolCallStatusBackgrounded,
	}
	for id, status := range want {
		assert.Equal(t, status, repo.toolCalls[id].Status, "tool call %s", id)
	}
	for _, id := range []string{"tc-exit0", "tc-exit2", "tc-killed", "tc-forgotten"} {
		require.NotNil(t, repo.toolCalls[id].CompletedAt, "%s is terminal and must say when it ended", id)
	}
	require.NotNil(t, repo.toolCalls["tc-exit2"].ErrorMessage)
	assert.Contains(t, *repo.toolCalls["tc-exit2"].ErrorMessage, "code 2")

	// An open chat must hear about it, not just a reload.
	emitted := map[string]string{}
	for _, u := range repo.emittedToolCallUpdates {
		emitted[u.ToolCallID] = string(u.Status)
	}
	assert.Equal(t, map[string]string{
		"tc-exit0":     "completed",
		"tc-exit2":     "failed",
		"tc-killed":    "cancelled",
		"tc-forgotten": "cancelled",
	}, emitted)
}

// An unreachable daemon is not evidence that anything ended: its calls stay
// backgrounded, and the next pass asks again. This is what makes the sweep
// durable across a daemon disconnect rather than a one-shot guess.
func TestReconcile_LeavesBackgroundedCallsAloneWhileTheirDaemonIsUnreachable(t *testing.T) {
	repo := newMockRepo()
	repo.daemons = map[string]bool{"d-offline": true}
	repo.seedBackgroundedCall("tc-1", "u-1", strp("p-1"), strp("d-offline"))
	daemons := &fakeProcessDaemons{processes: map[string]map[string]daemon.ProcessStatusInfo{}}

	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
	reconciler.SetBackgroundProcessDaemons(daemons)
	_, err := reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusBackgrounded, repo.toolCalls["tc-1"].Status)

	// The daemon reconnects; its process exited while it was away.
	daemons.processes["d-offline"] = map[string]daemon.ProcessStatusInfo{
		"p-1": {Status: "completed", ExitCode: intp(0)},
	}
	_, err = reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, repo.toolCalls["tc-1"].Status,
		"the pass after reconnect must settle what happened while the daemon was away")
}

// The owner going away ends its processes: a deleted daemon's calls close
// without asking anyone, because there is no one left to ask.
func TestReconcile_ClosesBackgroundedCallsOfADeletedDaemon(t *testing.T) {
	repo := newMockRepo()
	repo.daemons = map[string]bool{} // d-gone has no row
	repo.seedBackgroundedCall("tc-1", "u-1", strp("p-1"), strp("d-gone"))
	daemons := &fakeProcessDaemons{processes: map[string]map[string]daemon.ProcessStatusInfo{}}

	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
	reconciler.SetBackgroundProcessDaemons(daemons)
	_, err := reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
	require.NoError(t, err)

	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-1"].Status)
	assert.Empty(t, daemons.asked, "a deleted daemon cannot be asked anything")
}

// Rows written before the daemon was recorded name no daemon. They close only
// on proof: every connected daemon of the user answered and none is running
// the process. One daemon that cannot answer could be the owner, so nothing
// closes — a live dev server reported dead is a lie the user cannot undo.
func TestReconcile_UnattributedBackgroundedCallsCloseOnlyOnProof(t *testing.T) {
	t.Run("every connected daemon proves it gone", func(t *testing.T) {
		repo := newMockRepo()
		repo.seedBackgroundedCall("tc-legacy", "u-1", nil, nil)
		repo.seedBackgroundedCall("tc-legacy-pid", "u-1", strp("p-old"), nil)
		repo.seedBackgroundedCall("tc-still-running", "u-1", strp("p-live"), nil)
		daemons := &fakeProcessDaemons{
			connected: map[string][]string{"u-1": {"d-a", "d-b"}},
			processes: map[string]map[string]daemon.ProcessStatusInfo{
				"d-a": {},
				"d-b": {"p-live": {Status: "running"}},
			},
		}
		reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
		reconciler.SetBackgroundProcessDaemons(daemons)
		_, err := reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
		require.NoError(t, err)

		assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-legacy"].Status)
		assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-legacy-pid"].Status)
		assert.Equal(t, core.ToolCallStatusBackgrounded, repo.toolCalls["tc-still-running"].Status,
			"a daemon that is running the process must keep it backgrounded")
	})

	t.Run("one daemon cannot answer", func(t *testing.T) {
		repo := newMockRepo()
		repo.seedBackgroundedCall("tc-legacy", "u-1", strp("p-old"), nil)
		daemons := &fakeProcessDaemons{
			connected: map[string][]string{"u-1": {"d-a", "d-silent"}},
			processes: map[string]map[string]daemon.ProcessStatusInfo{"d-a": {}},
		}
		reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
		reconciler.SetBackgroundProcessDaemons(daemons)
		_, err := reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
		require.NoError(t, err)
		assert.Equal(t, core.ToolCallStatusBackgrounded, repo.toolCalls["tc-legacy"].Status)
	})

	t.Run("no daemon connected", func(t *testing.T) {
		repo := newMockRepo()
		repo.seedBackgroundedCall("tc-legacy", "u-1", strp("p-old"), nil)
		reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
		reconciler.SetBackgroundProcessDaemons(&fakeProcessDaemons{})
		_, err := reconciler.reconcileBackgroundedProcesses(context.Background(), &passStats{})
		require.NoError(t, err)
		assert.Equal(t, core.ToolCallStatusBackgrounded, repo.toolCalls["tc-legacy"].Status,
			"a machine that is asleep has not proven its processes ended")
	})
}

// A call some other writer settled between the list and the close is never
// overwritten. UpsertToolCallStatus allows terminal-to-terminal corrections,
// so the sweep itself must only ever move a row OFF backgrounded: a user's
// cancel that raced it stays a cancel.
func TestReconcile_BackgroundedSweepNeverOverwritesASettledCall(t *testing.T) {
	repo := newMockRepo()
	repo.daemons = map[string]bool{"d-1": true}
	repo.seedBackgroundedCall("tc-1", "u-1", strp("p-1"), strp("d-1"))
	listed := repo.backgroundedProcessCalls // what the pass read
	// A user cancel lands after the list, before the close.
	repo.toolCalls["tc-1"].Status = core.ToolCallStatusCancelled

	daemons := &fakeProcessDaemons{processes: map[string]map[string]daemon.ProcessStatusInfo{
		"d-1": {"p-1": {Status: "completed", ExitCode: intp(0)}},
	}}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())
	reconciler.SetBackgroundProcessDaemons(daemons)

	closed := reconciler.settleOwnedProcesses(context.Background(), &passStats{}, daemons, "u-1", "d-1", listed)
	assert.Equal(t, 0, closed)
	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-1"].Status,
		"the user's cancel must survive the sweep's later view of the process")
	assert.Empty(t, repo.emittedToolCallUpdates)
}
