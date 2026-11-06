// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// loopbackDaemonRouter is the daemon at the far end of the wire, in process:
// each request crosses as JSON (as it does to a real daemon), and the
// daemon's own LocalToolExecutor runs it with a real shell against a real
// LocalClient. Only the two sync sends are implemented; anything else the
// executor reaches for panics, which would be a test bug.
type loopbackDaemonRouter struct {
	toolexec.DaemonRouter
	local *toolexec.LocalToolExecutor

	mu       sync.Mutex
	contexts []map[string]any
}

func (r *loopbackDaemonRouter) SendToolRequestSync(ctx context.Context, _ string, request *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	wire, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var got toolexec.ToolExecutionRequest
	if err := json.Unmarshal(wire, &got); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.contexts = append(r.contexts, got.Context)
	r.mu.Unlock()

	res := r.local.ExecuteTool(ctx, got.ToolName, got.ToolInput, got.ToolCallID, got.TimeoutMs, got.Context)
	return &toolexec.ToolExecutionResponse{
		RequestID: got.RequestID, Success: res.Success, IsError: res.IsError, Content: res.Content,
		Metadata: res.Metadata, ErrorMessage: res.ErrorMessage, ErrorCode: res.ErrorCode,
	}, nil
}

func (r *loopbackDaemonRouter) SendToolRequestSyncWithSelector(ctx context.Context, userID string, request *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return r.SendToolRequestSync(ctx, userID, request)
}

func (r *loopbackDaemonRouter) lastContext(t *testing.T) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.contexts, "no request reached the daemon")
	return r.contexts[len(r.contexts)-1]
}

// reliant#2003: a run node's `timeout:` was silently capped at the model's
// 10-minute foreground limit, because run steps execute through the shell
// tool. Every long release-train step (cut, gate, build, deploy) died at 10
// minutes.
//
// The clock is shrunk a millionfold (tools.ShellTimeoutUnit: one "millisecond"
// of timeout is a microsecond), so the model's 10-minute cap is 0.6s and a
// command that takes "12 minutes" sleeps 0.72s. The node's timeout is two
// hours (7.2s), far past the command: what this proves is that the run step is
// not stopped at the cap, so nothing but the cap may stop it. A 30-minute node
// (1.8s) left ~1s for spawning bash and crossing the wire, and a loaded
// machine spent it: the step was stopped by its own timeout, not the cap.
// The run step goes through the real path: RemoteRunExecutor -> RemoteExecutor
// -> the wire -> the daemon's LocalToolExecutor -> the shell tool -> a real
// process. The same command from a model, even one claiming run_step in its
// input, is stopped at the cap.
func TestRunStep_LongNodeOutlivesTheModelCap(t *testing.T) {
	// Not parallel: it scales a package-level clock.
	prev := tools.ShellTimeoutUnit
	tools.ShellTimeoutUnit = time.Microsecond
	t.Cleanup(func() { tools.ShellTimeoutUnit = prev })

	dir := t.TempDir()
	const command = "sleep 0.72; echo finished" // "12 minutes" on the shrunk clock
	nodeTimeoutMs := int((2 * time.Hour).Milliseconds())

	local := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{ShellPlatform: tools.ShellPlatformUnix}))
	local.SetDaemonClient(daemon.NewLocalClient())
	router := &loopbackDaemonRouter{local: local}
	remote := toolexec.NewRemoteExecutor(router)
	remote.SetServerExecutor(local)

	t.Run("run node with timeout 2h is not killed at 10 minutes", func(t *testing.T) {
		run := NewRemoteRunExecutor(remote)
		run.SetContext(RunExecutorContext{UserID: "u", ChatID: "c", ProjectID: "p", ProjectPath: dir})
		result, err := run.ExecuteCommand(context.Background(), command, dir, nodeTimeoutMs, nil)
		stdout, stderr, exitCode := result.Stdout, result.Stderr, result.ExitCode
		require.NoError(t, err)
		assert.Equal(t, 0, exitCode, "the step was stopped: stderr %q", stderr)
		assert.Contains(t, stdout, "finished", "the command did not run to completion")
		assert.Equal(t, true, router.lastContext(t)[toolexec.RunStepContextKey],
			"the run step's flag did not reach the daemon in the request's context")
	})

	// The agent tool path: what execute_tools dispatches for a model's call.
	modelCall := func(input string) *toolexec.ToolRequest {
		tec := &toolExecutionContext{
			chat:    &db.Chat{ID: "c"},
			project: &db.Project{ID: "p", UserID: "u", Path: dir},
			// The machine's checkout, resolved at dispatch (callCheckout).
			checkout:      callCheckout{main: projectcheckout.Target{Path: dir}},
			worktree:      &rctx.WorktreeInfo{},
			chatID:        "c",
			toolName:      names.ToolShell,
			toolCallID:    "call-1",
			dispatchInput: input,
		}
		return tec.buildToolRequest()
	}

	t.Run("a model cannot set the run-step flag through its tool call", func(t *testing.T) {
		// A model asking for 30 minutes and claiming to be a run step.
		req := modelCall(`{"command":"` + command + `","timeout":1800000,"run_step":true}`)
		require.False(t, req.RunStep, "the agent tool path set RunStep from the model's input")

		res, err := remote.ExecuteTool(context.Background(), req)
		require.NoError(t, err)
		_, claimed := router.lastContext(t)[toolexec.RunStepContextKey]
		assert.False(t, claimed, "a model's tool call reached the daemon marked as a run step")
		// run_step is not a shell parameter, so the call is refused before
		// anything runs rather than silently granted hours.
		assert.True(t, res.IsError, "a tool call claiming run_step ran: %q", res.Content)
		assert.Contains(t, res.Content, "run_step")
		assert.NotContains(t, res.Content, "finished")
	})

	t.Run("the same command from a model stops at the 10-minute cap", func(t *testing.T) {
		req := modelCall(`{"command":"` + command + `","timeout":1800000}`)
		require.False(t, req.RunStep)

		res, err := remote.ExecuteTool(context.Background(), req)
		require.NoError(t, err)
		_, claimed := router.lastContext(t)[toolexec.RunStepContextKey]
		assert.False(t, claimed, "a model's tool call reached the daemon marked as a run step")

		var out tools.ShellOutput
		require.NoError(t, json.Unmarshal([]byte(res.Content), &out), "content %q", res.Content)
		assert.Equal(t, daemon.TimeoutExitCode, out.ExitCode, "the model's call outlived the 10-minute cap: %+v", out)
		assert.NotContains(t, out.Stdout, "finished")
		assert.Contains(t, out.Stderr, "10-minute foreground limit")
		assert.NotContains(t, out.Stderr, "asleep")
	})

	t.Run("a run step stopped by its own timeout says so", func(t *testing.T) {
		run := NewRemoteRunExecutor(remote)
		run.SetContext(RunExecutorContext{UserID: "u", ChatID: "c", ProjectID: "p", ProjectPath: dir})
		// A 2-minute node (0.12s here) running the "12-minute" command.
		result, err := run.ExecuteCommand(context.Background(), command, dir, int((2 * time.Minute).Milliseconds()), nil)
		stderr, exitCode := result.Stderr, result.ExitCode
		require.NoError(t, err)
		assert.Equal(t, daemon.TimeoutExitCode, exitCode)
		assert.Contains(t, stderr, "step exceeded its 2-minute timeout")
		assert.NotContains(t, stderr, "asleep")
	})
}
