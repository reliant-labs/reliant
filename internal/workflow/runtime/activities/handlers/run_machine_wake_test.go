// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// perMachineWakeRouter is several machines, each online or asleep; a run
// wake brings the named one up and is recorded.
type perMachineWakeRouter struct {
	*machineStateRouter
	mu     sync.Mutex
	online map[string]bool
	woken  []string
}

func newPerMachineWakeRouter(online map[string]bool) *perMachineWakeRouter {
	inner := &machineStateRouter{waitFakeRouter: &waitFakeRouter{}}
	inner.set(toolexec.MachineState{Exists: true, Suspended: true})
	return &perMachineWakeRouter{machineStateRouter: inner, online: online}
}

func (r *perMachineWakeRouter) IsDaemonOnline(_ context.Context, _ string, sel *toolexec.DaemonSelector) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sel != nil && r.online[sel.ID], nil
}

func (r *perMachineWakeRouter) WakeForRun(_ context.Context, _ string, sel *toolexec.DaemonSelector) (toolexec.WakeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.woken = append(r.woken, sel.ID)
	r.online[sel.ID] = true
	return toolexec.WakeResult{DaemonID: sel.ID, Resumed: true}, nil
}

func (r *perMachineWakeRouter) wokenIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.woken...)
}

// A chat in a branch workspace owned by B, whose run still carries the old
// pin A (a session selector) from before the chat moved to B. The run's
// tools and commands go to B (toolDaemonSelector), so the resume wake and the
// resuming run's preflight check and wake B: with A online and B suspended,
// B is woken and A is never what decides. A selector the workflow or node
// configures still decides.
func TestRunMachineWake_AStaleSessionPinWakesTheWorkspaceOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := db.NewTestRepo(t)
	t.Cleanup(func() { base.Close() })
	now := time.Now()
	projectID, chatID, worktreeID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, base.CreateProject(ctx, &db.Project{ID: projectID, UserID: "u1", Name: "p", Path: "/tmp/p",
		CreatedAt: now, UpdatedAt: now}))
	owner, pin := "owner-b", "pin-a"
	require.NoError(t, base.CreateWorktree(ctx, &core.Worktree{ID: worktreeID, Name: "f", Path: "/tmp/wt/f",
		Branch: "f", BaseBranch: "main", ProjectID: projectID, DaemonID: &owner, Status: 1,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, base.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "u1", Title: "t", ProjectID: projectID,
		WorktreeID: &worktreeID, ActiveDaemonID: &owner, State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now}))
	repo := &markerRepo{Repo: base}

	stale := &activitytypes.DaemonSelector{ID: pin, Session: true} // the run's session_daemon_id
	configured := &activitytypes.DaemonSelector{ID: "cicd"}

	t.Run("resume wake", func(t *testing.T) {
		for _, tc := range []struct {
			sel  *activitytypes.DaemonSelector
			want string
		}{{stale, owner}, {configured, "cicd"}, {nil, owner}} {
			router := newPerMachineWakeRouter(map[string]bool{pin: true})
			a := NewMachineResumeWakeActivity(repo, toolexec.NewRemoteExecutor(router))
			a.pollInterval = 5 * time.Millisecond
			out := runResumeWake(t, a, MachineResumeWakeInput{ChatID: chatID, DaemonSelector: tc.sel, WaitSeconds: 5})
			assert.True(t, out.Ready)
			assert.Equal(t, []string{tc.want}, router.wokenIDs())
		}
	})

	t.Run("resuming run's preflight", func(t *testing.T) {
		for _, tc := range []struct {
			sel  *activitytypes.DaemonSelector
			want string
		}{{stale, owner}, {configured, "cicd"}} {
			router := newPerMachineWakeRouter(map[string]bool{pin: true})
			a := NewPreflightDaemonCheckActivity(repo, toolexec.NewRemoteExecutor(router))
			a.pollInterval = 5 * time.Millisecond
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.RegisterActivity(a.Execute)
			res, err := env.ExecuteActivity(a.Execute, PreflightDaemonCheckInput{
				ChatID: chatID, DaemonSelector: tc.sel, RunResume: true, WaitSeconds: 5,
			})
			require.NoError(t, err)
			var out PreflightDaemonCheckOutput
			require.NoError(t, res.Get(&out))
			assert.True(t, out.DaemonAvailable)
			assert.Equal(t, []string{tc.want}, router.wokenIDs())
		}
	})
}
