// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Every reliant-temporal-worker rollout releases the run steps in flight 5s
// into its drain, and Temporal retries them on another worker. The retry
// reattaches to the command the first attempt started — still running, or
// finished with its result held — and returns ITS output and exit status. It
// never runs the command again: when the machine cannot vouch for the command
// the retry is refused, as before (rc.4, 2026-11-06 18:51:25).

// fakeMachine is a machine's side of run-step commands: each command it was
// sent, under which request id, and what became of it. It implements
// RunReattacher the way RemoteRunExecutor over a current daemon does.
type fakeMachine struct {
	mu       sync.Mutex
	runs     []string // the commands it ran, in order
	ids      []string // the request id each run came under
	commands map[string]*fakeCommand
	// lost makes an attached wait end without the result (the daemon went
	// away for good meanwhile).
	lost error
}

type fakeCommand struct {
	done   chan struct{}
	result CommandResult
}

func newFakeMachine() *fakeMachine {
	return &fakeMachine{commands: map[string]*fakeCommand{}}
}

// ExecuteCommand starts command, and the attempt that sent it goes away
// before its result: the worker was released by its drain.
func (m *fakeMachine) ExecuteCommand(ctx context.Context, command, _ string, _ int, _ map[string]string) (CommandResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := runRequestIDFrom(ctx)
	m.runs = append(m.runs, command)
	m.ids = append(m.ids, id)
	if id != "" {
		m.commands[id] = &fakeCommand{done: make(chan struct{})}
	}
	return CommandResult{ExitCode: -1}, &toolexec.TransportError{Code: toolexec.ErrorCodeDaemonUnreached,
		Command: command, Detail: "tool request via NATS failed: context canceled"}
}

func (m *fakeMachine) ReattachCommand(ctx context.Context, requestID string, _ int) (CommandResult, bool, error) {
	m.mu.Lock()
	c, ok := m.commands[requestID]
	lost := m.lost
	m.mu.Unlock()
	if !ok {
		return CommandResult{}, false, nil
	}
	if lost != nil {
		return CommandResult{ExitCode: -1}, true, lost
	}
	select {
	case <-c.done:
		return c.result, true, nil
	case <-ctx.Done():
		return CommandResult{ExitCode: -1}, true, ctx.Err()
	}
}

// finish ends the command sent as requestID with result.
func (m *fakeMachine) finish(requestID string, result CommandResult) {
	m.mu.Lock()
	c := m.commands[requestID]
	m.mu.Unlock()
	c.result = result
	close(c.done)
}

func (m *fakeMachine) ran() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.runs...)
}

// runStepAttempt runs attempt number attempt of the run step on chatID over
// executor, its command travelling as requestID.
func runStepAttempt(t *testing.T, act *ExecuteRunStepActivity, chatID string, attempt int32, requestID string) (ExecuteRunStepOutput, error) {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, in ExecuteRunStepInput) (ExecuteRunStepOutput, error) {
		return act.execute(ctx, in, attempt, requestID)
	}, activity.RegisterOptions{Name: "RunStepAttempt"})
	val, err := env.ExecuteActivity("RunStepAttempt", ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: chatID, StepID: "gate", Command: "task ci",
	})
	var out ExecuteRunStepOutput
	if err == nil {
		require.NoError(t, val.Get(&out))
	}
	return out, err
}

func newRunStepOn(t *testing.T, executor RunExecutor) (*ExecuteRunStepActivity, string) {
	t.Helper()
	repo := setupTestRepo(t)
	return NewExecuteRunStepActivity(repo, nil, executor), createTestChat(t, repo)
}

const stepRequestID = "runstep-00112233445566778899aabbccddeeff"

// The command is still running when the retry comes up: the retry waits for
// it and the step's verdict is that command's.
func TestRunStep_RetryReattachesToTheRunningCommand(t *testing.T) {
	t.Parallel()
	machine := newFakeMachine()
	act, chatID := newRunStepOn(t, machine)

	_, err := runStepAttempt(t, act, chatID, 1, stepRequestID)
	require.Error(t, err, "the first attempt's worker went away")

	time.AfterFunc(50*time.Millisecond, func() {
		machine.finish(stepRequestID, CommandResult{Stdout: "ok  ./...", ExitCode: 0})
	})
	out, err := runStepAttempt(t, act, chatID, 2, stepRequestID)
	require.NoError(t, err)
	assert.Equal(t, "ok  ./...", out.Stdout)
	assert.Equal(t, 0, out.ExitCode)
	assert.Equal(t, []string{"task ci"}, machine.ran(), "the command ran once: the retry never sent it")
}

// The command finished before the retry came up: its real output and exit
// status are the step's — a failing command fails the step as it would have.
func TestRunStep_RetryReattachesToAFinishedCommand(t *testing.T) {
	t.Parallel()
	machine := newFakeMachine()
	act, chatID := newRunStepOn(t, machine)

	_, _ = runStepAttempt(t, act, chatID, 1, stepRequestID)
	machine.finish(stepRequestID, CommandResult{Stdout: "FAIL internal/x", Stderr: "exit status 1", ExitCode: 1})

	out, err := runStepAttempt(t, act, chatID, 2, stepRequestID)
	require.NoError(t, err, "a command's own failure is a verdict, not an activity error")
	assert.Equal(t, 1, out.ExitCode)
	assert.Equal(t, "FAIL internal/x", out.Stdout)
	assert.Equal(t, "exit status 1", out.Stderr)
	assert.Equal(t, "FAIL internal/x\nexit status 1", out.Output)
	assert.Len(t, machine.ran(), 1)
}

// A retry whose machine does not know the command — it restarted, or never
// got it — is refused exactly as before, and runs nothing.
func TestRunStep_RetryOfAnUnknownCommandIsRefused(t *testing.T) {
	t.Parallel()
	machine := newFakeMachine()
	act, chatID := newRunStepOn(t, machine)

	out, err := runStepAttempt(t, act, chatID, 2, stepRequestID)
	requireNonRetryable(t, err, runStepRetryRefusedErrType)
	assert.Contains(t, err.Error(), "refusing to re-execute it")
	assert.Empty(t, out.Stdout)
	assert.Empty(t, machine.ran(), "a command that may have run is never run again")
}

// An executor that cannot reattach — a daemon or router from before reattach,
// or a test override — refuses the retry as before, and runs nothing.
func TestRunStep_RetryWithoutReattachIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("run executor without reattach", func(t *testing.T) {
		t.Parallel()
		mock := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ran again"}}}
		act, chatID := newRunStepOn(t, mock)
		_, err := runStepAttempt(t, act, chatID, 2, stepRequestID)
		requireNonRetryable(t, err, runStepRetryRefusedErrType)
		assert.Empty(t, mock.calls)
	})

	t.Run("tool executor without reattach", func(t *testing.T) {
		t.Parallel()
		tools := &recordingToolExecutor{}
		repo := setupTestRepo(t)
		act := NewExecuteRunStepActivity(repo, tools, nil)
		_, err := runStepAttempt(t, act, createTestChat(t, repo), 2, stepRequestID)
		requireNonRetryable(t, err, runStepRetryRefusedErrType)
		assert.Empty(t, tools.requests(), "nothing is sent to the machine")
	})
}

// Attached, and the wait then ends without the result: the outcome is
// unknown, the step fails without a retry, and the command is not sent.
func TestRunStep_ReattachedWaitThatEndsWithoutAResultIsNotRetried(t *testing.T) {
	t.Parallel()
	machine := newFakeMachine()
	act, chatID := newRunStepOn(t, machine)
	_, _ = runStepAttempt(t, act, chatID, 1, stepRequestID)
	machine.lost = &toolexec.TransportError{Code: toolexec.ErrorCodeDaemonInterrupted, Detail: "the machine no longer knows the request"}

	_, err := runStepAttempt(t, act, chatID, 2, stepRequestID)
	requireNonRetryable(t, err, runStepOutcomeUnknownErrType)
	assert.Len(t, machine.ran(), 1)
}

// The step's command travels under its request id; the log-file write after
// it is another command and carries none.
func TestRunStep_CommandCarriesTheStepsRequestID(t *testing.T) {
	t.Parallel()
	ids := &requestIDRecorder{}
	repo := setupTestRepo(t)
	act := NewExecuteRunStepActivity(repo, nil, ids)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, in ExecuteRunStepInput) (ExecuteRunStepOutput, error) {
		return act.execute(ctx, in, 1, stepRequestID)
	}, activity.RegisterOptions{Name: "RunStepAttempt"})
	_, err := env.ExecuteActivity("RunStepAttempt", ExecuteRunStepInput{
		WorkflowID: "wf-1", ChatID: createTestChat(t, repo), StepID: "gate", Command: "task ci", LogFile: "/tmp/gate.log",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{stepRequestID, ""}, ids.ids)
}

type requestIDRecorder struct{ ids []string }

func (r *requestIDRecorder) ExecuteCommand(ctx context.Context, _, _ string, _ int, _ map[string]string) (CommandResult, error) {
	r.ids = append(r.ids, runRequestIDFrom(ctx))
	return CommandResult{Stdout: "ok"}, nil
}

// One scheduled activity has one request id on every attempt, and no other
// activity shares it.
func TestRunStepRequestID_IsStablePerActivityAndUniqueAcrossThem(t *testing.T) {
	t.Parallel()
	info := func(workflowID, runID, activityID string) activity.Info {
		return activity.Info{WorkflowExecution: workflow.Execution{ID: workflowID, RunID: runID}, ActivityID: activityID}
	}
	base := runStepRequestID(info("wf", "run-1", "7"))
	assert.Regexp(t, `^runstep-[0-9a-f]{32}$`, base)

	retry := info("wf", "run-1", "7")
	retry.Attempt = 3
	assert.Equal(t, base, runStepRequestID(retry), "an attempt does not change it")
	for _, other := range []activity.Info{info("wf", "run-1", "8"), info("wf", "run-2", "7"), info("wf2", "run-1", "7")} {
		assert.NotEqual(t, base, runStepRequestID(other))
	}
}

// RemoteRunExecutor.ReattachCommand asks for the step's request id on the
// step's machine and maps the reattached result as ExecuteCommand maps one.
func TestRemoteRunExecutor_ReattachCommand(t *testing.T) {
	t.Parallel()
	tools := &reattachingToolExecutor{result: &toolexec.ToolResult{Success: true,
		Content: `{"stdout":"built","stderr":"warn","exit_code":2}`}, attached: true}
	exec := NewRemoteRunExecutor(tools)
	exec.SetContext(RunExecutorContext{UserID: "u-1", ChatID: "c-1", DaemonSelector: &toolexec.DaemonSelector{ID: "d-1"}})

	result, attached, err := exec.ReattachCommand(context.Background(), stepRequestID, 60_000)
	require.NoError(t, err)
	require.True(t, attached)
	assert.Equal(t, CommandResult{Stdout: "built", Stderr: "warn", ExitCode: 2}, result)
	require.NotNil(t, tools.req)
	assert.Equal(t, stepRequestID, tools.req.RequestID)
	assert.Equal(t, "d-1", tools.req.DaemonSelector.ID)
	assert.Equal(t, "u-1", tools.req.UserID)
	assert.True(t, tools.req.RunStep)

	tools.result = &toolexec.ToolResult{IsError: true, ErrorCode: toolexec.ErrorCodeDaemonInterrupted, Content: "lost"}
	_, attached, err = exec.ReattachCommand(context.Background(), stepRequestID, 60_000)
	assert.True(t, attached)
	var transportErr *toolexec.TransportError
	require.ErrorAs(t, err, &transportErr, "a lost reattached call is a transport failure, never a verdict")

	tools.attached, tools.result = false, nil
	_, attached, err = exec.ReattachCommand(context.Background(), stepRequestID, 60_000)
	require.NoError(t, err)
	assert.False(t, attached)
}

type reattachingToolExecutor struct {
	recordingToolExecutor
	req      *toolexec.ToolRequest
	result   *toolexec.ToolResult
	attached bool
}

func (e *reattachingToolExecutor) ReattachTool(_ context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, bool, error) {
	e.req = req
	return e.result, e.attached, nil
}

// recordingToolExecutor is a ToolExecutor that cannot reattach and records
// what it is asked to run.
type recordingToolExecutor struct {
	mu   sync.Mutex
	reqs []*toolexec.ToolRequest
}

func (e *recordingToolExecutor) ExecuteTool(_ context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reqs = append(e.reqs, req)
	return &toolexec.ToolResult{Success: true, Content: `{"stdout":"ran again","exit_code":0}`}, nil
}

func (e *recordingToolExecutor) Close() error { return nil }

func (e *recordingToolExecutor) requests() []*toolexec.ToolRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*toolexec.ToolRequest(nil), e.reqs...)
}
