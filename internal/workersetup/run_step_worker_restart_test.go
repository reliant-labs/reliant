// Copyright (c) 2026 Reliant Labs
package workersetup_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	userconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantgrpc "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/reliant-labs/reliant/internal/workersetup"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// A run node's command survives a restart of the worker running it (Reliant
// bug 35da85ee: the rc-train's prod deploy restarted the prod workers, which
// killed the deploy step executing on them).
//
// Everything on the command's path is real: a Temporal dev server; the
// production worker (NewWorker: the ActivityWrapper, the drain's activity
// release, ExecuteRunStep over the RemoteExecutor and the NATS router); an
// embedded NATS; the daemon gateway (ToolsDaemonService, its NATS bridge and
// result publisher, the daemon listener and its token auth); and the daemon
// runtime, running a real shell command in a real checkout. Only the workflow
// is a shim: it dispatches ExecuteRunStep with a graph step's options
// (StepExecutor.executeRunStep), so the run node's retry policy, heartbeat and
// deadline are the engine's.
//
// Worker A is drained mid-command, as a rollout drains it, and exits. Worker
// B, started later, takes the retry. The command must run exactly once, and
// the step's result must be that command's output and exit code, delivered on
// the second attempt.
func TestRunStepSurvivesAWorkerRestart(t *testing.T) {
	testkit.Slow(t, "boots a Temporal dev server, NATS, a daemon gateway and a daemon, and runs real shell commands across worker restarts")
	rig := newRestartRig(t)

	t.Run("the command outlives the worker running it", func(t *testing.T) {
		cmd := rig.newCommand(t, "outlives")
		a := rig.startWorker(t, cmd.queue)
		run := rig.dispatch(t, cmd)
		cmd.waitFor(t, "started")
		a.drain(t)

		// The retry reattaches while the command still runs: it asks the
		// daemon what became of the request, and the daemon answers
		// "running". Only then is the command let finish.
		questions := rig.watchToolStatus(t)
		rig.startWorker(t, cmd.queue)
		questions.awaitState(t, toolexec.ToolRequestRunning)
		_, err := os.Stat(filepath.Join(cmd.dir, "finished"))
		require.True(t, os.IsNotExist(err), "the command is still running while the retry waits for it")
		cmd.release(t)
		cmd.requireRanOnceAndDelivered(t, rig, run)
	})

	t.Run("the command finishes while no worker is up", func(t *testing.T) {
		cmd := rig.newCommand(t, "finishes-alone")
		a := rig.startWorker(t, cmd.queue)
		run := rig.dispatch(t, cmd)
		cmd.waitFor(t, "started")
		a.drain(t)
		cmd.release(t)
		cmd.waitFor(t, "finished")

		rig.startWorker(t, cmd.queue)
		cmd.requireRanOnceAndDelivered(t, rig, run)
	})

	// The rc-train's case in full: the deploy restarts the workers, and the
	// machine's daemon restarts too after the command finished. Its result,
	// delivered to the worker that was going away, is kept on disk
	// (daemonruntime result_retain.go), so the retry still gets it.
	t.Run("the machine's daemon restarts after the command finished", func(t *testing.T) {
		cmd := rig.newCommand(t, "daemon-restarts")
		retainedBefore := rig.retainedResults(t)
		a := rig.startWorker(t, cmd.queue)
		run := rig.dispatch(t, cmd)
		cmd.waitFor(t, "started")
		a.drain(t)
		cmd.release(t)
		cmd.waitFor(t, "finished")
		require.Eventually(t, func() bool { return rig.retainedResults(t) > retainedBefore }, time.Minute, 50*time.Millisecond,
			"the daemon delivered the result to the drained worker's gateway and retained it")

		rig.restartDaemon(t)
		rig.startWorker(t, cmd.queue)
		cmd.requireRanOnceAndDelivered(t, rig, run)
	})
}

// restartRig is one machine, its gateway, NATS and Temporal, shared by the
// scenarios; each scenario has its own task queue, workers and command.
type restartRig struct {
	repo      *db.Repo
	temporal  client.Client
	natsURL   string
	userID    string
	chatID    string
	daemonID  string
	checkout  string
	dataDir   string
	gateway   string
	token     string
	stopAgent func()
}

func newRestartRig(t *testing.T) *restartRig {
	t.Helper()
	// The daemon never touches the developer's ~/.reliant.
	userconfig.UseUserConfigDirForTest(t, t.TempDir())
	t.Setenv("HOME", t.TempDir())

	rig := &restartRig{
		repo:     db.NewTestRepo(t),
		userID:   "restart-user-" + uuid.NewString()[:8],
		checkout: t.TempDir(),
		dataDir:  t.TempDir(),
	}

	// NATS, and the gateway on it.
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second))
	rig.natsURL = srv.ClientURL()
	gatewayNATS := rig.connectNATS(t)
	toolsSvc, err := tools_daemon.New(tools_daemon.Deps{
		Logger: slog.Default(), Config: &typedconfig.Config{}, Repo: rig.repo,
		ResultPublisher: toolexec.NewToolResultPublisher(gatewayNATS),
	})
	require.NoError(t, err)
	bridge := toolexec.NewNATSToolBridge(gatewayNATS, nil, toolsSvc)
	toolsSvc.AddConnectionListener(bridge)
	require.NoError(t, bridge.Start())
	t.Cleanup(func() { _ = bridge.Close() })
	authority := tokenauthority.NewMemory()
	rig.gateway = serveGateway(t, authority, toolsSvc)
	minted, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: rig.userID, Name: "restart-rig", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
	})
	require.NoError(t, err)
	rig.token = minted.Plaintext

	// The machine, and the project checked out on it.
	rig.startDaemon(t)
	rig.daemonID = rig.awaitAttached(t, "")
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) // row timestamps only; nothing reads them
	projectID := uuid.NewString()
	require.NoError(t, rig.repo.CreateProject(ctx, &db.Project{ID: projectID, Name: "restart-rig", Path: rig.checkout,
		UserID: rig.userID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, rig.repo.UpsertProjectDaemon(ctx, projectID, rig.daemonID, rig.checkout, nil))
	rig.chatID = uuid.NewString()
	require.NoError(t, rig.repo.CreateChat(ctx, &db.Chat{ID: rig.chatID, Title: "rc-train", ProjectID: projectID,
		UserID: rig.userID, CreatedAt: now, UpdatedAt: now}))

	// Temporal.
	devOpts := testsuite.DevServerOptions{LogLevel: "never", ClientOptions: &client.Options{Namespace: "reliant-run-step-restart"}}
	if path, err := exec.LookPath("temporal"); err == nil {
		devOpts.ExistingPath = path
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dev, err := testsuite.StartDevServer(startCtx, devOpts)
	require.NoError(t, err, "start Temporal dev server")
	t.Cleanup(func() { _ = dev.Stop() })
	rig.temporal = dev.Client()
	return rig
}

func (r *restartRig) connectNATS(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(r.natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// serveGateway serves the daemon listener over svc, as the gateway root does,
// and returns its h2c URL.
func serveGateway(t *testing.T, tokens *tokenauthority.Memory, svc *tools_daemon.Service) string {
	t.Helper()
	daemonAuth, err := reliantgrpc.NewDaemonAuth(tokens)
	require.NoError(t, err)
	chain, err := reliantgrpc.NewDaemonChain(reliantgrpc.DaemonChainDeps{Auth: daemonAuth})
	require.NoError(t, err)
	listener, err := reliantgrpc.NewDaemonListener(reliantgrpc.DaemonListenerDeps{BindAddress: "127.0.0.1", Chain: chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			svc.Register(mux, opts...)
			return []string{reliantv1connect.ToolsDaemonServiceName}
		})})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, listener.Listen(ctx))
	ran := make(chan error, 1)
	go func() { ran <- listener.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	return "http://" + listener.Addr().String()
}

// startDaemon runs the daemon runtime against the gateway, on the rig's data
// directory (a restart reuses it, as a real one does).
func (r *restartRig) startDaemon(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemonruntime.Start(ctx, daemonruntime.StartOptions{BootstrapConfig: bootstrap.DaemonBootstrapConfig{
			AuthToken: r.token,
			GRPCURL:   r.gateway,
			TLSMode:   bootstrap.TLSModeH2C,
			DataDir:   r.dataDir,
			Name:      "restart-rig",
			ServerURL: "http://e2e.invalid",
		}})
	}()
	stopped := false
	r.stopAgent = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("the daemon did not stop within 30s")
		}
	}
	t.Cleanup(func() { r.stopAgent() })
}

// restartDaemon stops the daemon and starts it again on the same data
// directory, and waits until it is the same machine, attached again.
func (r *restartRig) restartDaemon(t *testing.T) {
	t.Helper()
	before, err := daemonstate.Read(r.dataDir)
	require.NoError(t, err)
	r.stopAgent()
	r.startDaemon(t)
	// The restarted process's own record (a new start), connected: an event
	// of the new process, not a comparison of clocks.
	require.Eventually(t, func() bool {
		state, err := daemonstate.Read(r.dataDir)
		return err == nil && !state.StartedAt.Equal(before.StartedAt) && state.Stream.Established()
	}, time.Minute, 50*time.Millisecond, "the restarted daemon connects to the gateway")
	require.Equal(t, r.daemonID, r.awaitAttached(t, r.daemonID), "the restarted daemon is the same machine")
}

// awaitAttached waits until the user's daemon (want, when named) is attached
// to the gateway, and returns its id.
func (r *restartRig) awaitAttached(t *testing.T, want string) string {
	t.Helper()
	var id string
	require.Eventually(t, func() bool {
		ctx := context.Background()
		daemons, err := r.repo.ListDaemonsByUserID(ctx, r.userID)
		if err != nil {
			return false
		}
		for _, d := range daemons {
			if want != "" && d.ID != want {
				continue
			}
			if attached, err := r.repo.IsDaemonIDAttached(ctx, d.ID, time.Minute); err == nil && attached {
				id = d.ID
				return true
			}
		}
		return false
	}, time.Minute, 50*time.Millisecond, "the daemon attaches to the gateway")
	return id
}

// retainedResults counts the run-step results the daemon keeps on disk after
// delivering them.
func (r *restartRig) retainedResults(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(r.dataDir, "outbox", "*.retained"))
	require.NoError(t, err)
	return len(matches)
}

// restartWorker is one reliant-temporal-worker process.
type restartWorker struct {
	w    *workersetup.Worker
	nc   *nats.Conn
	done chan error
}

// startWorker starts a production worker polling queue, on its own NATS
// connection, as its own process would.
func (r *restartRig) startWorker(t *testing.T, queue string) *restartWorker {
	t.Helper()
	nc := r.connectNATS(t)
	router := toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(r.repo))
	// The worker's executor as internal/app's NewRemoteExecutor builds it.
	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: r.repo})
	executor := toolexec.NewRemoteExecutor(router)
	executor.SetServerExecutor(toolexec.NewLocalToolExecutor(factory))
	executor.SetDaemonClientFactory(toolexec.RemoteDaemonClients(router))
	w, _, err := workersetup.NewWorker(&workersetup.Config{
		TemporalClient:  r.temporal,
		Database:        r.repo,
		ToolsFactory:    factory,
		ToolExecutor:    executor,
		DaemonRouter:    router,
		TaskQueueSuffix: queue,
	})
	require.NoError(t, err)
	rw := &restartWorker{w: w, nc: nc, done: make(chan error, 1)}
	go func() { rw.done <- w.Run(context.Background()) }()
	t.Cleanup(func() { rw.drainWithin(10 * time.Second) })
	return rw
}

// drain stops the worker the way a rollout does — Drain releases its
// in-flight activities — and then the process exits: its NATS connection
// closes with it.
func (w *restartWorker) drain(t *testing.T) {
	t.Helper()
	require.NoError(t, w.drainWithin(30*time.Second))
}

func (w *restartWorker) drainWithin(budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	err := w.w.Drain(ctx)
	w.nc.Close()
	return err
}

// stepCommand is one scenario's run node: a command that records each run,
// signals that it started, waits to be released, and then finishes with a
// distinctive output and exit code.
type stepCommand struct {
	name  string
	dir   string
	queue string
}

const commandExitCode = 3

func (r *restartRig) newCommand(t *testing.T, name string) *stepCommand {
	t.Helper()
	dir := filepath.Join(r.checkout, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return &stepCommand{name: name, dir: dir, queue: "restart-" + name + "-" + uuid.NewString()[:8]}
}

func (c *stepCommand) script() string {
	q := "'" + c.dir + "'"
	return "echo ran >> " + q + "/runs && touch " + q + "/started && " +
		"while [ ! -e " + q + "/release ]; do sleep 0.1; done; " +
		"echo 'deployed " + c.name + "'; touch " + q + "/finished; exit 3"
}

func (c *stepCommand) waitFor(t *testing.T, marker string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(c.dir, marker))
		return err == nil
	}, 2*time.Minute, 20*time.Millisecond, "the command reached %q", marker)
}

func (c *stepCommand) release(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(c.dir, "release"), nil, 0o600))
}

const runStepShimName = "RunStepWorkerRestartShim"

// dispatch starts the shim workflow for c, on a workflow worker of its own,
// and returns the run.
func (r *restartRig) dispatch(t *testing.T, c *stepCommand) client.WorkflowRun {
	t.Helper()
	shimQueue := c.queue + "-workflow"
	activityQueue := workersetup.TaskQueueName(c.queue)
	shim := worker.New(r.temporal, shimQueue, worker.Options{})
	shim.RegisterWorkflowWithOptions(func(ctx workflow.Context, in handlers.ExecuteRunStepInput) (handlers.ExecuteRunStepOutput, error) {
		// A graph step's options (StepExecutor.activityOptionsWithSlack and
		// stepActivityRetryPolicy, internal/workflow/runtime).
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			TaskQueue: activityQueue,
			StartToCloseTimeout: time.Duration(in.Timeout)*time.Millisecond +
				activitytypes.RunStepMachineWaitBudget + activitytypes.RunStepSendOverhead,
			HeartbeatTimeout: 60 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2,
				MaximumInterval: time.Minute, MaximumAttempts: 5},
		})
		var out handlers.ExecuteRunStepOutput
		err := workflow.ExecuteActivity(ctx, "ExecuteRunStep", in).Get(ctx, &out)
		return out, err
	}, workflow.RegisterOptions{Name: runStepShimName})
	require.NoError(t, shim.Start())
	t.Cleanup(shim.Stop)

	wfID := "run-step-restart-" + c.name + "-" + uuid.NewString()[:8]
	run, err := r.temporal.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: wfID, TaskQueue: shimQueue, WorkflowExecutionTimeout: 10 * time.Minute,
	}, runStepShimName, handlers.ExecuteRunStepInput{
		WorkflowID:     wfID,
		ChatID:         r.chatID,
		StepID:         "deploy",
		Command:        c.script(),
		Timeout:        int((5 * time.Minute).Milliseconds()),
		DaemonSelector: &activitytypes.DaemonSelector{ID: r.daemonID},
	})
	require.NoError(t, err)
	return run
}

// requireRanOnceAndDelivered waits for run and holds it to the contract: the
// step completed with the command's own output and exit code, the command ran
// once, and the result came from the second attempt — the retry on the new
// worker — not from a re-run.
func (c *stepCommand) requireRanOnceAndDelivered(t *testing.T, r *restartRig, run client.WorkflowRun) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out handlers.ExecuteRunStepOutput
	require.NoError(t, run.Get(ctx, &out), "the run step completed")
	assert.Equal(t, commandExitCode, out.ExitCode, "the command's own exit code")
	assert.Contains(t, out.Stdout, "deployed "+c.name, "the command's own output")
	assert.False(t, out.Interrupted)

	runs, err := os.ReadFile(filepath.Join(c.dir, "runs"))
	require.NoError(t, err)
	assert.Equal(t, "ran\n", string(runs), "the command ran exactly once")

	// The history's own record: the attempt that delivered the result is a
	// retry, the attempt before it was released by the drain, and the
	// activity completed. How many retries that took is Temporal's timing,
	// not the contract.
	var started *historypb.ActivityTaskStartedEventAttributes
	completed := false
	history := r.temporal.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for history.HasNext() {
		event, err := history.Next()
		require.NoError(t, err)
		if attrs := event.GetActivityTaskStartedEventAttributes(); attrs != nil {
			started = attrs
		}
		if event.GetActivityTaskCompletedEventAttributes() != nil {
			completed = true
		}
	}
	require.NotNil(t, started)
	assert.True(t, completed, "the step's activity completed")
	assert.Greater(t, started.GetAttempt(), int32(1), "the result came from a retry, not the drained attempt")
	assert.Contains(t, started.GetLastFailure().GetMessage(), "worker shut down",
		"the attempt before it was released by the worker's drain")
	assert.False(t, strings.Contains(out.Stderr, "not run again"), "the retry reattached; it did not refuse: %s", out.Stderr)
}

// statusWatch sees the daemon's answers to tool.status, the question a
// reattaching retry asks first: the explicit event that the retry reached
// the daemon about the command its first attempt started.
type statusWatch struct {
	states chan toolexec.ToolRequestState
}

// watchToolStatus starts watching, on NATS, every tool.status question to
// the rig's daemon and its answer.
func (r *restartRig) watchToolStatus(t *testing.T) *statusWatch {
	t.Helper()
	nc := r.connectNATS(t)
	w := &statusWatch{states: make(chan toolexec.ToolRequestState, 16)}
	var mu sync.Mutex
	questions := map[string]bool{}                    // reply subject -> asked
	answers := map[string]toolexec.ToolRequestState{} // reply subject -> answered
	match := func(reply string) {
		if questions[reply] {
			if state, ok := answers[reply]; ok {
				delete(questions, reply)
				delete(answers, reply)
				w.states <- state
			}
		}
	}
	commands, err := nc.Subscribe("daemon.command."+r.userID+"."+r.daemonID, func(msg *nats.Msg) {
		var cmd struct {
			CommandType string `json:"command_type"`
		}
		if json.Unmarshal(msg.Data, &cmd) != nil || cmd.CommandType != toolexec.ToolStatusCommand || msg.Reply == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		questions[msg.Reply] = true
		match(msg.Reply)
	})
	require.NoError(t, err)
	replies, err := nc.Subscribe("_INBOX.>", func(msg *nats.Msg) {
		var resp struct {
			Success bool   `json:"success"`
			Payload []byte `json:"payload"`
		}
		var status toolexec.ToolStatusResponse
		if json.Unmarshal(msg.Data, &resp) != nil || !resp.Success || json.Unmarshal(resp.Payload, &status) != nil || status.State == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		answers[msg.Subject] = status.State
		match(msg.Subject)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() {
		_ = commands.Unsubscribe()
		_ = replies.Unsubscribe()
	})
	return w
}

// awaitState waits until the daemon has answered a tool.status question with
// want.
func (w *statusWatch) awaitState(t *testing.T, want toolexec.ToolRequestState) {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case got := <-w.states:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("the daemon never answered a tool.status question with %q", want)
		}
	}
}
