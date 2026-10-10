// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A spawn with a preset must start in a run that has no project path.
//
// On 2026-10-10 chat 0028ced4 was started on a branch worktree that was still
// being created. The worktree row has no path until the daemon reports where
// it landed, so the launcher recorded no project_path, and preset loading
// refused the run with a TerminalError: every agent the chat spawned — six
// researchers and a one-line probe — "failed" within two seconds, before
// running a single turn. LoadPresetParams never reads project_path, so the
// refusal guarded nothing; the parent's own tools in the same run resolved
// the worktree at execution time and worked throughout.

type SpawnPresetWithoutProjectPathSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSpawnPresetWithoutProjectPath(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SpawnPresetWithoutProjectPathSuite))
}

// noProjectPathInput is a run launched with no checkout to record: no
// project_path input and no ExecContext.ProjectPath.
func noProjectPathInput(chatID string) WorkflowInput {
	input := spawnE2EWorkflowInput(chatID)
	input.ExecContext.ProjectPath = ""
	delete(input.Inputs, inputKeyProjectPath)
	return input
}

// TestPresetSpawnRuns is the incident: the spawned agent loads its preset,
// takes its turn and reports a completion.
func (s *SpawnPresetWithoutProjectPathSuite) TestPresetSpawnRuns() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)

	input := noProjectPathInput("chat-spawn-no-project-path")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-researcher", "research one area")}, nil
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	report := e.reportFor("tc-researcher")
	s.Require().NotNil(report, "the parent must hear back from its agent")
	body, _ := report["body"].(string)
	s.NotContains(body, "project path not set", "a run with no project path must not refuse its spawns")
	s.Equal(2, reportKind(report), "the agent completed (AgentMessageKindCompleted), not failed")
	s.Equal(1, e.turns(e.threadOf("tc-researcher")), "the agent ran its turn")
	s.Contains(e.toolStatusesFor("tc-researcher"), "completed")
}

// TestHistoryBeforeGateStillRefuses pins the replay half: a history recorded
// before presetsNeedNoPathChangeID refused the spawn and scheduled no
// LoadPresetParams, so replaying it must take the same path.
func (s *SpawnPresetWithoutProjectPathSuite) TestHistoryBeforeGateStillRefuses() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	env.OnGetVersion(presetsNeedNoPathChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)

	input := noProjectPathInput("chat-spawn-no-project-path-old")
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-researcher", "research one area")}, nil
		}
		return nil, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	report := e.reportFor("tc-researcher")
	s.Require().NotNil(report)
	body, _ := report["body"].(string)
	s.Contains(body, "project path not set, cannot load presets")
	s.Equal(4, reportKind(report), "the old history's agent failed (AgentMessageKindFailed)")
}
