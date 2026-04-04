package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

const parallelSleepDemoWorkflowYAML = `
name: parallel-sleep-demo
apiVersion: "0.0.5"
description: |
  Deterministic parallel loop demo.

  The loop fans out inline run steps in parallel. Each branch prints a start marker,
  sleeps for a fixed duration, then prints an end marker.

entry: [parallel_sleep]

inputs:
  mode:
    type: enum
    enum: [manual, auto, plan]
    default: auto
    description: Included for compatibility with standard workflow launch paths.
  branches:
    type: object
    description: Map of branch name -> sleep duration in milliseconds.

outputs:
  branch_count: "{{nodes.parallel_sleep._iterations}}"

nodes:
  - id: parallel_sleep
    type: loop
    parallel: true
    items: "{{inputs.branches}}"
    inline:
      entry: [sleep_branch]
      outputs:
        branch: "{{iter.key}}"
        exit_code: "{{nodes.sleep_branch.exit_code}}"
        stdout: "{{nodes.sleep_branch.stdout}}"
      nodes:
        - id: sleep_branch
          type: run
          command: |
            python3 -c "import time; branch='{{iter.key}}'; delay_ms=int({{iter.item}}); start=time.time_ns(); print(f'BRANCH_START name={branch} ts_ns={start} delay_ms={delay_ms}', flush=True); time.sleep(delay_ms / 1000.0); end=time.time_ns(); print(f'BRANCH_END name={branch} ts_ns={end} delay_ms={delay_ms}', flush=True)"
  - id: summary
    type: save_message
    args:
      role: assistant
      content: |
        Parallel sleep demo completed.
        - Branches: {{nodes.parallel_sleep._iterations}}

edges:
  - from: parallel_sleep
    default: [summary]
`

type parallelSleepRunOutput struct {
	StepID     string `json:"step_id"`
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration"`
}

type branchWindow struct {
	Name      string
	StartTime time.Time
	EndTime   time.Time
}

func TestParallelLoopRunBranchesExecuteConcurrently(t *testing.T) {
	t.Parallel()

	h := NewTestHarness(t)
	defer h.Cleanup()

	h.WriteWorkflowFile(t, "parallel-sleep-demo.yaml", parallelSleepDemoWorkflowYAML)

	const perBranchDelay = 1200 * time.Millisecond
	h.MockRun.SetDefault(MockRunResponse{
		Stdout:   "BRANCH_START simulated\nBRANCH_END simulated\n",
		ExitCode: 0,
		Delay:    perBranchDelay,
	})

	branchInputs := map[string]interface{}{
		"branches": map[string]interface{}{
			"alpha": int(perBranchDelay / time.Millisecond),
			"beta":  int(perBranchDelay / time.Millisecond),
			"gamma": int(perBranchDelay / time.Millisecond),
		},
	}

	startedAt := time.Now()
	chatID := h.StartWorkflowViaGRPC(t, "parallel-sleep-demo", branchInputs, "run the deterministic parallel sleep demo")
	h.WaitForWorkflowComplete(t, chatID)
	h.WaitForMessages(t, chatID, 2)
	elapsed := time.Since(startedAt)

	require.Less(t, elapsed, 2500*time.Millisecond,
		"workflow should finish materially faster than sequential execution; elapsed=%s", elapsed)

	calls := h.MockRun.GetCalls()
	require.Len(t, calls, 3, "expected one run command per loop branch")

	windows := make([]branchWindow, 0, len(calls))
	for callIndex, call := range calls {
		require.True(t, call.Completed, "run call should be marked completed: %+v", call)
		require.False(t, call.EndTimestamp.IsZero(), "run call should record end timestamp: %+v", call)
		require.GreaterOrEqual(t, call.EndTimestamp.Sub(call.Timestamp), perBranchDelay-200*time.Millisecond,
			"run call should remain active for approximately the configured delay: %+v", call)
		branchName := branchNameFromCommand(call.Command)
		if branchName == "" {
			branchName = fmt.Sprintf("call-%d", callIndex)
		}
		windows = append(windows, branchWindow{
			Name:      branchName,
			StartTime: call.Timestamp,
			EndTime:   call.EndTimestamp,
		})
	}

	sort.Slice(windows, func(i, j int) bool {
		return windows[i].StartTime.Before(windows[j].StartTime)
	})

	latestStart := windows[0].StartTime
	for _, window := range windows[1:] {
		if window.StartTime.After(latestStart) {
			latestStart = window.StartTime
		}
	}
	firstEnd := windows[0].EndTime
	for _, window := range windows[1:] {
		if window.EndTime.Before(firstEnd) {
			firstEnd = window.EndTime
		}
	}

	require.True(t, latestStart.Before(firstEnd) || latestStart.Equal(firstEnd),
		"expected overlapping execution windows, got windows=%+v", windows)

	updates, err := h.DB.GetUpdatesSince(context.Background(), chatID, 0, 100)
	require.NoError(t, err)
	runOutputs := collectRunOutputs(t, updates)
	require.Len(t, runOutputs, 3, "expected run_output updates for all parallel branches")
	for _, update := range runOutputs {
		require.Equal(t, 0, update.ExitCode, "branch run should succeed: %+v", update)
		require.Contains(t, update.Stdout, "BRANCH_START", "stdout should include branch start marker")
		require.Contains(t, update.Stdout, "BRANCH_END", "stdout should include branch end marker")
		require.GreaterOrEqual(t, update.DurationMs, int64(perBranchDelay/time.Millisecond)-200,
			"run duration should reflect actual sleep delay")
	}

	stepExecutions, err := h.DB.GetStepExecutionsByWorkflow(context.Background(), chatID)
	require.NoError(t, err)
	var loopStepExecutions []*db.StepExecution
	for _, exec := range stepExecutions {
		if exec.ActivityName != "ExecuteRunStep" {
			continue
		}
		if !exec.LoopNodeID.Valid || exec.LoopNodeID.String != "parallel_sleep" {
			continue
		}
		loopStepExecutions = append(loopStepExecutions, exec)
	}
	require.Len(t, loopStepExecutions, 3, "expected run step executions for each loop branch")
	iterationsSeen := map[int64]bool{}
	for _, exec := range loopStepExecutions {
		require.True(t, exec.LoopIteration.Valid, "loop iteration should be recorded: %+v", exec)
		iterationsSeen[exec.LoopIteration.Int64] = true
		require.True(t, exec.Success.Valid && exec.Success.Bool, "run step should succeed: %+v", exec)
		require.True(t, exec.DurationMs.Valid, "duration should be recorded: %+v", exec)
		require.GreaterOrEqual(t, exec.DurationMs.Int64, int64(perBranchDelay/time.Millisecond)-200,
			"step execution duration should reflect actual sleep delay")
	}
	require.Len(t, iterationsSeen, 3, "expected distinct loop iterations to be recorded")
}

func collectRunOutputs(t *testing.T, updates []db.ChatUpdate) []parallelSleepRunOutput {
	t.Helper()

	outputs := make([]parallelSleepRunOutput, 0)
	for _, update := range updates {
		if update.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT {
			continue
		}
		var parsed parallelSleepRunOutput
		require.NoError(t, json.Unmarshal(update.Data, &parsed), "run_output payload should parse")
		outputs = append(outputs, parsed)
	}
	return outputs
}

func branchNameFromCommand(command string) string {
	marker := "branch="
	idx := strings.Index(command, marker)
	if idx == -1 {
		return ""
	}
	remaining := command[idx+len(marker):]
	if len(remaining) == 0 {
		return ""
	}
	quote := remaining[0]
	if quote == '\'' || quote == '"' {
		remaining = remaining[1:]
		end := strings.IndexRune(remaining, rune(quote))
		if end >= 0 {
			return remaining[:end]
		}
	}
	for i, r := range remaining {
		if r == ' ' || r == ';' || r == ',' {
			return remaining[:i]
		}
	}
	return remaining
}
