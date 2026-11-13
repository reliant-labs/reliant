// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// control-plane pr-review, 2026-10-31: the risk classifier's run node printed
// 19,780 bytes of JSON on one line. Run steps execute through the shell tool,
// which cut stdout to the model's 16,000 bytes (head + tail, a marker in the
// middle) and reported exit 0. json.decode of the line returned none, and the
// PR went to review:error four times. A run node's stdout is workflow data:
// it arrives whole up to the run step's own cap, and a cut says so.
func TestRunStep_OutputIsWorkflowDataNotModelContext(t *testing.T) {
	dir := t.TempDir()
	local := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{ShellPlatform: tools.ShellPlatformUnix}))
	local.SetDaemonClient(daemon.NewLocalClient())
	remote := toolexec.NewRemoteExecutor(&loopbackDaemonRouter{local: local})
	remote.SetServerExecutor(local)

	// A JSON document of n bytes of payload, on one line.
	jsonLine := func(n int) string {
		return fmt.Sprintf(`printf '{"v":"'; head -c %d /dev/zero | tr '\0' a; printf '"}\n'`, n)
	}
	run := func(t *testing.T, command string) CommandResult {
		t.Helper()
		exec := NewRemoteRunExecutor(remote)
		exec.SetContext(RunExecutorContext{UserID: "u", ChatID: "c", ProjectID: "p", ProjectPath: dir})
		result, err := exec.ExecuteCommand(context.Background(), command, dir, 60_000, nil)
		require.NoError(t, err)
		require.Equal(t, 0, result.ExitCode, "stderr: %s", result.Stderr)
		return result
	}

	t.Run("a 40 KB JSON line arrives whole and decodes", func(t *testing.T) {
		result := run(t, jsonLine(40_000))
		assert.False(t, result.Truncated)
		var doc struct {
			V string `json:"v"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Stdout), &doc), "stdout was cut: %d bytes", len(result.Stdout))
		assert.Len(t, doc.V, 40_000)
	})

	t.Run("output past the run step's cap is cut and says so", func(t *testing.T) {
		result := run(t, jsonLine(tools.MaxRunStepOutputLength+10_000))
		assert.True(t, result.Truncated, "a cut run step's output must say it was cut")
		assert.LessOrEqual(t, len(result.Stdout), tools.MaxRunStepOutputLength+200)
		assert.Contains(t, result.Stdout, "lines truncated")
	})

	t.Run("a model's call keeps the model's cap", func(t *testing.T) {
		input, err := json.Marshal(map[string]any{"command": jsonLine(40_000)})
		require.NoError(t, err)
		res, err := remote.ExecuteTool(context.Background(), &toolexec.ToolRequest{
			ToolName: tools.ShellToolName, ToolInput: string(input), UserID: "u", ChatID: "c",
			ProjectID: "p", ProjectPath: dir, WorkingDir: dir,
		})
		require.NoError(t, err)
		var out tools.ShellOutput
		require.NoError(t, json.Unmarshal([]byte(res.Content), &out), "content %q", res.Content)
		assert.LessOrEqual(t, len(out.Stdout), tools.MaxShellOutputLength+200)
		assert.Contains(t, out.Stdout, "lines truncated")
	})
}
