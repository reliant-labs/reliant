package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// The real registry, router, remote executor and Temporal activity boundary;
// only the gateway/daemon's reply is simulated on an in-process NATS server.
type chatMachineRecovery struct {
	h                                       *IdempotencyTestHelper
	user, project, chat, original, ci, path string
	nc                                      *nats.Conn
	router                                  *toolexec.NATSDaemonRouter
	remote                                  *toolexec.RemoteExecutor
	mu                                      sync.Mutex
	deliveries                              map[string]int
	// wait, when set, replaces the run step's machine wait (30m by default:
	// a command that never reached an asleep machine is held for it).
	wait *machineWait
}

func newChatMachineRecovery(t *testing.T) *chatMachineRecovery {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	f := &chatMachineRecovery{h: h, user: uuid.NewString(), project: uuid.NewString(), chat: uuid.NewString(),
		original: "original", ci: "cicd", path: t.TempDir(), deliveries: map[string]int{}}
	h.CreateTestProjectWithPath(context.Background(), f.project, f.user, f.path)
	h.CreateTestChat(context.Background(), f.chat, f.project, f.user)
	require.NoError(t, h.Repo().UpdateChatActiveDaemon(context.Background(), f.chat, &f.original))
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	f.nc = nc
	f.router = toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(h.Repo()))
	f.remote = toolexec.NewRemoteExecutor(f.router)
	f.remote.SetServerExecutor(toolexec.NewLocalToolExecutor(nil))
	return f
}

func (f *chatMachineRecovery) register(t *testing.T, id, user string, updated time.Time) {
	t.Helper()
	managed := "managed"
	ctx := context.Background()
	require.NoError(t, f.h.Repo().UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: user, DaemonType: &managed}))
	_, err := f.h.sqlDB.ExecContext(ctx, `UPDATE daemons SET updated_at = $1 WHERE id = $2`, updated, id)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, f.h.Repo().UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
		DaemonID: id, UserID: user, Source: db.DaemonAttachmentSourceInbound, AttachedAt: now, LastStreamActivity: now,
	}))
}

func (f *chatMachineRecovery) serve(t *testing.T, id, user string) func() {
	t.Helper()
	status, err := f.nc.Subscribe(daemonquery.SubjectStatus(id), func(m *nats.Msg) {
		body, _ := json.Marshal(daemonquery.Status{Connected: true, LastActiveMs: time.Now().UnixMilli()})
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	subs := []*nats.Subscription{status}
	for _, prefix := range []string{"tools.request.sync.", "daemon.command.", "tools.online."} {
		prefix := prefix
		sub, err := f.nc.Subscribe(prefix+user+"."+id, func(m *nats.Msg) {
			if prefix == "tools.online." {
				_ = m.Respond([]byte("true"))
				return
			}
			f.mu.Lock()
			f.deliveries[id]++
			f.mu.Unlock()
			var body []byte
			if prefix == "tools.request.sync." {
				body, _ = json.Marshal(&toolexec.ToolExecutionResponse{Success: true, Content: id})
			} else {
				payload := []byte(`{"status":"present","path":"` + f.path + `","loaded_servers":[],"failed_servers":[]}`)
				body, _ = json.Marshal(map[string]any{"success": true, "payload": payload})
			}
			_ = m.Respond(body)
		})
		require.NoError(t, err)
		subs = append(subs, sub)
	}
	require.NoError(t, f.nc.Flush())
	stop := func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	}
	t.Cleanup(stop)
	return stop
}

func (f *chatMachineRecovery) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deliveries[id]
}

func (f *chatMachineRecovery) run(selector *activitytypes.DaemonSelector, executor toolexec.ToolExecutor) (ExecuteRunStepOutput, error) {
	var out ExecuteRunStepOutput
	act := NewExecuteRunStepActivity(f.h.Repo(), executor, nil)
	act.machineWait = f.wait
	err := f.h.ExecuteActivity(act.Execute, ExecuteRunStepInput{
		ChatID: f.chat, WorkflowID: "live-workflow", StepID: "run", Command: "pwd", Timeout: 5000,
		DaemonSelector: selector,
	}, &out)
	return out, err
}

// shortMachineWait ends the run step's hold for an undelivered command in
// seconds, for the steps that assert the command never ran.
func shortMachineWait() *machineWait {
	w := defaultMachineWait()
	w.budget, w.poll, w.firstResend, w.maxResend = 2*time.Second, 100*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond
	return w
}

func (f *chatMachineRecovery) pinIs(t *testing.T, want string) {
	t.Helper()
	chat, err := f.h.Repo().GetChat(context.Background(), f.chat)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, want, *chat.ActiveDaemonID)
}

func TestRunStep_DurableMachineAcrossRegistrationOfflineAndReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("real NATS server, database and reconnect round trips; runs without -short")
	}
	f := newChatMachineRecovery(t)
	ctx := context.Background()
	f.register(t, f.original, f.user, time.Now().Add(-time.Minute))
	stopOriginal := f.serve(t, f.original, f.user)
	out, err := f.run(nil, f.remote)
	require.NoError(t, err)
	require.Equal(t, f.original, out.Stdout)

	// Register the later CI machine: real DB ordering now makes it default.
	f.register(t, f.ci, f.user, time.Now())
	f.serve(t, f.ci, f.user)
	id, err := f.router.ResolveDaemonIDForSelector(ctx, f.user, nil)
	require.NoError(t, err)
	require.Equal(t, f.ci, id)
	out, err = f.run(nil, f.remote)
	require.NoError(t, err)
	require.Equal(t, f.original, out.Stdout, "old run snapshot must honor the chat pin")
	out, err = f.run(&activitytypes.DaemonSelector{ID: f.ci}, f.remote)
	require.NoError(t, err)
	require.Equal(t, f.ci, out.Stdout, "explicit node selector overrides pin without rewriting it")
	f.pinIs(t, f.original)
	ciBefore := f.count(f.ci)

	// Park original while cicd stays attached and answering. No command may
	// escape to cicd, including workspace repair and MCP discovery.
	stopOriginal()
	_, err = f.h.sqlDB.ExecContext(ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, f.original)
	require.NoError(t, err)
	_, err = f.h.Repo().ApplyDaemonLifecycle(ctx, db.DaemonLifecycleUpdate{DaemonID: f.original, Phase: "suspended", ChangedAt: time.Now()})
	require.NoError(t, err)
	// The step holds a command that never reached its machine (run_step.go's
	// machineWait); a short budget ends that hold here instead of in 30m.
	f.wait = shortMachineWait()
	out, err = f.run(nil, f.remote)
	f.wait = nil
	require.Error(t, err, "offline is a transport error, not a command verdict or a switch")
	assert.Contains(t, err.Error(), "suspended")
	assert.Contains(t, err.Error(), "the command never ran")
	assert.Empty(t, out.Stdout)
	wake := NewMachineResumeWakeActivity(f.h.Repo(), f.remote)
	wakeOut, err := wake.Execute(ctx, MachineResumeWakeInput{ChatID: f.chat})
	require.NoError(t, err)
	require.True(t, wakeOut.Waiting || wakeOut.NotComing != "", "nil snapshot must not consider online cicd ready for this chat")

	chat, err := f.h.Repo().GetChat(ctx, f.chat)
	require.NoError(t, err)
	sel := toolDaemonSelector("", chatDaemonSelector(chat, nil))
	_, err = f.remote.EnsureWorkspace(ctx, f.user, sel, toolexec.WorkspaceEnsureRequest{Path: f.path})
	require.ErrorIs(t, err, toolexec.ErrDaemonSuspended)
	toolCtx := toolexec.WithDaemonSelector(context.WithValue(ctx, auth.UserIDContextKey, f.user), sel)
	bound := toolexec.NewDaemonMCPContextBinder(f.router).Bind(rctx.NewToolContext(toolCtx, "", "", nil, nil))
	mcp := bound.MCP.EnsureProjectServersLoaded(toolCtx, f.path)
	require.NotEmpty(t, mcp.Errors)
	var toolsOut ExecuteToolsOutput
	err = f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), f.remote).Execute, ExecuteToolsInput{
		ChatID: f.chat, Thread: f.chat, ProjectPath: f.path,
		ToolCalls: []message.ToolCall{{ID: uuid.NewString(), Name: "shell", Input: `{"command":"pwd"}`}},
	}, &toolsOut)
	require.NoError(t, err)
	require.Len(t, toolsOut.ToolResults, 1)
	require.True(t, toolsOut.ToolResults[0].GetIsError())
	assert.Equal(t, ciBefore, f.count(f.ci), "no tool/workspace/run/MCP delivery to cicd while original is parked")
	f.pinIs(t, f.original)

	// A reconnecting original (not suspended) waits on its exact id. Trigger
	// its return only after the router proves it is waiting for THAT daemon.
	_, err = f.h.Repo().ApplyDaemonLifecycle(ctx, db.DaemonLifecycleUpdate{DaemonID: f.original, Phase: "ready", ChangedAt: time.Now()})
	require.NoError(t, err)
	_, err = f.h.sqlDB.ExecContext(ctx, `UPDATE daemons SET last_seen_at = NOW() WHERE id = $1`, f.original)
	require.NoError(t, err)
	waiting := make(chan string, 1)
	exec := &reconnectObservingExecutor{remote: f.remote, waiting: waiting}
	type outcome struct {
		out ExecuteRunStepOutput
		err error
	}
	done := make(chan outcome, 1)
	go func() { out, err := f.run(nil, exec); done <- outcome{out, err} }()
	select {
	case id := <-waiting:
		require.Equal(t, f.original, id)
	case result := <-done:
		t.Fatalf("run did not wait for pinned machine: %+v", result)
	case <-time.After(10 * time.Second):
		t.Fatal("run never entered reconnect wait")
	}
	assert.Equal(t, ciBefore, f.count(f.ci))
	f.pinIs(t, f.original)
	f.serve(t, f.original, f.user)
	f.register(t, f.original, f.user, time.Now().Add(-time.Minute))
	require.NoError(t, f.nc.Publish(daemonwire.Subject(f.original, daemonwire.EventConnected), nil))
	require.NoError(t, f.nc.Flush())
	select {
	case result := <-done:
		require.NoError(t, result.err)
		assert.Equal(t, f.original, result.out.Stdout)
	case <-time.After(10 * time.Second):
		t.Fatal("reconnected run did not finish")
	}
	assert.Equal(t, ciBefore, f.count(f.ci))
	f.pinIs(t, f.original)
	wakeOut, err = wake.Execute(ctx, MachineResumeWakeInput{ChatID: f.chat})
	require.NoError(t, err)
	require.True(t, wakeOut.Ready)
	_, err = f.remote.EnsureWorkspace(ctx, f.user, sel, toolexec.WorkspaceEnsureRequest{Path: f.path})
	require.NoError(t, err)
	require.Empty(t, bound.MCP.EnsureProjectServersLoaded(toolCtx, f.path).Errors)

	// A corrupted/foreign persisted pin must be refused, never fall back to
	// the user's available CI machine (or leak another user's transport).
	f.register(t, "foreign", "other-user", time.Now())
	f.serve(t, "foreign", "other-user")
	foreign := "foreign"
	require.NoError(t, f.h.Repo().UpdateChatActiveDaemon(ctx, f.chat, &foreign))
	f.wait = shortMachineWait()
	_, err = f.run(nil, f.remote)
	f.wait = nil
	require.Error(t, err)
	assert.Zero(t, f.count("foreign"))
	assert.Equal(t, ciBefore, f.count(f.ci))
	f.pinIs(t, foreign)

	// Even an explicit selector cannot make a no-machine run execute a
	// command: that refusal precedes selector overrides in ExecuteRunStep.
	_, err = f.h.sqlDB.ExecContext(ctx, `UPDATE chats SET active_daemon_id = NULL, no_machine = true WHERE id = $1`, f.chat)
	require.NoError(t, err)
	out, err = f.run(&activitytypes.DaemonSelector{ID: f.ci}, f.remote)
	require.NoError(t, err)
	assert.Equal(t, -1, out.ExitCode)
	assert.NotEmpty(t, out.Stderr)
	assert.Equal(t, ciBefore, f.count(f.ci))
}

type reconnectObservingExecutor struct {
	remote  *toolexec.RemoteExecutor
	waiting chan string
}

func (e *reconnectObservingExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = toolexec.WithDaemonReconnectWait(ctx, func(id string) func(bool) { e.waiting <- id; return nil })
	return e.remote.ExecuteTool(ctx, req)
}
func (e *reconnectObservingExecutor) Close() error { return nil }
